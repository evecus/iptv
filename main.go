package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/robfig/cron/v3"
)

func main() {
	_ = os.Setenv("TZ", "Asia/Shanghai")
	initFlags()
	initPidFile()

	bindHost := "0.0.0.0"

	fmt.Printf("IPTV Aggregator v%s  port=%d workers=%d top=%d interval=%s\n",
		VERSION, flagPort, flagWorkers, flagTopN, flagInterval)
	fmt.Printf("Subscribe URLs (%d):\n", len(flagURLs))
	for i, u := range flagURLs {
		fmt.Printf("  %d. %s\n", i+1, u)
	}

	if !TakeoverPreviousInstance(bindHost, flagPort) {
		fmt.Printf("[error] port %d still in use after waiting. Exiting.\n", flagPort)
		os.Exit(1)
	}
	writePidFile(os.Getpid())
	defer cleanupPidFile()

	// restore cache so HTTP responds immediately before first scan
	m3u8, txt := ReadCache()
	serverMu.Lock()
	globalM3U8 = m3u8
	globalTXT = txt
	serverMu.Unlock()

	// graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n[main] shutting down...")
		cleanupPidFile()
		os.Exit(0)
	}()

	// scheduled updates
	c := cron.New()
	spec := "@every " + flagInterval
	if _, err := c.AddFunc(spec, func() { go RunTask() }); err != nil {
		fmt.Printf("[cron] invalid interval %q: %v\n", flagInterval, err)
		os.Exit(1)
	}
	c.Start()
	defer c.Stop()

	// first run immediately
	go RunTask()

	// HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleM3U8)
	mux.HandleFunc("/iptv", handleM3U8)
	mux.HandleFunc("/txt", handleTxt)
	mux.HandleFunc("/status", handleStatus)
	mux.HandleFunc("/retest", handleForceRetest)

	addr := fmt.Sprintf("%s:%d", bindHost, flagPort)
	fmt.Printf("[main] listening on http://%s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Printf("[main] server error: %v\n", err)
		os.Exit(1)
	}
}
