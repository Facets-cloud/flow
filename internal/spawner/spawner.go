// Package spawner picks a terminal backend at runtime and forwards
// SpawnTab / FocusSession calls to it.
//
// Backend selection differs by OS — see spawner_darwin.go,
// spawner_windows.go, and spawner_unix.go for the per-OS Detect()
// implementations. Common to all platforms:
//
//	$ZELLIJ set                                    → internal/zellij
//	$KITTY_WINDOW_ID set or $TERM=xterm-kitty      → internal/kitty
//	$FLOW_TERM=<valid backend>                     → that backend (user override)
//
// Beyond those shared rules, each OS has its own native defaults:
//
//   - darwin: TERM_PROGRAM detection (iTerm.app, Apple_Terminal,
//     WarpTerminal, ghostty) with iterm as the historical fallback.
//   - windows: defaults to Windows Terminal (internal/wt) when no
//     cross-platform marker is set.
//   - other (linux/bsd): zellij/kitty only; falls back to zellij if
//     no marker is set. (Native linux terminal backends are a future
//     extension.)
//
// The Override var lets tests pin the backend deterministically without
// having to set env vars via t.Setenv.
package spawner

import "strings"

// Backend identifies which terminal app a SpawnTab call targets.
type Backend string

const (
	BackendITerm    Backend = "iterm"
	BackendTerminal Backend = "terminal"
	BackendZellij   Backend = "zellij"
	BackendKitty    Backend = "kitty"
	BackendWarp     Backend = "warp"
	BackendGhostty  Backend = "ghostty"
	BackendWT       Backend = "wt" // Windows Terminal
)

// Override, if non-empty, forces a backend regardless of env vars.
// Used by tests; production code should leave it as "".
var Override Backend

// ShellQuote wraps s in POSIX single-quotes with proper escaping.
// Used by callers that need to quote a value before handing it to
// SpawnTab. Every POSIX-shell backend quotes identically; the
// Windows Terminal backend ignores quoting because it builds its
// own argv slice rather than typing into a shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
