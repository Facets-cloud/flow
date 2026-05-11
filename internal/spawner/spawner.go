// Package spawner picks a terminal backend (zellij, iTerm2, or macOS
// Terminal.app) at runtime and forwards SpawnTab to it.
//
// Selection priority:
//
//	$ZELLIJ set                       → internal/zellij
//	TERM_PROGRAM=Apple_Terminal       → internal/terminal
//	TERM_PROGRAM=iTerm.app            → internal/iterm
//	anything else (or unset)          → internal/iterm  (historical default)
//
// $ZELLIJ wins over TERM_PROGRAM because if the user is inside a zellij
// session, that's where their workflow lives — the host terminal is a
// substrate detail.
//
// The Override var lets tests pin the backend deterministically without
// having to set TERM_PROGRAM via t.Setenv.
package spawner

import (
	"flow/internal/iterm"
	"flow/internal/terminal"
	"flow/internal/zellij"
	"os"
)

// Backend identifies which terminal app a SpawnTab call targets.
type Backend string

const (
	BackendITerm    Backend = "iterm"
	BackendTerminal Backend = "terminal"
	BackendZellij   Backend = "zellij"
)

// Override, if non-empty, forces a backend regardless of TERM_PROGRAM.
// Used by tests; production code should leave it as "".
var Override Backend

// Detect returns the backend that SpawnTab will use for the current
// process environment. Exposed so callers (and tests) can inspect the
// choice without spawning.
func Detect() Backend {
	if Override != "" {
		return Override
	}
	if os.Getenv("ZELLIJ") != "" {
		return BackendZellij
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "Apple_Terminal":
		return BackendTerminal
	case "iTerm.app":
		return BackendITerm
	default:
		return BackendITerm
	}
}

// SpawnTab opens a tab in the auto-detected backend. The contract
// matches both iterm.SpawnTab and terminal.SpawnTab.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	switch Detect() {
	case BackendZellij:
		return zellij.SpawnTab(title, cwd, command, envVars)
	case BackendTerminal:
		return terminal.SpawnTab(title, cwd, command, envVars)
	default:
		return iterm.SpawnTab(title, cwd, command, envVars)
	}
}

// FocusSession tries to focus an existing tab/pane that is already
// running `claude` with the given session UUID. Returns (true, nil)
// on focus, (false, nil) if no matching tab was found in the active
// backend, and (false, err) only on a backend failure.
//
// Callers should treat (false, nil) as "fall through" — typically by
// surfacing the existing "session running elsewhere" error so the
// user knows to switch manually or pass --force.
//
// Backend dispatch mirrors SpawnTab:
//   - Zellij: list-panes JSON match on pane_command + focus-pane-id
//   - Terminal.app: pid → tty via ps, then osascript walk
//   - iTerm2 (default): pid → tty via ps, then osascript walk
func FocusSession(sessionID string) (bool, error) {
	switch Detect() {
	case BackendZellij:
		return zellij.FocusSession(sessionID)
	case BackendTerminal:
		return terminal.FocusSession(sessionID)
	default:
		return iterm.FocusSession(sessionID)
	}
}

// NotifyFocused posts a macOS notification to tell the user the tab
// switch happened. Dispatched per backend so a future backend can
// override (e.g., a Linux-only one could use notify-send) without
// touching the rest of flow. All current backends delegate to
// internal/notify.MacOS, which respects FLOW_NOTIFY to skip when
// notifications are disabled.
func NotifyFocused(message string) error {
	switch Detect() {
	case BackendZellij:
		return zellij.NotifyFocused(message)
	case BackendTerminal:
		return terminal.NotifyFocused(message)
	default:
		return iterm.NotifyFocused(message)
	}
}

// ShellQuote is re-exported so callers don't need to import the chosen
// backend just to quote a value before handing it to SpawnTab. Both
// backends quote identically (POSIX single-quote with embedded-quote
// escape), so we delegate to iterm's implementation.
func ShellQuote(s string) string {
	return iterm.ShellQuote(s)
}
