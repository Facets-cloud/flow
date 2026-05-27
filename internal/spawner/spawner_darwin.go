//go:build darwin

package spawner

import (
	"flow/internal/ghostty"
	"flow/internal/iterm"
	"flow/internal/kitty"
	"flow/internal/terminal"
	"flow/internal/warp"
	"flow/internal/zellij"
	"os"
)

// Detect returns the backend that SpawnTab will use for the current
// process environment.
//
// Selection priority (highest first):
//
//	$ZELLIJ set                                    → internal/zellij
//	$KITTY_WINDOW_ID set or $TERM=xterm-kitty      → internal/kitty
//	$FLOW_TERM=<valid backend>                     → that backend (user override)
//	TERM_PROGRAM=WarpTerminal                      → internal/warp
//	TERM_PROGRAM=Apple_Terminal                    → internal/terminal
//	TERM_PROGRAM=iTerm.app                         → internal/iterm
//	TERM_PROGRAM=ghostty                           → internal/ghostty
//	anything else (or unset)                       → internal/iterm  (historical default)
//
// $ZELLIJ and kitty's per-window markers win over $FLOW_TERM because if
// the user is inside a session-manager terminal, that's where their
// workflow lives — the host terminal is a substrate detail.
func Detect() Backend {
	if Override != "" {
		return Override
	}
	if os.Getenv("ZELLIJ") != "" {
		return BackendZellij
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" {
		return BackendKitty
	}
	if v := os.Getenv("FLOW_TERM"); v != "" {
		switch Backend(v) {
		case BackendITerm, BackendTerminal, BackendZellij, BackendKitty, BackendWarp, BackendGhostty:
			return Backend(v)
		}
		// Unknown value falls through to TERM_PROGRAM detection.
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "Apple_Terminal":
		return BackendTerminal
	case "iTerm.app":
		return BackendITerm
	case "WarpTerminal":
		return BackendWarp
	case "ghostty":
		return BackendGhostty
	default:
		return BackendITerm
	}
}

// SpawnTab opens a tab in the auto-detected backend.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	switch Detect() {
	case BackendZellij:
		return zellij.SpawnTab(title, cwd, command, envVars)
	case BackendKitty:
		return kitty.SpawnTab(title, cwd, command, envVars)
	case BackendTerminal:
		return terminal.SpawnTab(title, cwd, command, envVars)
	case BackendWarp:
		return warp.SpawnTab(title, cwd, command, envVars)
	case BackendGhostty:
		return ghostty.SpawnTab(title, cwd, command, envVars)
	default:
		return iterm.SpawnTab(title, cwd, command, envVars)
	}
}

// FocusSession tries to focus an existing tab/pane running the named
// harness binary with the given session UUID. Returns (true, nil) on
// focus, (false, nil) if no matching tab is found in the active backend,
// and (false, err) only on a backend failure.
func FocusSession(sessionID, binary string) (bool, error) {
	switch Detect() {
	case BackendZellij:
		return zellij.FocusSession(sessionID, binary)
	case BackendKitty:
		return kitty.FocusSession(sessionID, binary)
	case BackendTerminal:
		return terminal.FocusSession(sessionID, binary)
	default:
		return iterm.FocusSession(sessionID, binary)
	}
}
