// Package zellij provides zellij-session tab spawning via the `zellij`
// CLI. Activated by spawner.Detect() when $ZELLIJ is set in the
// environment (zellij sets this in every shell it spawns).
//
// Mechanism:
//
//  1. zellij action new-tab --name <title> --cwd <cwd>
//  2. zellij action write-chars " <env-prefix><flattened-command>\n"
//
// Step 1 creates and focuses the new tab; step 2 types the command
// into the new pane's PTY so the shell executes it. The leading space
// triggers histignorespace on shells that have it on, matching the
// iterm/terminal backends. The trailing newline submits the line.
//
// Newline handling: write-chars writes raw bytes to the new pane's
// PTY, so any embedded `\n` in the command is interpreted by the
// shell as Enter — submitting a partial line and dropping into a
// continuation/error state instead of running the whole command.
// `flow do`'s bootstrap prompt is a multi-line numbered list, which
// would have its lines executed individually (and fail) without
// flattening. We replace embedded newlines with spaces before
// emitting the line; the bootstrap text is whitespace-insensitive
// for the LLM, so this is lossless.
//
// This file mirrors the contract of internal/iterm and internal/terminal
// — same SpawnTab signature, same Runner mock var for tests, same
// ShellQuote helper.
package zellij

import (
	"encoding/json"
	"flow/internal/notify"
	"fmt"
	"os/exec"
	"regexp"
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

// RunnerOutput executes zellij and returns stdout. Used by FocusSession
// to read `zellij action list-panes --all --json`. Separate var from
// Runner so existing SpawnTab tests stay untouched.
var RunnerOutput = func(args []string) ([]byte, error) {
	return exec.Command("zellij", args...).Output()
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
	flat := strings.ReplaceAll(command, "\n", " ")
	line := " " + envPrefix + flat + "\n"
	return Runner([]string{"action", "write-chars", line})
}

// FocusSession tries to focus the zellij pane currently running
// `claude` with the given session UUID. Returns (true, nil) on focus,
// (false, nil) if no pane in the current zellij session matches, and
// (false, err) only on a backend failure (zellij CLI errored or
// returned malformed JSON).
//
// Mechanism: `zellij action list-panes --all --json` returns every
// pane's `pane_command` (the actual command line of the running
// process). We scan for a pane whose command contains
// `claude --session-id <uuid>` or `--resume <uuid>`, then call
// `zellij action focus-pane-id terminal_<id>` to switch to it.
//
// Limitation: list-panes covers only the *current* zellij session.
// If the user opened the task tab from a different zellij session,
// this returns (false, nil) and the caller falls through to the
// existing "running elsewhere" error.
func FocusSession(sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	out, err := RunnerOutput([]string{"action", "list-panes", "--all", "--json"})
	if err != nil {
		return false, fmt.Errorf("zellij list-panes: %w", err)
	}
	paneID, ok, err := paneIDForClaudeSession(out, sessionID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := Runner([]string{"action", "focus-pane-id", fmt.Sprintf("terminal_%d", paneID)}); err != nil {
		return false, fmt.Errorf("zellij focus-pane-id: %w", err)
	}
	return true, nil
}

// paneInfo is the subset of zellij's list-panes JSON we care about.
// Pane records have many more fields (geometry, plugin metadata, etc.)
// but only id, is_plugin, and pane_command matter for focus matching.
type paneInfo struct {
	ID          int    `json:"id"`
	IsPlugin    bool   `json:"is_plugin"`
	PaneCommand string `json:"pane_command"`
}

// claudeSessionRowRe matches `claude` invocations carrying a session
// UUID in the same shape as the iterm/terminal regex.
var claudeSessionRowRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// paneIDForClaudeSession parses zellij list-panes JSON and returns the
// pane id of the first non-plugin pane whose pane_command runs
// `claude` with the given session UUID. Returns (0, false, nil) if no
// match. Returns (0, false, err) on JSON parse failure.
func paneIDForClaudeSession(jsonBytes []byte, sessionID string) (int, bool, error) {
	var panes []paneInfo
	if err := json.Unmarshal(jsonBytes, &panes); err != nil {
		return 0, false, fmt.Errorf("parse list-panes JSON: %w", err)
	}
	needle := strings.ToLower(sessionID)
	for _, p := range panes {
		if p.IsPlugin || p.PaneCommand == "" {
			continue
		}
		if !strings.Contains(p.PaneCommand, "claude") {
			continue
		}
		matches := claudeSessionRowRe.FindStringSubmatch(p.PaneCommand)
		if len(matches) < 2 {
			continue
		}
		if strings.ToLower(matches[1]) != needle {
			continue
		}
		return p.ID, true, nil
	}
	return 0, false, nil
}

// NotifyFocused posts a macOS notification. zellij itself has no
// native notification API, so we delegate to the shared macOS notifier
// exactly like iTerm and Terminal.app. Best-effort — see
// internal/notify.MacOS.
func NotifyFocused(message string) error {
	return notify.MacOS("flow", message)
}

// ShellQuote wraps s in single quotes with proper escaping. Same
// implementation as iterm.ShellQuote and terminal.ShellQuote.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
