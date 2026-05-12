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
	"fmt"
	"os/exec"
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

// ShellQuote wraps s in single quotes with proper escaping. Same
// implementation as iterm.ShellQuote / terminal.ShellQuote /
// zellij.ShellQuote.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
