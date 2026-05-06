// Package spawner picks a terminal backend (iTerm2 or macOS Terminal.app)
// at runtime and forwards SpawnTab to it.
//
// Selection is driven by the TERM_PROGRAM env var that the host
// terminal sets in every shell it spawns:
//
//	TERM_PROGRAM=iTerm.app        → internal/iterm
//	TERM_PROGRAM=Apple_Terminal   → internal/terminal
//	anything else (or unset)      → internal/iterm  (historical default)
//
// The Override var lets tests pin the backend deterministically without
// having to set TERM_PROGRAM via t.Setenv. Existing tests that mock
// iterm.Runner continue to work unchanged because the default fallback
// is iTerm.
package spawner

import (
	"flow/internal/iterm"
	"flow/internal/terminal"
	"os"
)

// Backend identifies which terminal app a SpawnTab call targets.
type Backend string

const (
	BackendITerm    Backend = "iterm"
	BackendTerminal Backend = "terminal"
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
	case BackendTerminal:
		return terminal.SpawnTab(title, cwd, command, envVars)
	default:
		return iterm.SpawnTab(title, cwd, command, envVars)
	}
}

// ShellQuote is re-exported so callers don't need to import the chosen
// backend just to quote a value before handing it to SpawnTab. Both
// backends quote identically (POSIX single-quote with embedded-quote
// escape), so we delegate to iterm's implementation.
func ShellQuote(s string) string {
	return iterm.ShellQuote(s)
}
