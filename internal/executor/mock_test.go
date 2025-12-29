package executor

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// MockExecutor implements IExecutor for testing
type MockExecutor struct {
	Response DelayResponse
}

type DelayResponse struct {
	Env     *ExecutionEnvelope
	Delay   time.Duration
	ShouldFail bool
}

func (m *MockExecutor) Execute(ctx context.Context, resultID int64, attempt int, payload json.RawMessage, timeout time.Duration) (*ExecutionEnvelope, string, string, error) {
	select {
	case <-time.After(m.Delay):
		if m.ShouldFail {
			return nil, "", "", context.DeadlineExceeded
		}
		return m.Response.Env, "mock stdout", "mock stderr", nil
	case <-ctx.Done():
		return nil, "", "", ctx.Err()
	}
}

func TestMockExecution(t *testing.T) {
	exec := &MockExecutor{
		Response: DelayResponse{
			Env: &ExecutionEnvelope{Ok: true, Return: json.RawMessage(`1`)},
		},
	}

	ctx := context.Background()
	env, _, _, err := exec.Execute(ctx, 1, 1, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !env.Ok {
		t.Error("expected ok")
	}
}

func TestMockTimeout(t *testing.T) {
	exec := &MockExecutor{
		Response: DelayResponse{
			Delay: 2 * time.Second,
			ShouldFail: true,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, _, _, err := exec.Execute(ctx, 1, 1, nil, 100*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}
