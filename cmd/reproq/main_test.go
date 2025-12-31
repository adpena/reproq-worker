package main

import (
	"testing"
	"time"
)

func TestIsLowMemoryMode(t *testing.T) {
	t.Setenv("LOW_MEMORY_MODE", "true")
	if !isLowMemoryMode() {
		t.Fatal("expected low memory mode to be enabled")
	}

	t.Setenv("LOW_MEMORY_MODE", "0")
	if isLowMemoryMode() {
		t.Fatal("expected low memory mode to be disabled")
	}
}

func TestMemoryLogIntervalFromEnv(t *testing.T) {
	t.Run("duration", func(t *testing.T) {
		t.Setenv("REPROQ_MEMORY_LOG_INTERVAL", "45s")
		got := memoryLogIntervalFromEnv(nil)
		if got != 45*time.Second {
			t.Fatalf("expected 45s, got %s", got)
		}
	})
	t.Run("seconds-only", func(t *testing.T) {
		t.Setenv("REPROQ_MEMORY_LOG_INTERVAL", "120")
		got := memoryLogIntervalFromEnv(nil)
		if got != 120*time.Second {
			t.Fatalf("expected 120s, got %s", got)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		t.Setenv("REPROQ_MEMORY_LOG_INTERVAL", "nope")
		got := memoryLogIntervalFromEnv(nil)
		if got != 0 {
			t.Fatalf("expected 0, got %s", got)
		}
	})
}
