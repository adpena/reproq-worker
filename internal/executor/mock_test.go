package executor

import (
	"context"
	"testing"
	"time"
)

func TestMockExecution(t *testing.T) {
	exec := &MockExecutor{
		Sleep: 0,
	}

	ctx := context.Background()
	env, _, _, err := exec.Execute(ctx, 1, 1, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if env == nil || !env.Ok {
		t.Error("expected ok")
	}
}

func TestMockTimeout(t *testing.T) {
	exec := &MockExecutor{
		Sleep: 200 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, _, _, err := exec.Execute(ctx, 1, 1, nil, 50*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}
