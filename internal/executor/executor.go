package executor

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"reproq-worker/internal/config"
)

var (
	ErrOutputLimit = errors.New("executor output exceeds byte limit")
	ErrProtocol    = errors.New("invalid executor result protocol")
	ErrProcessExit = errors.New("executor process failed")
)

// Bound pipe draining even if a descendant retains a standard-stream handle.
const processWaitDelay = time.Second

func validLeaseToken(token string) bool {
	if len(token) != 36 || token[8] != '-' || token[13] != '-' || token[18] != '-' || token[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(token, "-", ""))
	if err != nil || len(raw) != 16 {
		return false
	}
	for _, part := range raw {
		if part != 0 {
			return true
		}
	}
	return false
}

// ResultEnvelope matches the strict JSON protocol requirement.
type ResultEnvelope struct {
	Ok             bool            `json:"ok"`
	Return         json.RawMessage `json:"return,omitempty"`
	ExceptionClass string          `json:"exception_class,omitempty"`
	Traceback      string          `json:"traceback,omitempty"`
	Message        string          `json:"message,omitempty"`
}

type IExecutor interface {
	CheckCompatibility(context.Context) error
	Execute(ctx context.Context, claim Claim, payload json.RawMessage, timeout time.Duration) (*ResultEnvelope, string, string, error)
}

type Claim struct {
	ResultID   int64
	Attempt    int
	WorkerID   string
	LeaseToken string
}

type ShellExecutor struct {
	PythonBin       string
	ExecutorModule  string
	PayloadMode     string // "stdin", "file", "inline"
	MaxPayloadBytes int
	MaxStdoutBytes  int
	MaxStderrBytes  int
}

func (e *ShellExecutor) Execute(ctx context.Context, claim Claim, payload json.RawMessage, timeout time.Duration) (*ResultEnvelope, string, string, error) {
	if err := config.ValidateExecutionLimits(e.MaxPayloadBytes, e.MaxStdoutBytes, e.MaxStderrBytes, timeout); err != nil {
		return nil, "", "", err
	}
	if len(payload) > e.MaxPayloadBytes {
		return nil, "", "", fmt.Errorf("payload size %d exceeds limit %d", len(payload), e.MaxPayloadBytes)
	}
	if claim.ResultID <= 0 || claim.Attempt <= 0 || strings.TrimSpace(claim.WorkerID) == "" || !validLeaseToken(claim.LeaseToken) {
		return nil, "", "", errors.New("executor requires a positive task/attempt and fenced worker claim")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-m", e.ExecutorModule, "--result-id", fmt.Sprintf("%d", claim.ResultID), "--attempt", fmt.Sprintf("%d", claim.Attempt),
		"--worker-id", claim.WorkerID, "--lease-token", claim.LeaseToken,
		"--max-output-bytes", fmt.Sprint(e.MaxStdoutBytes), "--max-payload-bytes", fmt.Sprint(e.MaxPayloadBytes)}

	var tempFile *os.File
	var err error

	switch e.PayloadMode {
	case "stdin":
		args = append(args, "--payload-stdin")
	case "file":
		tempFile, err = os.CreateTemp("", fmt.Sprintf("reproq-payload-%d-*.json", claim.ResultID))
		if err != nil {
			return nil, "", "", fmt.Errorf("failed to create temp file: %w", err)
		}
		defer os.Remove(tempFile.Name())
		if _, err := tempFile.Write(payload); err != nil {
			_ = tempFile.Close()
			return nil, "", "", fmt.Errorf("failed to write payload to file: %w", err)
		}
		if err := tempFile.Close(); err != nil {
			return nil, "", "", fmt.Errorf("failed to close payload file: %w", err)
		}
		args = append(args, "--payload-file", tempFile.Name())
	case "inline":
		args = append(args, "--payload-json", string(payload))
	default:
		return nil, "", "", fmt.Errorf("invalid payload mode: %s", e.PayloadMode)
	}

	cmd := exec.CommandContext(ctx, e.PythonBin, args...)
	if e.PayloadMode == "stdin" {
		cmd.Stdin = bytes.NewReader(payload)
	}
	stdoutBuf, stderrBuf, waitErr := runBoundedProcess(ctx, cmd, e.MaxStdoutBytes, e.MaxStderrBytes)
	if ctx.Err() != nil {
		return nil, stdoutBuf.String(), stderrBuf.String(), ctx.Err()
	}

	stdoutBytes := bytes.TrimSpace(stdoutBuf.Bytes())
	stdoutStr := stdoutBuf.String()
	stderrStr := stderrBuf.String()
	if stdoutBuf.truncated {
		return nil, stdoutStr, stderrStr, fmt.Errorf("%w: retained_bytes=%d dropped_bytes=%d", ErrOutputLimit, stdoutBuf.buf.Len(), stdoutBuf.dropped())
	}
	env, parseErr := parseResult(stdoutBytes)
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) {
			return nil, stdoutStr, stderrStr, errors.Join(fmt.Errorf("executor wait failed: %w", waitErr), parseErr)
		}
		// A failure envelope can classify a task exception, but a successful
		// envelope can never turn a failed process into a successful task.
		if env != nil && env.Ok {
			env = nil
		}
		return env, stdoutStr, stderrStr, errors.Join(fmt.Errorf("%w: %w", ErrProcessExit, waitErr), parseErr)
	}
	return env, stdoutStr, stderrStr, parseErr
}

func parseProtocolObject(data []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: stdout is not UTF-8", ErrProtocol)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("%w: expected one JSON object", ErrProtocol)
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("%w: non-string field", ErrProtocol)
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrProtocol, name)
		}
		switch strings.ToLower(name) {
		case "ok", "return", "exception_class", "traceback", "message":
			if name != strings.ToLower(name) {
				return nil, fmt.Errorf("%w: noncanonical field %q", ErrProtocol, name)
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing output", ErrProtocol)
	}
	return fields, nil
}

func parseResult(data []byte) (*ResultEnvelope, error) {
	fields, err := parseProtocolObject(data)
	if err != nil {
		return nil, err
	}

	if string(fields["ok"]) != "true" && string(fields["ok"]) != "false" {
		return nil, fmt.Errorf("%w: boolean ok field required", ErrProtocol)
	}
	var env ResultEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return &env, nil
}

type boundedBuffer struct {
	buf       *bytes.Buffer
	cap       int
	truncated bool
	received  int64
}

func newBoundedBuffer(cap int) *boundedBuffer {
	return &boundedBuffer{buf: new(bytes.Buffer), cap: cap}
}

func (b *boundedBuffer) Write(p []byte) (n int, err error) {
	n = len(p)
	b.received += int64(n)
	limit := b.cap - b.buf.Len()
	if len(p) > limit {
		p = p[:limit]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	// Continue draining the child. Returning a short write here closes its pipe.
	return n, nil
}

func (b *boundedBuffer) String() string {
	// Native task output need not be valid UTF-8. Render a bounded, valid text
	// prefix while retaining exact raw-byte counts for overflow diagnostics.
	var rendered strings.Builder
	for remaining := b.Bytes(); len(remaining) > 0; {
		r, size := utf8.DecodeRune(remaining)
		if rendered.Len()+utf8.RuneLen(r) > b.cap {
			break
		}
		rendered.WriteRune(r)
		remaining = remaining[size:]
	}
	s := rendered.String()
	if b.truncated {
		s += fmt.Sprintf("\n[TRUNCATED retained_bytes=%d dropped_bytes=%d]", b.buf.Len(), b.dropped())
	}
	return s
}

func (b *boundedBuffer) dropped() int64 { return b.received - int64(b.buf.Len()) }

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

// Both startup probes and real task execution use this same bounded process-tree lifecycle.
func runBoundedProcess(ctx context.Context, cmd *exec.Cmd, stdoutLimit, stderrLimit int) (*boundedBuffer, *boundedBuffer, error) {
	stdout, stderr := newBoundedBuffer(stdoutLimit), newBoundedBuffer(stderrLimit)
	cmd.WaitDelay = processWaitDelay
	tree, err := newProcessTree(cmd)
	if err != nil {
		return stdout, stderr, err
	}
	defer tree.close()
	cmd.Cancel = tree.terminate
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := tree.start(); err != nil {
		return stdout, stderr, err
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return stdout, stderr, ctx.Err()
	}
	return stdout, stderr, err
}

type MockExecutor struct {
	Sleep time.Duration
}

func (m *MockExecutor) CheckCompatibility(context.Context) error { return nil }

func (m *MockExecutor) Execute(ctx context.Context, claim Claim, payload json.RawMessage, timeout time.Duration) (*ResultEnvelope, string, string, error) {

	ctx, cancel := context.WithTimeout(ctx, timeout)

	defer cancel()

	select {

	case <-time.After(m.Sleep):

		return &ResultEnvelope{Ok: true, Return: json.RawMessage(`{"status":"mocked"}`)}, "mock stdout", "mock stderr", nil

	case <-ctx.Done():

		return nil, "", "", ctx.Err()

	}

}
