//go:build windows

package executor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processTree struct {
	cmd *exec.Cmd
	mu  sync.Mutex
	job windows.Handle
}

func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create executor job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure executor job: %w", err)
	}
	// Assignment must precede any user code, including descendant creation.
	// Go closes the initial thread handle, so resume it through a thread snapshot.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
	return &processTree{cmd: cmd, job: job}, nil
}

func (p *processTree) start() error {
	p.mu.Lock()
	err := p.cmd.Start()
	if err == nil {
		err = p.assignAndResume()
		if err != nil {
			_ = windows.TerminateJobObject(p.job, 1)
			_ = p.cmd.Process.Kill()
		}
	}
	p.mu.Unlock()
	if err != nil && p.cmd.Process != nil {
		// Do not hold mu while Wait joins the context-cancellation goroutine.
		_ = p.cmd.Wait()
	}
	return err
}

func (p *processTree) assignAndResume() error {
	pid := uint32(p.cmd.Process.Pid)
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open suspended executor: %w", err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(p.job, process); err != nil {
		return fmt.Errorf("assign executor job: %w", err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot suspended executor threads: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("open suspended executor thread: %w", err)
		}
		count, err := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if err != nil {
			return fmt.Errorf("resume executor thread: %w", err)
		}
		if count != 1 {
			return fmt.Errorf("unexpected executor suspend count: %d", count)
		}
		return nil
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return errors.New("suspended executor thread not found")
	}
	return fmt.Errorf("enumerate suspended executor threads: %w", err)
}

func (p *processTree) terminate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return windows.TerminateJobObject(p.job, 1)
}

func (p *processTree) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		_ = windows.TerminateJobObject(p.job, 1)
		_ = windows.CloseHandle(p.job)
		p.job = 0
	}
}
