package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const sharedTerminalHistoryLimit = "2147483647"

// tmuxConfigBody is the tiny tmux config flow ships so that browser /
// shared-terminal sessions have sensible defaults — most importantly
// mouse scroll, which 99% of new tmux users get tripped up by. We avoid
// stamping any opinion beyond "scroll should work" and let the user's
// personal ~/.tmux.conf (sourced at the bottom) override anything.
//
// Why this lives here rather than in CLI app:
//   - The terminal bridge is the only path that spawns tmux sessions.
//     `flow init` doesn't need to write it preemptively; lazy-write on
//     first ensureSharedTerminalSession call is sufficient and means
//     existing installs pick it up automatically on next session.
//   - Tests stub out sharedTerminalCommand and don't exercise the
//     filesystem write path; keeping this self-contained avoids
//     touching app init code.
const tmuxConfigBody = `# flow tmux defaults (auto-managed; safe to delete — flow will recreate
# it on the next session start).
#
# These exist so that scrolling JUST WORKS the first time you open a
# flow-spawned session without anyone having to hand-write a tmux.conf.
# Edit your personal ~/.tmux.conf for permanent customization — it is
# sourced at the bottom of this file and wins on conflicts.

# Mouse wheel scrolls tmux history. Enters copy-mode automatically;
# press 'q' or Escape to exit. Drag-select copies to OS clipboard on
# any modern terminal (OSC 52). This is the #1 ergonomic miss in
# default tmux.
set -g mouse on

# Clipboard pass-through (OSC 52). With mouse mode on, drag-select
# enters tmux copy-mode and never reaches the browser as a DOM
# selection; without this, the highlight is invisible to xterm.js
# and nothing ever lands on the OS clipboard. ` + "`on`" + ` makes tmux emit
# OSC 52 for its own copy commands too (not just inner apps), which
# is what lets flow's browser terminal capture drag-selections and
# write them to the page's clipboard.
set -g set-clipboard on

# Mouse-drag-end: copy to the active clipboard target (which is now
# OSC 52 → outer terminal) and exit copy-mode, so a single drag is
# the entire copy gesture. Bind both copy-mode and copy-mode-vi so
# this works regardless of the user's mode-keys preference.
bind-key -T copy-mode    MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel
bind-key -T copy-mode-vi MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel

# History buffer per pane. tmux does not expose a true "unlimited"
# history switch; this uses the largest value accepted by tmux so
# flow-spawned terminals can scroll back to the beginning of practical
# interactive sessions.
set -g history-limit ` + sharedTerminalHistoryLimit + `

# Source the user's personal config last so their settings win on
# conflicts. -q makes this a no-op if the file doesn't exist.
if-shell '[ -f ~/.tmux.conf ]' 'source-file -q ~/.tmux.conf'
`

var (
	tmuxConfigWriteOnce sync.Mutex
	// tmuxConfigWritten guards against re-writing the file on every
	// session spawn within a single server process. Process restart
	// happily re-checks and re-writes if needed; it's cheap.
	tmuxConfigWritten = false
)

// ensureTmuxConfig writes flow's tmux defaults to <flowRoot>/tmux.conf if
// it's missing. Returns the absolute path so callers can pass it to
// tmux via `-f`. Idempotent: writes once per server process, then short
// -circuits. Errors are returned but callers may safely ignore them —
// tmux without `-f` still works, just without our defaults.
func ensureTmuxConfig(flowRoot string) (string, error) {
	if flowRoot == "" {
		return "", fmt.Errorf("flow root not set")
	}
	path := filepath.Join(flowRoot, "tmux.conf")
	tmuxConfigWriteOnce.Lock()
	defer tmuxConfigWriteOnce.Unlock()
	if tmuxConfigWritten {
		return path, nil
	}
	// If the file already exists (any size, any content) we leave it
	// alone. Users who customize the flow defaults shouldn't have those
	// stomped on every restart. To revert: delete the file and let flow
	// recreate it.
	if _, err := os.Stat(path); err == nil {
		tmuxConfigWritten = true
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat tmux config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("mkdir for tmux config: %w", err)
	}
	if err := os.WriteFile(path, []byte(tmuxConfigBody), 0o644); err != nil {
		return "", fmt.Errorf("write tmux config: %w", err)
	}
	tmuxConfigWritten = true
	return path, nil
}

func ensureSharedTerminalScrollOptions(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("tmux session name not set")
	}
	if out, err := sharedTerminalCommand("set-option", "-t", name, "mouse", "on"); err != nil {
		return fmt.Errorf("enable tmux mouse for %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	if out, err := sharedTerminalCommand("set-option", "-t", name, "set-clipboard", "on"); err != nil {
		return fmt.Errorf("enable tmux OSC 52 clipboard for %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	if out, err := sharedTerminalCommand("set-window-option", "-t", name+":", "history-limit", sharedTerminalHistoryLimit); err != nil {
		return fmt.Errorf("set tmux history limit for %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	if err := ensureSharedTerminalCopyBindings(); err != nil {
		return err
	}
	return nil
}

func ensureSharedTerminalDefaultScrollOptions() error {
	if out, err := sharedTerminalCommand("set-option", "-g", "mouse", "on"); err != nil {
		return fmt.Errorf("enable tmux mouse globally: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := sharedTerminalCommand("set-option", "-g", "set-clipboard", "on"); err != nil {
		return fmt.Errorf("enable tmux OSC 52 clipboard globally: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := sharedTerminalCommand("set-window-option", "-g", "history-limit", sharedTerminalHistoryLimit); err != nil {
		return fmt.Errorf("set tmux global history limit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := ensureSharedTerminalCopyBindings(); err != nil {
		return err
	}
	return nil
}

// ensureSharedTerminalCopyBindings rebinds MouseDragEnd1Pane to
// copy-pipe-and-cancel in both copy-mode and copy-mode-vi. Without this
// the user's selection sits in tmux's internal buffer and never reaches
// the OS clipboard via OSC 52 — which, combined with mouse-mode being
// on, is why drag-to-copy appears broken in the browser terminal.
// Bindings are server-global in tmux, so a single call covers every
// session attached to the running server.
func ensureSharedTerminalCopyBindings() error {
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		if out, err := sharedTerminalCommand(
			"bind-key", "-T", table,
			"MouseDragEnd1Pane",
			"send-keys", "-X", "copy-pipe-and-cancel",
		); err != nil {
			return fmt.Errorf("bind tmux MouseDragEnd1Pane in %s: %w: %s", table, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
