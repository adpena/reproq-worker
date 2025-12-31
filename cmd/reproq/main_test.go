package main

import "testing"

func TestNormalizeMetricsAuthFallback(t *testing.T) {
	token, secret, used := normalizeMetricsAuth("", "shared-secret")
	if token != "shared-secret" {
		t.Fatalf("expected token fallback to secret, got %q", token)
	}
	if secret != "shared-secret" {
		t.Fatalf("expected secret preserved, got %q", secret)
	}
	if !used {
		t.Fatal("expected fallback to be reported as used")
	}
}

func TestNormalizeMetricsAuthPreservesToken(t *testing.T) {
	token, secret, used := normalizeMetricsAuth("token", "secret")
	if token != "token" {
		t.Fatalf("expected token to remain, got %q", token)
	}
	if secret != "secret" {
		t.Fatalf("expected secret to remain, got %q", secret)
	}
	if used {
		t.Fatal("expected fallback to be unused when token is set")
	}
}

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
