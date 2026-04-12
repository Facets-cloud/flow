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
// envVars are exported in the new shell before running command.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	exports := ""
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
		exports = "export " + strings.Join(parts, " ") + " && "
	}
	setTitle := fmt.Sprintf(`printf '\033]0;%%s\007' %s && `, ShellQuote(title))
	fullCommand := fmt.Sprintf("cd %s && %s%s%s", ShellQuote(cwd), exports, setTitle, command)
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

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
