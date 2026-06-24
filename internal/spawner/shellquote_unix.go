//go:build !windows

package spawner

import "flow/internal/iterm"

// ShellQuote quotes for the POSIX shell that the spawned tab runs on
// Unix. All Unix backends (iterm/terminal/zellij/warp/ghostty/kitty)
// quote identically — POSIX single-quote with embedded-quote escape —
// so we delegate to iterm's implementation.
func ShellQuote(s string) string {
	return iterm.ShellQuote(s)
}
