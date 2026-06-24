//go:build windows

package spawner

import "flow/internal/winterm"

// ShellQuote quotes for PowerShell, which is the shell the Windows
// Terminal backend runs the launch command in. PowerShell's single-
// quoted string literal escapes embedded quotes by doubling them — see
// winterm.ShellQuote.
func ShellQuote(s string) string {
	return winterm.ShellQuote(s)
}
