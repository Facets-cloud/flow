// Package spawner picks a terminal backend (zellij, kitty, Warp, iTerm2,
// or macOS Terminal.app) at runtime and forwards SpawnTab to it.
//
// Selection priority (highest first):
//
//	$ZELLIJ set                                    → internal/zellij
//	$KITTY_WINDOW_ID set or $TERM=xterm-kitty      → internal/kitty
//	$FLOW_TERM=<valid backend>                     → that backend (user override)
//	TERM_PROGRAM=WarpTerminal                      → internal/warp
//	TERM_PROGRAM=Apple_Terminal                    → internal/terminal
//	TERM_PROGRAM=iTerm.app                         → internal/iterm
//	anything else (or unset)                       → internal/iterm  (historical default)
//
// $ZELLIJ and kitty's per-window markers win over $FLOW_TERM because if
// the user is inside a session-manager terminal, that's where their
// workflow lives — the host terminal is a substrate detail. $FLOW_TERM
// lets users on non-standard hosts (tmux inside Warp, shell-script
// invocations, Hyper, wezterm, etc.) opt into a specific backend
// without relying on TERM_PROGRAM. Unknown values silently fall
// through to TERM_PROGRAM detection.
//
// The Override var lets tests pin the backend deterministically without
// having to set env vars via t.Setenv.
package spawner

import (
	"flow/internal/iterm"
	"flow/internal/kitty"
	"flow/internal/terminal"
	"flow/internal/warp"
	"flow/internal/zellij"
	"os"
)

// Backend identifies which terminal app a SpawnTab call targets.
type Backend string

const (
	BackendITerm    Backend = "iterm"
	BackendTerminal Backend = "terminal"
	BackendZellij   Backend = "zellij"
	BackendKitty    Backend = "kitty"
	BackendWarp     Backend = "warp"
)

// Override, if non-empty, forces a backend regardless of env vars.
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
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" {
		return BackendKitty
	}
	if v := os.Getenv("FLOW_TERM"); v != "" {
		switch Backend(v) {
		case BackendITerm, BackendTerminal, BackendZellij, BackendKitty, BackendWarp:
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
	default:
		return BackendITerm
	}
}

// SpawnTab opens a tab in the auto-detected backend. The contract
// matches every backend's SpawnTab.
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
	default:
		return iterm.SpawnTab(title, cwd, command, envVars)
	}
}

// ShellQuote is re-exported so callers don't need to import the chosen
// backend just to quote a value before handing it to SpawnTab. All
// backends quote identically (POSIX single-quote with embedded-quote
// escape), so we delegate to iterm's implementation.
func ShellQuote(s string) string {
	return iterm.ShellQuote(s)
}
