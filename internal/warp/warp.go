// Package warp provides Warp terminal tab spawning on macOS.
//
// Warp has no AppleScript dictionary, no `-e` flag, and no CLI for
// running commands (warpdotdev/warp#3364). The only programmatic
// surface is `warp://action/new_tab?path=<cwd>`, which opens a tab in
// cwd but accepts no command, env vars, or title. Launch configs
// can theoretically run commands but silently fail when triggered
// via URI (warpdotdev/warp#9007).
//
// So SpawnTab does the only reliable thing:
//
//  1. Write a self-deleting bootstrap script to os.TempDir(). The
//     script sets the tab title via OSC 2, cds to work_dir, and
//     `exec env`s the real command with the requested env vars.
//  2. `open warp://action/new_tab?path=<cwd>` opens the tab.
//  3. osascript activates Warp, then keystrokes `bash <script-path>`
//     + ASCII char 13 into the front session.
//
// The keystroke step needs macOS Accessibility for Warp — same gate
// the Terminal.app backend uses.
package warp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Runner runs osascript. Tests override this to capture the AppleScript
// argv without invoking osascript.
var Runner = func(args []string) error {
	cmd := exec.Command("osascript", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript failed: %v: %s", err, string(out))
	}
	return nil
}

// OpenURL runs the macOS `open` command on a URL. Tests override this
// to capture the warp:// URI without launching Warp.
var OpenURL = func(uri string) error {
	cmd := exec.Command("open", uri)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("open %s failed: %v: %s", uri, err, string(out))
	}
	return nil
}

// WriteScript writes the bootstrap script body to a per-user temp
// file and returns the absolute path. Tests override this to avoid
// touching the real filesystem.
var WriteScript = func(body string) (string, error) {
	path, err := tempScriptPath()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("write warp bootstrap script: %w", err)
	}
	return path, nil
}

// removeScript deletes the temp script on the error path. The happy
// path leaves cleanup to the script's own `rm -- "$0"` first line.
// Tests override to observe error-path cleanup.
var removeScript = func(path string) error {
	return os.Remove(path)
}

// SpawnTab opens a new Warp tab in cwd via the warp:// URI, then
// injects `command` (with envVars set, title set, and cwd entered)
// by keystroking the path to a per-spawn shell script.
//
// `command` is interpolated raw into the script as `exec env … <command>`
// — it MUST already be a valid, shell-safe command line. Callers are
// responsible for quoting any embedded arguments (typically via
// ShellQuote). This matches the iterm/terminal/zellij contract.
//
// envVars are attached as an `exec env` prefix to `command` inside
// the script — so they are present in the spawned process's
// environment but do NOT persist in the tab's shell after the command
// exits.
//
// The script writes its own `rm -- "$0"` first, so the temp file is
// unlinked the moment bash starts executing it. Bash has already
// read the script into memory at that point, so the rest of the
// script still runs after the unlink.
//
// If anything fails after the script is written, removeScript is
// called to clean up the orphaned temp file.
func SpawnTab(title, cwd, command string, envVars map[string]string) error {
	body := buildScript(title, cwd, command, envVars)

	scriptPath, err := WriteScript(body)
	if err != nil {
		return err
	}

	uri := "warp://action/new_tab?path=" + url.QueryEscape(cwd)
	if err := OpenURL(uri); err != nil {
		_ = removeScript(scriptPath)
		return err
	}

	script := buildAppleScript(scriptPath)
	if err := Runner([]string{"-e", script}); err != nil {
		_ = removeScript(scriptPath)
		if isAccessibilityDenied(err) {
			return wrapAccessibilityError(err)
		}
		return err
	}
	return nil
}

// ShellQuote wraps s in single quotes with proper escaping. Identical
// to iterm.ShellQuote / terminal.ShellQuote / zellij.ShellQuote.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildScript produces the bash script body that the keystroked
// `bash <path>` line invokes. Shape:
//
//	#!/bin/bash
//	rm -- "$0"
//	printf '\033]2;%s\007' '<title>'
//	cd '<cwd>' || exit 1
//	exec env FOO='bar' BAZ='qux' <command>
//
// Notes:
//   - Env vars are sorted alphabetically for stable test output,
//     matching the iterm/terminal/zellij contract exactly.
//   - When envVars is empty, the final line is `exec <command>` with
//     no `env` wrapper, so the command process isn't a child of env.
//   - When title is empty, the OSC 2 line is omitted.
func buildScript(title, cwd, command string, envVars map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString(`rm -- "$0"`)
	b.WriteString("\n")
	if title != "" {
		fmt.Fprintf(&b, "printf '\\033]2;%%s\\007' %s\n", ShellQuote(title))
	}
	fmt.Fprintf(&b, "cd %s || exit 1\n", ShellQuote(cwd))

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

// buildAppleScript activates Warp, waits for the new tab from
// `open warp://...` to take key focus, then keystrokes
// `bash <scriptPath>` + Return into the front Warp session.
//
// Why `keystroke (ASCII character 13)` instead of `keystroke return`
// or `key code 36`: Warp v0.2026.04 filters synthetic Return-key
// events for ~2s after typed input. Empirically:
//
//	key code 36              → swallowed (text typed, never submits)
//	keystroke return         → swallowed
//	key code 36 + Cmd        → swallowed
//	paste + key code 36      → swallowed
//	delay 2.0 + key code 36  → submits (filter window closes)
//	ASCII character 13       → submits immediately
//
// ASCII char 13 flows through Warp's input field to the shell PTY,
// where the line discipline interprets CR as line submission, before
// any UI-layer filter fires.
//
// `tell application "Warp" to activate` is needed because when flow
// runs from a non-Warp host (FLOW_TERM=warp, shell script), `open
// warp://...` opens the new tab but macOS may not foreground Warp —
// keystrokes would then hit the invoking app. Same guard as
// terminal.go.
//
// Delays calibrated against Warp v0.2026.04:
//   - 0.6s warm  — focus settle after activate (0.3s misfired on slow boxes).
//   - 1.8s cold  — cold-start Warp launch.
//   - 0.5s mid   — between typed text and CR; needed for ~90-char temp paths.
func buildAppleScript(scriptPath string) string {
	safePath := escapeAppleScriptString(scriptPath)
	return fmt.Sprintf(`set wasRunning to application id "dev.warp.Warp-Stable" is running
tell application "Warp" to activate
if wasRunning then
  delay 0.6
else
  delay 1.8
end if
tell application "System Events"
  tell process "Warp"
    keystroke "bash %s"
    delay 0.5
    keystroke (ASCII character 13)
  end tell
end tell
`, safePath)
}

// tempScriptPath returns os.TempDir()/flow-warp-<uuid>.sh. UUID is 16
// crypto/rand bytes hex-encoded — sufficient uniqueness for concurrent
// spawns. No dependency on internal/app's UUID helper because backend
// packages shouldn't import app.
func tempScriptPath() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate warp script id: %w", err)
	}
	name := fmt.Sprintf("flow-warp-%s.sh", hex.EncodeToString(buf[:]))
	return filepath.Join(os.TempDir(), name), nil
}

// isAccessibilityDenied reports whether an osascript failure looks
// like a missing-Accessibility-permission error. Matches the same
// fragments as internal/terminal.isAccessibilityDenied — macOS uses
// the same wording regardless of the target app being scripted.
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

// wrapAccessibilityError returns a Warp-specific multi-line error
// pointing at the right System Settings pane and naming "Warp" (not
// "Terminal" — that wording belongs to the Terminal.app backend).
func wrapAccessibilityError(err error) error {
	return fmt.Errorf(`Warp tab spawn requires macOS Accessibility permission for Warp.

Why this is needed: Warp exposes no AppleScript dictionary, no -e flag, and no CLI for running commands. The only way flow can inject a command into a new Warp tab is to keystroke it via System Events, which checks Accessibility against the parent app — Warp itself.

How to grant it:
  1. Open the right pane: open "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
  2. In the Accessibility list, enable the toggle for "Warp". If "Warp" is not listed, click + and add /Applications/Warp.app.
  3. Re-run the same "flow do" command.

After the grant, future "flow do" invocations from Warp spawn tabs silently with no further prompts.

Underlying osascript error: %w`, err)
}

// escapeAppleScriptString escapes a string for safe embedding in a
// double-quoted AppleScript string literal. Same implementation as
// the sibling packages — duplicated to avoid cross-package coupling.
func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
