// Package terminal provides macOS Terminal.app tab spawning via osascript.
//
// Mirrors the contract of internal/iterm — same SpawnTab signature,
// same env-injection semantics (inline prefix, single-leading-space
// for histignorespace), same Runner mock var for tests. The only
// difference is the AppleScript talks to "Terminal" (not "iTerm2") and
// has to drive a cmd-T keystroke through System Events to get a new
// tab in the front window. That requires Accessibility permission for
// whichever process invokes osascript; macOS prompts the user the
// first time, and the prompt is documented in the README.
package terminal

import (
	"flow/internal/notify"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Runner is the function used to execute osascript. Tests override
// this to capture arguments without touching real Terminal.app.
var Runner = func(args []string) error {
	cmd := exec.Command("osascript", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, string(out))
	}
	return nil
}

// RunnerOutput executes osascript and returns stdout. Used by
// FocusSession when the AppleScript itself reports match/miss via its
// stdout. Separate var from Runner so SpawnTab tests stay untouched.
var RunnerOutput = func(args []string) ([]byte, error) {
	return exec.Command("osascript", args...).Output()
}

// PSRunner returns the output of `ps -axo pid,tty,command`. Overridable
// for tests. Includes tty so FocusSession can map a claude PID → tty
// → Terminal.app tab in one pass.
var PSRunner = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,tty,command").Output()
}

// SpawnTab opens a new Terminal.app tab with the given title, cwd, and
// command. envVars are attached as an inline prefix to `command` only —
// so they are present in the spawned process's environment but do NOT
// persist in the tab's shell after the command exits.
//
// The typed line is prefixed with a single space so shells with
// `histignorespace` (zsh) or `HISTCONTROL=ignorespace`/`ignoreboth`
// (bash) skip writing it to the shared history file.
//
// New-tab behavior: when Terminal.app already has a window open, we
// drive a cmd-T keystroke via System Events to open a new tab in the
// front window. When Terminal.app has no windows (e.g. it wasn't
// running), `do script` with no `in` clause opens a new window with a
// single tab and we use that. Either way the result is one fresh tab
// running our command, with the requested title.
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

	script := fmt.Sprintf(`tell application "Terminal"
  activate
  if (count of windows) is 0 then
    set newTab to do script "%s"
  else
    tell application "System Events"
      keystroke "t" using {command down}
    end tell
    delay 0.2
    set newTab to selected tab of front window
    do script "%s" in newTab
  end if
  set custom title of newTab to "%s"
end tell
`, safeCommand, safeCommand, safeTitle)

	if err := Runner([]string{"-e", script}); err != nil {
		if isAccessibilityDenied(err) {
			return wrapAccessibilityError(err)
		}
		return err
	}
	return nil
}

// isAccessibilityDenied reports whether an osascript failure looks
// like a missing-Accessibility-permission error. Patterns are the
// standard error fragments macOS surfaces when System Events is
// invoked from a process that hasn't been granted Accessibility:
//
//   - "not allowed assistive access"  — pre-Catalina wording
//   - "is not allowed to send keystrokes" — common Catalina+ wording
//   - "not authorized to send Apple events"
//   - error codes -1002, -1719, -1743, -25211 returned by osascript
//
// We deliberately match liberally — false positives only mean the
// user sees a slightly verbose error when something else broke, which
// is recoverable; false negatives mean the user gets a cryptic
// osascript error and has to figure out the fix on their own, which
// is the whole problem we're solving.
func isAccessibilityDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, pat := range []string{
		"not allowed assistive access",
		"is not allowed to send keystrokes",
		"is not allowed sending keystrokes",
		"not authorized to send Apple events",
		"(-1002)",
		"(-1719)",
		"(-1743)",
		"(-25211)",
	} {
		if strings.Contains(msg, pat) {
			return true
		}
	}
	return false
}

// wrapAccessibilityError returns a multi-line error explaining what's
// missing and how to fix it. The Claude session that ran `flow do`
// surfaces this error verbatim and can walk the user through System
// Settings → Privacy → Accessibility from there.
//
// Note on which app to grant: macOS attributes Accessibility to the
// "responsible process" — the user-launched terminal app that owns
// the shell, NOT the flow binary or Claude Code. This function only
// fires when the Terminal.app backend was selected, which only
// happens when TERM_PROGRAM=Apple_Terminal — so we can name "Terminal"
// definitively without enumerating other candidates. Past wording
// listed "Terminal / iTerm / Claude" as possible answers and sent
// Claude sessions advising users to toggle the wrong app.
func wrapAccessibilityError(err error) error {
	return fmt.Errorf(`Terminal.app tab spawn requires macOS Accessibility permission for Terminal — the app hosting this shell.

Why this is needed: Terminal.app's AppleScript dictionary has no "make new tab" command. Apple never exposed it. The only way to open a new tab from code is to send cmd-T through System Events, and System Events checks Accessibility against the responsible parent app, which is Terminal.app itself — NOT Claude Code, NOT the flow binary. This gate only applies to the Terminal.app backend; iTerm2 has a native "create tab" verb and does not need it.

How to grant it:
  1. Open the right pane: open "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
  2. In the Accessibility list, enable the toggle for "Terminal". If "Terminal" is not listed, click + and add /System/Applications/Utilities/Terminal.app.
  3. Re-run the same "flow do" command.

After the grant, future "flow do" invocations from Terminal.app spawn tabs silently with no further prompts.

Underlying osascript error: %w`, err)
}

// FocusSession tries to focus the Terminal.app tab whose underlying
// process is `claude` running with `--session-id <sessionID>` or
// `--resume <sessionID>`. Returns (true, nil) on focus, (false, nil)
// if no matching tab was found in Terminal.app, and (false, err) only
// on a backend (ps / osascript) failure.
//
// Mechanism: scan `ps -axo pid,tty,command` for a claude process whose
// argv contains the session UUID, extract its controlling tty, then
// drive osascript to walk every window/tab and select the one whose
// `tty` property matches.
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
// and a `--session-id <uuid>` or `--resume <uuid>` flag.
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
		// `ps -axo pid,tty,command` columns: pid, tty, command. After
		// Fields() splits on whitespace, fields[1] is the tty.
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

// focusByTTY drives Terminal.app's AppleScript dictionary to find a
// tab whose `tty` property matches and select it. Terminal.app
// addresses tabs at the window level (no separate session object
// like iTerm2). The script writes "ok" on match and "miss" otherwise.
func focusByTTY(tty string) (bool, error) {
	safeTTY := escapeAppleScriptString(tty)
	script := fmt.Sprintf(`tell application "Terminal"
  activate
  repeat with w in windows
    repeat with t in tabs of w
      if tty of t is "%s" then
        set frontmost of w to true
        set selected of t to true
        return "ok"
      end if
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

// NotifyFocused posts a macOS notification announcing the focus
// switch. Best-effort — see internal/notify.MacOS.
func NotifyFocused(message string) error {
	return notify.MacOS("flow", message)
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
