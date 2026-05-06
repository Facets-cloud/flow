// Package zellij provides zellij-session tab spawning via the `zellij`
// CLI. Activated by spawner.Detect() when $ZELLIJ is set in the
// environment (zellij sets this in every shell it spawns).
//
// Mechanism:
//
//  1. zellij action new-tab --name <title> --cwd <cwd>
//  2. zellij action write-chars " <env-prefix><command>\n"
//
// Step 1 creates and focuses the new tab; step 2 types the command
// into the new pane's PTY so the shell executes it. The leading space
// triggers histignorespace on shells that have it on, matching the
// iterm/terminal backends. The trailing newline submits the line.
//
// This file mirrors the contract of internal/iterm and internal/terminal
// — same SpawnTab signature, same Runner mock var for tests, same
// ShellQuote helper.
package zellij

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Runner is the function used to execute zellij.
// Tests override this to capture argv without invoking the real CLI.
var Runner = func(args []string) error {
	cmd := exec.Command("zellij", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zellij failed: %v: %s", err, string(out))
	}
	return nil
}

// SpawnTab opens a new zellij tab in the current session, sets its
// name and cwd, and types `command` into the new pane's PTY.
//
// envVars are attached as an inline shell prefix to `command` only —
// they are present in the command's environment but do NOT persist
// in the tab's shell after the command exits.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	if err := Runner([]string{"action", "new-tab", "--name", title, "--cwd", cwd}); err != nil {
		return err
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
	line := " " + envPrefix + command + "\n"
	return Runner([]string{"action", "write-chars", line})
}

// ShellQuote wraps s in single quotes with proper escaping. Same
// implementation as iterm.ShellQuote and terminal.ShellQuote.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
