// Package tmux provides tmux-session window spawning via the `tmux` CLI.
// Activated by spawner.Detect() when $TMUX is set in the environment
// (tmux sets it in every shell it spawns, and the CLI reads the socket
// path out of it, so a non-default `-L`/`-S` server is handled for free).
//
// Mechanism:
//
//  1. tmux new-window -P -F '#{pane_id}' -n <title> -c <cwd>
//     (creates + selects the window, prints the new pane's id, e.g. %7)
//  2. tmux send-keys -t <pane> -l " <env-prefix><flattened-command>"
//  3. tmux send-keys -t <pane> Enter
//
// Step 1 returns the pane id so steps 2–3 target it explicitly instead
// of "whatever pane is focused by now". Step 2 uses `-l` so tmux types
// the bytes literally (no key-name parsing of "Enter", "Space", …);
// the leading space triggers histignorespace on shells that have it
// on, matching the other backends. Step 3 submits the line.
//
// Newline handling: send-keys writes raw bytes to the pane's PTY, so
// embedded `\n` would submit partial lines. Same flattening rule as
// the zellij and kitty backends.
//
// This is the first terminal backend that works on Linux and macOS
// alike; it is also the one `flow do` recommends when no supported
// terminal is detected on a non-macOS host.
//
// This file mirrors the contract of internal/zellij and internal/kitty:
// same SpawnTab / FocusSession signatures, same Runner / RunnerOutput /
// PSRunner mock vars for tests, same ShellQuote helper.
package tmux

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Runner executes tmux for side-effect calls (send-keys, select-*).
// Tests override this to capture argv without invoking the real CLI.
var Runner = func(args []string) error {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux failed: %v: %s", err, string(out))
	}
	return nil
}

// RunnerOutput executes tmux and returns stdout. Used to read the new
// pane id from `new-window -P` and the pane↔tty table from list-panes.
var RunnerOutput = func(args []string) ([]byte, error) {
	return exec.Command("tmux", args...).Output()
}

// PSRunner returns the output of `ps -axo pid,tty,command`. Overridable
// for tests. FocusSession maps a harness process → tty → tmux pane.
// Both procps (Linux) and the BSD ps on macOS accept this form.
var PSRunner = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,tty,command").Output()
}

// SpawnTab opens a new tmux window in the current session, names it,
// sets its cwd, and types `command` into the new pane's PTY.
//
// envVars are attached as an inline shell prefix to `command` only —
// they are present in the command's environment but do NOT persist
// in the window's shell after the command exits.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	out, err := RunnerOutput([]string{
		"new-window", "-P", "-F", "#{pane_id}", "-n", title, "-c", cwd,
	})
	if err != nil {
		return fmt.Errorf("tmux new-window: %w (is a tmux server running and $TMUX set?)", err)
	}
	paneID := strings.TrimSpace(string(out))
	if paneID == "" {
		return fmt.Errorf("tmux new-window returned an empty pane id")
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
	line := " " + envPrefix + flat
	if err := Runner([]string{"send-keys", "-t", paneID, "-l", line}); err != nil {
		return err
	}
	return Runner([]string{"send-keys", "-t", paneID, "Enter"})
}

// sessionUUIDRowRe matches a `ps` line carrying a session UUID via
// `--session-id <uuid>` or `--resume <uuid>` — same shape as the other
// backends so focus behaviour stays consistent.
var sessionUUIDRowRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// FocusSession tries to select the tmux window/pane currently running
// `binary` with the given session UUID. Returns (true, nil) on focus,
// (false, nil) if no pane on this server matches, and (false, err)
// only on a backend failure.
//
// Mechanism: `ps -axo pid,tty,command` finds the harness process and
// its controlling tty; `tmux list-panes -a -F '#{pane_id} #{pane_tty}'`
// maps ttys to panes across every session on the server; the first
// match gets `select-window` + `select-pane`. Panes in other tmux
// sessions are selected but not attached — the caller's "running
// elsewhere" message still applies in that case.
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
	out, err := RunnerOutput([]string{"list-panes", "-a", "-F", "#{pane_id} #{pane_tty}"})
	if err != nil {
		return false, fmt.Errorf("tmux list-panes: %w", err)
	}
	paneID := ""
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == tty {
			paneID = fields[0]
			break
		}
	}
	if paneID == "" {
		return false, nil
	}
	if err := Runner([]string{"select-window", "-t", paneID}); err != nil {
		return false, fmt.Errorf("tmux select-window: %w", err)
	}
	if err := Runner([]string{"select-pane", "-t", paneID}); err != nil {
		return false, fmt.Errorf("tmux select-pane: %w", err)
	}
	return true, nil
}

// ttyForHarnessSession returns the controlling tty (e.g. "/dev/pts/3"
// or "/dev/ttys012") of the process matching `binary` and carrying the
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
		m := sessionUUIDRowRe.FindStringSubmatch(line)
		if len(m) < 2 || strings.ToLower(m[1]) != needle {
			continue
		}
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

// ShellQuote wraps s in single quotes with proper escaping. Same
// implementation as every other backend.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
