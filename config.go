package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

// ── Version ───────────────────────────────────────────────────────
const VERSION = "3.0.0"

// ── File paths ────────────────────────────────────────────────────
const (
	CACHE_M3U8             = "iptv_sources.m3u8"
	CACHE_TXT              = "iptv_sources.txt"
	CHANNEL_LIST_FILE      = "channel_list.txt"
	HSMD_ADDRESS_LIST_FILE = "hsmd_address_list.txt"
)

// ── Remote endpoints ──────────────────────────────────────────────
const (
	API_URL         = "https://iptvs.pes.im"
	EPG_URL         = "https://epg.zsdc.eu.org/t.xml"
	LOGO_BASE_URL   = "https://ghfast.top/https://raw.githubusercontent.com/Jarrey/iptv_logo/main/tv/"
	DEFAULT_SUB_URL = "http://gh-proxy.com/raw.githubusercontent.com/suxuang/myIPTV/main/ipv4.m3u"
)

// ── IPTV type paths ───────────────────────────────────────────────
const (
	ZHGXTV_INTERFACE = "/ZHGXTV/Public/json/live_interface.txt"
	HSMDTV_TEST_URI  = "/newlive/live/hls/1/live.m3u8"
)

// ── Speed tiers (MB/s) ────────────────────────────────────────────
const (
	SPEED_HIGH = 5.0 // ≥ 5   → 高速
	SPEED_MID  = 1.0 // 1–5   → 普通
	SPEED_LOW  = 0.5 // 0.5–1 → 低速
	// < 0.5 → discard
)

// ── Timeouts / batch ─────────────────────────────────────────────
const (
	HOST_TIMEOUT    = 15 * time.Second
	SUB_TIMEOUT     = 10 * time.Second
	SPEED_TEST_SECS = 8 * time.Second
	BATCH_SIZE      = 60
)

// ── CLI flags ─────────────────────────────────────────────────────
var (
	flagPort     int
	flagWorkers  int
	flagTopN     int
	flagInterval string
	flagURLs     []string // subscribe URLs (--url1..url20 + env URL1..)
)

func initFlags() {
	flag.IntVar(&flagPort, "port", 3030, "HTTP listen port")
	flag.IntVar(&flagWorkers, "workers", 20, "concurrent speed-test workers")
	flag.IntVar(&flagTopN, "top", 5, "top N API sources per matchType")
	flag.StringVar(&flagInterval, "interval", "6h", "update interval (e.g. 6h, 30m)")

	urlPtrs := make([]*string, 20)
	for i := 1; i <= 20; i++ {
		s := ""
		urlPtrs[i-1] = &s
		flag.StringVar(&s, fmt.Sprintf("url%d", i), "", fmt.Sprintf("subscribe URL #%d", i))
	}
	flag.Parse()

	// --urlN flags
	for _, p := range urlPtrs {
		if *p != "" {
			flagURLs = append(flagURLs, *p)
		}
	}
	// env URL1..URL20
	for i := 1; i <= 20; i++ {
		if v := os.Getenv(fmt.Sprintf("URL%d", i)); v != "" {
			flagURLs = append(flagURLs, v)
		}
	}
	// built-in default (always appended; skipped on fetch error)
	flagURLs = append(flagURLs, DEFAULT_SUB_URL)
}
