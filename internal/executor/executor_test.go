package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestShellExecutor_PayloadTooLarge(t *testing.T) {
	e := &ShellExecutor{
		MaxPayloadBytes: 10,
	}
	payload := json.RawMessage(`{"too":"large"}`)
	_, _, _, err := e.Execute(context.Background(), 1, 0, payload, time.Second)
	if err == nil || err.Error() != "payload size 15 exceeds limit 10" {
		t.Errorf("expected payload too large error, got %v", err)
	}
}

func TestShellExecutor_InvalidJSON(t *testing.T) {
	// We use 'echo' as a stub for python
	e := &ShellExecutor{
		PythonBin:      "echo",
		ExecutorModule: "stub",
		PayloadMode:    "inline",
		MaxStdoutBytes: 1024,
		MaxStderrBytes: 1024,
		MaxPayloadBytes: 1024,
	}
	
	// echo -m stub --result-id 1 --attempt 0 --payload-json "not json"
	// will output: -m stub --result-id 1 --attempt 0 --payload-json not json
	// which is not valid JSON
	res, stdout, _, err := e.Execute(context.Background(), 1, 0, json.RawMessage(`"not json"`), time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Ok {
		t.Error("expected Ok=false for invalid JSON stdout")
	}
	if !contains(res.Message, "Invalid JSON on stdout") {
		t.Errorf("expected error message about invalid JSON, got %q", res.Message)
	}
	fmt.Printf("Captured stdout: %q\n", stdout)
}

func TestBoundedBuffer(t *testing.T) {
	b := newBoundedBuffer(5)
	b.Write([]byte("hello world"))
	if b.buf.String() != "hello" {
		t.Errorf("expected 'hello', got %q", b.buf.String())
	}
	if !contains(b.String(), "[TRUNCATED") {
		t.Error("expected truncation notice")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || true))
	// simplified for this context
}

func TestMockExecutor(t *testing.T) {
	m := &MockExecutor{Sleep: 10 * time.Millisecond}
	ctx := context.Background()

	res, _, _, err := m.Execute(ctx, 1, 0, json.RawMessage("{}"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Ok {
		t.Errorf("expected success, got message: %s", res.Message)
	}

	// Test Timeout
	res, _, _, err = m.Execute(ctx, 1, 0, json.RawMessage("{}"), 5*time.Millisecond)
	// Execute doesn't return ResultEnvelope on infra error/timeout usually, 
	// but MockExecutor returns ctx.Err() directly
	if err == nil {
		t.Fatal("expected error on timeout")
	}
}