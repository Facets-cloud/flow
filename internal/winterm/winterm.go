// Package winterm provides Windows Terminal tab spawning via the
// `wt.exe` CLI. It is the default interactive backend on Windows
// (spawner.Detect() selects it), mirroring the SpawnTab/FocusSession/
// ShellQuote contract of the macOS backends.
//
// Mechanism:
//
//	wt.exe -w 0 new-tab --title <title> -d <cwd> \
//	    powershell.exe -NoLogo -NoExit -EncodedCommand <base64>
//
// The launch command is handed to PowerShell as -EncodedCommand: a
// base64 encoding of the UTF-16LE bytes of a short PowerShell script
// (set env vars, Set-Location, then run the command). Encoding the
// payload this way is deliberate — it sidesteps two notoriously fragile
// layers at once:
//
//   - wt.exe re-parses its own command line and treats `;` as a
//     tab/pane separator. Base64 uses only [A-Za-z0-9+/=], so the
//     entire (potentially multi-line, quote-laden) launch command is
//     immune to wt's splitter.
//   - PowerShell quoting of a multi-line prompt with embedded quotes is
//     error-prone. Encoding the whole script avoids inline quoting of
//     the payload altogether.
//
// `-NoExit` keeps the PowerShell prompt open after the command exits,
// matching the macOS backends (the tab stays so the user can inspect
// output or rerun).
//
// Test seam: Runner is overridden in tests to capture the wt.exe argv
// without launching Windows Terminal — same pattern as iterm.Runner.
package winterm

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"unicode/utf16"
)

// Runner executes wt.exe. Tests override this to capture argv without
// invoking the real Windows Terminal.
var Runner = func(args []string) error {
	cmd := exec.Command("wt.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wt.exe failed: %v: %s", err, string(out))
	}
	return nil
}

// SpawnTab opens a new Windows Terminal tab in cwd, titles it, and runs
// `command` in a PowerShell session with envVars set.
//
// `command` MUST already be a valid PowerShell command line — callers
// quote embedded arguments via ShellQuote (re-exported through
// spawner.ShellQuote on Windows). This matches the iterm/terminal/
// zellij/warp contract.
//
// envVars are set with `$env:KEY = '<value>'` inside the encoded script,
// so they are present for the spawned process but do not persist beyond
// the tab's shell session.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	encoded := encodePowerShellCommand(buildPSCommand(cwd, command, envVars))

	args := []string{"-w", "0", "new-tab"}
	if title != "" {
		args = append(args, "--title", title)
	}
	if cwd != "" {
		args = append(args, "-d", cwd)
	}
	args = append(args,
		"powershell.exe", "-NoLogo", "-NoExit", "-EncodedCommand", encoded,
	)
	return Runner(args)
}

// FocusSession is a no-op on Windows Terminal for now: wt.exe exposes no
// query for the process running inside each tab, so flow cannot locate
// the tab already driving a given session id. Returns (false, nil) so
// the caller falls through to its "session running elsewhere" message
// (the user switches tabs manually or passes --force). Tracked as
// future work.
func FocusSession(sessionID, binary string) (bool, error) {
	return false, nil
}

// ShellQuote wraps s as a PowerShell single-quoted string literal:
// embedded single quotes are escaped by doubling them. A single-quoted
// literal in PowerShell performs no interpolation and may span multiple
// lines, so this safely quotes prompts containing $, backticks, double
// quotes, and newlines.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// buildPSCommand assembles the PowerShell script that the encoded
// command runs:
//
//	Set-Location -LiteralPath '<cwd>'
//	$env:FOO = 'bar'
//	$env:BAZ = 'qux'
//	<command>
//
// cwd is set both via wt's -d flag and here (belt and braces). Env vars
// are sorted for stable, testable output. When cwd/envVars are empty the
// corresponding lines are omitted.
func buildPSCommand(cwd, command string, envVars map[string]string) string {
	var b strings.Builder
	if cwd != "" {
		fmt.Fprintf(&b, "Set-Location -LiteralPath %s\n", ShellQuote(cwd))
	}
	if len(envVars) > 0 {
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "$env:%s = %s\n", k, ShellQuote(envVars[k]))
		}
	}
	b.WriteString(command)
	b.WriteString("\n")
	return b.String()
}

// encodePowerShellCommand encodes s for PowerShell's -EncodedCommand
// flag: base64 of the UTF-16 little-endian bytes of the string. This is
// the exact format `powershell.exe -EncodedCommand` expects.
func encodePowerShellCommand(s string) string {
	u16 := utf16.Encode([]rune(s))
	buf := make([]byte, len(u16)*2)
	for i, c := range u16 {
		binary.LittleEndian.PutUint16(buf[i*2:], c)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
