// Package ghostty provides Ghostty tab spawning via osascript.
//
// Mirrors the contract of internal/iterm — same SpawnTab signature,
// same env-injection semantics, same Runner mock var for tests. Two
// places differ from iterm:
//
//   - Ghostty's `name` property on tab and terminal classes is
//     read-only (`access="r"` in the .sdef). AppleScript can read
//     it but cannot set it. We set the tab title by prepending an
//     OSC 2 escape sequence to the typed command; Ghostty intercepts
//     it like every other modern xterm-compatible terminal.
//
//   - Ghostty's `new tab` command REQUIRES an `in <window>`
//     parameter. Calling it bare errors -1708. We branch on whether
//     any windows exist and call `new window` first when none do.
package ghostty

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Runner is the function used to execute osascript. Tests override
// this to capture arguments without touching real Ghostty.
var Runner = func(args []string) error {
	cmd := exec.Command("osascript", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, string(out))
	}
	return nil
}

// SpawnTab opens a new Ghostty tab with the given title, cwd, and
// command. envVars are attached as an inline prefix to `command` only
// — so they are present in the spawned process's environment but do
// NOT persist in the tab's shell after the command exits.
//
// The typed line is prefixed with a single space so shells with
// `histignorespace` (zsh) or `HISTCONTROL=ignorespace`/`ignoreboth`
// (bash) skip writing it to the shared history file.
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

	titlePrefix := fmt.Sprintf(`printf '\033]2;%%s\007' %s ; `, ShellQuote(title))
	fullCommand := fmt.Sprintf(" %scd %s && %s%s", titlePrefix, ShellQuote(cwd), envPrefix, command)
	safeCommand := escapeAppleScriptString(fullCommand)

	script := fmt.Sprintf(`tell application "Ghostty"
  activate
  if (count of windows) is 0 then
    set newWin to (new window)
    set targetTerm to focused terminal of (first tab of newWin)
  else
    set newTab to (new tab in front window)
    set targetTerm to focused terminal of newTab
  end if
  input text "%s" & return to targetTerm
end tell
`, safeCommand)

	return Runner([]string{"-e", script})
}

// ShellQuote wraps s in single quotes with proper escaping.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
