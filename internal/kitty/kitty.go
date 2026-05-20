// Package kitty provides kitty-terminal tab spawning via the `kitty @`
// remote-control CLI. Activated by spawner.Detect() when $KITTY_WINDOW_ID
// is set or $TERM=xterm-kitty (kitty sets these in every shell it
// spawns).
//
// Mechanism:
//
//  1. kitty @ launch --type=tab --tab-title=<title> --cwd=<cwd>
//     (prints the new window id to stdout)
//  2. kitty @ send-text --match=id:<id> " <env-prefix><flat-command>\n"
//
// Step 1 opens a new tab in the current OS window, running the default
// shell, and returns the kitty window id. Step 2 types the command
// into that window's PTY so the shell executes it. The leading space
// triggers histignorespace on shells that have it on, matching the
// iterm/terminal/zellij backends. The trailing newline submits the
// line.
//
// Newline handling: send-text writes raw bytes to the target window's
// PTY, so any embedded `\n` in the command is interpreted by the shell
// as Enter. Same flattening rule as the zellij backend.
//
// Prereq: `allow_remote_control yes` (or `socket-only`) in kitty.conf.
// Without it, `kitty @ launch` exits non-zero with an explicit error;
// SpawnTab surfaces that as a wrapped error so the user knows to enable
// remote control.
//
// This file mirrors the contract of internal/iterm, internal/terminal,
// and internal/zellij — same SpawnTab signature, same Runner mock var
// for tests, same ShellQuote helper.
package kitty

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Runner is the function used to execute kitty for side-effect calls
// (send-text). Tests override this to capture argv without invoking
// the real CLI.
var Runner = func(args []string) error {
	cmd := exec.Command("kitty", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kitty failed: %v: %s", err, string(out))
	}
	return nil
}

// RunnerOutput executes kitty and returns stdout. Used by SpawnTab to
// read the new window id from `kitty @ launch`. Separate var from
// Runner so existing SpawnTab argv tests stay readable.
var RunnerOutput = func(args []string) ([]byte, error) {
	return exec.Command("kitty", args...).Output()
}

// SpawnTab opens a new kitty tab in the current OS window, sets its
// title and cwd, and types `command` into the new window's PTY.
//
// envVars are attached as an inline shell prefix to `command` only —
// they are present in the command's environment but do NOT persist in
// the tab's shell after the command exits.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	out, err := RunnerOutput([]string{
		"@", "launch",
		"--type=tab",
		"--tab-title=" + title,
		"--cwd=" + cwd,
	})
	if err != nil {
		return fmt.Errorf("kitty @ launch: %w (is `allow_remote_control yes` set in kitty.conf?)", err)
	}
	windowID := strings.TrimSpace(string(out))
	if windowID == "" {
		return fmt.Errorf("kitty @ launch returned empty window id")
	}

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
	flat := strings.ReplaceAll(command, "\n", " ")
	line := " " + envPrefix + flat + "\n"
	return Runner([]string{"@", "send-text", "--match=id:" + windowID, line})
}

// FocusSession tries to focus the kitty window currently running
// `binary` with the given session UUID. The `binary` arg is the
// harness's executable name (e.g. "claude", "codex", "gemini") used
// to filter windows. Returns (true, nil) on focus, (false, nil) if no
// window across any kitty OS window matches, and (false, err) only on
// a backend failure (kitty CLI errored or returned malformed JSON).
//
// Mechanism: `kitty @ ls` returns the full OS-window → tab → window
// tree. Each terminal window's foreground_processes array carries the
// argv of the child processes currently running under that PTY — the
// harness invocation lives there, not in the window's own cmdline
// (which is the shell). We join each foreground process's cmdline
// with spaces, apply the same UUID regex used by the iterm / zellij
// backends, and call `kitty @ focus-window --match=id:<id>` on the
// first hit. That single call raises the OS window, selects the tab,
// and focuses the window — no need to chain focus-tab.
//
// Scope: unlike zellij's `list-panes` (which only sees the current
// zellij session), `kitty @ ls` enumerates every OS window the kitty
// instance owns, so cross-OS-window focus works without any extra IPC.
//
// Prereq: `allow_remote_control yes` in kitty.conf — same as SpawnTab.
// If remote control is disabled, `kitty @ ls` exits non-zero and the
// error is surfaced wrapped.
func FocusSession(sessionID, binary string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	out, err := RunnerOutput([]string{"@", "ls"})
	if err != nil {
		return false, fmt.Errorf("kitty @ ls: %w", err)
	}
	winID, ok, err := windowIDForHarnessSession(out, sessionID, binary)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := Runner([]string{"@", "focus-window", fmt.Sprintf("--match=id:%d", winID)}); err != nil {
		return false, fmt.Errorf("kitty @ focus-window: %w", err)
	}
	return true, nil
}

// kittyOSWindow / kittyTab / kittyWindow / kittyFGProc are the minimal
// subset of the `kitty @ ls` JSON schema we care about. The full schema
// has many more fields (geometry, env, title, padding, etc.) but only
// the window id and the foreground_processes cmdline matter for focus
// matching.
type kittyOSWindow struct {
	Tabs []kittyTab `json:"tabs"`
}
type kittyTab struct {
	Windows []kittyWindow `json:"windows"`
}
type kittyWindow struct {
	ID                  int           `json:"id"`
	ForegroundProcesses []kittyFGProc `json:"foreground_processes"`
}
type kittyFGProc struct {
	Cmdline []string `json:"cmdline"`
}

// sessionUUIDRowRe matches a harness invocation carrying a session
// UUID in argv. Same shape as the iterm and zellij regex so focus
// behaviour stays consistent across backends.
var sessionUUIDRowRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// windowIDForHarnessSession parses `kitty @ ls` JSON and returns the
// kitty window id of the first window whose foreground_processes
// contain a process matching `binary` and carrying the given session
// UUID. Returns (0, false, nil) on no match, (0, false, err) only on
// JSON parse failure.
func windowIDForHarnessSession(jsonBytes []byte, sessionID, binary string) (int, bool, error) {
	var osWindows []kittyOSWindow
	if err := json.Unmarshal(jsonBytes, &osWindows); err != nil {
		return 0, false, fmt.Errorf("parse kitty ls JSON: %w", err)
	}
	needle := strings.ToLower(sessionID)
	for _, osw := range osWindows {
		for _, tab := range osw.Tabs {
			for _, win := range tab.Windows {
				for _, proc := range win.ForegroundProcesses {
					if len(proc.Cmdline) == 0 {
						continue
					}
					joined := strings.Join(proc.Cmdline, " ")
					if !strings.Contains(joined, binary) {
						continue
					}
					matches := sessionUUIDRowRe.FindStringSubmatch(joined)
					if len(matches) < 2 {
						continue
					}
					if strings.ToLower(matches[1]) != needle {
						continue
					}
					return win.ID, true, nil
				}
			}
		}
	}
	return 0, false, nil
}

// ShellQuote wraps s in single quotes with proper escaping. Same
// implementation as iterm.ShellQuote / terminal.ShellQuote /
// zellij.ShellQuote.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
