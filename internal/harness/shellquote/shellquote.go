// Package shellquote provides POSIX single-quote escaping for values
// embedded in shell command strings. It is a leaf package with zero
// non-stdlib imports, extracted from the spawner/iterm backend so that
// harness adapters (claude, praxis, …) can quote launch-command
// arguments without transitively importing the terminal-spawning layer
// (~1,260 lines of macOS AppleScript/exec across six backends).
package shellquote

import "strings"

// Quote wraps s in single quotes, escaping embedded single quotes via
// the standard POSIX '\” sequence. All terminal backends in spawner/
// quote identically, so this is the canonical implementation.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
