//go:build windows

package spawner

import (
	"flow/internal/wt"
	"os"
)

// Detect returns the backend that SpawnTab will use on Windows.
//
// Selection priority:
//
//	Override                                       → Override (test escape hatch)
//	$FLOW_TERM=wt                                  → internal/wt
//	default                                        → internal/wt (Windows Terminal)
//
// Only Windows Terminal (wt.exe) is supported as a native Windows
// backend in v1. $ZELLIJ and $KITTY_WINDOW_ID are not honored on
// Windows — those terminals don't run natively here, and a value
// leaking in from a WSL session would target the WSL environment,
// not Windows-native flow.
func Detect() Backend {
	if Override != "" {
		return Override
	}
	if v := os.Getenv("FLOW_TERM"); v != "" {
		switch Backend(v) {
		case BackendWT:
			return Backend(v)
		}
		// Unknown value falls through to the default.
	}
	return BackendWT
}

// SpawnTab opens a tab in the auto-detected backend.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	switch Detect() {
	default:
		return wt.SpawnTab(title, cwd, command, envVars)
	}
}

// FocusSession tries to focus an existing tab running the named harness
// binary with the given session UUID.
func FocusSession(sessionID, binary string) (bool, error) {
	switch Detect() {
	default:
		return wt.FocusSession(sessionID, binary)
	}
}
