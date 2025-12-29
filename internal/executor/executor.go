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

// Executor handles the execution of external task processes.
type Executor struct {
	// BaseCommand is the initial command to run, e.g., ["python", "-m", "myproject.task_executor"]
	BaseCommand []string
}

// New creates a new Executor.
func New(baseCommand []string) *Executor {
	return &Executor{
		BaseCommand: baseCommand,
	}
}

// Execute runs the task with the given payload and timeout.
func (e *Executor) Execute(ctx context.Context, payload json.RawMessage, timeout time.Duration) (*Result, error) {
	// Create a context with timeout for the command execution
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Prepare the command arguments
	// We assume the interface is: python -m ... --payload <json_string>
	// or passing via stdin. Passing large JSON via arg can hit shell limits.
	// For this implementation, let's assume passing via a flag for simplicity, 
	// but production might prefer stdin or a temp file.
	// Let's go with passing as a string argument for now, based on "payload-json" requirement.
	
	args := append([]string{}, e.BaseCommand...)
	args = append(args, "--payload", string(payload))

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

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
