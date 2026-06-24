// Package spawner picks a terminal backend (zellij, kitty, Warp, iTerm2,
// Ghostty, macOS Terminal.app, or Windows Terminal) at runtime and
// forwards SpawnTab to it.
//
// Selection priority (highest first):
//
//	$ZELLIJ set                                    → internal/zellij
//	$KITTY_WINDOW_ID set or $TERM=xterm-kitty      → internal/kitty
//	$FLOW_TERM=<valid backend>                     → that backend (user override)
//	GOOS=windows                                   → internal/winterm  (Windows Terminal)
//	TERM_PROGRAM=WarpTerminal                      → internal/warp
//	TERM_PROGRAM=Apple_Terminal                    → internal/terminal
//	TERM_PROGRAM=iTerm.app                         → internal/iterm
//	TERM_PROGRAM=ghostty                           → internal/ghostty
//	anything else (or unset)                       → internal/iterm  (historical macOS default)
//
// $ZELLIJ and kitty's per-window markers win over $FLOW_TERM because if
// the user is inside a session-manager terminal, that's where their
// workflow lives — the host terminal is a substrate detail. $FLOW_TERM
// lets users on non-standard hosts (tmux inside Warp, shell-script
// invocations, Hyper, wezterm, etc.) opt into a specific backend
// without relying on TERM_PROGRAM. On Windows the default is Windows
// Terminal (the macOS TERM_PROGRAM ladder and its iTerm fallback never
// apply); zellij/kitty/$FLOW_TERM still win if set. Unknown $FLOW_TERM
// values silently fall through to the OS default.
//
// The Override var lets tests pin the backend deterministically without
// having to set env vars via t.Setenv.
package spawner

import (
	"flow/internal/ghostty"
	"flow/internal/iterm"
	"flow/internal/kitty"
	"flow/internal/terminal"
	"flow/internal/warp"
	"flow/internal/winterm"
	"flow/internal/zellij"
	"os"
	"runtime"
)

// Backend identifies which terminal app a SpawnTab call targets.
type Backend string

const (
	BackendITerm    Backend = "iterm"
	BackendTerminal Backend = "terminal"
	BackendZellij   Backend = "zellij"
	BackendKitty    Backend = "kitty"
	BackendWarp     Backend = "warp"
	BackendGhostty  Backend = "ghostty"
	BackendWinTerm  Backend = "winterm"
)

// Override, if non-empty, forces a backend regardless of env vars.
// Used by tests; production code should leave it as "".
var Override Backend

// BackgroundOverride, if non-nil, forces IsBackground's result
// regardless of $FLOW_TERM. Used by tests; production code leaves it nil.
var BackgroundOverride *bool

// IsBackground reports whether flow should spawn this session as a
// terminal-free background agent ($FLOW_TERM=bg) rather than opening a
// terminal tab. bg mode is NOT a terminal backend — it bypasses SpawnTab
// entirely (see do.go's bg branch), so it lives in its own predicate
// rather than as a Detect() case. The match is exact and case-sensitive,
// mirroring Detect's $FLOW_TERM handling.
func IsBackground() bool {
	if BackgroundOverride != nil {
		return *BackgroundOverride
	}
	return os.Getenv("FLOW_TERM") == "bg"
}

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
		case BackendITerm, BackendTerminal, BackendZellij, BackendKitty, BackendWarp, BackendGhostty, BackendWinTerm:
			return Backend(v)
		}
		// Unknown value falls through to OS-default detection.
	}
	// On Windows the macOS TERM_PROGRAM ladder (and its iTerm fallback)
	// never applies — default to Windows Terminal.
	if runtime.GOOS == "windows" {
		return BackendWinTerm
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
	case BackendGhostty:
		return ghostty.SpawnTab(title, cwd, command, envVars)
	case BackendWinTerm:
		return winterm.SpawnTab(title, cwd, command, envVars)
	default:
		return iterm.SpawnTab(title, cwd, command, envVars)
	}
}

// FocusSession tries to focus an existing tab/pane that is already
// running the named harness binary with the given session UUID. The
// `binary` arg is the harness's executable name (e.g. "claude",
// "codex", "gemini") — backends use it to filter the process table
// down to relevant rows. Returns (true, nil) on focus, (false, nil)
// if no matching tab was found in the active backend, and
// (false, err) only on a backend failure.
//
// Callers should treat (false, nil) as "fall through" — typically by
// surfacing the existing "session running elsewhere" error so the
// user knows to switch manually or pass --force.
//
// Backend dispatch mirrors SpawnTab:
//   - Zellij: list-panes JSON match on pane_command + focus-pane-id
//   - Kitty: `kitty @ ls` JSON match on foreground_processes cmdline + focus-window
//   - Terminal.app: pid → tty via ps, then osascript walk
//   - iTerm2 (default): pid → tty via ps, then osascript walk
func FocusSession(sessionID, binary string) (bool, error) {
	switch Detect() {
	case BackendZellij:
		return zellij.FocusSession(sessionID, binary)
	case BackendKitty:
		return kitty.FocusSession(sessionID, binary)
	case BackendTerminal:
		return terminal.FocusSession(sessionID, binary)
	case BackendWinTerm:
		return winterm.FocusSession(sessionID, binary)
	default:
		return iterm.FocusSession(sessionID, binary)
	}
}

// ShellQuote is re-exported so callers don't need to import the chosen
// backend just to quote a value before handing it to SpawnTab. The
// quoting style depends on the shell the spawned tab runs, which is
// platform-specific: POSIX single-quote on Unix (shellquote_unix.go),
// PowerShell single-quote on Windows (shellquote_windows.go).
