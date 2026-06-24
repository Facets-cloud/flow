//go:build windows

package claude

import "os/exec"

// runPS returns process-table output on Windows in the same shape the
// Unix `ps -axo pid,command` produces: one line per process, the pid
// first, then the full command line. There is no `ps` on Windows, so we
// query Win32_Process via PowerShell CIM, which exposes CommandLine
// (the only source that includes claude's --session-id / --resume
// flags; tasklist does not).
//
// The format string uses single quotes ('{0} {1}') to avoid embedded
// double quotes, so the whole expression survives being passed as a
// single -Command argument without extra escaping. Win32_Process.
// CommandLine is readable for the user's own processes without
// elevation — sufficient for detecting flow's own claude sessions.
func runPS() ([]byte, error) {
	const script = `Get-CimInstance Win32_Process | ForEach-Object { '{0} {1}' -f $_.ProcessId, $_.CommandLine }`
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
}
