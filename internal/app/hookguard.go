package app

import (
	"flag"
	"os"
	"path/filepath"
	"time"

	"flow/internal/harness"
)

// Legacy-hook guard (two-release migration to the Claude skills-dir plugin).
//
// Claude Code now loads flow's hooks from the plugin's hooks/hooks.json,
// whose commands carry `--src plugin`. Installs made by older flow
// versions also have the bare commands in ~/.claude/settings.json, and
// this release deliberately KEEPS those entries: plugin hooks load only at
// session start, so stripping settings.json under a running session would
// silence its hooks until it restarts. Plugin and settings hooks merge,
// so a session that loaded the plugin would fire every handler twice.
//
// The guard settles it per session:
//   - a plugin-invoked handler records $FLOW_ROOT/hooks/plugin/<session_id>
//     and runs normally;
//   - a legacy-invoked handler (no --src) under Claude no-ops when that
//     marker exists — the plugin covers the session.
//
// Hooks for one event run in parallel, so at a session start the legacy
// SessionStart handler can't rely on the marker already being there. When
// the plugin is installed and enabled it waits up to legacyStartWait for
// the marker before deciding. A session that started before the plugin
// existed never gets a marker, so its legacy hooks keep working
// (compaction included) until it restarts.

// hookSrcPlugin is the --src value the plugin's hook commands pass.
const hookSrcPlugin = "plugin"

// pluginMarkerTTL bounds how long a session marker is kept.
const pluginMarkerTTL = 14 * 24 * time.Hour

// legacyStartWait is how long a legacy start-time handler waits for the
// plugin's marker before running anyway. A var so tests can shorten it.
var legacyStartWait = 2 * time.Second

// hookSrcFlag registers --src on a hook handler's flag set.
func hookSrcFlag(fs *flag.FlagSet) *string {
	return fs.String("src", "", "who invoked the hook (\"plugin\" = the Claude flow plugin; empty = legacy settings entry)")
}

func pluginMarkerDir() string {
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "hooks", "plugin")
}

func pluginMarkerPath(sessionID string) string {
	dir := pluginMarkerDir()
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, filepath.Base(sessionID))
}

// markPluginSession records that the plugin's hooks cover sessionID.
// Every plugin handler calls it (cheap when the marker exists), so a
// plugin loaded mid-session by /reload-plugins is picked up too.
func markPluginSession(sessionID string, prune bool) {
	p := pluginMarkerPath(sessionID)
	if p == "" {
		return
	}
	if _, err := os.Stat(p); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, nil, 0o600)
	if prune {
		prunePluginMarkers()
	}
}

func prunePluginMarkers() {
	dir := pluginMarkerDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-pluginMarkerTTL)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func pluginMarked(sessionID string) bool {
	p := pluginMarkerPath(sessionID)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// claudeHookProcess reports whether this process was started by Claude
// Code as a hook (the env Claude Code gives hook processes).
func claudeHookProcess() bool {
	return os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET") != "" || os.Getenv("CLAUDE_PID") != ""
}

// claudePluginActive reports whether the flow plugin is on disk and not
// disabled by the user, i.e. whether a Claude session starting now loads
// the plugin's hooks.
func claudePluginActive() bool {
	h, err := harnessByName(string(harness.NameClaude))
	if err != nil {
		return false
	}
	ph, ok := h.(harness.PluginHookInstaller)
	return ok && ph.PluginInstalled() && !ph.PluginDisabled()
}

// hookSuperseded is the guard every flow hook handler runs right after it
// reads its payload. src is the --src value; atStart is true for a
// handler running on a session start (SessionStart that isn't a
// compaction). It returns true when this invocation must no-op: a legacy
// settings.json entry in a Claude session the plugin already covers.
func hookSuperseded(src string, atStart bool) bool {
	sid := currentSessionID()
	if src == hookSrcPlugin {
		markPluginSession(sid, atStart)
		return false
	}
	if sid == "" || !claudeHookProcess() {
		return false // Codex / prx / a manual run: no plugin there
	}
	if pluginMarked(sid) {
		return true
	}
	if !atStart || !claudePluginActive() {
		return false
	}
	deadline := time.Now().Add(legacyStartWait)
	for time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		if pluginMarked(sid) {
			return true
		}
	}
	return false
}
