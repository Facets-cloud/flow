// Package wt provides Windows Terminal tab spawning via wt.exe.
//
// wt.exe accepts a command-line subcommand grammar:
//
//	wt.exe [global-flags] [<subcommand> [<sub-flags>] [<commandline-to-run>]]
//
// SpawnTab uses:
//
//	wt -w 0 nt -d <cwd> --title <title> powershell.exe -NoExit -Command <psCommand>
//
// `-w 0` targets the current window if any, falling back to a new
// window. `nt` creates a new tab. `-d` sets the working directory
// for the new tab. `--title` sets the tab title. The trailing
// `powershell.exe -NoExit -Command <X>` is the program wt launches
// in the new tab; -NoExit keeps the shell open after <X> completes
// so the user can see output / interact with the spawned session.
//
// Env-var injection is done by prefixing the PowerShell command
// with `$env:NAME='value'; ` for each var, matching the shell-
// prefix pattern the POSIX backends use. The prefix is set inside
// the new tab's PowerShell scope only and does NOT persist in the
// user's profile.
//
// Newline handling: PowerShell's -Command flag expects a single
// script string. Embedded newlines in the bootstrap prompt are
// flattened to spaces (matching internal/zellij and internal/kitty)
// because the prompt is intentionally whitespace-insensitive for
// the LLM. This is a lossless transformation for the bootstrap
// text by design.
//
// Single-quote handling: the `command` arg is already POSIX-single-
// quoted by the harness (the bootstrap prompt is shell-safe — no
// embedded quotes, backticks, or dollar signs — per
// internal/app/do.go's prompt-builder contract). PowerShell parses
// '...' as a literal string with the same rules as POSIX shells
// for non-quote characters, so the command line passes through
// unchanged. Inject text supplied via `flow do --with` could carry
// single quotes; this is a known v1 limitation for Windows.
//
// FocusSession returns (false, nil) — Windows Terminal exposes no
// programmatic API to enumerate or focus tabs by their inner
// process. The "session running elsewhere" message that the caller
// surfaces in that case is the right UX on Windows for v1.
package wt

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Runner is the function used to execute wt.exe. Tests override this
// to capture argv without invoking the real binary.
var Runner = func(args []string) error {
	cmd := exec.Command("wt.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wt.exe failed: %v: %s", err, string(out))
	}
	return nil
}

// SpawnTab opens a new Windows Terminal tab with the given title,
// cwd, and command. envVars are attached as an inline PowerShell
// prefix on `command` only — they exist in the spawned session's
// environment but do not persist after the tab is closed.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	envPrefix := ""
	if len(envVars) > 0 {
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(envVars))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("$env:%s=%s;", k, ShellQuote(envVars[k])))
		}
		envPrefix = strings.Join(parts, " ") + " "
	}
	flat := strings.ReplaceAll(command, "\n", " ")
	psCommand := envPrefix + flat

	args := []string{
		"-w", "0",
		"nt",
		"-d", cwd,
		"--title", title,
		"powershell.exe",
		"-NoExit",
		"-Command", psCommand,
	}
	return Runner(args)
}

// FocusSession returns (false, nil) on Windows — wt.exe exposes no
// API to enumerate or focus existing tabs by their inner process.
// Callers should treat (false, nil) as "fall through" and surface
// the "session running elsewhere" message so the user can switch
// manually.
func FocusSession(sessionID, binary string) (bool, error) {
	return false, nil
}

// ShellQuote wraps s in PowerShell single-quotes with proper
// escaping (`'` is doubled). PowerShell parses '...' as a literal
// string with no $-interpolation, backtick-escape, or other
// surprises — so this quoting is safe for arbitrary text.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
