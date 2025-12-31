package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// ResultEnvelope matches the strict JSON protocol requirement.
type ResultEnvelope struct {
	Ok             bool            `json:"ok"`
	Return         json.RawMessage `json:"return,omitempty"`
	ExceptionClass string          `json:"exception_class,omitempty"`
	Traceback      string          `json:"traceback,omitempty"`
	Message        string          `json:"message,omitempty"`
}

type IExecutor interface {
	Execute(ctx context.Context, resultID int64, attempt int, payload json.RawMessage, timeout time.Duration) (*ResultEnvelope, string, string, error)
}

type ShellExecutor struct {
	PythonBin       string
	ExecutorModule  string
	PayloadMode     string // "stdin", "file", "inline"
	MaxPayloadBytes int
	MaxStdoutBytes  int
	MaxStderrBytes  int
}

func (e *ShellExecutor) Execute(ctx context.Context, resultID int64, attempt int, payload json.RawMessage, timeout time.Duration) (*ResultEnvelope, string, string, error) {
	if len(payload) > e.MaxPayloadBytes {
		return nil, "", "", fmt.Errorf("payload size %d exceeds limit %d", len(payload), e.MaxPayloadBytes)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-m", e.ExecutorModule, "--result-id", fmt.Sprintf("%d", resultID), "--attempt", fmt.Sprintf("%d", attempt)}

	var tempFile *os.File
	var err error

	switch e.PayloadMode {
	case "stdin":
		args = append(args, "--payload-stdin")
	case "file":
		tempFile, err = os.CreateTemp("", fmt.Sprintf("reproq-payload-%d-*.json", resultID))
		if err != nil {
			return nil, "", "", fmt.Errorf("failed to create temp file: %w", err)
		}
		defer os.Remove(tempFile.Name())
		if _, err := tempFile.Write(payload); err != nil {
			return nil, "", "", fmt.Errorf("failed to write payload to file: %w", err)
		}
		tempFile.Close()
		args = append(args, "--payload-file", tempFile.Name())
	case "inline":
		args = append(args, "--payload-json", string(payload))
	default:
		return nil, "", "", fmt.Errorf("invalid payload mode: %s", e.PayloadMode)
	}

	cmd := exec.CommandContext(ctx, e.PythonBin, args...)
	setProcessGroup(cmd)

	var stdinPipe io.WriteCloser
	if e.PayloadMode == "stdin" {
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			return nil, "", "", err
		}
	}

	stdoutBuf := newBoundedBuffer(e.MaxStdoutBytes)
	stderrBuf := newBoundedBuffer(e.MaxStderrBytes)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, "", "", err
	}

	if e.PayloadMode == "stdin" {
		go func() {
			defer stdinPipe.Close()
			stdinPipe.Write(payload)
		}()
	}

	err = cmd.Wait()

	// Process Cleanup
	if ctx.Err() == context.DeadlineExceeded {
		terminateProcessGroup(cmd)
		return nil, stdoutBuf.String(), stderrBuf.String(), context.DeadlineExceeded
	}

	stdoutBytes := bytes.TrimSpace(stdoutBuf.Bytes())
	stdoutStr := string(stdoutBytes)
	stderrStr := stderrBuf.String()

	// Strict JSON Parsing
	var env ResultEnvelope
	if err := json.Unmarshal(stdoutBytes, &env); err != nil {
		return &ResultEnvelope{
				Ok:      false,
				Message: fmt.Sprintf("Invalid JSON on stdout: %v", err),
			},
			stdoutStr, stderrStr, nil
	}

	return &env, stdoutStr, stderrStr, nil
}

type boundedBuffer struct {
	buf       *bytes.Buffer
	cap       int
	truncated bool
}

func newBoundedBuffer(cap int) *boundedBuffer {
	return &boundedBuffer{buf: new(bytes.Buffer), cap: cap}
}

func (b *boundedBuffer) Write(p []byte) (n int, err error) {
	if b.buf.Len() >= b.cap {
		b.truncated = true
		return len(p), nil
	}
	limit := b.cap - b.buf.Len()
	if len(p) > limit {
		p = p[:limit]
		b.truncated = true
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) String() string {
	s := b.buf.String()
	if b.truncated {
		s += "\n[TRUNCATED due to size limit]"
	}
	return s
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

type MockExecutor struct {
	Sleep time.Duration
}

func (m *MockExecutor) Execute(ctx context.Context, resultID int64, attempt int, payload json.RawMessage, timeout time.Duration) (*ResultEnvelope, string, string, error) {

	ctx, cancel := context.WithTimeout(ctx, timeout)

	defer cancel()

	select {

	case <-time.After(m.Sleep):

		return &ResultEnvelope{Ok: true, Return: json.RawMessage(`{"status":"mocked"}`)}, "mock stdout", "mock stderr", nil

	case <-ctx.Done():

		return nil, "", "", ctx.Err()

	}

}
