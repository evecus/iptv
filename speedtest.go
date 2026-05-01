package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ── Low-level HTTP helpers ────────────────────────────────────────

func remainingTimeout(deadline time.Time, fallback time.Duration) time.Duration {
	if deadline.IsZero() {
		return fallback
	}
	r := time.Until(deadline)
	if r <= 0 {
		return 0
	}
	if r < fallback {
		return r
	}
	return fallback
}

// getTsURL fetches an m3u8 playlist and returns the first TS/segment URL.
func getTsURL(m3u8URL string, deadline time.Time) string {
	t := remainingTimeout(deadline, 5*time.Second)
	if t <= 0 {
		return ""
	}
	resp, err := (&http.Client{Timeout: t}).Get(m3u8URL)
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

// measureSpeed downloads from streamURL for up to SPEED_TEST_SECS and returns MB/s.
func measureSpeed(streamURL string, deadline time.Time) float64 {
	t := remainingTimeout(deadline, 10*time.Second)
	if t <= 0 {
		return -1
	}
	start := time.Now()
	resp, err := (&http.Client{Timeout: t}).Get(streamURL)
	if err != nil || resp.StatusCode >= 400 {
		return -1
	}
	defer resp.Body.Close()

	var size int64
	buf := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(buf)
		size += int64(n)
		if size > 10*1024*1024 || time.Since(start) > SPEED_TEST_SECS || time.Now().After(deadline) {
			break
		}
		if err != nil {
			break
		}
	}
	dur := time.Since(start).Seconds()
	if dur == 0 {
		dur = 0.001
	}
	return float64(size) / 1024 / 1024 / dur
}

// testStreamURL is a convenience wrapper: resolve m3u8 → segment → measure speed.
// Returns -1 if anything fails or deadline exceeded.
func testStreamURL(streamURL string, deadline time.Time, timedOut func() bool) float64 {
	if timedOut() {
		return -1
	}
	ts := getTsURL(streamURL, deadline)
	if ts == "" {
		return -1
	}
	if timedOut() {
		return -1
	}
	return measureSpeed(ts, deadline)
}

// ── API host speed tests ──────────────────────────────────────────

// TestAPIHostSpeed tests an API host item.
// If fetchChannels=true also returns the channel list.
func TestAPIHostSpeed(item map[string]interface{}, fetchChannels bool) (float64, []Channel) {
	host, _ := item["host"].(string)
	mt, _ := item["matchType"].(string)
	if host == "" {
		return -1, nil
	}
	deadline := time.Now().Add(HOST_TIMEOUT)
	timedOut := func() bool { return time.Now().After(deadline) }

	switch mt {
	case "txiptv":
		return testTxiptv(host, deadline, timedOut, fetchChannels)
	case "hsmdtv":
		return testHsmdtv(host, deadline, timedOut)
	case "jsmpeg":
		return testJsmpeg(host, deadline, timedOut, fetchChannels)
	case "zhgxtv":
		return testZhgxtv(host, deadline, timedOut, fetchChannels)
	}
	return -1, nil
}

// FetchChannelsForSource re-runs a speed test to extract channel list.
func FetchChannelsForSource(src *SourceResult) {
	switch src.MatchType {
	case "txiptv", "jsmpeg", "zhgxtv":
		_, chs := TestAPIHostSpeed(map[string]interface{}{
			"host": src.Host, "matchType": src.MatchType,
		}, true)
		if chs != nil {
			src.Channels = chs
		} else {
			src.Channels = []Channel{}
		}
	}
}

// RunAPISpeedTests tests all API items concurrently in batches.
// Returns items with speed >= SPEED_LOW.
func RunAPISpeedTests(items []map[string]interface{}, workers int) []SourceResult {
	type workItem struct {
		item  map[string]interface{}
		speed float64
	}

	total := len(items)
	completed, valid := 0, 0
	var mu sync.Mutex
	var results []SourceResult

	printProgress(0, total, 0)

	for i := 0; i < len(items); i += BATCH_SIZE {
		end := i + BATCH_SIZE
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		sem := make(chan struct{}, workers)
		resCh := make(chan workItem, len(batch))
		var wg sync.WaitGroup

		for _, item := range batch {
			item := item
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				speed, _ := TestAPIHostSpeed(item, false)
				resCh <- workItem{item: item, speed: speed}
			}()
		}
		go func() { wg.Wait(); close(resCh) }()

		for r := range resCh {
			mu.Lock()
			completed++
			if r.speed >= SPEED_LOW {
				valid++
				host, _ := r.item["host"].(string)
				mt, _ := r.item["matchType"].(string)
				src, _ := r.item["source"].(string)
				results = append(results, SourceResult{
					Host: host, MatchType: mt, Source: src,
					Speed: r.speed, Channels: []Channel{},
				})
			}
			printProgress(completed, total, valid)
			mu.Unlock()
		}
	}
	fmt.Println()
	return results
}

// ── Per-type test functions ───────────────────────────────────────

func testTxiptv(host string, deadline time.Time, timedOut func() bool, fetchCh bool) (float64, []Channel) {
	if timedOut() {
		return -1, nil
	}
	resp, err := (&http.Client{Timeout: remainingTimeout(deadline, 2*time.Second)}).
		Get(fmt.Sprintf("http://%s/iptv/live/1000.json?key=txiptv", host))
	if err != nil || resp.StatusCode != 200 {
		return -1, nil
	}
	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	resp.Body.Close()

	var channels []Channel
	var firstURL string
	if arr, ok := data["data"].([]interface{}); ok {
		for _, d := range arr {
			ch, ok := d.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := ch["name"].(string)
			u, _ := ch["url"].(string)
			if name == "" || u == "" || strings.Contains(u, ",") {
				continue
			}
			var full string
			switch {
			case strings.Contains(u, "http"):
				full = u
			case strings.HasPrefix(u, "/"):
				full = "http://" + host + u
			default:
				full = "http://" + host + "/" + u
			}
			if fetchCh {
				channels = append(channels, Channel{Name: name, URL: full})
			}
			if firstURL == "" {
				firstURL = full
			}
		}
	}
	if firstURL == "" {
		return -1, channels
	}
	return testStreamURL(firstURL, deadline, timedOut), channels
}

func testHsmdtv(host string, deadline time.Time, timedOut func() bool) (float64, []Channel) {
	if timedOut() {
		return -1, nil
	}
	return testStreamURL(fmt.Sprintf("http://%s%s", host, HSMDTV_TEST_URI), deadline, timedOut), nil
}

func testJsmpeg(host string, deadline time.Time, timedOut func() bool, fetchCh bool) (float64, []Channel) {
	if timedOut() {
		return -1, nil
	}
	resp, err := (&http.Client{Timeout: remainingTimeout(deadline, 2*time.Second)}).
		Get(fmt.Sprintf("http://%s/streamer/list", host))
	if err != nil || resp.StatusCode != 200 {
		return -1, nil
	}
	var list []map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()

	var channels []Channel
	var firstURL string
	for _, d := range list {
		name, _ := d["name"].(string)
		key, _ := d["key"].(string)
		name, key = strings.TrimSpace(name), strings.TrimSpace(key)
		if name == "" || key == "" {
			continue
		}
		full := fmt.Sprintf("http://%s/hls/%s/index.m3u8", host, key)
		if fetchCh {
			channels = append(channels, Channel{Name: name, URL: full})
		}
		if firstURL == "" {
			firstURL = full
		}
	}
	if firstURL == "" {
		return -1, channels
	}
	return testStreamURL(firstURL, deadline, timedOut), channels
}

func testZhgxtv(host string, deadline time.Time, timedOut func() bool, fetchCh bool) (float64, []Channel) {
	if timedOut() {
		return -1, nil
	}
	resp, err := (&http.Client{Timeout: remainingTimeout(deadline, 5*time.Second)}).
		Get(fmt.Sprintf("http://%s%s", host, ZHGXTV_INTERFACE))
	if err != nil || resp.StatusCode != 200 {
		return -1, nil
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var channels []Channel
	var firstURL string
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
		var full string
		switch {
		case strings.HasPrefix(urlPart, "http"):
			p, err := url.Parse(urlPart)
			if err != nil {
				continue
			}
			full = p.Scheme + "://" + host + p.Path
			if p.RawQuery != "" {
				full += "?" + p.RawQuery
			}
		case strings.HasPrefix(urlPart, "/"):
			full = "http://" + host + urlPart
		default:
			full = "http://" + host + "/" + urlPart
		}
		if fetchCh {
			channels = append(channels, Channel{Name: name, URL: full})
		}
		if firstURL == "" {
			firstURL = full
		}
	}
	if firstURL == "" {
		return -1, channels
	}
	return testStreamURL(firstURL, deadline, timedOut), channels
}

func printProgress(completed, total, success int) {
	if total <= 0 {
		return
	}
	bw := 30
	ratio := float64(completed) / float64(total)
	filled := int(float64(bw) * ratio)
	bar := strings.Repeat("=", filled) + strings.Repeat("-", bw-filled)
	fmt.Printf("\r测速进度 [%s] %d/%d (%5.1f%%) 有效源: %d", bar, completed, total, ratio*100, success)
}
