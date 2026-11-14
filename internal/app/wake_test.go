package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// fakeWaker records pushes; live/err/busy steer the gates.
type fakeWaker struct {
	live    bool
	err     error
	pushes  []harness.WakeRequest
	busy    bool
	known   bool
	checked int
	detail  string // returned on success (a Codex queue "Warning:" line)
}

func (f *fakeWaker) WakeLive(harness.WakeRequest) bool { return f.live }
func (f *fakeWaker) Wake(r harness.WakeRequest) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.pushes = append(f.pushes, r)
	return f.detail, nil
}
func (f *fakeWaker) SessionBusy(string, time.Duration) (bool, bool) {
	f.checked++
	return f.busy, f.known
}

func stubWaker(t *testing.T, f *fakeWaker) {
	t.Helper()
	orig := wakeAdapterFor
	wakeAdapterFor = func(*flowdb.Task) harness.Waker { return f }
	t.Cleanup(func() { wakeAdapterFor = orig })
	t.Setenv(wakeOffEnv, "")
}

// wakeSetup: a bound in-progress task "w" (session sid-w) plus a sender
// task "s"; returns the DB.
func wakeSetup(t *testing.T) *sql.DB {
	t.Helper()
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	hookSessionID = "" // a previous test's hook payload must not bind us
	t.Cleanup(func() { hookSessionID = "" })
	db := openFlowDB(t)
	mkBusTask(t, db, "w", "sid-w")
	return db
}

func sendTo(t *testing.T, slug, body string) string {
	t.Helper()
	return captureStdout(t, func() {
		if rc := cmdMessage([]string{"user/" + slug, body}); rc != 0 {
			t.Fatalf("message rc=%d", rc)
		}
	})
}

func TestWakeIdleLiveSessionPushesNotice(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())

	out := sendTo(t, "w", "hello there")
	if !strings.Contains(out, "woke w") {
		t.Errorf("sender output must say it woke the session: %q", out)
	}
	if len(f.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(f.pushes))
	}
	n := f.pushes[0].Notice
	for _, want := range []string{harness.WakeMarker, "1 new message", "flow inbox pop"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice %q missing %q", n, want)
		}
	}
	if strings.Contains(n, "hello there") {
		t.Errorf("notice must not carry the body (the bus stays the source of truth): %q", n)
	}
	if f.pushes[0].SessionID != "sid-w" {
		t.Errorf("pushed to %q", f.pushes[0].SessionID)
	}
	// The push starts a turn: a second message right after is NOT a
	// second notice (the session is busy now; its hooks deliver).
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionBusy {
		t.Errorf("state after push = %q, want busy", st.State)
	}
	out = sendTo(t, "w", "second")
	if len(f.pushes) != 1 || !strings.Contains(out, "stored: session busy") {
		t.Errorf("burst re-woke: pushes=%d out=%q", len(f.pushes), out)
	}
	// The push is only the wake record: the hooks still see both rows (a
	// push may be swallowed), and wake will not push for "hello" again.
	if got, _ := flowdb.ClaimWakeNudge(db, "w", time.Time{}, false, time.Now()); len(got) != 1 || got[0].Body != "second" {
		t.Errorf("wake claim after push = %d rows", len(got))
	}
	if got, _ := flowdb.ClaimUnsurfaced(db, "w"); len(got) != 2 {
		t.Errorf("hook record after push = %d rows, want 2", len(got))
	}
}

func TestWakeGates(t *testing.T) {
	cases := []struct {
		name   string
		prep   func(t *testing.T, db *sql.DB, f *fakeWaker)
		reason string
	}{
		{"busy", func(t *testing.T, db *sql.DB, f *fakeWaker) {
			_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now())
		}, "session busy"},
		{"not live", func(t *testing.T, db *sql.DB, f *fakeWaker) { f.live = false }, "no live push channel"},
		{"opt-out", func(t *testing.T, db *sql.DB, f *fakeWaker) { t.Setenv(wakeOffEnv, "1") }, "wake off"},
		{"auto run", func(t *testing.T, db *sql.DB, f *fakeWaker) {
			// A live supervisor (this test process): a dead one is reconciled.
			_, _ = db.Exec(`UPDATE tasks SET auto_run_status='running', auto_run_pid=? WHERE slug='w'`, os.Getpid())
		}, "auto run"},
		{"done", func(t *testing.T, db *sql.DB, f *fakeWaker) {
			_, _ = db.Exec(`UPDATE tasks SET status='done' WHERE slug='w'`)
		}, "task done"},
		{"archived", func(t *testing.T, db *sql.DB, f *fakeWaker) {
			_, _ = db.Exec(`UPDATE tasks SET archived_at=? WHERE slug='w'`, flowdb.NowISO())
		}, "task archived"},
		{"stale busy, harness says busy", func(t *testing.T, db *sql.DB, f *fakeWaker) {
			_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now().Add(-time.Hour))
			f.busy, f.known = true, true
		}, "session busy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := wakeSetup(t)
			f := &fakeWaker{live: true}
			stubWaker(t, f)
			c.prep(t, db, f)
			// Closed tasks refuse `flow message`; other inserts (broadcast
			// fan-out, dead-child pings) still land, so insert directly.
			m := &flowdb.BusMessage{ID: "g-" + strings.ReplaceAll(c.name, " ", "-"), CreatedAt: flowdb.NowISO(),
				Kind: "message", FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "w", Body: "x"}
			if err := flowdb.InsertBusMessage(db, m); err != nil {
				t.Fatal(err)
			}
			out := wakeOutcome(m.ID)
			if len(f.pushes) != 0 {
				t.Fatalf("pushed despite gate")
			}
			if !strings.Contains(out, "stored: "+c.reason) {
				t.Errorf("out = %q, want stored: %s", out, c.reason)
			}
			// A gated message stays unclaimed for the hooks.
			if got, _ := flowdb.ClaimUnsurfaced(db, "w"); len(got) != 1 {
				t.Errorf("gated message was claimed")
			}
		})
	}
}

// An Esc-interrupt ends a Claude turn without a Stop hook (verified live
// on 2.1.322): the hook state stays "busy". After wakeBusyRecheck of hook
// silence the harness cross-check decides.
func TestWakeQuietBusyCrossChecked(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true, busy: false, known: true}
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now().Add(-2*time.Minute))
	if out := sendTo(t, "w", "x"); !strings.Contains(out, "woke w") {
		t.Fatalf("harness says idle after an interrupt: must wake, got %q", out)
	}
	// Recent hook activity: trust the hooks, no cross-check.
	db2 := wakeSetup(t)
	f2 := &fakeWaker{live: true, busy: false, known: true}
	stubWaker(t, f2)
	_ = flowdb.SetSessionState(db2, "w", flowdb.SessionBusy, time.Now())
	if out := sendTo(t, "w", "x"); !strings.Contains(out, "stored: session busy") || f2.checked != 0 {
		t.Fatalf("fresh busy: out=%q checked=%d", out, f2.checked)
	}
	// Quiet but harness can't tell, not yet stale: still busy.
	db3 := wakeSetup(t)
	f3 := &fakeWaker{live: true}
	stubWaker(t, f3)
	_ = flowdb.SetSessionState(db3, "w", flowdb.SessionBusy, time.Now().Add(-time.Minute))
	if out := sendTo(t, "w", "x"); !strings.Contains(out, "stored: session busy") {
		t.Fatalf("unknown cross-check before stale must stay busy: %q", out)
	}
}

func TestWakeStaleBusyCountsAsIdle(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true} // cross-check unknown
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now().Add(-time.Hour))
	if out := sendTo(t, "w", "x"); !strings.Contains(out, "woke w") || len(f.pushes) != 1 {
		t.Fatalf("stale busy must wake: %q", out)
	}
	if f.checked == 0 {
		t.Error("stale busy must be cross-checked with the harness")
	}
}

// A failed push releases its claim, so the Stop check / waiter /
// PreToolUse can still announce the message.
func TestWakePushFailureReleasesClaim(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true, err: errors.New("dial: refused")}
	stubWaker(t, f)
	out := sendTo(t, "w", "x")
	if !strings.Contains(out, "stored: push failed") {
		t.Errorf("out = %q", out)
	}
	if got, _ := flowdb.ClaimWakeNudge(db, "w", time.Time{}, false, time.Now()); len(got) != 1 {
		t.Fatal("failed push kept its wake claim")
	}
	if st, _ := flowdb.GetSessionState(db, "w"); st.State == flowdb.SessionBusy {
		t.Error("failed push must not mark the session busy")
	}
}

// Human mail never wakes a session.
func TestWakeIgnoresHumanMail(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	m := &flowdb.BusMessage{ID: "h1", CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: "user", FromTaskSlug: "w", ToAssignee: "user", Body: "ask"}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		t.Fatal(err)
	}
	if len(f.pushes) != 0 {
		t.Fatal("human mail woke a session")
	}
}

// A watched broadcast wakes each idle watcher once and reports it.
func TestWakeBroadcastWatchers(t *testing.T) {
	db := wakeSetup(t)
	mkBusTask(t, db, "src", "sid-src")
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	if err := flowdb.AddWatch(db, "user/w", "src"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdBroadcast([]string{"shipped it", "--from", "src"}); rc != 0 {
			t.Fatalf("broadcast rc=%d", rc)
		}
	})
	if len(f.pushes) != 1 || !strings.Contains(out, "woke w") {
		t.Fatalf("pushes=%d out=%q", len(f.pushes), out)
	}
}

func stopHookPayload(t *testing.T, payload string) string {
	t.Helper()
	var out string
	withHookStdin(t, payload, func() {
		out = captureStdout(t, func() {
			if rc := cmdHookStop(nil); rc != 0 {
				t.Fatalf("stop rc != 0")
			}
		})
	})
	return out
}

// Mail that landed after the session's last tool call (wake saw it busy)
// is caught at Stop: block with the notice so the turn continues and pops.
func TestStopHookCatchesUnsurfacedMail(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now())
	sendTo(t, "w", "late news")
	if len(f.pushes) != 0 {
		t.Fatal("busy session was pushed")
	}
	out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":false}`)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	reason, _ := parsed["reason"].(string)
	if parsed["decision"] != "block" || !strings.Contains(reason, "flow inbox pop") ||
		!strings.Contains(reason, harness.WakeMarker) {
		t.Fatalf("stop must block with the notice: %s", out)
	}
	// The turn continues: still busy, so a concurrent sender stays quiet.
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionBusy {
		t.Errorf("state after block = %q", st.State)
	}
	// Next Stop (continuation, nothing new): ends the turn → idle, no block.
	if out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":true}`); strings.TrimSpace(out) != "" {
		t.Errorf("re-blocked for the same mail: %s", out)
	}
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionIdle {
		t.Errorf("state after final stop = %q, want idle", st.State)
	}
	// Exactly one notice: wake now sees idle but the rows are claimed.
	if got, _ := flowdb.ClaimUnsurfaced(db, "w"); len(got) != 0 {
		t.Error("stop did not claim the mail it announced")
	}
}

// New mail during a hook-driven continuation is still caught (the claim
// bounds the loop: each block needs mail no one announced yet).
func TestStopHookActiveStillCatchesNewMail(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now())
	sendTo(t, "w", "x")
	out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":true}`)
	if !strings.Contains(out, `"block"`) {
		t.Fatalf("new mail under stop_hook_active must block once: %q", out)
	}
}

func TestStopHookCodexBlocksWithNotice(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	_, _ = db.Exec(`UPDATE tasks SET harness='codex', session_id=? WHERE slug='w'`, codexHookSID)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now())
	sendTo(t, "w", "x")
	out := stopHookPayload(t, `{"session_id":"`+codexHookSID+`","stop_hook_active":false}`)
	var parsed map[string]any
	_ = json.Unmarshal([]byte(out), &parsed)
	if parsed["decision"] != "block" || parsed["hookSpecificOutput"] != nil {
		t.Fatalf("codex stop output: %s", out)
	}
}

func preToolUseSID(t *testing.T, sid string) string {
	t.Helper()
	var out string
	withHookStdin(t, `{"session_id":"`+sid+`"}`, func() {
		out = captureStdout(t, func() {
			if rc := cmdHookPreToolUse(nil); rc != 0 {
				t.Fatalf("rc")
			}
		})
	})
	return busHookContext(t, out)
}

func TestPreToolUseMarksBusyAndClaims(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: false}
	stubWaker(t, f)
	sendTo(t, "w", "mid-turn stop")
	ctx := preToolUseSID(t, "sid-w")
	if !strings.Contains(ctx, "mid-turn stop") {
		t.Fatalf("pre-tool-use ctx = %q", ctx)
	}
	if strings.Contains(ctx, "Monitor") {
		t.Errorf("wake active: no Monitor wording: %q", ctx)
	}
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionBusy {
		t.Errorf("pre-tool-use must mark busy, got %q", st.State)
	}
	if again := preToolUseSID(t, "sid-w"); again != "" {
		t.Errorf("re-announced: %q", again)
	}
	// Stop finds nothing new: no block.
	if out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":false}`); strings.Contains(out, "block") {
		t.Errorf("stop re-announced surfaced mail: %s", out)
	}
}

func TestUserPromptSubmitMarksBusy(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{})
	withHookStdin(t, `{"session_id":"sid-w","prompt":"hi"}`, func() {
		captureStdout(t, func() { cmdHookUserPromptSubmit(nil) })
	})
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionBusy {
		t.Errorf("prompt must mark busy, got %q", st.State)
	}
}

// A wake notice arriving as a prompt is never the human typing (#118).
func TestIsHumanPromptRejectsWakeNotice(t *testing.T) {
	env := func(string) string { return "" }
	n := wakeNotice(2)
	if isHumanPrompt(true, n, env) {
		t.Error("bare notice counted as human")
	}
	if isHumanPrompt(true, "Another Claude session sent a message: "+n, env) {
		t.Error("socket-framed notice counted as human")
	}
}

func TestSessionStartWakeActiveNoMonitor(t *testing.T) {
	db := wakeSetup(t)
	_ = db
	stubWaker(t, &fakeWaker{})
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-w")
	ctx := busSessionStartContext()
	if strings.Contains(ctx, "Monitor(") || strings.Contains(ctx, "arm ONE inbox Monitor") {
		t.Errorf("wake active: SessionStart still arms a Monitor: %q", ctx)
	}
	if !strings.Contains(ctx, "don't arm a listener") {
		t.Errorf("wake active: SessionStart must say not to arm a listener: %q", ctx)
	}
	t.Setenv(wakeOffEnv, "1")
	if ctx := busSessionStartContext(); !strings.Contains(ctx, "Monitor(") {
		t.Errorf("wake off: the Monitor fallback must remain: %q", ctx)
	}
}

func TestSessionStartSetsIdle(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{})
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now())
	withHookStdin(t, `{"session_id":"sid-w"}`, func() {
		captureStdout(t, func() { cmdHookSessionStart(nil) })
	})
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionIdle {
		t.Errorf("session start must reset to idle, got %q", st.State)
	}
}

// runInboxWait runs `flow inbox wait`; captureStdout folds stderr into
// the returned text, so stdout and stderr carry the same combined output.
func runInboxWait(t *testing.T, payload string, args ...string) (rc int, stdout, stderr string) {
	t.Helper()
	run := func() {
		stdout = captureStdout(t, func() { rc = cmdInbox(append([]string{"wait"}, args...)) })
	}
	if payload != "" {
		withHookStdin(t, payload, run)
	} else {
		run()
	}
	return rc, stdout, stdout
}

// `flow inbox wait` is the non-acking wait: it returns when unannounced
// mail lands, claims it (so nothing re-announces it) but leaves it unread.
func TestInboxWaitNonAcking(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{live: false})
	sendTo(t, "w", "x")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-w")
	rc, out, _ := runInboxWait(t, "", "--timeout", "2")
	if rc != 0 || !strings.Contains(out, "flow inbox pop") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if n, _ := flowdb.PendingCountForTask(db, "w"); n != 1 {
		t.Fatalf("wait consumed mail: pending=%d", n)
	}
	if rc, _, _ := runInboxWait(t, "", "--timeout", "1"); rc != 1 {
		t.Fatalf("same mail re-announced / no timeout: rc=%d", rc)
	}
	_, _ = db.Exec(`UPDATE tasks SET status='done' WHERE slug='w'`)
	if rc, _, _ := runInboxWait(t, "", "--timeout", "1"); rc != 3 {
		t.Fatalf("closed task: rc=%d, want 3", rc)
	}
}

func shortRewakeGrace(t *testing.T) {
	t.Helper()
	orig := rewakeGrace
	rewakeGrace = 0
	t.Cleanup(func() { rewakeGrace = orig })
}

// The asyncRewake fallback: exit 2 with the notice on stderr when an IDLE
// session has mail nobody announced.
func TestInboxWaitRewakeIdle(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{live: false}) // socket unavailable
	shortRewakeGrace(t)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	sendTo(t, "w", "x")
	rc, _, errOut := runInboxWait(t, `{"session_id":"sid-w","hook_event_name":"Stop"}`, "--rewake", "--timeout", "3")
	if rc != 2 || !strings.Contains(errOut, harness.WakeMarker) || !strings.Contains(errOut, "flow inbox pop") {
		t.Fatalf("rc=%d stderr=%q", rc, errOut)
	}
	if n, _ := flowdb.PendingCountForTask(db, "w"); n != 1 {
		t.Fatal("rewake consumed mail")
	}
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionBusy {
		t.Errorf("the rewake starts a turn: state=%q", st.State)
	}
}

// Busy sessions are left to the hooks; the waiter just times out quietly.
func TestInboxWaitRewakeSkipsBusy(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{live: false})
	shortRewakeGrace(t)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now())
	sendTo(t, "w", "x")
	rc, _, _ := runInboxWait(t, `{"session_id":"sid-w"}`, "--rewake", "--timeout", "1")
	if rc != 0 {
		t.Fatalf("rc=%d, want 0 (no wake while busy)", rc)
	}
	if got, _ := flowdb.ClaimUnsurfaced(db, "w"); len(got) != 1 {
		t.Fatal("waiter claimed mail of a busy session")
	}
}

func TestInboxWaitRewakeNoOps(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{})
	// Unbound session: exit at once.
	if rc, _, _ := runInboxWait(t, `{"session_id":"nobody"}`, "--rewake", "--timeout", "5"); rc != 0 {
		t.Fatalf("unbound rc=%d", rc)
	}
	// Opt-out.
	t.Setenv(wakeOffEnv, "1")
	start := time.Now()
	if rc, _, _ := runInboxWait(t, `{"session_id":"sid-w"}`, "--rewake", "--timeout", "5"); rc != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("opt-out must exit 0 at once: rc=%d", rc)
	}
	t.Setenv(wakeOffEnv, "")
	// A live waiter already holds the session's lock: no second one.
	lock := rewakeLockPath("sid-w")
	_ = os.MkdirAll(filepath.Dir(lock), 0o700)
	_ = os.WriteFile(lock, []byte(strconv.Itoa(os.Getppid())), 0o600)
	start = time.Now()
	if rc, _, _ := runInboxWait(t, `{"session_id":"sid-w"}`, "--rewake", "--timeout", "5"); rc != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("double arm: rc=%d", rc)
	}
	_ = db
}

func TestSkillInstallArmsRewakeHook(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	for _, ev := range []string{"SessionStart", "Stop"} {
		hooks, _ := readSettings(t, filepath.Join(home, ".claude", "settings.json"))["hooks"].(map[string]any)
		found := false
		entries, _ := hooks[ev].([]any)
		for _, e := range entries {
			for _, h := range e.(map[string]any)["hooks"].([]any) {
				hm := h.(map[string]any)
				if hm["command"] == rewakeHookCommand && hm["asyncRewake"] == true {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s: no asyncRewake %q hook: %v", ev, rewakeHookCommand, entries)
		}
	}
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Contains(string(raw), rewakeHookCommand) {
		t.Errorf("uninstall left the rewake hook: %s", raw)
	}
}

// The waiter also cross-checks a quiet "busy" session (Esc-interrupt left
// no Stop hook) instead of waiting for the stale mark.
func TestInboxWaitRewakeQuietBusyCrossChecked(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: false, busy: false, known: true}
	stubWaker(t, f)
	shortRewakeGrace(t)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionBusy, time.Now().Add(-time.Minute))
	sendTo(t, "w", "x")
	rc, _, out := runInboxWait(t, `{"session_id":"sid-w"}`, "--rewake", "--timeout", "3")
	if rc != 2 || !strings.Contains(out, harness.WakeMarker) {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

// shortRenudge shrinks the waiter's re-nudge window for a test.
func shortRenudge(t *testing.T, d time.Duration) {
	t.Helper()
	orig := rewakeRenudgeAfter
	rewakeRenudgeAfter = d
	t.Cleanup(func() { rewakeRenudgeAfter = orig })
}

// (a) A swallowed push (Wake returns nil, nothing delivered) never hides
// the mail: the next PreToolUse still announces it.
func TestSwallowedPushPreToolUseStillAnnounces(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	if out := sendTo(t, "w", "swallowed one"); !strings.Contains(out, "woke w") || len(f.pushes) != 1 {
		t.Fatalf("setup: push not attempted: %q", out)
	}
	ctx := preToolUseSID(t, "sid-w")
	if !strings.Contains(ctx, "swallowed one") {
		t.Fatalf("PreToolUse after a swallowed push = %q", ctx)
	}
	if again := preToolUseSID(t, "sid-w"); again != "" {
		t.Errorf("PreToolUse re-announced: %q", again)
	}
}

// Codex: `codex queue` printed a Warning and exited 0 (queued, never
// resumed). The hooks still announce the mail.
func TestCodexQueueWarningPreToolUseStillAnnounces(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true, detail: "Warning: queued but could not resume thread x"}
	stubWaker(t, f)
	_, _ = db.Exec(`UPDATE tasks SET harness='codex', session_id=? WHERE slug='w'`, codexHookSID)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	sendTo(t, "w", "queued only")
	if len(f.pushes) != 1 {
		t.Fatalf("pushes = %d", len(f.pushes))
	}
	if ctx := preToolUseSID(t, codexHookSID); !strings.Contains(ctx, "queued only") {
		t.Fatalf("PreToolUse after a codex queue Warning = %q", ctx)
	}
}

// (b)+(d) The Stop check still blocks on mail whose push was swallowed,
// exactly once.
func TestSwallowedPushStopBlocksOnce(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	sendTo(t, "w", "x")
	if len(f.pushes) != 1 {
		t.Fatal("setup: no push")
	}
	out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":false}`)
	if !strings.Contains(out, `"block"`) || !strings.Contains(out, "flow inbox pop") {
		t.Fatalf("Stop must block on the swallowed mail: %q", out)
	}
	if out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":true}`); strings.TrimSpace(out) != "" {
		t.Errorf("double Stop block: %s", out)
	}
	if out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":false}`); strings.TrimSpace(out) != "" {
		t.Errorf("Stop re-blocked on a later turn: %s", out)
	}
}

// A delivered wake prompt proves the notice is in context: it records the
// hook side, so neither PreToolUse nor Stop repeats it (no extra turn).
func TestDeliveredWakePromptRecordsHookSide(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	sendTo(t, "w", "x")
	payload, _ := json.Marshal(map[string]string{"session_id": "sid-w", "prompt": f.pushes[0].Notice})
	withHookStdin(t, string(payload), func() {
		captureStdout(t, func() { cmdHookUserPromptSubmit(nil) })
	})
	if ctx := preToolUseSID(t, "sid-w"); ctx != "" {
		t.Errorf("PreToolUse repeated a delivered wake: %q", ctx)
	}
	if out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":false}`); strings.Contains(out, "block") {
		t.Errorf("Stop blocked after a delivered wake: %s", out)
	}
}

// (c) An idle session whose push was swallowed gets ONE waiter re-nudge
// once the re-nudge window passes; never a second for the same batch.
func TestWaiterRenudgesSwallowedPushOnce(t *testing.T) {
	db := wakeSetup(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	shortRewakeGrace(t)
	shortRenudge(t, time.Second)
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	sendTo(t, "w", "x")
	if len(f.pushes) != 1 {
		t.Fatal("setup: no push")
	}
	// The push marked the session busy; a swallowed push leaves it idle in
	// reality. The waiter's harness cross-check says idle.
	f.known, f.busy = true, false
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	start := time.Now()
	rc, _, out := runInboxWait(t, `{"session_id":"sid-w","hook_event_name":"Stop"}`, "--rewake", "--timeout", "5")
	if rc != 2 || !strings.Contains(out, harness.WakeMarker) {
		t.Fatalf("waiter did not re-nudge: rc=%d out=%q", rc, out)
	}
	if el := time.Since(start); el < 500*time.Millisecond {
		t.Errorf("re-nudged before the window (%v)", el)
	}
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	if rc, _, out := runInboxWait(t, `{"session_id":"sid-w"}`, "--rewake", "--timeout", "3"); rc != 0 {
		t.Fatalf("second re-nudge for the same batch: rc=%d out=%q", rc, out)
	}
	if n, _ := flowdb.PendingCountForTask(db, "w"); n != 1 {
		t.Fatal("waiter consumed mail")
	}
}

// The waiter leaves mail the hooks already showed alone: the session
// knows, so a nudge would only cost a turn.
func TestWaiterSkipsHookSurfacedMail(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{live: false})
	shortRewakeGrace(t)
	shortRenudge(t, 0)
	sendTo(t, "w", "x")
	if ctx := preToolUseSID(t, "sid-w"); ctx == "" {
		t.Fatal("setup: PreToolUse silent")
	}
	_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
	if rc, _, out := runInboxWait(t, `{"session_id":"sid-w"}`, "--rewake", "--timeout", "2"); rc != 0 {
		t.Fatalf("waiter nudged hook-surfaced mail: rc=%d out=%q", rc, out)
	}
}

// A waiter nudge (exit 2) is itself the delivery: its stderr lands in
// context without passing UserPromptSubmit, so the waiter records the hook
// side. Neither PreToolUse nor Stop repeats it (no extra turn). Covers the
// first nudge (push unavailable) and the re-nudge of a swallowed push.
func TestWaiterNudgeRecordsHookSide(t *testing.T) {
	for _, tc := range []struct {
		name  string
		live  bool
		renud bool
	}{{"first-nudge", false, false}, {"re-nudge", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			db := wakeSetup(t)
			f := &fakeWaker{live: tc.live}
			stubWaker(t, f)
			shortRewakeGrace(t)
			shortRenudge(t, 0)
			_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
			sendTo(t, "w", "x")
			if tc.renud {
				if len(f.pushes) != 1 {
					t.Fatal("setup: no push")
				}
				f.known, f.busy = true, false
				_ = flowdb.SetSessionState(db, "w", flowdb.SessionIdle, time.Now())
			}
			rc, _, out := runInboxWait(t, `{"session_id":"sid-w","hook_event_name":"Stop"}`, "--rewake", "--timeout", "3")
			if rc != 2 || !strings.Contains(out, harness.WakeMarker) {
				t.Fatalf("waiter did not nudge: rc=%d out=%q", rc, out)
			}
			if ctx := preToolUseSID(t, "sid-w"); ctx != "" {
				t.Errorf("PreToolUse repeated the waiter nudge: %q", ctx)
			}
			if out := stopHookPayload(t, `{"session_id":"sid-w","stop_hook_active":false}`); strings.Contains(out, "block") {
				t.Errorf("Stop blocked after the waiter nudge: %s", out)
			}
		})
	}
}
