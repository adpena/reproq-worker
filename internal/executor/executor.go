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

// limitedBuffer is a simple wrapper around bytes.Buffer that caps the total size.
type limitedBuffer struct {
	bytes.Buffer
	cap int
}

func (l *limitedBuffer) Write(p []byte) (n int, err error) {
	left := l.cap - l.Len()
	if left <= 0 {
		return len(p), nil // Drop the data but report success to the caller
	}
	if len(p) > left {
		p = p[:left]
	}
	return l.Buffer.Write(p)
}

// Executor handles the execution of external task processes.
type Executor struct {
	// BaseCommand is the initial command to run, e.g., ["python", "-m", "myproject.task_executor"]
	BaseCommand []string
	MaxLogSize  int // Maximum bytes to capture for stdout/stderr
}

// New creates a new Executor.
func New(baseCommand []string) *Executor {
	return &Executor{
		BaseCommand: baseCommand,
		MaxLogSize:  1024 * 1024, // 1MB default
	}
}

// Execute runs the task with the given payload and timeout.
func (e *Executor) Execute(ctx context.Context, payload json.RawMessage, timeout time.Duration) (*Result, error) {
	// Create a context with timeout for the command execution
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append([]string{}, e.BaseCommand...)
	args = append(args, "--payload", string(payload))

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)

	stdoutBuf := &limitedBuffer{cap: e.MaxLogSize}
	stderrBuf := &limitedBuffer{cap: e.MaxLogSize}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	// Run the command
	err := cmd.Run()

	result := &Result{
		Stdout: stdoutBuf.String(),
		Stderr: stderrBuf.String(),
	}

	// Determine exit code
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitError.ExitCode()
		} else {
			// If it's not an ExitError (e.g. timeout killed it), exit code might be -1 or similar
			result.ExitCode = -1
		}
	} else {
		result.ExitCode = 0
	}

	// Attempt to parse the last line of stdout (or the whole thing) as the JSON result envelope.
	// Strategy: Try to unmarshal the whole stdout. If that fails, the executor might have printed logs.
	// A robust protocol is needed. For now, we try to unmarshal the Stdout.
	// If the process was successful (0 exit), we expect a JSON result.
	if result.ExitCode == 0 {
		// We expect the script to print valid JSON to stdout
		if jsonErr := json.Unmarshal(stdoutBuf.Bytes(), &result.JSONResult); jsonErr != nil {
			// If we can't parse stdout as JSON, treat it as a system error wrapped in JSON
			// or just leave JSONResult empty.
			// Let's create a wrapper error.
			errMsg := fmt.Sprintf("failed to parse result JSON: %v", jsonErr)
			errJSON, _ := json.Marshal(map[string]string{"error": errMsg, "raw_output": result.Stdout})
			result.ErrorJSON = errJSON
			// We might want to mark this as a failure even if exit code was 0?
			// Let's keep exit code 0 but indicate the data issue.
		}
	} else {
		// If failed, we try to see if stderr or stdout contained a structured error
		// Otherwise wrap stderr in a generic error JSON
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
