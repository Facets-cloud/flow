//go:build !windows

package claude

import "os/exec"

// runPS returns the output of `ps -axo pid,command` for use by
// LiveSessionIDs. The PID column is not consumed — only the command
// column matters for regex-scanning UUIDs — but we include both
// because the literal `ps -axo pid,command` form is what the
// equivalent macOS spawn backends also use.
func runPS() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}
