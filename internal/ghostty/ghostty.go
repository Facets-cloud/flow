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
	"regexp"
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

// RunnerOutput executes osascript and returns stdout. Separate from
// Runner so FocusSession can read the script's match/miss verdict while
// existing SpawnTab tests keep mocking Runner alone. Mirrors
// iterm.RunnerOutput.
var RunnerOutput = func(args []string) ([]byte, error) {
	return exec.Command("osascript", args...).Output()
}

// PSRunner returns the output of `ps -axo pid,tty,command`. Overridable
// for tests. Mirrors iterm.PSRunner / terminal.PSRunner.
var PSRunner = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,tty,command").Output()
}

// ActivateApp foregrounds Ghostty. Used as the degraded fallback when
// tty-matching isn't available (see FocusSession).
var ActivateApp = func() error {
	cmd := exec.Command("open", "-a", "Ghostty")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("open -a Ghostty failed: %v: %s", err, string(out))
	}
	return nil
}

// FocusSession tries to focus the Ghostty terminal whose underlying
// process is `binary` running with `--session-id <sessionID>` or
// `--resume <sessionID>`. Returns (true, nil) on focus, (false, nil)
// when no matching terminal was found OR when the running Ghostty is
// too old to support the lookup, and (false, err) only on a ps failure.
//
// Version gate — the reason this probes instead of just running the
// script: Ghostty's `terminal` class only gained `tty` (and `pid`)
// properties in the 1.4.0 development line
// (ghostty-org/ghostty#11922, closing #11592). Every release up to and
// including v1.3.1 exposes just `id`, `name`, and `working directory`
// on a terminal, with no way to correlate a terminal to a process. On
// those versions the tty-matching script raises an AppleScript error
// rather than returning empty, so we treat any script error as "this
// Ghostty can't do it" and degrade instead of surfacing a fault.
//
// The degraded path activates Ghostty and reports a miss — the same
// contract internal/warp uses, and for the same reason: the caller's
// (false, nil) fallback prints "switch to that tab manually", which is
// honest, whereas claiming a focus that didn't happen is not. We
// deliberately do NOT fall back to matching `working directory`, the
// only other correlating property available on old versions: flow
// routinely opens several sessions in the same repo, so that match is
// ambiguous by construction and would focus an arbitrary sibling tab.
//
// Two Ghostty API shapes differ from iTerm2 and are easy to get wrong:
// its per-tab objects are `terminals` (not `sessions`), and `focus` is
// a command taking a terminal specifier — `selected` and `index` are
// read-only, so `set selected to true` silently fails.
func FocusSession(sessionID, binary string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	tty, err := ttyForHarnessSession(sessionID, binary)
	if err != nil {
		return false, err
	}
	if tty == "" {
		return false, nil
	}

	focused, scriptErr := focusByTTY(tty)
	if scriptErr != nil {
		// Old Ghostty (no tty property) lands here. Degrade rather
		// than reporting a backend fault.
		_ = ActivateApp()
		return false, nil
	}
	if !focused {
		return false, nil
	}
	return true, nil
}

// sessionUUIDRowRe matches a `ps` line carrying a session UUID via
// `--session-id <uuid>` or `--resume <uuid>`. Paired with a binary-name
// check by ttyForHarnessSession. Duplicated from the sibling backends
// to avoid cross-package coupling, per the package convention.
var sessionUUIDRowRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// ttyForHarnessSession returns the controlling tty (e.g.
// "/dev/ttys012") of the process matching `binary` and carrying the
// given session UUID in its argv, or "" if no such process exists.
func ttyForHarnessSession(sessionID, binary string) (string, error) {
	out, err := PSRunner()
	if err != nil {
		return "", fmt.Errorf("ps: %w", err)
	}
	needle := strings.ToLower(sessionID)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, binary) {
			continue
		}
		matches := sessionUUIDRowRe.FindStringSubmatch(line)
		if len(matches) < 2 {
			continue
		}
		if strings.ToLower(matches[1]) != needle {
			continue
		}
		// `ps -axo pid,tty,command` columns: pid, tty, command.
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

// focusByTTY walks Ghostty's window → tab → terminal object graph
// looking for a terminal whose `tty` matches, and focuses it. Writes
// "ok" on match and "miss" otherwise so we distinguish at the Go level
// rather than via osascript's exit code.
//
// On Ghostty ≤ v1.3.1 the `tty of trm` reference is invalid and
// osascript exits non-zero; FocusSession maps that error to the
// degraded path.
func focusByTTY(tty string) (bool, error) {
	safeTTY := escapeAppleScriptString(tty)
	script := fmt.Sprintf(`tell application "Ghostty"
  repeat with w in windows
    repeat with t in tabs of w
      repeat with trm in terminals of t
        if tty of trm is "%s" then
          activate
          focus trm
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
