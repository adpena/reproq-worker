//go:build !windows

package executor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type processTree struct{ cmd *exec.Cmd }

func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processTree{cmd: cmd}, nil
}

func (p *processTree) start() error { return p.cmd.Start() }

func (p *processTree) terminate() error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	// Setpgid makes the original PID the group ID. Do not look it up after
	// Wait reaps the leader: descendants can retain that group and its pipes.
	group := -p.cmd.Process.Pid
	err := syscall.Kill(group, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	time.Sleep(100 * time.Millisecond)
	killErr := syscall.Kill(group, syscall.SIGKILL)
	if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		return killErr
	}
	return err
}

func (p *processTree) close() {
	if p.cmd.Process != nil {
		// A task may finish successfully while leaving a descendant behind.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}
