// Package spawner picks a terminal backend (zellij, tmux, Rex, kitty, Warp,
// iTerm2, Ghostty, or macOS Terminal.app) at runtime and forwards SpawnTab to it.
//
// Selection priority (highest first):
//
//	$FLOW_TERM=<valid backend>                     → that backend (explicit user choice)
//	$ZELLIJ set                                    → internal/zellij
//	$TMUX set                                      → internal/tmux
//	$REX_SESSION set or TERM_PROGRAM=rex           → internal/rex
//	$KITTY_WINDOW_ID set or $TERM=xterm-kitty      → internal/kitty
//	TERM_PROGRAM=WarpTerminal                      → internal/warp
//	TERM_PROGRAM=Apple_Terminal                    → internal/terminal
//	TERM_PROGRAM=iTerm.app                         → internal/iterm
//	TERM_PROGRAM=ghostty                           → internal/ghostty
//	anything else (or unset)                       → internal/iterm  (historical default)
//
// An explicit $FLOW_TERM outranks every ambient marker: $ZELLIJ, $TMUX,
// Rex's and kitty's variables describe the environment flow happens to
// be running in, while $FLOW_TERM is the user saying which backend they
// want (tmux inside iTerm but tabs in iTerm; a shell script; Hyper,
// wezterm, …). Among the ambient markers the session managers win over
// $TERM_PROGRAM because if the user is inside one, that's where their
// workflow lives — the host terminal is a substrate detail. tmux and
// zellij are the only backends that work on Linux and macOS alike; the
// osascript backends are macOS-only and SpawnTab returns an error
// wrapping ErrNoTerminal when one is selected elsewhere, so callers can
// fall back (print the command, point at --here / --auto). Unknown
// $FLOW_TERM values (including bg, which IsBackground handles) are
// ignored and detection proceeds as if it were unset.
//
// The Override var lets tests pin the backend deterministically without
// having to set env vars via t.Setenv.
package spawner

import (
	"errors"
	"flow/internal/ghostty"
	"flow/internal/iterm"
	"flow/internal/kitty"
	"flow/internal/rex"
	"flow/internal/terminal"
	"flow/internal/tmux"
	"flow/internal/warp"
	"flow/internal/zellij"
	"fmt"
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
	BackendRex      Backend = "rex"
	BackendTmux     Backend = "tmux"
)

// ErrNoTerminal is wrapped by the error SpawnTab returns when the
// detected backend cannot open a tab on this host (a macOS-only backend
// on Linux). Callers test for it with errors.Is and fall back to
// printing the command for the user to run by hand.
var ErrNoTerminal = errors.New("no supported terminal detected")

// Override, if non-empty, forces a backend regardless of env vars.
// Used by tests; production code should leave it as "".
var Override Backend

// GOOS is the host OS used by the macOS-only backend guard. Defaults to
// runtime.GOOS; tests pin it to exercise the linux path on a Mac.
var GOOS = runtime.GOOS

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
	// Explicit user choice first — see the package comment.
	if v := os.Getenv("FLOW_TERM"); v != "" {
		switch Backend(v) {
		case BackendITerm, BackendTerminal, BackendZellij, BackendKitty, BackendWarp, BackendGhostty, BackendRex, BackendTmux:
			return Backend(v)
		}
		// Unknown value falls through to ambient detection.
	}
	if os.Getenv("ZELLIJ") != "" {
		return BackendZellij
	}
	if os.Getenv("TMUX") != "" {
		return BackendTmux
	}
	if os.Getenv("REX_SESSION") != "" || os.Getenv("TERM_PROGRAM") == "rex" {
		return BackendRex
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" {
		return BackendKitty
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
	return SpawnTabIn("", title, cwd, command, envVars)
}

// SpawnTabIn is SpawnTab with a target group: the Rex session label the
// tab should open in. Only the Rex backend has groups; every other
// backend ignores it. An empty group means "wherever flow ran from".
func SpawnTabIn(group, title, cwd, command string, envVars map[string]string) error {
	if err := unsupportedHere(); err != nil {
		return err
	}
	switch Detect() {
	case BackendZellij:
		return zellij.SpawnTab(title, cwd, command, envVars)
	case BackendTmux:
		return tmux.SpawnTab(title, cwd, command, envVars)
	case BackendKitty:
		return kitty.SpawnTab(title, cwd, command, envVars)
	case BackendRex:
		return rex.SpawnTabIn(group, title, cwd, command, envVars)
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
// Backend dispatch mirrors SpawnTab:
//   - Zellij: list-panes JSON match on pane_command + focus-pane-id
//   - tmux: pid → tty via ps, list-panes tty match, select-window/pane
//   - Kitty: `kitty @ ls` JSON match on foreground_processes cmdline + focus-window
//   - Terminal.app: pid → tty via ps, then osascript walk
//   - iTerm2 (default): pid → tty via ps, then osascript walk
func FocusSession(sessionID, binary string) (bool, error) {
	if unsupportedHere() != nil {
		return false, nil
	}
	switch Detect() {
	case BackendZellij:
		return zellij.FocusSession(sessionID, binary)
	case BackendTmux:
		return tmux.FocusSession(sessionID, binary)
	case BackendKitty:
		return kitty.FocusSession(sessionID, binary)
	case BackendRex:
		return rex.FocusSession(sessionID, binary)
	case BackendTerminal:
		return terminal.FocusSession(sessionID, binary)
	default:
		return iterm.FocusSession(sessionID, binary)
	}
}

// TerminalAvailable reports, without spawning anything, whether SpawnTab
// can open a tab on this host: nil when it can, an error wrapping
// ErrNoTerminal when auto-detection lands on a backend this host cannot
// drive (a macOS-only backend off macOS). Callers that must not leave
// state behind on a failed spawn (`flow do --auto --tab`) check it first.
func TerminalAvailable() error {
	return unsupportedHere()
}

// macOnly reports whether a backend drives its terminal via osascript and
// therefore only works on macOS.
func macOnly(b Backend) bool {
	switch b {
	case BackendITerm, BackendTerminal, BackendWarp, BackendGhostty:
		return true
	}
	return false
}

// unsupportedHere returns an actionable error when auto-detection lands on
// a macOS-only backend on a non-darwin host (e.g. Linux, where iTerm is
// still the fallback default). Override is test-only and bypasses the
// guard so tests can pin a mocked backend on any CI host.
func unsupportedHere() error {
	if Override != "" || GOOS == "darwin" {
		return nil
	}
	b := Detect()
	if !macOnly(b) {
		return nil
	}
	return fmt.Errorf("%w: the %q backend is macOS-only and this host is %s.\n"+
		"Run flow inside tmux, zellij or kitty (or set FLOW_TERM=tmux|zellij|kitty), or skip the tab:\n"+
		"  flow do --here <task>   bind the current session\n"+
		"  flow do --auto <task>   run headless in the background", ErrNoTerminal, b, GOOS)
}

// ShellQuote is re-exported so callers don't need to import the chosen
// backend just to quote a value before handing it to SpawnTab. All
// backends quote identically (POSIX single-quote with embedded-quote
// escape), so we delegate to iterm's implementation.
func ShellQuote(s string) string {
	return iterm.ShellQuote(s)
}
