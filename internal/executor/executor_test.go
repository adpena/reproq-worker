package executor

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMockExecutor(t *testing.T) {
	m := NewMockExecutor(10 * time.Millisecond)
	ctx := context.Background()

	res, err := m.Execute(ctx, json.RawMessage("{}"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}

	// Test Timeout
	res, err = m.Execute(ctx, json.RawMessage("{}"), 5*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error on timeout: %v", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("expected timeout exit code -1, got %d", res.ExitCode)
	}
}
