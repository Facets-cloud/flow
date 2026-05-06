// Package iterm provides iTerm2 tab spawning via osascript.
package iterm

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Runner is the function used to execute osascript.
// Tests override this to capture arguments without invoking osascript.
var Runner = func(args []string) error {
	cmd := exec.Command("osascript", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, string(out))
	}
	return nil
}

// SpawnTab opens a new iTerm2 tab with the given title, cwd, and command.
// envVars are attached as an inline prefix to `command` only — so they
// are present in the spawned process's environment but do NOT persist in
// the tab's shell after the command exits.
//
// The typed line is prefixed with a single space so shells with
// `histignorespace` (zsh) or `HISTCONTROL=ignorespace`/`ignoreboth`
// (bash) skip writing it to the shared history file. Shells without
// that opt-in will still record the line — see README for the one-line
// shell config that turns it on.
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
			parts = append(parts, fmt.Sprintf("%s=%s", k, ShellQuote(envVars[k])))
		}
		envPrefix = strings.Join(parts, " ") + " "
	}
	fullCommand := fmt.Sprintf(" cd %s && %s%s", ShellQuote(cwd), envPrefix, command)
	safeCommand := escapeAppleScriptString(fullCommand)
	safeTitle := escapeAppleScriptString(title)

	script := fmt.Sprintf(`tell application "iTerm2"
  tell current window
    set newTab to (create tab with default profile)
    tell current session of newTab
      set name to "%s"
      write text "%s"
    end tell
  end tell
end tell
`, safeTitle, safeCommand)

	return Runner([]string{"-e", script})
}

// ShellQuote wraps s in single quotes with proper escaping.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// escapeAppleScriptString prepares s for embedding inside an
// AppleScript double-quoted string literal. Newlines / carriage
// returns / tabs are converted to their AppleScript escape sequences
// (`\n`, `\r`, `\t`) so the resulting literal is single-line. See
// the matching helper in internal/terminal for the rationale —
// keeping the two backends consistent so neither one silently
// regresses.
func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}
