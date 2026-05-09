// Package iterm provides iTerm2 tab spawning via osascript.
package iterm

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Runner is the function used to execute osascript for SpawnTab.
// Tests override this to capture arguments without invoking osascript.
var Runner = func(args []string) error {
	cmd := exec.Command("osascript", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, string(out))
	}
	return nil
}

// RunnerOutput is the function used to execute osascript when the
// caller needs to read stdout (e.g., FocusSession needs to know
// whether a match was found). Separate from Runner so tests can mock
// it independently and existing SpawnTab tests stay untouched.
var RunnerOutput = func(args []string) ([]byte, error) {
	return exec.Command("osascript", args...).Output()
}

// PSRunner returns the output of `ps -axo pid,tty,command`. Overridable
// for tests. Includes tty so FocusSession can map a claude PID → tty
// → iTerm2 session in one pass.
var PSRunner = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,tty,command").Output()
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

// FocusSession tries to focus the iTerm2 session whose underlying
// process is `claude` running with `--session-id <sessionID>` or
// `--resume <sessionID>`. Returns (true, nil) on focus, (false, nil)
// if no matching tab was found in iTerm2, and (false, err) only on a
// backend (ps / osascript) failure.
//
// Mechanism: scan `ps -axo pid,tty,command` for a claude process whose
// argv contains the session UUID, extract its controlling tty, then
// drive osascript to walk every window/tab/session and select the one
// whose `tty` property matches.
func FocusSession(sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	tty, err := ttyForClaudeSession(sessionID)
	if err != nil {
		return false, err
	}
	if tty == "" {
		return false, nil
	}
	return focusByTTY(tty)
}

// claudeSessionRowRe matches a `ps` line that has BOTH a `claude` token
// and a `--session-id <uuid>` or `--resume <uuid>` flag. Used by
// ttyForClaudeSession to filter to claude rows that carry the UUID.
var claudeSessionRowRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// ttyForClaudeSession returns the controlling tty (e.g., "/dev/ttys012")
// of the claude process running with the given session UUID, or "" if
// no such process is currently running.
func ttyForClaudeSession(sessionID string) (string, error) {
	out, err := PSRunner()
	if err != nil {
		return "", fmt.Errorf("ps: %w", err)
	}
	needle := strings.ToLower(sessionID)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "claude") {
			continue
		}
		matches := claudeSessionRowRe.FindStringSubmatch(line)
		if len(matches) < 2 {
			continue
		}
		if strings.ToLower(matches[1]) != needle {
			continue
		}
		// `ps -axo pid,tty,command` columns: pid, tty, command.
		// After Fields() splits on whitespace, fields[1] is the tty
		// (e.g., "ttys012", or "??" for no controlling terminal).
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		tty := fields[1]
		if tty == "??" || tty == "?" || tty == "" {
			continue
		}
		if !strings.HasPrefix(tty, "/dev/") {
			tty = "/dev/" + tty
		}
		return tty, nil
	}
	return "", nil
}

// focusByTTY drives iTerm2's AppleScript dictionary to find a session
// whose `tty` property matches and select it (and its enclosing tab
// and window). The script writes "ok" to stdout on match, "miss"
// otherwise; we distinguish at the Go level instead of relying on
// osascript's exit code.
func focusByTTY(tty string) (bool, error) {
	safeTTY := escapeAppleScriptString(tty)
	script := fmt.Sprintf(`tell application "iTerm2"
  activate
  repeat with w in windows
    repeat with t in tabs of w
      repeat with s in sessions of t
        if tty of s is "%s" then
          select w
          tell t to select
          tell s to select
          return "ok"
        end if
      end repeat
    end repeat
  end repeat
  return "miss"
end tell
`, safeTTY)
	out, err := RunnerOutput([]string{"-e", script})
	if err != nil {
		return false, fmt.Errorf("osascript: %w", err)
	}
	return strings.TrimSpace(string(out)) == "ok", nil
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
