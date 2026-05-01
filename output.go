package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// tierGroups defines the fixed display order for speed-tier groups.
var tierGroups = []struct{ base, tier, label string }{
	{"央视", "高速", "央视（高速）"},
	{"央视", "普通", "央视（普通）"},
	{"央视", "低速", "央视（低速）"},
	{"卫视", "高速", "卫视（高速）"},
	{"卫视", "普通", "卫视（普通）"},
	{"卫视", "低速", "卫视（低速）"},
	{"其他", "高速", "其他（高速）"},
	{"其他", "普通", "其他（普通）"},
	{"其他", "低速", "其他（低速）"},
}

// BuildAndWrite takes all collected entries, groups/sorts them, writes files,
// and returns the rendered m3u8 and txt strings.
func BuildAndWrite(allEntries []Entry, updateTime time.Time) (m3u8 string, txt string) {
	// ── Group by channel name ─────────────────────────────────────
	byName := map[string][]Entry{}
	for _, e := range allEntries {
		byName[e.Name] = append(byName[e.Name], e)
	}

	// ── Sort names ────────────────────────────────────────────────
	allNames := make([]string, 0, len(byName))
	for name := range byName {
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

	// ── Dedupe URLs within each name, sort by tier then index ─────
	for name := range byName {
		seen := map[string]bool{}
		var deduped []Entry
		for _, e := range byName[name] {
			if !seen[e.URL] {
				seen[e.URL] = true
				deduped = append(deduped, e)
			}
		}
		sort.Slice(deduped, func(i, j int) bool {
			ti := tierOrder(speedTier(deduped[i].Speed))
			tj := tierOrder(speedTier(deduped[j].Speed))
			if ti != tj {
				return ti < tj
			}
			return deduped[i].Index < deduped[j].Index
		})
		byName[name] = deduped
	}

	ts := updateTime.Format("2006-01-02 15:04:05")
	dummyName := "更新时间: " + ts
	const dummyURL = "http://127.0.0.1/"

	// ── M3U8 ─────────────────────────────────────────────────────
	var m3u8Lines []string
	m3u8Lines = append(m3u8Lines,
		fmt.Sprintf(`#EXTM3U x-tvg-url="%s"`, EPG_URL),
		"#EXT-X-UPDATED: "+ts,
	)
	for _, name := range allNames {
		for _, e := range byName[name] {
			m3u8Lines = append(m3u8Lines, e.Content)
		}
	}
	m3u8Lines = append(m3u8Lines,
		fmt.Sprintf(`#EXTINF:-1 group-title="更新时间",%s`, dummyName)+"\n"+dummyURL,
	)
	m3u8 = strings.Join(m3u8Lines, "\n")
	_ = os.WriteFile(CACHE_M3U8, []byte(m3u8), 0644)

	// ── TXT ──────────────────────────────────────────────────────
	// Collect lines per tier-group label
	groupLines := map[string][]string{}
	for _, name := range allNames {
		for _, e := range byName[name] {
			label := baseGroup(e.Name) + "（" + speedTier(e.Speed) + "）"
			groupLines[label] = append(groupLines[label], e.Name+","+e.URL)
		}
	}
	var txtParts []string
	for _, tg := range tierGroups {
		lines := groupLines[tg.label]
		if len(lines) == 0 {
			continue
		}
		txtParts = append(txtParts, tg.label+",#genre#")
		txtParts = append(txtParts, lines...)
		txtParts = append(txtParts, "")
	}
	txtParts = append(txtParts, "更新时间,#genre#")
	txtParts = append(txtParts, dummyName+","+dummyURL)
	txt = strings.Join(txtParts, "\n")
	_ = os.WriteFile(CACHE_TXT, []byte(txt), 0644)

	fmt.Printf("[output] m3u8 %d bytes  txt %d bytes  channels %d\n",
		len(m3u8), len(txt), len(allNames))
	return
}

// ReadCache loads previously written cache files.
func ReadCache() (m3u8 string, txt string) {
	if d, err := os.ReadFile(CACHE_M3U8); err == nil {
		m3u8 = string(d)
	}
	if d, err := os.ReadFile(CACHE_TXT); err == nil {
		txt = string(d)
	}
	return
}
