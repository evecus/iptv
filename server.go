package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ── Global HTTP state ─────────────────────────────────────────────

var (
	serverMu    sync.RWMutex
	globalM3U8  string
	globalTXT   string
	lastRunTime = "Never"
	pidFile     string
)

// ── HTTP handlers ─────────────────────────────────────────────────

func handleStatus(w http.ResponseWriter, _ *http.Request) {
	serverMu.RLock()
	status := "idle"
	if isRunning {
		status = "updating"
	}
	t := lastRunTime
	serverMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   status,
		"last_run": t,
		"version":  VERSION,
	})
}

func handleM3U8(w http.ResponseWriter, _ *http.Request) {
	serverMu.RLock()
	body := globalM3U8
	serverMu.RUnlock()
	if body == "" {
		body = loadCacheM3U8()
	}
	if body == "" {
		http.Error(w, "Not ready yet. Please wait for the first scan.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = fmt.Fprint(w, body)
}

func handleTxt(w http.ResponseWriter, _ *http.Request) {
	serverMu.RLock()
	body := globalTXT
	serverMu.RUnlock()
	if body == "" {
		body = loadCacheTxt()
	}
	if body == "" {
		http.Error(w, "Not ready yet. Please wait for the first scan.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, body)
}

func handleForceRetest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	serverMu.RLock()
	running := isRunning
	serverMu.RUnlock()
	if running {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "busy"})
		return
	}
	go RunTask()
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

func loadCacheM3U8() string {
	d, _ := os.ReadFile(CACHE_M3U8)
	return string(d)
}

func loadCacheTxt() string {
	d, _ := os.ReadFile(CACHE_TXT)
	return string(d)
}

// ── PID management ────────────────────────────────────────────────

func initPidFile() {
	exe, _ := os.Executable()
	pidFile = filepath.Join(filepath.Dir(exe), "app.pid")
}

func readPidFile() int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
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

// TakeoverPreviousInstance stops any previous instance holding the port.
func TakeoverPreviousInstance(bindHost string, bindPort int) bool {
	cur := os.Getpid()
	old := readPidFile()
	if old > 0 && old != cur && isProcessAlive(old) {
		fmt.Printf("[pid] stopping previous instance PID=%d\n", old)
		terminateProcess(old)
	}
	for i := 0; i < 20; i++ {
		if canBindPort(bindHost, bindPort) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return canBindPort(bindHost, bindPort)
}
