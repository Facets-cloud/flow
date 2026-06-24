//go:build windows

package app

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setDetached configures cmd so the child outlives the parent
// `flow do --auto` / `flow owner tick-due` process. On Windows there is
// no Setsid; the equivalent is creating the child in its own process
// group and detaching it from the parent's console so closing the
// launching terminal does not signal the child.
//
//   - CREATE_NEW_PROCESS_GROUP: the child is the root of a new group, so
//     a Ctrl-C/Ctrl-Break delivered to the parent's group is not
//     propagated to it.
//   - DETACHED_PROCESS: the child does not inherit the parent's console.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// stillActive is the exit code Windows reports for a process that has
// not terminated (STILL_ACTIVE / STATUS_PENDING).
const stillActive = 259

// processAliveImpl reports whether a process with the given pid is
// currently running. Windows has no signal-0 probe, so we open a
// limited-information handle and ask for the exit code: a live process
// reports STILL_ACTIVE. This is best-effort and matches the Unix
// contract's caveats — pid reuse, or a process that genuinely exited
// with code 259, can be misreported, which only affects stale-row
// reconciliation.
func processAliveImpl(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
