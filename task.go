package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ── Task state ────────────────────────────────────────────────────

var (
	taskLock  sync.Mutex
	isRunning bool
)

// RunTask is the main scheduled task:
//  1. Download subscribe files in parallel
//  2. Fetch API host list
//  3. Speed-test API hosts
//  4. Speed-test subscribe hosts (per unique server)
//  5. Build entries and write output
func RunTask() {
	if !taskLock.TryLock() {
		fmt.Println("[task] already running, skipping")
		return
	}
	isRunning = true
	defer func() {
		isRunning = false
		taskLock.Unlock()
	}()

	start := time.Now()
	fmt.Println("[task] ── start ──────────────────────────────────────────────")

	stdMap := getStandardChannelMap()
	var allEntries []Entry
	sourceIdx := 0

	// ── Step 1: Download subscribe files ─────────────────────────
	fmt.Println("[task] downloading subscribe files...")
	subCache := DownloadSubscribes(flagURLs) // map[url]localPath

	// ── Step 2 & 3: Fetch + speed-test API hosts ─────────────────
	apiItems := fetchAPIData()
	if len(apiItems) > 0 {
		fmt.Printf("[task] speed-testing %d API hosts...\n", len(apiItems))
		rawResults := RunAPISpeedTests(apiItems, flagWorkers)
		topSources := selectTopSources(rawResults, flagTopN)
		fmt.Printf("[task] selected %d API sources\n", len(topSources))

		for idx := range topSources {
			src := &topSources[idx]
			fmt.Printf("  [api] #%d %.2fMB/s [%s] %s (%s)\n",
				idx+1, src.Speed, speedTier(src.Speed), src.Host, src.MatchType)
			FetchChannelsForSource(src)
			var entries []Entry
			switch src.MatchType {
			case "txiptv", "zhgxtv", "jsmpeg":
				entries = buildEntries(src.Channels, sourceIdx, src.Speed, stdMap)
			case "hsmdtv":
				entries = ProcessHsmdtvChannels(src.Host, sourceIdx, src.Speed, stdMap)
			}
			allEntries = append(allEntries, entries...)
			sourceIdx++
		}
	}

	// ── Step 4: Speed-test subscribe hosts ────────────────────────
	for rawURL, cachePath := range subCache {
		channels := ParseSubscribeFile(cachePath)
		if len(channels) == 0 {
			fmt.Printf("[subscribe] no channels parsed from %s\n", rawURL)
			continue
		}
		fmt.Printf("[subscribe] %d channels from %s — testing hosts...\n", len(channels), rawURL)
		hostSpeeds := TestSubscribeHosts(channels, flagWorkers)

		added := 0
		for _, ch := range channels {
			hk := hostKey(ch.URL)
			spd, ok := hostSpeeds[hk]
			if !ok || spd < SPEED_LOW {
				continue
			}
			name := mapToStandardName(cleanChannelName(ch.Name), stdMap)
			allEntries = append(allEntries, Entry{
				Name:    name,
				URL:     ch.URL,
				Content: buildM3U8Entry(name, ch.URL, spd),
				Index:   sourceIdx,
				Speed:   spd,
			})
			added++
		}
		fmt.Printf("[subscribe] kept %d / %d channels\n", added, len(channels))
		sourceIdx++
	}

	if len(allEntries) == 0 {
		fmt.Println("[task] no entries collected, keeping cache")
		return
	}

	// ── Step 5: Build & write output ─────────────────────────────
	updateTime := time.Now()
	m3u8, txt := BuildAndWrite(allEntries, updateTime)

	serverMu.Lock()
	globalM3U8 = m3u8
	globalTXT = txt
	lastRunTime = updateTime.Format("2006-01-02 15:04:05")
	serverMu.Unlock()

	fmt.Printf("[task] done — elapsed %s\n", time.Since(start).Round(time.Second))
}

// ── Internal helpers ──────────────────────────────────────────────

func fetchAPIData() []map[string]interface{} {
	client := &http.Client{Timeout: 10 * time.Second}
	for attempt := 1; attempt <= 3; attempt++ {
		fmt.Printf("[api] fetch attempt %d: %s\n", attempt, API_URL)
		resp, err := client.Get(API_URL)
		if err == nil && resp.StatusCode == 200 {
			var data map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
				resp.Body.Close()
				if results, ok := data["results"].([]interface{}); ok {
					out := make([]map[string]interface{}, 0, len(results))
					for _, r := range results {
						if m, ok := r.(map[string]interface{}); ok {
							out = append(out, m)
						}
					}
					fmt.Printf("[api] received %d hosts\n", len(out))
					return out
				}
			} else {
				resp.Body.Close()
			}
		} else if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		time.Sleep(5 * time.Second)
	}
	fmt.Println("[api] fetch failed after 3 retries")
	return nil
}

func selectTopSources(results []SourceResult, topN int) []SourceResult {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Speed > results[j].Speed
	})
	selectedHosts := map[string]bool{}
	var final []SourceResult
	// ensure at least one per matchType
	for _, mt := range []string{"txiptv", "hsmdtv", "zhgxtv", "jsmpeg"} {
		for _, r := range results {
			if r.MatchType == mt && !selectedHosts[r.Host] {
				final = append(final, r)
				selectedHosts[r.Host] = true
				break
			}
		}
	}
	// fill up to topN
	for _, r := range results {
		if len(final) >= topN {
			break
		}
		if !selectedHosts[r.Host] {
			final = append(final, r)
			selectedHosts[r.Host] = true
		}
	}
	sort.Slice(final, func(i, j int) bool { return final[i].Speed > final[j].Speed })
	return final
}

func buildEntries(channels []Channel, idx int, speed float64, stdMap map[string]string) []Entry {
	entries := make([]Entry, 0, len(channels))
	for _, ch := range channels {
		name := mapToStandardName(cleanChannelName(ch.Name), stdMap)
		entries = append(entries, Entry{
			Name:    name,
			URL:     ch.URL,
			Content: buildM3U8Entry(name, ch.URL, speed),
			Index:   idx,
			Speed:   speed,
		})
	}
	return entries
}
