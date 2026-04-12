package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// osascriptRunner is the function used to actually execute osascript.
// Tests override this to capture arguments without invoking osascript.
var osascriptRunner = func(args []string) error {
	cmd := exec.Command("osascript", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, string(out))
	}
	return nil
}

// SpawnITermTab opens a new iTerm2 tab with the given title, cwd, and command.
// envVars are exported in the new shell before running command.
//
// The title is applied twice: once via AppleScript `set name` (so the tab
// shows the right label the instant it appears) and again via an OSC 0
// escape (\033]0;TITLE\007) emitted from within the spawned shell command.
// The second pass is load-bearing: most shells' first prompt emits its own
// title escape that would otherwise clobber the AppleScript-set name.
func SpawnITermTab(title, cwd, command string, envVars map[string]string) error {
	exports := ""
	if len(envVars) > 0 {
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(envVars))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, shellQuote(envVars[k])))
		}
		exports = "export " + strings.Join(parts, " ") + " && "
	}
	setTitle := fmt.Sprintf(`printf '\033]0;%%s\007' %s && `, shellQuote(title))
	fullCommand := fmt.Sprintf("cd %s && %s%s%s", shellQuote(cwd), exports, setTitle, command)
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

	return osascriptRunner([]string{"-e", script})
}

func shellQuote(s string) string {
	// Single-quote shell quoting: wrap in single quotes and escape any existing
	// single quotes by closing, inserting an escaped quote, and reopening.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
