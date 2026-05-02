package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── Download ──────────────────────────────────────────────────────

// DownloadSubscribes downloads all subscribe URLs concurrently and caches
// their content to local files. Returns map[url]localFilePath.
// Failed downloads are skipped (logged but not fatal).
func DownloadSubscribes(urls []string) map[string]string {
	var mu sync.Mutex
	result := map[string]string{}
	var wg sync.WaitGroup

	for i, rawURL := range urls {
		rawURL := strings.TrimSpace(rawURL)
		if rawURL == "" {
			continue
		}
		wg.Add(1)
		go func(idx int, u string) {
			defer wg.Done()
			cachePath := fmt.Sprintf("sub_cache_%d.txt", idx)
			body, err := fetchRaw(u)
			if err != nil {
				fmt.Printf("[subscribe] skip %s: %v\n", u, err)
				return
			}
			if err := os.WriteFile(cachePath, body, 0644); err != nil {
				fmt.Printf("[subscribe] cache write %s: %v\n", u, err)
				return
			}
			mu.Lock()
			result[u] = cachePath
			mu.Unlock()
			fmt.Printf("[subscribe] downloaded (%d bytes): %s\n", len(body), u)
		}(i, rawURL)
	}
	wg.Wait()
	return result
}

func fetchRaw(rawURL string) ([]byte, error) {
	resp, err := (&http.Client{Timeout: SUB_TIMEOUT}).Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ── Parse ─────────────────────────────────────────────────────────

// ParseSubscribeFile reads a cached local file and returns raw channels.
func ParseSubscribeFile(path string) []Channel {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	content := string(data)
	if strings.HasPrefix(strings.TrimSpace(content), "#EXTM3U") {
		return parseM3U(content)
	}
	return parseTxtChannels(content)
}

func parseM3U(content string) []Channel {
	var channels []Channel
	var pending string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF") {
			if idx := strings.LastIndex(line, ","); idx >= 0 {
				pending = strings.TrimSpace(line[idx+1:])
			}
		} else if line != "" && !strings.HasPrefix(line, "#") && pending != "" {
			channels = append(channels, Channel{Name: pending, URL: line})
			pending = ""
		}
	}
	return channels
}

func parseTxtChannels(content string) []Channel {
	var channels []Channel
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ",", 2)
		if len(parts) == 2 {
			name, u := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if name != "" && u != "" && !strings.Contains(u, "#genre#") {
				channels = append(channels, Channel{Name: name, URL: u})
			}
		}
	}
	return channels
}

// ── Host-level speed test for subscribe channels ──────────────────

// hostKey extracts "scheme://host:port" from a URL as a grouping key.
func hostKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + u.Host
}


// TestSubscribeHosts groups channels by host, tests one sample URL per host,
// returns a map[hostKey]speed. Hosts with speed < SPEED_LOW get -1.
func TestSubscribeHosts(channels []Channel, workers int) map[string]float64 {
	// group by host
	hostChannels := map[string][]Channel{}
	for _, ch := range channels {
		hk := hostKey(ch.URL)
		hostChannels[hk] = append(hostChannels[hk], ch)
	}

	total := len(hostChannels)
	fmt.Printf("[subscribe] testing %d unique hosts...\n", total)

	type result struct {
		key   string
		speed float64
	}

	sem := make(chan struct{}, workers)
	resCh := make(chan result, total)
	var wg sync.WaitGroup

	completed, valid := 0, 0
	printProgress(0, total, 0)

	// collect keys so we can report progress
	keys := make([]string, 0, len(hostChannels))
	for k := range hostChannels {
		keys = append(keys, k)
	}

	for _, hk := range keys {
		chs := hostChannels[hk]
		sampleURL := chs[0].URL // one representative URL per host
		hk := hk
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			speed := testOneSubscribeURL(sampleURL)
			resCh <- result{key: hk, speed: speed}
		}()
	}
	go func() { wg.Wait(); close(resCh) }()

	// collect
	speeds := map[string]float64{}
	for r := range resCh {
		completed++
		spd := r.speed
		if spd < SPEED_LOW {
			spd = -1
		} else {
			valid++
		}
		speeds[r.key] = spd
		printProgress(completed, total, valid)
	}
	fmt.Println()
	return speeds
}

// testOneSubscribeURL tries to measure speed for a subscribe channel URL.
// Handles both direct stream URLs and m3u8 playlists.
func testOneSubscribeURL(rawURL string) float64 {
	deadline := time.Now().Add(HOST_TIMEOUT)
	timedOut := func() bool { return time.Now().After(deadline) }

	// If it looks like an m3u8, resolve segment first
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, ".m3u8") || strings.Contains(lower, "/hls/") || strings.Contains(lower, "/live/") {
		return testStreamURL(rawURL, deadline, timedOut)
	}
	// Otherwise try direct download speed
	if timedOut() {
		return -1
	}
	return measureSpeed(rawURL, deadline)
}

// ── HSMD channel processor ────────────────────────────────────────

var (
	reURL = regexp.MustCompile(`(http://[^\s]+)`)
	reID  = regexp.MustCompile(`^\s*\d+\s+`)
)

// ProcessHsmdtvChannels reads the local hsmd address list file and builds entries.
func ProcessHsmdtvChannels(host string, sourceIndex int, speed float64, stdMap map[string]string) []Entry {
	var entries []Entry
	data, err := os.ReadFile(HSMD_ADDRESS_LIST_FILE)
	if err != nil {
		fmt.Printf("[hsmd] %s not found, skipping\n", HSMD_ADDRESS_LIST_FILE)
		return entries
	}
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
		name := strings.TrimSpace(strings.ReplaceAll(reID.ReplaceAllString(line[:loc[0]], ""), "（默认频道）", ""))
		name = mapToStandardName(cleanChannelName(name), stdMap)
		p, err := url.Parse(urlInFile)
		if err != nil {
			continue
		}
		newURL := "http://" + host + p.Path
		entries = append(entries, Entry{
			Name:    name,
			URL:     newURL,
			Content: buildM3U8Entry(name, newURL, speed),
			Index:   sourceIndex,
			Speed:   speed,
		})
	}
	return entries
}
