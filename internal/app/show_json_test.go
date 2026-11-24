package app

import (
	"encoding/json"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/harness/claude"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeShowJSON runs `flow show task <slug> --json` and decodes stdout.
func decodeShowJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var rc int
	out := captureStdout(t, func() { rc = cmdShow(append([]string{"task"}, args...)) })
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, out)
	}
	return m
}

func stubClaudeAgents(t *testing.T, raw string) {
	t.Helper()
	old := claude.BGCommandRunner
	claude.BGCommandRunner = func(string, []string) ([]byte, error) { return []byte(raw), nil }
	t.Cleanup(func() { claude.BGCommandRunner = old })
}

// TestShowTaskJSONUnbootstrapped: a backlog task has a null harness and
// session, live=false, and the stable identity/work_dir fields.
func TestShowTaskJSONUnbootstrapped(t *testing.T) {
	root, db := showListEditDB(t)
	wd := filepath.Join(root, "repo")
	insertProject(t, db, "demo", "Demo", wd, "high")
	insertTask(t, db, "auth-fix", "Fix auth", "backlog", "high", wd, "demo")
	stubPS(t, "  PID COMMAND\n")
	stubClaudeAgents(t, "[]")

	m := decodeShowJSON(t, "auth-fix", "--json")
	if m["schema"] != float64(1) {
		t.Errorf("schema = %v, want 1", m["schema"])
	}
	for k, want := range map[string]any{
		"slug": "auth-fix", "name": "Fix auth", "project": "demo",
		"status": "backlog", "priority": "high", "work_dir": wd,
		"live": false, "archived": false,
	} {
		if m[k] != want {
			t.Errorf("%s = %#v, want %#v", k, m[k], want)
		}
	}
	for _, k := range []string{"harness", "session_id", "bg", "auto_run"} {
		v, ok := m[k]
		if !ok {
			t.Errorf("key %q missing (must be present, null)", k)
		} else if v != nil {
			t.Errorf("%s = %#v, want null", k, v)
		}
	}
	if ps, ok := m["previous_sessions"].([]any); !ok || len(ps) != 0 {
		t.Errorf("previous_sessions = %#v, want []", m["previous_sessions"])
	}
	if m["brief"] != filepath.Join(root, "tasks", "auth-fix", "brief.md") {
		t.Errorf("brief = %v", m["brief"])
	}
}

// TestShowTaskJSONLiveSessionAndAutoRun: a bootstrapped claude task reports
// its harness, pinned session, live flag (per-harness LiveSessionIDs), the
// background registry entry and the auto-run state.
func TestShowTaskJSONLiveSessionAndAutoRun(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "live-one", "Live", "in-progress", "medium", filepath.Join(root, "repo"), nil)
	sid := fakeSessionID("live-one")
	if _, err := db.Exec(`UPDATE tasks SET harness='claude', auto_run_status='running',
		auto_run_pid=4242, auto_run_started='2026-11-24T10:00:00+05:30', auto_run_log='/x/run.log'
		WHERE slug='live-one'`); err != nil {
		t.Fatal(err)
	}
	oldAlive := processAlive
	processAlive = func(int) bool { return true }
	t.Cleanup(func() { processAlive = oldAlive })
	stubPS(t, "  PID COMMAND\n12345 /bin/claude --resume "+sid+"\n")
	stubClaudeAgents(t, `[{"kind":"background","id":"abcd1234","sessionId":"`+sid+`","pid":777,"status":"busy","state":"working"}]`)

	m := decodeShowJSON(t, "--json", "live-one")
	if m["harness"] != "claude" || m["session_id"] != sid || m["live"] != true {
		t.Errorf("harness/session/live = %v/%v/%v", m["harness"], m["session_id"], m["live"])
	}
	bg, ok := m["bg"].(map[string]any)
	if !ok {
		t.Fatalf("bg = %#v, want object", m["bg"])
	}
	if bg["state"] != "working" || bg["status"] != "busy" || bg["pid"] != float64(777) || bg["short_id"] != "abcd1234" {
		t.Errorf("bg = %#v", bg)
	}
	ar, ok := m["auto_run"].(map[string]any)
	if !ok {
		t.Fatalf("auto_run = %#v, want object", m["auto_run"])
	}
	if ar["status"] != "running" || ar["pid"] != float64(4242) || ar["log"] != "/x/run.log" {
		t.Errorf("auto_run = %#v", ar)
	}
}

// TestShowTaskJSONLegacyNullHarnessIsClaude: rows from before the harness
// column carry NULL; once bootstrapped they are claude (back-compat).
func TestShowTaskJSONLegacyNullHarnessIsClaude(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "legacy", "Legacy", "in-progress", "low", filepath.Join(root, "repo"), nil)
	stubPS(t, "  PID COMMAND\n")
	stubClaudeAgents(t, "[]")
	m := decodeShowJSON(t, "legacy", "--json")
	if m["harness"] != "claude" || m["live"] != false {
		t.Errorf("harness/live = %v/%v", m["harness"], m["live"])
	}
}

// TestShowTaskJSONPreviousSessions: sessions replaced by a re-pin or --fresh
// are listed oldest first.
func TestShowTaskJSONPreviousSessions(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "moved", "Moved", "in-progress", "low", filepath.Join(root, "repo"), nil)
	if err := flowdb.RecordPriorSession(db, "moved", "claude", "old-claude-sid", "2026-11-01T00:00:00Z", "repin"); err != nil {
		t.Fatal(err)
	}
	stubPS(t, "  PID COMMAND\n")
	stubClaudeAgents(t, "[]")
	m := decodeShowJSON(t, "moved", "--json")
	ps, _ := m["previous_sessions"].([]any)
	if len(ps) != 1 {
		t.Fatalf("previous_sessions = %#v", m["previous_sessions"])
	}
	p := ps[0].(map[string]any)
	if p["harness"] != "claude" || p["session_id"] != "old-claude-sid" || p["reason"] != "repin" ||
		p["session_started"] != "2026-11-01T00:00:00Z" || !strings.Contains(p["ended_at"].(string), "T") {
		t.Errorf("previous session = %#v", p)
	}
}

// A background Claude session (claude --bg) is not visible to the ps-based
// LiveSessionIDs probe, so live also holds when the bg registry entry's pid
// is alive. A registry entry whose pid is dead (or 0) does not count.
func TestShowTaskJSONLiveFromBackgroundPID(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "bg-live", "BG", "in-progress", "medium", filepath.Join(root, "repo"), nil)
	sid := fakeSessionID("bg-live")
	if _, err := db.Exec(`UPDATE tasks SET harness='claude' WHERE slug='bg-live'`); err != nil {
		t.Fatal(err)
	}
	stubPS(t, "  PID COMMAND\n") // ps sees nothing

	oldAlive := processAlive
	t.Cleanup(func() { processAlive = oldAlive })
	for _, tc := range []struct {
		name  string
		pid   int
		alive bool
		want  bool
	}{
		{"bg pid alive", 777, true, true},
		{"bg pid dead", 777, false, false},
		{"bg pid 0 (exited)", 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubClaudeAgents(t, fmt.Sprintf(`[{"kind":"background","id":"abcd1234","sessionId":"%s","pid":%d,"status":"idle","state":"idle"}]`, sid, tc.pid))
			alive := tc.alive
			processAlive = func(pid int) bool { return pid == tc.pid && alive }
			m := decodeShowJSON(t, "bg-live", "--json")
			if m["live"] != tc.want {
				t.Errorf("live = %v, want %v", m["live"], tc.want)
			}
			if bg, _ := m["bg"].(map[string]any); bg == nil || bg["pid"] != float64(tc.pid) {
				t.Errorf("bg = %#v", m["bg"])
			}
		})
	}
}

// CodeRabbit #200: a failed liveness probe is unknown, not stopped: live is
// JSON null. An unbootstrapped task stays false.
func TestShowTaskJSONLiveNullWhenProbeFails(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "probe-err", "P", "in-progress", "low", filepath.Join(root, "repo"), nil)
	stubClaudeAgents(t, "[]")
	old := claude.PSRunner
	claude.PSRunner = func() ([]byte, error) { return nil, errors.New("ps failed") }
	t.Cleanup(func() { claude.PSRunner = old })
	m := decodeShowJSON(t, "probe-err", "--json")
	if v, ok := m["live"]; !ok || v != nil {
		t.Errorf("live = %#v (present=%v), want null", v, ok)
	}
}

// CodeRabbit #200: an unreadable task directory is an error, not "other": [].
func TestShowTaskJSONAuxDiscoveryErrorFails(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "aux-err", "A", "backlog", "low", filepath.Join(root, "repo"), nil)
	dir := filepath.Join(root, "tasks", "aux-err")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Open(dir); err == nil {
		f.Close()
		t.Skip("directory still readable (running as root?)")
	}
	stubPS(t, "  PID COMMAND\n")
	stubClaudeAgents(t, "[]")
	var rc int
	_ = captureStdout(t, func() { rc = cmdShow([]string{"task", "aux-err", "--json"}) })
	if rc == 0 {
		t.Error("rc=0, want an error when aux-file discovery fails")
	}
}

// CodeRabbit #200: a replaced session with no recorded start time reports
// session_started as JSON null, not "".
func TestShowTaskJSONPreviousSessionNullStart(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "nostart", "N", "in-progress", "low", filepath.Join(root, "repo"), nil)
	if err := flowdb.RecordPriorSession(db, "nostart", "claude", "old-sid", "", "fresh"); err != nil {
		t.Fatal(err)
	}
	stubPS(t, "  PID COMMAND\n")
	stubClaudeAgents(t, "[]")
	m := decodeShowJSON(t, "nostart", "--json")
	ps, _ := m["previous_sessions"].([]any)
	if len(ps) != 1 {
		t.Fatalf("previous_sessions = %#v", m["previous_sessions"])
	}
	if v, ok := ps[0].(map[string]any)["session_started"]; !ok || v != nil {
		t.Errorf("session_started = %#v (present=%v), want null", v, ok)
	}
}
