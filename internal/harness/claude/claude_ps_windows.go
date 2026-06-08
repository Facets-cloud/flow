//go:build windows

package claude

import "os/exec"

// runPS on Windows uses PowerShell's Get-CimInstance Win32_Process to
// emit one process command line per row. LiveSessionIDs scans for
// rows containing "claude" and extracts session UUIDs via regex; the
// PID column is unused, so we don't bother emitting it. WMI access
// is granted to the current user for their own processes without
// elevation, which is the scope LiveSessionIDs needs.
//
// We pass -NoProfile so the call ignores the user's PowerShell
// profile (faster startup, no PATH/module surprises) and
// -NonInteractive so it never prompts for credentials.
func runPS() ([]byte, error) {
	return exec.Command(
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		"Get-CimInstance Win32_Process | Where-Object { $_.CommandLine } | Select-Object -ExpandProperty CommandLine",
	).Output()
}
