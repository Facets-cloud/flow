// Package spawner picks a terminal backend (zellij, kitty, iTerm2, or
// macOS Terminal.app) at runtime and forwards SpawnTab to it.
//
// Selection priority:
//
//	$ZELLIJ set                                    → internal/zellij
//	$KITTY_WINDOW_ID set or $TERM=xterm-kitty      → internal/kitty
//	TERM_PROGRAM=Apple_Terminal                    → internal/terminal
//	TERM_PROGRAM=iTerm.app                         → internal/iterm
//	anything else (or unset)                       → internal/iterm  (historical default)
//
// $ZELLIJ wins over everything because if the user is inside a zellij
// session, that's where their workflow lives — the host terminal is a
// substrate detail. Kitty sits same tier as zellij (explicit
// per-window session marker set by the terminal itself) and beats the
// TERM_PROGRAM fallback because kitty does not set TERM_PROGRAM.
//
// The Override var lets tests pin the backend deterministically without
// having to set env vars via t.Setenv.
package spawner

import (
	"flow/internal/iterm"
	"flow/internal/kitty"
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
	BackendKitty    Backend = "kitty"
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
// matches iterm.SpawnTab, terminal.SpawnTab, zellij.SpawnTab, and
// kitty.SpawnTab.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	switch Detect() {
	case BackendZellij:
		return zellij.SpawnTab(title, cwd, command, envVars)
	case BackendKitty:
		return kitty.SpawnTab(title, cwd, command, envVars)
	case BackendTerminal:
		return terminal.SpawnTab(title, cwd, command, envVars)
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
