// Package iterm provides iTerm2 tab spawning via osascript.
package iterm

import (
	"fmt"
	"os"
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
// envVars are exported inside a short launcher script, then the script
// replaces itself with `command`, so they are present only in that process
// tree and do NOT persist in the tab's shell after the command exits.
//
// The typed line is prefixed with a single space so shells with
// `histignorespace` (zsh) or `HISTCONTROL=ignorespace`/`ignoreboth`
// (bash) skip writing it to the shared history file. Shells without
// that opt-in will still record the line — see README for the one-line
// shell config that turns it on.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	envExports := ""
	if len(envVars) > 0 {
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(envVars))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("export %s=%s", k, ShellQuote(envVars[k])))
		}
		envExports = strings.Join(parts, "\n") + "\n"
	}
	launchCommand, cleanup, err := writeLaunchScript(cwd, envExports, command)
	if err != nil {
		return err
	}
	safeCommand := escapeAppleScriptString(" " + launchCommand)
	safeTitle := escapeAppleScriptString(title)

	script := fmt.Sprintf(`tell application "iTerm2"
  activate
  if (count of windows) is 0 then
    set newWindow to (create window with default profile)
    tell current session of newWindow
      set name to "%s"
      write text "%s" newline yes
    end tell
  else
    tell current window
    set newTab to (create tab with default profile)
    tell current session of newTab
      set name to "%s"
      write text "%s" newline yes
    end tell
  end tell
  end if
end tell
`, safeTitle, safeCommand, safeTitle, safeCommand)

	if err := Runner([]string{"-e", script}); err != nil {
		cleanup()
		return err
	}
	return nil
}

func writeLaunchScript(cwd, envExports, command string) (string, func(), error) {
	f, err := os.CreateTemp("", "flow-iterm-*.sh")
	if err != nil {
		return "", func() {}, fmt.Errorf("create iTerm launcher: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	body := fmt.Sprintf("#!/bin/sh\nrm -f \"$0\"\ncd %s || exit\n%sexec %s\n", ShellQuote(cwd), envExports, command)
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write iTerm launcher: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close iTerm launcher: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("chmod iTerm launcher: %w", err)
	}
	return "/bin/sh " + ShellQuote(path), cleanup, nil
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
