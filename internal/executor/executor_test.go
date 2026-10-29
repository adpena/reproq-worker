package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestShellExecutor_PayloadTooLarge(t *testing.T) {
	e := &ShellExecutor{
		MaxPayloadBytes: 10,
		MaxStdoutBytes:  1024,
		MaxStderrBytes:  1024,
	}
	payload := json.RawMessage(`{"too":"large"}`)
	_, _, _, err := e.Execute(context.Background(), testClaim(), payload, time.Second)
	if err == nil || err.Error() != "payload size 15 exceeds limit 10" {
		t.Errorf("expected payload too large error, got %v", err)
	}
}

func TestShellExecutor_InvalidJSON(t *testing.T) {
	e := helperExecutor(t, "invalid-json")
	res, _, _, err := e.Execute(context.Background(), testClaim(), json.RawMessage(`"not json"`), time.Second)
	if !errors.Is(err, ErrProtocol) || res != nil {
		t.Fatalf("expected a rejected protocol, got result=%+v err=%v", res, err)
	}
}

func TestBoundedBuffer(t *testing.T) {
	b := newBoundedBuffer(5)
	_, _ = b.Write([]byte("hello world"))
	if b.buf.String() != "hello" {
		t.Errorf("expected 'hello', got %q", b.buf.String())
	}
	if !strings.Contains(b.String(), "[TRUNCATED") {
		t.Error("expected truncation notice")
	}
}

func TestMockExecutor(t *testing.T) {
	m := &MockExecutor{Sleep: 10 * time.Millisecond}
	ctx := context.Background()

	res, _, _, err := m.Execute(ctx, testClaim(), json.RawMessage("{}"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Ok {
		t.Errorf("expected success, got message: %s", res.Message)
	}

	// Test Timeout
	_, _, _, err = m.Execute(ctx, testClaim(), json.RawMessage("{}"), 5*time.Millisecond)
	// Execute doesn't return ResultEnvelope on infra error/timeout usually,
	// but MockExecutor returns ctx.Err() directly
	if err == nil {
		t.Fatal("expected error on timeout")
	}
}

func testClaim() Claim {
	return Claim{ResultID: 1, Attempt: 1, WorkerID: "owned-test-worker", LeaseToken: "41332b05-43ce-49de-8b56-b68950ab401a"}
}
