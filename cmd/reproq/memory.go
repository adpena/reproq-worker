package main

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func memoryLogIntervalFromEnv(logger *slog.Logger) time.Duration {
	value := strings.TrimSpace(os.Getenv("REPROQ_MEMORY_LOG_INTERVAL"))
	if value == "" {
		return 0
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		if digitsOnly(value) {
			seconds, convErr := strconv.Atoi(value)
			if convErr == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
		if logger != nil {
			logger.Warn("Invalid REPROQ_MEMORY_LOG_INTERVAL; skipping memory logger", "value", value, "error", err)
		}
		return 0
	}
	return parsed
}

func startMemoryLogger(ctx context.Context, logger *slog.Logger, interval time.Duration) {
	if logger == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		logMemoryStats(logger)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logMemoryStats(logger)
			}
		}
	}()
}

func logMemoryStats(logger *slog.Logger) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	attrs := []any{
		"heap_alloc_bytes", m.HeapAlloc,
		"heap_sys_bytes", m.HeapSys,
		"heap_idle_bytes", m.HeapIdle,
		"heap_inuse_bytes", m.HeapInuse,
		"stack_inuse_bytes", m.StackInuse,
		"num_gc", m.NumGC,
		"gc_cpu_fraction", m.GCCPUFraction,
		"goroutines", runtime.NumGoroutine(),
	}
	if rss, ok := readRSSBytes(); ok {
		attrs = append(attrs, "rss_bytes", rss)
	}
	logger.Info("reproq memory usage", attrs...)
}

func readRSSBytes() (uint64, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, false
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return value * 1024, true
		}
	}
	return 0, false
}

func digitsOnly(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}
