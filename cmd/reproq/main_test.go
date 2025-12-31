package main

import "testing"

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
