package claude

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"flow/internal/harness"
)

// Claude wake: two channels, verified live on Claude Code 2.1.322
// (task claude-wake-research).
//
//  1. Cross-session socket (primary). Every session listens on a unix
//     socket ($CLAUDE_CODE_MESSAGING_SOCKET) and accepts, on one
//     connection, an auth line carrying $CLAUDE_CODE_MESSAGING_TOKEN then a
//     user line. The session runs it as a turn ("Another Claude session
//     sent a message: …"). Only processes inside the session see the token,
//     so flow's SessionStart hook records {socket, token, pid} (0600) under
//     $FLOW_ROOT/wake/claude/<session>.json, overwritten on every start,
//     resume and compaction (pid and token change on respawn).
//  2. asyncRewake hook (fallback): see InstallRewakeHook.

// ProcessAlive reports whether pid is a live process. Tests replace it.
var ProcessAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// InboundPolicy returns Claude Code's crossSessionInbound setting
// ("accept" | "hold" | "refuse" | "" when unset). Under hold/refuse a
// socket push is accepted on the wire but never delivered, so flow must
// not record the endpoint (the fallback waiter then takes over). Tests
// replace it.
var InboundPolicy = readInboundPolicy

func readInboundPolicy() string {
	p, err := settingsPath()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var s struct {
		CrossSessionInbound string `json:"crossSessionInbound"`
	}
	_ = json.Unmarshal(raw, &s)
	return strings.ToLower(strings.TrimSpace(s.CrossSessionInbound))
}

type wakeEndpoint struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
	PID    int    `json:"pid"`
}

func endpointPath(stateDir, sessionID string) string {
	return filepath.Join(stateDir, "claude", filepath.Base(sessionID)+".json")
}

// RecordWakeEndpoint stores the session's socket, token and pid (from the
// hook's environment) so a later `flow message` can push into it. With no
// socket in the environment (older Claude Code) or an inbound policy that
// holds/refuses socket mail, any stale record is removed instead.
func RecordWakeEndpoint(stateDir, sessionID string, getenv func(string) string) error {
	if stateDir == "" || sessionID == "" {
		return nil
	}
	p := endpointPath(stateDir, sessionID)
	ep := wakeEndpoint{
		Socket: getenv("CLAUDE_CODE_MESSAGING_SOCKET"),
		Token:  getenv("CLAUDE_CODE_MESSAGING_TOKEN"),
	}
	ep.PID, _ = strconv.Atoi(getenv("CLAUDE_PID"))
	if pol := InboundPolicy(); ep.Socket == "" || ep.Token == "" || pol == "hold" || pol == "refuse" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(p), 0o700)
	raw, _ := json.Marshal(ep)
	tmp, err := os.CreateTemp(filepath.Dir(p), ".ep-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func readWakeEndpoint(stateDir, sessionID string) (wakeEndpoint, error) {
	var ep wakeEndpoint
	raw, err := os.ReadFile(endpointPath(stateDir, sessionID))
	if err != nil {
		return ep, err
	}
	err = json.Unmarshal(raw, &ep)
	return ep, err
}

// WakeLive: an endpoint is recorded and its Claude process is alive.
func (c *claude) WakeLive(req harness.WakeRequest) bool {
	ep, err := readWakeEndpoint(req.StateDir, req.SessionID)
	return err == nil && ep.Socket != "" && ProcessAlive(ep.PID)
}

// Wake pushes req.Notice through the session's socket. A missing record
// or dead process is ErrWakeUnavailable; a failed dial/write is an error
// (the caller releases its claim so the fallback can deliver).
func (c *claude) Wake(req harness.WakeRequest) (string, error) {
	ep, err := readWakeEndpoint(req.StateDir, req.SessionID)
	if err != nil || ep.Socket == "" || !ProcessAlive(ep.PID) {
		return "", harness.ErrWakeUnavailable
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	conn, err := net.DialTimeout("unix", ep.Socket, timeout)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", ep.Socket, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	auth, _ := json.Marshal(map[string]any{"type": "auth", "token": ep.Token})
	user, _ := json.Marshal(map[string]any{"type": "user",
		"message": map[string]any{"role": "user", "content": req.Notice}})
	if _, err := conn.Write(append(append(append(auth, '\n'), user...), '\n')); err != nil {
		return "", fmt.Errorf("write %s: %w", ep.Socket, err)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	return "", nil
}

// SessionBusy cross-checks the turn state with `claude agents --json`,
// bounded by timeout (the query can take seconds right after a launch).
func (c *claude) SessionBusy(sessionID string, timeout time.Duration) (busy, known bool) {
	type res struct {
		out []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := BGCommandRunner("", []string{"agents", "--json"})
		ch <- res{out, err}
	}()
	var r res
	select {
	case r = <-ch:
	case <-time.After(timeout):
		return false, false
	}
	if r.err != nil {
		return false, false
	}
	var entries []bgAgentJSON
	if json.Unmarshal(r.out, &entries) != nil {
		return false, false
	}
	for _, e := range entries {
		if !strings.EqualFold(e.SessionID, sessionID) {
			continue
		}
		switch strings.ToLower(e.Status) {
		case "idle":
			return false, true
		case "busy", "working", "running":
			return true, true
		}
		return false, false
	}
	return false, false
}

// rewakeTimeout is the asyncRewake hook's timeout (seconds). Claude Code
// enforces it (a killed waiter is silent, no wake), so it is long: one
// day. A session idle longer than that re-arms on its next turn.
const rewakeTimeout = 86400

// InstallRewakeHook registers command as an asyncRewake hook on
// SessionStart (startup|resume|compact) and Stop. Claude Code runs it in
// the background; when it exits 2 the session starts a turn whose prompt
// is rewakeMessage + the hook's stderr. An entry for command that lost the
// async fields is repaired in place, never duplicated.
func (c *claude) InstallRewakeHook(command, rewakeMessage string) (bool, error) {
	changed := false
	for _, ev := range []struct{ event, matcher string }{{"SessionStart", hookMatcher}, {"Stop", ""}} {
		ch, err := installAsyncHook(ev.event, ev.matcher, command, rewakeMessage)
		if err != nil {
			return changed, err
		}
		changed = changed || ch
	}
	return changed, nil
}

func (c *claude) UninstallRewakeHook(command string) (bool, error) {
	a, err := uninstallHook("SessionStart", command)
	if err != nil {
		return a, err
	}
	b, err := uninstallHook("Stop", command)
	return a || b, err
}

func (c *claude) RewakeHookInstalled(command string) bool {
	path, err := settingsPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	for _, ev := range []string{"SessionStart", "Stop"} {
		ok := false
		for _, e := range s.Hooks[ev] {
			for _, h := range e.Hooks {
				if h["command"] == command && h["asyncRewake"] == true {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func installAsyncHook(event, matcher, command, rewakeMessage string) (bool, error) {
	path, err := settingsPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		raw = []byte("{}")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	want := map[string]any{"type": "command", "command": command, "asyncRewake": true,
		"timeout": float64(rewakeTimeout), "rewakeMessage": rewakeMessage}
	entries, _ := hooks[event].([]any)
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok || hm["command"] != command {
				continue
			}
			changed := false
			for k, v := range want {
				if hm[k] != v {
					hm[k] = v
					changed = true
				}
			}
			if old, _ := m["matcher"].(string); matcher != "" && old != matcher {
				m["matcher"] = matcher
				changed = true
			}
			if !changed {
				return false, nil
			}
			settings["hooks"] = hooks
			return true, writeSettings(path, settings)
		}
	}
	entry := map[string]any{"hooks": []any{want}}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	hooks[event] = append(entries, entry)
	settings["hooks"] = hooks
	return true, writeSettings(path, settings)
}
