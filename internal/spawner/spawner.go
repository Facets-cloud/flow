// Package spawner picks a terminal backend (zellij, kitty, Warp, iTerm2,
// Ghostty, or macOS Terminal.app) at runtime and forwards SpawnTab to it.
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
	"flow/internal/ghostty"
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
	BackendGhostty  Backend = "ghostty"
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
// Backend dispatch mirrors SpawnTab — every backend is matched
// explicitly:
//   - Zellij: list-panes JSON match on pane_command + focus-pane-id
//   - Kitty: `kitty @ ls` JSON match on foreground_processes cmdline + focus-window
//   - Terminal.app: pid → tty via ps, then osascript walk
//   - iTerm2: pid → tty via ps, then osascript walk
//   - Warp: activates the app, always reports a miss (no scripting surface)
//   - Ghostty: activates the app, always reports a miss (no tty in its sdef)
//
// The unknown-backend case returns (false, nil) — "no matching tab" —
// rather than falling through to iTerm2. An earlier version used
// `default: iterm.FocusSession(...)`, which meant Warp and Ghostty
// (whose Backend constants exist and whose SpawnTab paths are fully
// implemented) silently drove iTerm2's AppleScript dictionary. On a
// machine without iTerm2 installed that raises an osascript error, or
// worse prompts the user to locate the application; on a machine with
// iTerm2 it could focus an unrelated iTerm2 tab that happens to match
// the tty. Dispatching every known backend explicitly and treating
// unknown ones as a miss keeps a new Backend constant from silently
// inheriting the wrong implementation again.
// A session lives in whichever terminal spawned it, which is NOT
// necessarily the backend Detect() picks. Detect() answers "where would
// a new tab go", driven by the ambient environment — and $FLOW_TERM in
// particular is a *spawn* preference that outranks $TERM_PROGRAM. So a
// user with FLOW_TERM=iterm who still has older tabs in Terminal.app
// would have every focus attempt aimed at iTerm2, missing tabs that
// plainly exist elsewhere.
//
// Focus therefore follows the TAB, not the environment: try the detected
// backend first (the common case, and the cheapest), then fall back to
// every other backend until one claims the session. Each backend matches
// on the session's controlling tty, so a backend that doesn't host the
// tab reports a miss rather than focusing something wrong — which makes
// the sweep safe.
//
// Warp and Ghostty are skipped in the fallback sweep despite being
// probed first when detected: neither can select a specific tab, and
// both have the side effect of foregrounding their app. Running them
// speculatively would yank focus to an app that cannot complete the job.
func FocusSession(sessionID, binary string) (bool, error) {
	detected := Detect()
	focused, err := focusVia(detected, sessionID, binary)
	if focused {
		return true, nil
	}
	// Only the detected backend's error is worth surfacing: it's the one
	// the user is actually sitting in, so a genuine failure there (a
	// broken osascript, a dead zellij socket) is real signal. Errors from
	// the speculative sweep below are not — "kitty: executable file not
	// found" just means the user doesn't have kitty, which says nothing
	// about whether the session was found.
	firstErr := err

	for _, b := range []Backend{BackendITerm, BackendTerminal, BackendKitty, BackendZellij} {
		if b == detected {
			continue // already tried
		}
		if focused, _ := focusVia(b, sessionID, binary); focused {
			return true, nil
		}
	}
	return false, firstErr
}

// focusVia dispatches to one backend's FocusSession.
func focusVia(b Backend, sessionID, binary string) (bool, error) {
	switch b {
	case BackendZellij:
		return zellij.FocusSession(sessionID, binary)
	case BackendKitty:
		return kitty.FocusSession(sessionID, binary)
	case BackendTerminal:
		return terminal.FocusSession(sessionID, binary)
	case BackendWarp:
		return warp.FocusSession(sessionID, binary)
	case BackendGhostty:
		return ghostty.FocusSession(sessionID, binary)
	case BackendITerm:
		return iterm.FocusSession(sessionID, binary)
	default:
		return false, nil
	}
}

// ShellQuote is re-exported so callers don't need to import the chosen
// backend just to quote a value before handing it to SpawnTab. All
// backends quote identically (POSIX single-quote with embedded-quote
// escape), so we delegate to iterm's implementation.
func ShellQuote(s string) string {
	return iterm.ShellQuote(s)
}
