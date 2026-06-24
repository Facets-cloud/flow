//go:build !windows

package app

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setDetached configures cmd so the child outlives the parent
// `flow do --auto` / `flow owner tick-due` process. On Unix this means
// a new session (Setsid) — detached from the parent's controlling
// terminal, reparented to init when the parent exits.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processAliveImpl reports whether a process with the given pid is
// currently running. Used for read-time reconciliation of stale
// 'running' / tick rows. Signal 0 performs error checking without
// delivering a signal: nil → alive and ours; EPERM → alive but owned
// by another user.
func processAliveImpl(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
