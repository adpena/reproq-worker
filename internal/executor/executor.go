package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// Result holds the outcome of a task execution.
type Result struct {
	ExitCode    int             `json:"exit_code"`
	Stdout      string          `json:"stdout"`
	Stderr      string          `json:"stderr"`
	JSONResult  json.RawMessage `json:"result_json,omitempty"`
	ErrorJSON   json.RawMessage `json:"error_json,omitempty"`
}

// IExecutor defines the interface for task execution.
type IExecutor interface {
	Execute(ctx context.Context, payload json.RawMessage, timeout time.Duration) (*Result, error)
}

// limitedBuffer is a simple wrapper around bytes.Buffer that caps the total size.
type limitedBuffer struct {
	bytes.Buffer
	cap int
}

func (l *limitedBuffer) Write(p []byte) (n int, err error) {
	left := l.cap - l.Len()
	if left <= 0 {
		return len(p), nil
	}
	if len(p) > left {
		p = p[:left]
	}
	return l.Buffer.Write(p)
}

// ShellExecutor handles the execution of external task processes.
type ShellExecutor struct {
	BaseCommand []string
	MaxLogSize  int
}

func NewShellExecutor(baseCommand []string) *ShellExecutor {
	return &ShellExecutor{
		BaseCommand: baseCommand,
		MaxLogSize:  1024 * 1024,
	}
}

func (e *ShellExecutor) Execute(ctx context.Context, payload json.RawMessage, timeout time.Duration) (*Result, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string{}, e.BaseCommand...)
	args = append(args, "--payload", string(payload))

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)

	stdoutBuf := &limitedBuffer{cap: e.MaxLogSize}
	stderrBuf := &limitedBuffer{cap: e.MaxLogSize}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	err := cmd.Run()

	result := &Result{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitError.ExitCode()
		} else {
			result.ExitCode = -1
		}
	} else {
		result.ExitCode = 0
	}

	if result.ExitCode == 0 {
		if jsonErr := json.Unmarshal(stdoutBuf.Bytes(), &result.JSONResult); jsonErr != nil {
			errMsg := fmt.Sprintf("failed to parse result JSON: %v", jsonErr)
			errJSON, _ := json.Marshal(map[string]string{"error": errMsg, "raw_output": result.Stdout})
			result.ErrorJSON = errJSON
		}
	} else {
		errMap := map[string]string{
			"message": "Task execution failed",
			"stderr":  result.Stderr,
			"stdout":  result.Stdout,
		}
		if ctx.Err() == context.DeadlineExceeded {
			errMap["message"] = "Task execution timed out"
		}
		errJSON, _ := json.Marshal(errMap)
		result.ErrorJSON = errJSON
	}

	return result, nil
}

// MockExecutor stubs execution for benchmarking.
type MockExecutor struct {
	SleepDuration time.Duration
}

func NewMockExecutor(sleep time.Duration) *MockExecutor {
	return &MockExecutor{SleepDuration: sleep}
}

func (m *MockExecutor) Execute(ctx context.Context, payload json.RawMessage, timeout time.Duration) (*Result, error) {
	select {
	case <-time.After(m.SleepDuration):
		return &Result{
			ExitCode:   0,
			Stdout:     "mock success",
			JSONResult: json.RawMessage(`{"status":"ok"}`),
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return &Result{
			ExitCode:  -1,
			Stderr:    "timeout",
			ErrorJSON: json.RawMessage(`{"error":"timeout"}`),
		}, nil
	}
}