//go:build !windows

package claude

import "os/exec"

// runPS returns `ps -axo pid,command` output — one line per process,
// pid first, then the full command line. LiveSessionIDs scans this for
// claude invocations carrying --session-id / --resume.
func runPS() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}
