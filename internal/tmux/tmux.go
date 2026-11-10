// Package tmux provides tmux-session window spawning via the `tmux` CLI.
// Activated by spawner.Detect() when $TMUX is set in the environment
// (tmux sets it in every shell it spawns, and the CLI reads the socket
// path out of it, so a non-default `-L`/`-S` server is handled for free).
//
// Mechanism:
//
//  1. Write a self-deleting per-spawn script to os.TempDir() (mode
//  0700. whose last line is `exec env <vars> <command>`.
//  2. tmux new-window -P -F '#{pane_id}' -n <title> -c <cwd>
//     (creates + selects the window, prints the new pane's id, e.g. %7)
//  3. tmux send-keys -t <pane> -l " sh '<script-path>'"
//  4. tmux send-keys -t <pane> Enter
//
// Step 2 returns the pane id so steps 3–4 target it explicitly instead
// of "whatever pane is focused by now". Step 3 uses `-l` so tmux types
// the bytes literally (no key-name parsing of "Enter", "Space", …);
// the leading space triggers histignorespace on shells that have it
// on, matching the other backends. Step 4 submits the line.
//
// Why a script instead of typing the command (the way the zellij and
// kitty backends do): send-keys writes raw bytes to the pane's PTY, and
// the bytes land before the new window's shell has finished starting.
// Until an interactive shell switches the tty to raw mode, the tty is
// in canonical mode and buffers at most MAX_CANON bytes per line —
// 1024 on macOS (4096 on Linux). A full launch line (harness binary +
// `--session-id` + the quoted bootstrap prompt) is well over 1 KB, so
// on macOS with zsh it was cut inside the quoted prompt, the window sat
// at `quote>`, and the task was left in-progress with a session id
// that never started. The typed line is now a few dozen bytes no
// matter how long the prompt or env block is. The command is still run
// by the window's own interactive shell (so the user's rc PATH applies
// and the window stays open after the harness exits), and `exec`
// keeps the harness a direct child of that shell so the ps-based
// FocusSession keeps matching pane ttys. Passing the command to
// `new-window` directly would have skipped the user's shell (rc PATH
// lost — the usual "claude: command not found" on Debian) and closed
// the window with the harness.
//
// The script deletes itself (`rm -- "$0"`) as its first action, same
// as internal/warp. A write-then-tmux failure removes it explicitly.
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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// WriteScript writes the per-spawn script body to a private temp file
// (os.TempDir()/flow-tmux-<id>.sh, mode 0700) and returns its path.
// Tests override this to avoid touching the real filesystem.
var WriteScript = func(body string) (string, error) {
	path, err := tempScriptPath()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		return "", fmt.Errorf("write tmux launch script: %w", err)
	}
	return path, nil
}

// removeScript deletes the temp script on the error path. The happy
// path leaves cleanup to the script's own `rm -- "$0"` first line.
// Tests override to observe error-path cleanup.
var removeScript = func(path string) error {
	return os.Remove(path)
}

// SpawnTab opens a new tmux window in the current session, names it,
// sets its cwd, and starts `command` in it via a short typed line that
// runs a per-spawn script (see the package comment for why).
//
// `command` is interpolated raw into the script as `exec env … <command>`
// — it MUST already be a valid, shell-safe command line (callers quote
// arguments with ShellQuote). Embedded newlines are preserved: they sit
// inside the caller's quotes in a script file, not on a typed line.
//
// envVars are attached as an `exec env` prefix inside the script — they
// are present in the command's environment but do NOT persist in the
// window's shell after the command exits.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	scriptPath, err := WriteScript(buildScript(command, envVars))
	if err != nil {
		return err
	}
	out, err := RunnerOutput([]string{
		"new-window", "-P", "-F", "#{pane_id}", "-n", title, "-c", cwd,
	})
	if err != nil {
		_ = removeScript(scriptPath)
		return fmt.Errorf("tmux new-window: %w (is a tmux server running and $TMUX set?)", err)
	}
	paneID := strings.TrimSpace(string(out))
	if paneID == "" {
		_ = removeScript(scriptPath)
		return fmt.Errorf("tmux new-window returned an empty pane id")
	}

	line := " sh " + ShellQuote(scriptPath)
	if err := Runner([]string{"send-keys", "-t", paneID, "-l", line}); err != nil {
		_ = removeScript(scriptPath)
		return err
	}
	if err := Runner([]string{"send-keys", "-t", paneID, "Enter"}); err != nil {
		_ = removeScript(scriptPath)
		return err
	}
	return nil
}

// buildScript produces the sh script body the typed line runs:
//
//	#!/bin/sh
//	rm -- "$0"
//	exec env FOO='bar' BAZ='qux' <command>
//
// Env vars are sorted alphabetically for stable output; with none the
// last line is a bare `exec <command>`. cwd and title are not in the
// script — new-window's -c and -n already set them.
func buildScript(command string, envVars map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`rm -- "$0"`)
	b.WriteString("\n")
	if len(envVars) == 0 {
		fmt.Fprintf(&b, "exec %s\n", command)
		return b.String()
	}
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(envVars))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, ShellQuote(envVars[k])))
	}
	fmt.Fprintf(&b, "exec env %s %s\n", strings.Join(parts, " "), command)
	return b.String()
}

// tempScriptPath returns os.TempDir()/flow-tmux-<id>.sh with a 16-byte
// random hex id — plenty for concurrent spawns, and short enough that
// the typed line stays far below any tty line limit.
func tempScriptPath() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate tmux script id: %w", err)
	}
	return filepath.Join(os.TempDir(), "flow-tmux-"+hex.EncodeToString(buf[:])+".sh"), nil
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
