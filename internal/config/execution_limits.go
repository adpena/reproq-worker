package config

import (
	"fmt"
	"time"
)

// ValidateExecutionLimits applies to configured and directly constructed executors.
func ValidateExecutionLimits(payloadBytes, stdoutBytes, stderrBytes int, timeout time.Duration) error {
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"max_payload_bytes", payloadBytes}, {"max_stdout_bytes", stdoutBytes}, {"max_stderr_bytes", stderrBytes},
	} {
		if limit.value <= 0 {
			return fmt.Errorf("worker.%s must be positive", limit.name)
		}
	}
	if stdoutBytes < 13 {
		return fmt.Errorf("worker.max_stdout_bytes must fit a complete failure envelope (at least 13 bytes)")
	}
	if timeout <= 0 {
		return fmt.Errorf("worker.exec_timeout must be positive")
	}
	return nil
}
