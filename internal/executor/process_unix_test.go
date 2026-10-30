//go:build !windows

package executor

import (
	"os/signal"
	"syscall"
)

func ignoreTestTermination() { signal.Ignore(syscall.SIGTERM) }
