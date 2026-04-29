package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

// ─────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────

const (
	VERSION                 = "1.0.4"
	API_URL                 = "https://iptvs.pes.im"
	CACHE_FILE              = "iptv_sources.m3u8"
	TXT_CACHE_FILE          = "iptv_sources.txt"
	CHANNEL_LIST_FILE       = "channel_list.txt"
	ADDRESS_LIST_FILE       = "address_list.txt"
	HSMD_ADDRESS_LIST_FILE  = "hsmd_address_list.txt"
	ZHGXTV_INTERFACE        = "/ZHGXTV/Public/json/live_interface.txt"
	TXIPTV_TEST_URI         = "/tsfile/live/0001_1.m3u8"
	HSMDTV_TEST_URI         = "/newlive/live/hls/1/live.m3u8"
	MAX_WORKERS             = 20
	TOP_N                   = 5
	HOST_SPEED_TEST_TIMEOUT = 15 * time.Second
	SPEED_TEST_BATCH_SIZE   = 60
	EPG_URL                 = "https://epg.zsdc.eu.org/t.xml"
	LOGO_BASE_URL           = "https://ghfast.top/https://raw.githubusercontent.com/Jarrey/iptv_logo/main/tv/"
)

// ─────────────────────────────────────────────
// Global state
// ─────────────────────────────────────────────

var (
	globalM3U8Content string
	globalTxtContent  string
	lastRunTime       = "Never"
	isRunning         bool
	taskLock          sync.Mutex
	pidFile           string
)

// ─────────────────────────────────────────────
// PID management
// ─────────────────────────────────────────────

func readPidFile() int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func writePidFile(pid int) {
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644)
}

func cleanupPidFile() {
	if readPidFile() == os.Getpid() {
		_ = os.Remove(pidFile)
	}
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func terminateProcess(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F", "/T").Run()
	} else {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
}

func canBindPort(host string, port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func takeoverPreviousInstance(bindHost string, bindPort int) bool {
	currentPid := os.Getpid()
	oldPid := readPidFile()
	if oldPid > 0 && oldPid != currentPid && isProcessAlive(oldPid) {
		fmt.Printf("Detected previous instance PID=%d, stopping it...\n", oldPid)
		terminateProcess(oldPid)
	}
	for i := 0; i < 20; i++ {
		if canBindPort(bindHost, bindPort) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return canBindPort(bindHost, bindPort)
}

// ─────────────────────────────────────────────
// Auto-update
// ─────────────────────────────────────────────

func checkForUpdates() {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("https://iptvs.pes.im/message")
	if err != nil {
		fmt.Printf("Failed to check for updates: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return
	}
	remoteVersion, _ := data["version"].(string)
	if remoteVersion == "" || remoteVersion <= VERSION {
		return
	}
	fmt.Printf("New version %s found! Current version is %s. Downloading...\n", remoteVersion, VERSION)
	codeResp, err := (&http.Client{Timeout: 10 * time.Second}).Get("https://iptvs.pes.im/latest")
	if err != nil || codeResp.StatusCode != 200 {
		return
	}
	defer codeResp.Body.Close()
	content, _ := io.ReadAll(codeResp.Body)
	exe, _ := os.Executable()
	if err := os.WriteFile(exe, content, 0755); err != nil {
		fmt.Printf("Failed to write update: %v\n", err)
		return
	}
	fmt.Println("Update successful. Restarting...")
	_ = syscall.Exec(exe, os.Args, os.Environ())
}

// ─────────────────────────────────────────────
// Timeout helper
// ─────────────────────────────────────────────

func getRemainingTimeout(deadline time.Time, fallback time.Duration) time.Duration {
	if deadline.IsZero() {
		return fallback
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	if remaining < fallback {
		return remaining
	}
	return fallback
}

// ─────────────────────────────────────────────
// Progress bar
// ─────────────────────────────────────────────

func printSpeedTestProgress(completed, total, success int) {
	if total <= 0 {
		return
	}
	barWidth := 30
	ratio := float64(completed) / float64(total)
	filled := int(float64(barWidth) * ratio)
	bar := strings.Repeat("=", filled) + strings.Repeat("-", barWidth-filled)
	fmt.Printf("\r测速进度 [%s] %d/%d (%5.1f%%) 有效源: %d", bar, completed, total, ratio*100, success)
}

// ─────────────────────────────────────────────
// Logo / M3U8 helpers
// ─────────────────────────────────────────────

func buildLogoURL(channelName string) string {
	return LOGO_BASE_URL + url.PathEscape(channelName) + ".png"
}

func buildM3U8Entry(name, streamURL, groupTitle string) string {
	return fmt.Sprintf(
		"#EXTINF:-1 tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n%s",
		name, buildLogoURL(name), groupTitle, name, streamURL,
	)
}

// ─────────────────────────────────────────────
// Channel name mapping
// ─────────────────────────────────────────────

func getStandardChannelMap() map[string]string {
	mapping := map[string]string{}
	data, err := os.ReadFile(CHANNEL_LIST_FILE)
	if err != nil {
		return mapping
	}
	for _, line := range strings.Split(string(data), "\n") {
		stdName := strings.TrimSpace(line)
		if stdName == "" {
			continue
		}
		key := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(stdName, "-", ""), " ", ""))
		mapping[key] = stdName
	}
	return mapping
}

func mapToStandardName(name string, mapping map[string]string) string {
	key := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), " ", ""))
	if std, ok := mapping[key]; ok {
		return std
	}
	return name
}

// cleanChannelName mirrors Python's clean_channel_name exactly.
func cleanChannelName(name string) string {
	name = strings.ReplaceAll(name, "cctv", "CCTV")
	name = strings.ReplaceAll(name, "中央", "CCTV")
	name = strings.ReplaceAll(name, "央视", "CCTV")

	// Python: for rep in ["高清","超高","HD","标清","频道","-"," ","PLUS","＋","(",")"]
	//           name.replace(rep, "" if rep not in ["PLUS","＋"] else "+")
	for _, rep := range []string{"高清", "超高", "HD", "标清", "频道", "-", " ", "(", ")"} {
		name = strings.ReplaceAll(name, rep, "")
	}
	name = strings.ReplaceAll(name, "PLUS", "+")
	name = strings.ReplaceAll(name, "＋", "+")

	re := regexp.MustCompile(`CCTV(\d+)台`)
	name = re.ReplaceAllString(name, "CCTV$1")

	nameMap := map[string]string{
		"CCTV1综合": "CCTV1", "CCTV2财经": "CCTV2", "CCTV3综艺": "CCTV3",
		"CCTV4国际": "CCTV4", "CCTV4中文国际": "CCTV4", "CCTV4欧洲": "CCTV4",
		"CCTV5体育": "CCTV5", "CCTV6电影": "CCTV6",
		"CCTV7军事": "CCTV7", "CCTV7军农": "CCTV7", "CCTV7农业": "CCTV7", "CCTV7国防军事": "CCTV7",
		"CCTV8电视剧": "CCTV8", "CCTV9记录": "CCTV9", "CCTV9纪录": "CCTV9",
		"CCTV10科教": "CCTV10", "CCTV11戏曲": "CCTV11", "CCTV12社会与法": "CCTV12",
		"CCTV13新闻": "CCTV13", "CCTV新闻": "CCTV13",
		"CCTV14少儿": "CCTV14", "CCTV15音乐": "CCTV15", "CCTV16奥林匹克": "CCTV16",
		"CCTV17农业农村": "CCTV17", "CCTV17农业": "CCTV17",
		"CCTV5+体育赛视": "CCTV5+", "CCTV5+体育赛事": "CCTV5+", "CCTV5+体育": "CCTV5+",
		"CCTV01": "CCTV1", "CCTV02": "CCTV2", "CCTV03": "CCTV3",
		"CCTV04": "CCTV4", "CCTV05": "CCTV5", "CCTV06": "CCTV6",
		"CCTV07": "CCTV7", "CCTV08": "CCTV8", "CCTV09": "CCTV9",
	}
	if mapped, ok := nameMap[name]; ok {
		return mapped
	}
	return name
}

// ─────────────────────────────────────────────
// Channel sort key
// ─────────────────────────────────────────────

func channelSortKey(name string) (int, float64, string) {
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "CCTV") {
		re := regexp.MustCompile(`CCTV(\d+)`)
		if m := re.FindStringSubmatch(upper); m != nil {
			num, _ := strconv.ParseFloat(m[1], 64)
			return 0, num, ""
		}
		if strings.Contains(upper, "5+") {
			return 0, 5.5, ""
		}
		return 0, 999, ""
	}
	if strings.Contains(name, "卫视") {
		return 1, 0, name
	}
	return 2, 0, name
}

// ─────────────────────────────────────────────
// HTTP speed helpers
// ─────────────────────────────────────────────

func getTsURL(m3u8URL string, deadline time.Time) string {
	timeout := getRemainingTimeout(deadline, 5*time.Second)
	if timeout <= 0 {
		return ""
	}
	resp, err := (&http.Client{Timeout: timeout}).Get(m3u8URL)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	parsed, _ := url.Parse(m3u8URL)
	origin := parsed.Scheme + "://" + parsed.Host
	base := m3u8URL[:strings.LastIndex(m3u8URL, "/")+1]

	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "http"):
			return line
		case strings.HasPrefix(line, "/"):
			return origin + line
		default:
			return base + line
		}
	}
	return ""
}

func getDownloadSpeed(streamURL string, deadline time.Time) float64 {
	timeout := getRemainingTimeout(deadline, 10*time.Second)
	if timeout <= 0 {
		return -1
	}
	start := time.Now()
	resp, err := (&http.Client{Timeout: timeout}).Get(streamURL)
	if err != nil || resp.StatusCode >= 400 {
		return -1
	}
	defer resp.Body.Close()

	var size int64
	buf := make([]byte, 8192)
	limitSize := int64(10 * 1024 * 1024)

	for {
		n, err := resp.Body.Read(buf)
		size += int64(n)
		if size > limitSize || time.Since(start) > 8*time.Second {
			break
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		if err != nil {
			break
		}
	}
	duration := time.Since(start).Seconds()
	if duration == 0 {
		duration = 0.001
	}
	return float64(size) / 1024 / 1024 / duration
}

// ─────────────────────────────────────────────
// Data types
// ─────────────────────────────────────────────

type Channel struct {
	Name string
	URL  string
}

type SourceResult struct {
	Host      string
	MatchType string
	Source    string
	Speed     float64
	Channels  []Channel
}

type Entry struct {
	Name    string
	URL     string
	Content string
	Index   int
}

// ─────────────────────────────────────────────
// Speed test  (mirrors Python test_host_speed)
// ─────────────────────────────────────────────

func testHostSpeed(item map[string]interface{}, fetchChannels bool) (float64, []Channel) {
	host, _ := item["host"].(string)
	matchType, _ := item["matchType"].(string)
	if host == "" {
		return -1, nil
	}

	var speed float64 = -1
	var channels []Channel
	deadline := time.Now().Add(HOST_SPEED_TEST_TIMEOUT)
	timedOut := func() bool { return time.Now().After(deadline) }

	switch matchType {

	case "txiptv":
		if timedOut() {
			return -1, channels
		}
		timeout := getRemainingTimeout(deadline, 2*time.Second)
		if timeout <= 0 {
			return -1, channels
		}
		jsonURL := fmt.Sprintf("http://%s/iptv/live/1000.json?key=txiptv", host)
		resp, err := (&http.Client{Timeout: timeout}).Get(jsonURL)
		if err != nil || resp.StatusCode != 200 {
			return -1, channels
		}
		var jsonData map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&jsonData)
		resp.Body.Close()

		var validChannelURL string
		if dataArr, ok := jsonData["data"].([]interface{}); ok {
			for _, d := range dataArr {
				ch, ok := d.(map[string]interface{})
				if !ok {
					continue
				}
				name, _ := ch["name"].(string)
				urlx, _ := ch["url"].(string)
				if name == "" || urlx == "" || strings.Contains(urlx, ",") {
					continue
				}
				var fullURL string
				if strings.Contains(urlx, "http") {
					fullURL = urlx
				} else if strings.HasPrefix(urlx, "/") {
					fullURL = "http://" + host + urlx
				} else {
					fullURL = "http://" + host + "/" + urlx
				}
				if fetchChannels {
					channels = append(channels, Channel{Name: name, URL: fullURL})
				}
				if validChannelURL == "" {
					validChannelURL = fullURL
				}
			}
		}
		if validChannelURL != "" {
			if timedOut() {
				return -1, channels
			}
			if ts := getTsURL(validChannelURL, deadline); ts != "" {
				speed = getDownloadSpeed(ts, deadline)
			}
		}

	case "hsmdtv":
		if timedOut() {
			return -1, channels
		}
		testURL := fmt.Sprintf("http://%s%s", host, HSMDTV_TEST_URI)
		if ts := getTsURL(testURL, deadline); ts != "" {
			speed = getDownloadSpeed(ts, deadline)
		}

	case "jsmpeg":
		if timedOut() {
			return -1, channels
		}
		timeout := getRemainingTimeout(deadline, 2*time.Second)
		if timeout <= 0 {
			return -1, channels
		}
		jsonURL := fmt.Sprintf("http://%s/streamer/list", host)
		resp, err := (&http.Client{Timeout: timeout}).Get(jsonURL)
		if err != nil || resp.StatusCode != 200 {
			return -1, channels
		}
		var jsonData []map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&jsonData)
		resp.Body.Close()

		var validChannelURL string
		for _, d := range jsonData {
			name, _ := d["name"].(string)
			key, _ := d["key"].(string)
			name = strings.TrimSpace(name)
			key = strings.TrimSpace(key)
			if name == "" || key == "" {
				continue
			}
			fullURL := fmt.Sprintf("http://%s/hls/%s/index.m3u8", host, key)
			if fetchChannels {
				channels = append(channels, Channel{Name: name, URL: fullURL})
			}
			if validChannelURL == "" {
				validChannelURL = fullURL
			}
		}
		if validChannelURL != "" {
			if timedOut() {
				return -1, channels
			}
			if ts := getTsURL(validChannelURL, deadline); ts != "" {
				speed = getDownloadSpeed(ts, deadline)
			}
		}

	case "zhgxtv":
		if timedOut() {
			return -1, channels
		}
		timeout := getRemainingTimeout(deadline, 5*time.Second)
		if timeout <= 0 {
			return -1, channels
		}
		interfaceURL := fmt.Sprintf("http://%s%s", host, ZHGXTV_INTERFACE)
		resp, err := (&http.Client{Timeout: timeout}).Get(interfaceURL)
		if err != nil || resp.StatusCode != 200 {
			return -1, channels
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var validChannelURL string
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, ",") {
				continue
			}
			parts := strings.SplitN(line, ",", 2)
			if len(parts) < 2 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			urlPart := strings.TrimSpace(parts[1])
			var fullURL string
			if strings.HasPrefix(urlPart, "http") {
				p, err := url.Parse(urlPart)
				if err != nil {
					continue
				}
				fullURL = p.Scheme + "://" + host + p.Path
				if p.RawQuery != "" {
					fullURL += "?" + p.RawQuery
				}
			} else if strings.HasPrefix(urlPart, "/") {
				fullURL = "http://" + host + urlPart
			} else {
				fullURL = "http://" + host + "/" + urlPart
			}
			if fetchChannels {
				channels = append(channels, Channel{Name: name, URL: fullURL})
			}
			if validChannelURL == "" {
				validChannelURL = fullURL
			}
		}
		if validChannelURL != "" {
			if timedOut() {
				return -1, channels
			}
			if ts := getTsURL(validChannelURL, deadline); ts != "" {
				speed = getDownloadSpeed(ts, deadline)
			}
		}
	}

	return speed, channels
}

// ─────────────────────────────────────────────
// fetch_channels_for_source
// ─────────────────────────────────────────────

func fetchChannelsForSource(source *SourceResult) {
	switch source.MatchType {
	case "txiptv", "jsmpeg", "zhgxtv":
		_, channels := testHostSpeed(map[string]interface{}{
			"host":      source.Host,
			"matchType": source.MatchType,
		}, true)
		if channels != nil {
			source.Channels = channels
		} else {
			source.Channels = []Channel{}
		}
	}
}

// ─────────────────────────────────────────────
// Channel processors
// ─────────────────────────────────────────────

// processGenericChannels handles txiptv / zhgxtv / jsmpeg (same logic).
func processGenericChannels(channels []Channel, sourceIndex int) []Entry {
	stdMap := getStandardChannelMap()
	entries := make([]Entry, 0, len(channels))
	for _, ch := range channels {
		name := cleanChannelName(ch.Name)
		name = mapToStandardName(name, stdMap)
		entries = append(entries, Entry{
			Name:    name,
			URL:     ch.URL,
			Content: buildM3U8Entry(name, ch.URL, "IPTV"),
			Index:   sourceIndex,
		})
	}
	return entries
}

// processHsmdtvChannels mirrors Python process_hsmdtv_channels exactly.
func processHsmdtvChannels(host string, sourceIndex int) []Entry {
	stdMap := getStandardChannelMap()
	var entries []Entry

	data, err := os.ReadFile(HSMD_ADDRESS_LIST_FILE)
	if err != nil {
		fmt.Printf("%s not found.\n", HSMD_ADDRESS_LIST_FILE)
		return entries
	}

	reURL := regexp.MustCompile(`(http://[^\s]+)`)
	reID := regexp.MustCompile(`^\s*\d+\s+`)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		loc := reURL.FindStringIndex(line)
		if loc == nil {
			continue
		}
		urlInFile := line[loc[0]:loc[1]]
		partBeforeURL := line[:loc[0]]

		name := reID.ReplaceAllString(partBeforeURL, "")
		name = strings.TrimSpace(strings.ReplaceAll(name, "（默认频道）", ""))
		name = cleanChannelName(name)
		name = mapToStandardName(name, stdMap)

		p, err := url.Parse(urlInFile)
		if err != nil {
			continue
		}
		newURL := "http://" + host + p.Path

		entries = append(entries, Entry{
			Name:    name,
			URL:     newURL,
			Content: buildM3U8Entry(name, newURL, "IPTV"),
			Index:   sourceIndex,
		})
	}
	return entries
}

// ─────────────────────────────────────────────
// API fetch
// ─────────────────────────────────────────────

func fetchAPIData() []map[string]interface{} {
	client := &http.Client{Timeout: 10 * time.Second}
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Printf("Fetching API data (Attempt %d)...\n", attempt+1)
		resp, err := client.Get(API_URL)
		if err == nil && resp.StatusCode == 200 {
			var data map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
				resp.Body.Close()
				fmt.Println("API data fetched successfully.")
				results, ok := data["results"].([]interface{})
				if !ok {
					fmt.Println("No valid data received or 'results' key missing.")
					return nil
				}
				out := make([]map[string]interface{}, 0, len(results))
				for _, r := range results {
					if m, ok := r.(map[string]interface{}); ok {
						out = append(out, m)
					}
				}
				return out
			}
			resp.Body.Close()
		}
		time.Sleep(5 * time.Second)
	}
	fmt.Println("API fetch failed after retries.")
	return nil
}

// ─────────────────────────────────────────────
// Scheduled task
// ─────────────────────────────────────────────

func scheduledTask() {
	if !taskLock.TryLock() {
		fmt.Println("Scheduled task skipped: another update is already running.")
		return
	}
	isRunning = true
	defer func() {
		isRunning = false
		taskLock.Unlock()
	}()

	checkForUpdates()
	fmt.Println("Executing scheduled task...")
	lastRunTime = time.Now().Format("2006-01-02 15:04:05")

	result := fetchAPIData()
	if result == nil {
		fmt.Println("No valid data received.")
		return
	}

	// ── Phase 1: parallel speed test (batched) ──────────────────
	type workResult struct {
		item  map[string]interface{}
		speed float64
	}

	var resultsWithSpeed []SourceResult
	totalHosts := len(result)
	completedHosts, validHosts := 0, 0
	printSpeedTestProgress(0, totalHosts, 0)

	for i := 0; i < len(result); i += SPEED_TEST_BATCH_SIZE {
		end := i + SPEED_TEST_BATCH_SIZE
		if end > len(result) {
			end = len(result)
		}
		batch := result[i:end]

		sem := make(chan struct{}, MAX_WORKERS)
		resCh := make(chan workResult, len(batch))
		var wg sync.WaitGroup

		for _, item := range batch {
			item := item
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				speed, _ := testHostSpeed(item, false)
				resCh <- workResult{item: item, speed: speed}
			}()
		}
		go func() { wg.Wait(); close(resCh) }()

		for r := range resCh {
			if r.speed > 0 {
				validHosts++
				host, _ := r.item["host"].(string)
				matchType, _ := r.item["matchType"].(string)
				source, _ := r.item["source"].(string)
				resultsWithSpeed = append(resultsWithSpeed, SourceResult{
					Host:      host,
					MatchType: matchType,
					Source:    source,
					Speed:     r.speed,
					Channels:  []Channel{},
				})
			}
			completedHosts++
			printSpeedTestProgress(completedHosts, totalHosts, validHosts)
		}
	}
	fmt.Println()

	// ── Filter speed > 1.5 MB/s, sort descending ────────────────
	var validResults []SourceResult
	for _, r := range resultsWithSpeed {
		if r.Speed > 1.5 {
			validResults = append(validResults, r)
		}
	}
	sort.Slice(validResults, func(i, j int) bool {
		return validResults[i].Speed > validResults[j].Speed
	})

	// ── Ensure at least one per type, then fill to TOP_N ────────
	var finalSources []SourceResult
	selectedHosts := map[string]bool{}
	for _, m := range []string{"txiptv", "hsmdtv", "zhgxtv", "jsmpeg"} {
		for _, res := range validResults {
			if res.MatchType == m && !selectedHosts[res.Host] {
				finalSources = append(finalSources, res)
				selectedHosts[res.Host] = true
				break
			}
		}
	}
	for _, res := range validResults {
		if len(finalSources) >= TOP_N {
			break
		}
		if !selectedHosts[res.Host] {
			finalSources = append(finalSources, res)
			selectedHosts[res.Host] = true
		}
	}
	sort.Slice(finalSources, func(i, j int) bool {
		return finalSources[i].Speed > finalSources[j].Speed
	})
	topSources := finalSources
	fmt.Printf("Selected top %d sources.\n", len(topSources))

	updateTimeStr := time.Now().Format("2006-01-02 15:04:05")
	dummyName := "更新时间: " + updateTimeStr
	dummyURL := "http://127.0.0.1/"

	// ── Not enough sources: only update timestamps ───────────────
	if len(topSources) < 3 {
		fmt.Printf("Not enough sources found (%d < 3).\n", len(topSources))
		if globalM3U8Content == "" {
			if d, err := os.ReadFile(CACHE_FILE); err == nil {
				globalM3U8Content = string(d)
			}
		}
		if globalTxtContent == "" {
			if d, err := os.ReadFile(TXT_CACHE_FILE); err == nil {
				globalTxtContent = string(d)
			}
		}
		if globalM3U8Content != "" {
			lines := strings.Split(globalM3U8Content, "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "#EXT-X-UPDATED:") {
					lines[i] = "#EXT-X-UPDATED: " + updateTimeStr
				} else if strings.HasPrefix(line, `#EXTINF:-1 group-title="Update",更新时间:`) {
					lines[i] = `#EXTINF:-1 group-title="Update",` + dummyName
				}
			}
			globalM3U8Content = strings.Join(lines, "\n")
			_ = os.WriteFile(CACHE_FILE, []byte(globalM3U8Content), 0644)
		}
		if globalTxtContent != "" {
			lines := strings.Split(globalTxtContent, "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "更新时间:") {
					lines[i] = dummyName + "," + dummyURL
					break
				}
			}
			globalTxtContent = strings.Join(lines, "\n")
			_ = os.WriteFile(TXT_CACHE_FILE, []byte(globalTxtContent), 0644)
		}
		return
	}

	// ── Phase 2: fetch channels & build entries ──────────────────
	var allEntries []Entry
	for idx := range topSources {
		src := &topSources[idx]
		fmt.Printf("Processing 源%d %.2fMB/s: %s (%s) %s\n",
			idx+1, src.Speed, src.Host, src.MatchType, src.Source)
		fetchChannelsForSource(src)
		var entries []Entry
		switch src.MatchType {
		case "txiptv", "zhgxtv", "jsmpeg":
			entries = processGenericChannels(src.Channels, idx)
		case "hsmdtv":
			entries = processHsmdtvChannels(src.Host, idx)
		}
		allEntries = append(allEntries, entries...)
	}

	// ── Group by name ────────────────────────────────────────────
	groupedEntries := map[string][]Entry{}
	var channelOrder []string

	// Pre-populate from channel_list.txt (preserves manual ordering)
	if d, err := os.ReadFile(CHANNEL_LIST_FILE); err == nil {
		for _, line := range strings.Split(string(d), "\n") {
			name := strings.TrimSpace(line)
			if name != "" {
				groupedEntries[name] = []Entry{}
				channelOrder = append(channelOrder, name)
			}
		}
	}
	for _, entry := range allEntries {
		groupedEntries[entry.Name] = append(groupedEntries[entry.Name], entry)
	}

	// Collect all unique names and sort (mirrors Python unique_channel_names.sort)
	allNames := make([]string, 0, len(groupedEntries))
	for name := range groupedEntries {
		allNames = append(allNames, name)
	}
	sort.Slice(allNames, func(i, j int) bool {
		a0, a1, a2 := channelSortKey(allNames[i])
		b0, b1, b2 := channelSortKey(allNames[j])
		if a0 != b0 {
			return a0 < b0
		}
		if a1 != b1 {
			return a1 < b1
		}
		return a2 < b2
	})
	channelOrder = allNames

	// ── Build M3U8 ───────────────────────────────────────────────
	m3u8Lines := []string{
		fmt.Sprintf(`#EXTM3U x-tvg-url="%s"`, EPG_URL),
		"#EXT-X-UPDATED: " + updateTimeStr,
		fmt.Sprintf(`#EXTINF:-1 group-title="Update",%s`, dummyName) + "\n" + dummyURL,
	}
	for _, name := range channelOrder {
		entries := groupedEntries[name]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Index < entries[j].Index })
		for _, e := range entries {
			m3u8Lines = append(m3u8Lines, e.Content)
		}
	}
	globalM3U8Content = strings.Join(m3u8Lines, "\n")
	_ = os.WriteFile(CACHE_FILE, []byte(globalM3U8Content), 0644)

	// ── Build TXT ────────────────────────────────────────────────
	txtLines := []string{dummyName + "," + dummyURL}
	seen := map[string]bool{}
	for _, name := range channelOrder {
		if seen[name] {
			continue
		}
		seen[name] = true
		entries := groupedEntries[name]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Index < entries[j].Index })
		for _, e := range entries {
			txtLines = append(txtLines, e.Name+","+e.URL)
		}
	}
	globalTxtContent = strings.Join(txtLines, "\n")
	_ = os.WriteFile(TXT_CACHE_FILE, []byte(globalTxtContent), 0644)

	fmt.Printf("M3U8 and TXT generation complete at %s.\n", lastRunTime)
}

// ─────────────────────────────────────────────
// HTTP handlers
// ─────────────────────────────────────────────

func handleStatus(w http.ResponseWriter, _ *http.Request) {
	status := "running"
	if isRunning {
		status = "updating"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   status,
		"last_run": lastRunTime,
		"message":  "Visit /iptv for m3u8 playlist, /txt for text playlist.",
	})
}

func handleM3U8(w http.ResponseWriter, _ *http.Request) {
	if globalM3U8Content == "" {
		if d, err := os.ReadFile(CACHE_FILE); err == nil {
			globalM3U8Content = string(d)
		}
	}
	if globalM3U8Content == "" {
		http.Error(w, "Not ready yet. Please wait for the first scan.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = fmt.Fprint(w, globalM3U8Content)
}

func handleTxt(w http.ResponseWriter, _ *http.Request) {
	if globalTxtContent == "" {
		if d, err := os.ReadFile(TXT_CACHE_FILE); err == nil {
			globalTxtContent = string(d)
		}
	}
	if globalTxtContent == "" {
		http.Error(w, "Not ready yet. Please wait for the first scan.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, globalTxtContent)
}

func handleForceRetest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if isRunning {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"message": "Update already in progress.",
			"status":  "busy",
		})
		return
	}
	go scheduledTask()
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "Force retest started in background.",
		"status":  "started",
	})
}

// ─────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────

func main() {
	_ = os.Setenv("TZ", "Asia/Shanghai")

	bindHost := "0.0.0.0"
	bindPort := 5000

	exe, _ := os.Executable()
	pidFile = filepath.Join(filepath.Dir(exe), "app.pid")

	fmt.Printf("Starting... - Version: %s\n", VERSION)
	checkForUpdates()

	if !takeoverPreviousInstance(bindHost, bindPort) {
		fmt.Printf("Port %d is still in use. Exit startup to avoid duplicate instance.\n", bindPort)
		fmt.Println("检测到新版本更新~请手动重启程序以应用更新~")
		os.Exit(1)
	}

	writePidFile(os.Getpid())
	defer cleanupPidFile()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cleanupPidFile()
		os.Exit(0)
	}()

	c := cron.New()
	_, _ = c.AddFunc("@every 6h", func() { go scheduledTask() })
	c.Start()
	defer c.Stop()

	go scheduledTask()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleStatus)
	mux.HandleFunc("/iptv", handleM3U8)
	mux.HandleFunc("/txt", handleTxt)
	mux.HandleFunc("/forceRetest", handleForceRetest)

	addr := fmt.Sprintf("%s:%d", bindHost, bindPort)
	fmt.Printf("Listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}
