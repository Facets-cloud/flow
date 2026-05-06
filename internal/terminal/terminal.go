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
	"fmt"
	"os/exec"
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

// ShellQuote wraps s in single quotes with proper escaping.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
