// Package shellquote provides POSIX single-quote escaping for values
// embedded in shell command strings.
//
// It is a leaf package with zero non-stdlib imports, and it is the ONLY
// implementation in the tree: every terminal backend (iterm, terminal,
// warp, zellij, kitty, ghostty) and every harness adapter (claude,
// praxis) quotes through it. Keeping it out of both internal/harness and
// internal/spawner is deliberate — a harness adapter must be able to
// quote a launch-command argument without transitively importing the
// terminal-spawning layer (~1,260 lines of macOS AppleScript/exec), and
// a terminal backend must not import the harness layer.
package shellquote

import "strings"

// Quote wraps s in single quotes, rewriting each embedded single quote
// as the standard POSIX escape sequence:
//
//	'\''
//
// Everything else — spaces, $VAR, backticks, backslashes — is inert
// inside single quotes and passes through untouched.
//
// (The sequence is shown in an indented block on purpose: in ordinary doc
// prose gofmt rewrites two consecutive apostrophes into a typographic
// close-quote, which would corrupt it.)
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
