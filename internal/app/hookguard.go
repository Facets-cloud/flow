package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"flow/internal/harness"
)

// Duplicate-copy guard for the move to the Claude skills-dir plugin.
//
// Claude Code loads flow's hooks from the plugin's hooks/hooks.json
// (commands carry a FLOW_HOOK_SRC=plugin prefix). An install made by an older flow also
// has the bare commands in ~/.claude/settings.json until the legacy strip
// (legacystrip.go) removes them. Plugin and settings hooks merge and a
// plugin's copy is never deduplicated against a settings copy, so a
// session that loaded both would run every handler twice — in parallel.
//
// Every handler therefore CLAIMS its invocation before doing anything:
// an O_EXCL create of a claim file under $FLOW_ROOT/hooks/claims/. The
// first copy to claim runs; the other exits 0 silently. Two key kinds:
//
//   - claimHandle(handler, payload, handle): keyed on (handler, session,
//     a natively unique id the hook input carries) with no time window —
//     PreToolUse's tool_use_id, UserPromptSubmit's prompt_id.
//   - claim(handler, payload): keyed on sha256(handler, session, raw
//     stdin) and valid for claimWindow — SessionStart, Stop and the
//     rewake waiter, whose input has no per-invocation id. Both copies of
//     one event get byte-identical stdin; a genuinely repeated identical
//     event after the window runs again.
//
// A plugin invocation also records the session as "on plugin"
// ($FLOW_ROOT/hooks/plugin/<session_id>); the legacy strip waits until
// every live Claude session is on plugin (or gone).
//
// Only Claude hook processes claim: Codex and prx have one hook copy.

// hookSrcPlugin is the source value the plugin's hook commands set
// (FLOW_HOOK_SRC=plugin; `--src plugin` is accepted too).
const hookSrcPlugin = "plugin"

// hookSrcEnv carries the hook source in the plugin's commands.
const hookSrcEnv = "FLOW_HOOK_SRC"

// claimWindow is how long a payload-hash claim blocks an identical
// invocation. Both copies of one event start within milliseconds.
var claimWindow = 5 * time.Second

// claimTTL is how long any claim file is kept before pruning; handle
// claims have no window but their ids never recur, so pruning is safe.
const claimTTL = time.Hour

// pluginMarkerTTL bounds how long an "on plugin" session marker is kept.
const pluginMarkerTTL = 14 * 24 * time.Hour

// hookSrcFlag registers --src on a hook handler's flag set.
func hookSrcFlag(fs *flag.FlagSet) *string {
	return fs.String("src", "", "who invoked the hook (\"plugin\" = the Claude flow plugin; empty = legacy settings entry)")
}

func hooksStateDir() string {
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "hooks")
}

func pluginMarkerPath(sessionID string) string {
	dir := hooksStateDir()
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, "plugin", strings.ToLower(filepath.Base(sessionID)))
}

// markPluginSession records that sessionID loaded the flow plugin.
func markPluginSession(sessionID string) {
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
}

// sessionOnPlugin reports whether a plugin copy ever ran in sessionID.
func sessionOnPlugin(sessionID string) bool {
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

// claudePlugin returns the Claude harness's plugin capability.
func claudePlugin() harness.PluginHookInstaller {
	h, err := harnessByName(string(harness.NameClaude))
	if err != nil {
		return nil
	}
	ph, _ := h.(harness.PluginHookInstaller)
	return ph
}

// claudePluginActive reports whether the flow plugin is on disk and not
// disabled by the user, i.e. whether a Claude session starting now loads
// the plugin's hooks.
func claudePluginActive() bool {
	ph := claudePlugin()
	return ph != nil && ph.PluginInstalled() && !ph.PluginDisabled()
}

// hookHandle is the natively unique id a handler's input carries, "" when
// it has none (the payload-hash claim applies).
func hookHandle(handler string, p hookPayload) string {
	switch handler {
	case "pre-tool-use":
		return p.ToolUseID
	case "user-prompt-submit":
		return p.PromptID
	}
	return ""
}

// hookLoses is the guard every flow hook handler runs right after it reads
// its payload: true means another copy of this invocation already claimed
// it and this one must exit 0 silently.
func hookLoses(handler, src string, p hookPayload) bool {
	if src == "" {
		src = os.Getenv(hookSrcEnv)
	}
	sid := currentSessionID()
	if src == hookSrcPlugin {
		markPluginSession(sid)
	}
	if sid == "" || !claudeHookProcess() {
		return false // Codex / prx / a manual run: one copy only
	}
	if legacyStripDone() {
		return false // settings copies are gone: the plugin is the only copy
	}
	if h := hookHandle(handler, p); h != "" {
		return !claimHandle(handler, sid, h)
	}
	return !claim(handler, sid, hookRawPayload)
}

// claim claims (handler, session, payload) for claimWindow.
func claim(handler, sessionID string, payload []byte) bool {
	sum := sha256.Sum256([]byte("hash\x00" + handler + "\x00" + sessionID + "\x00" + string(payload)))
	return claimKey(hex.EncodeToString(sum[:]), claimWindow)
}

// claimHandle claims (handler, session, handle) with no window.
func claimHandle(handler, sessionID, handle string) bool {
	sum := sha256.Sum256([]byte("handle\x00" + handler + "\x00" + sessionID + "\x00" + handle))
	return claimKey(hex.EncodeToString(sum[:]), 0)
}

// claimKey atomically creates claims/<key>. window > 0 lets a claim older
// than window be re-claimed. The stale-replace and pruning run under an
// exclusive flock on claims/.lock so two copies can never both win; any
// filesystem error fails open (the handler runs).
func claimKey(key string, window time.Duration) bool {
	dir := hooksStateDir()
	if dir == "" {
		return true
	}
	dir = filepath.Join(dir, "claims")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true
	}
	lf, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return true
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	path := filepath.Join(dir, key)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) && window > 0 {
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) >= window {
			_ = os.Remove(path)
			f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		}
	}
	if errors.Is(err, os.ErrExist) {
		return false
	}
	if err != nil {
		return true
	}
	_ = f.Close()
	pruneClaims(dir)
	return true
}

// pruneClaims drops claim files past claimTTL and plugin markers past
// pluginMarkerTTL, at most once per 10 minutes (tracked by the lock
// file's sibling stamp). Called with the claims lock held.
func pruneClaims(dir string) {
	stamp := filepath.Join(dir, ".pruned")
	if fi, err := os.Stat(stamp); err == nil && time.Since(fi.ModTime()) < 10*time.Minute {
		return
	}
	_ = os.WriteFile(stamp, nil, 0o600)
	prune := func(d string, ttl time.Duration) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		cutoff := time.Now().Add(-ttl)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(d, e.Name()))
			}
		}
	}
	prune(dir, claimTTL)
	prune(filepath.Join(filepath.Dir(dir), "plugin"), pluginMarkerTTL)
}
