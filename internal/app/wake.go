package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

// Harness wake: get bus mail in front of a bound session with no listener
// armed.
//
//	flow message / broadcast / dead-child ping / driver notice / attachment
//	   │  flowdb.InsertBusMessage ──► AfterInsert ──► wakeTask(slug)
//	   ▼
//	gates: wake on · task recipient · in-progress · not an auto run ·
//	       session live (harness Waker) · idle (hook-tracked state)
//	   │ idle                                   │ busy
//	   ▼                                        ▼
//	claim rows (bus_surfaced) ─► push notice    nothing: the session's own
//	(Claude socket / Codex queue)               hooks announce it — PreToolUse
//	   │ push failed → release the claim        on the next tool call, the
//	   ▼                                        Stop check at turn end
//	mark busy (the notice starts a turn)
//
// Ordering makes it race-free: Stop sets idle THEN claims; wake inserts
// THEN reads the state. Every path claims through flowdb.ClaimUnsurfaced
// (INSERT OR IGNORE), so exactly one of them announces a given message.
// Claude also arms an asyncRewake waiter (`flow inbox wait --rewake`) as
// the fallback when the socket is unavailable.

// wakeOffEnv opts out: with it set, flow never pushes notices and the
// sessions it starts keep the old Monitor listener discipline.
const wakeOffEnv = "FLOW_NO_WAKE"

const (
	// wakeStaleBusy: a "busy" session with no hook activity this long is
	// treated as idle when the harness cannot say (an Esc-interrupt ends
	// the turn without a Stop hook in BOTH harnesses — verified live). A
	// false idle is harmless: both harnesses hold a pushed notice until
	// the running turn ends (Claude injects it after the running tool,
	// Codex runs it as the next turn).
	wakeStaleBusy = 2 * time.Minute
	// wakeBusyRecheck: hook silence after which a "busy" session is
	// cross-checked with the harness (tools longer than this are common;
	// the check is cheap: `claude agents --json` takes ~0.15s).
	wakeBusyRecheck = 30 * time.Second
	// wakePushTimeout bounds one push; wake never blocks the sender long.
	wakePushTimeout = 3 * time.Second
	// wakeCrossCheckTimeout bounds the Claude `claude agents` cross-check.
	wakeCrossCheckTimeout = 3 * time.Second
	// rewakeHookCommand is the asyncRewake hook command recorded in
	// ~/.claude/settings.json. Stable — changing it orphans installs.
	rewakeHookCommand = "flow inbox wait --rewake"
	rewakeMessage     = "flow bus:"
)

// rewakeGrace leaves young rows to the sender's immediate push; the
// fallback waiter only claims mail nobody announced for this long.
var rewakeGrace = 3 * time.Second

// rewakePoll is how often a waiter checks the inbox.
const rewakePoll = time.Second

func wakeActive() bool { return os.Getenv(wakeOffEnv) == "" }

// wakeAdapterFor returns the task's harness push channel (nil when the
// harness has none). Tests replace it.
var wakeAdapterFor = func(t *flowdb.Task) harness.Waker {
	h, err := harnessForTask(t)
	if err != nil {
		return nil
	}
	w, _ := h.(harness.Waker)
	return w
}

func wakeStateDir() string {
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "wake")
}

// wakeNotice is the text pushed into a session (and the Stop block
// reason): a count and the command, never sender/subject/body — the bus
// stays the source of truth. WakeMarker keeps isHumanPrompt from taking
// it for the human typing.
func wakeNotice(pending int) string {
	return fmt.Sprintf("%s %d new message(s) in this session's flow inbox. Run `flow inbox pop` "+
		"in the foreground now (one-shot, never --wait), repeat until it says empty, and act on each.",
		harness.WakeMarker, pending)
}

// wakeResult is what wakeTask did for one inserted message.
type wakeResult struct {
	Slug   string
	Woke   bool
	Reason string // why it was only stored; or a push detail when Woke
}

func (r wakeResult) String() string {
	if r.Woke {
		if r.Reason != "" {
			return "woke " + r.Slug + " (" + r.Reason + ")"
		}
		return "woke " + r.Slug
	}
	return "stored: " + r.Reason
}

// wakeResults maps message id → outcome for this process, so the sending
// command can report it.
var wakeResults = map[string]wakeResult{}

func init() { flowdb.AfterInsert = afterBusInsert }

// wakeOutcome renders what happened to message id for the sender
// ("woke <slug>" | "stored: <reason>").
func wakeOutcome(id string) string {
	if r, ok := wakeResults[id]; ok {
		return r.String()
	}
	return "stored: delivered on its next activity or pop"
}

func afterBusInsert(db *sql.DB, m *flowdb.BusMessage) {
	if m.ToTaskSlug == "" {
		return // human mail never wakes a session
	}
	wakeResults[m.ID] = wakeTask(db, m.ToTaskSlug)
}

// wakeTask pushes a notice into slug's session if every gate passes.
// Never fails the caller; the result says what happened.
func wakeTask(db *sql.DB, slug string) wakeResult {
	r := wakeResult{Slug: slug}
	stored := func(why string) wakeResult { r.Reason = why; return r }
	if !wakeActive() {
		return stored("wake off (" + wakeOffEnv + ")")
	}
	t, err := flowdb.GetTask(db, slug)
	if err != nil || t == nil {
		return stored("no such task")
	}
	switch {
	case t.ArchivedAt.Valid:
		return stored("task archived")
	case t.Status != "in-progress":
		return stored("task " + t.Status)
	case t.AutoRunStatus.Valid && t.AutoRunStatus.String == "running":
		return stored("auto run (it pops in the foreground)")
	case !t.SessionID.Valid || t.SessionID.String == "":
		return stored("no session")
	}
	w := wakeAdapterFor(t)
	if w == nil {
		return stored("harness has no wake channel")
	}
	req := harness.WakeRequest{SessionID: t.SessionID.String, StateDir: wakeStateDir(), Timeout: wakePushTimeout}
	if !w.WakeLive(req) {
		return stored("no live push channel (a fallback waiter or its hooks deliver)")
	}
	const busyWhy = "session busy (its hooks deliver at the next tool call or turn end)"
	if !sessionIdleNow(db, slug, t.SessionID.String, w) {
		return stored(busyWhy)
	}
	won, err := flowdb.ClaimUnsurfaced(db, slug)
	if err != nil {
		return stored("claim failed: " + err.Error())
	}
	if len(won) == 0 {
		return stored("already announced to the session")
	}
	n, _ := flowdb.PendingCountForTask(db, slug)
	if n < len(won) {
		n = len(won)
	}
	req.Notice = wakeNotice(n)
	detail, err := w.Wake(req)
	if err != nil {
		ids := make([]string, len(won))
		for i, m := range won {
			ids[i] = m.ID
		}
		_ = flowdb.ReleaseSurfaced(db, slug, ids)
		return stored("push failed: " + err.Error())
	}
	_ = flowdb.SetSessionState(db, slug, flowdb.SessionBusy, time.Now())
	r.Woke, r.Reason = true, detail
	return r
}

// sessionIdleNow decides busy vs idle from the hook-tracked state. A
// "busy" session whose hooks went quiet for wakeBusyRecheck is
// cross-checked with the harness when it can tell (Claude: `claude agents
// --json`), because an Esc-interrupt ends a Claude turn without a Stop
// hook; with no answer it counts as idle only once stale (wakeStaleBusy).
func sessionIdleNow(db *sql.DB, slug, sessionID string, w harness.Waker) bool {
	st, _ := flowdb.GetSessionState(db, slug)
	if st.State != flowdb.SessionBusy {
		return true
	}
	now := time.Now()
	quiet := now.Sub(st.LastActivity)
	if quiet < wakeBusyRecheck {
		return false
	}
	if tc, ok := w.(harness.TurnStateChecker); ok {
		if busy, known := tc.SessionBusy(sessionID, wakeCrossCheckTimeout); known {
			return !busy
		}
	}
	return quiet >= wakeStaleBusy
}

// recordClaudeEndpoint is the SessionStart side of the Claude socket push:
// store this session's socket, token and pid (only hook processes see
// them). Pruning drops records whose process is gone.
func recordClaudeEndpoint(sessionID string) {
	if sessionID == "" || os.Getenv(autoRunEnvVar) != "" {
		return
	}
	if os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET") == "" && os.Getenv("CLAUDE_PID") == "" {
		return // not a Claude Code hook process
	}
	dir := wakeStateDir()
	if dir == "" {
		return
	}
	_ = claude.RecordWakeEndpoint(dir, sessionID, os.Getenv)
	pruneClaudeEndpoints(dir)
}

func pruneClaudeEndpoints(dir string) {
	entries, err := os.ReadDir(filepath.Join(dir, "claude"))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(dir, "claude", name)
		switch {
		case strings.HasSuffix(name, ".waiter.pid"):
			raw, _ := os.ReadFile(p)
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); !claude.ProcessAlive(pid) {
				_ = os.Remove(p)
			}
		case strings.HasSuffix(name, ".json"):
			sid := strings.TrimSuffix(name, ".json")
			w := claude.New().(harness.Waker)
			if !w.WakeLive(harness.WakeRequest{SessionID: sid, StateDir: dir}) {
				_ = os.Remove(p)
			}
		}
	}
}

// cmdInboxWait implements `flow inbox wait` — the NON-ACKING wait:
//
//	flow inbox wait [--timeout <s>]   block until mail no notice announced
//	                                  yet lands in this session's inbox,
//	                                  claim it, print the notice, exit 0
//	                                  (1 = timed out, 3 = task closed).
//	flow inbox wait --rewake          the Claude asyncRewake hook form
//	                                  (SessionStart + Stop): exits 2 with
//	                                  the notice on stderr, which wakes the
//	                                  idle session; 0 = nothing to do.
//
// Neither form consumes: the rows stay unread for `flow inbox pop`. The
// rewake form only claims while the session is idle (a busy session's
// hooks announce its mail), only rows older than rewakeGrace (the
// sender's socket push goes first), and runs one waiter per session (pid
// lock). It exits quietly when wake is off, in an auto run, in an unbound
// or closed session, or when the Claude process is gone.
func cmdInboxWait(args []string) int {
	fs := flagSet("inbox wait")
	rewake := fs.Bool("rewake", false, "asyncRewake hook mode (exit 2 + notice on stderr)")
	timeout := fs.Int("timeout", 3600, "seconds to wait before giving up")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *rewake {
		return rewakeWait(time.Duration(*timeout) * time.Second)
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	s := currentBusSender()
	if s.TaskSlug == "" {
		fmt.Fprintln(os.Stderr, "error: flow inbox wait needs a bound session")
		return 2
	}
	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	for {
		if taskClosed(db, s.TaskSlug) {
			fmt.Printf("task %s is closed — stop waiting (exit 3)\n", s.TaskSlug)
			return popExitTaskClosed
		}
		if won, _ := flowdb.ClaimUnsurfaced(db, s.TaskSlug); len(won) > 0 {
			n, _ := flowdb.PendingCountForTask(db, s.TaskSlug)
			if n < len(won) {
				n = len(won)
			}
			fmt.Println(wakeNotice(n))
			return 0
		}
		if !time.Now().Before(deadline) {
			return 1
		}
		time.Sleep(rewakePoll)
	}
}

func taskClosed(db *sql.DB, slug string) bool {
	t, err := flowdb.GetTask(db, slug)
	return err != nil || t == nil || t.Status == "done" || t.ArchivedAt.Valid
}

func rewakeLockPath(sessionID string) string {
	return filepath.Join(wakeStateDir(), "claude", filepath.Base(sessionID)+".waiter.pid")
}

func rewakeWait(timeout time.Duration) int {
	readHookPayload()
	if !wakeActive() || os.Getenv(autoRunEnvVar) != "" {
		return 0
	}
	sid := currentSessionID()
	if sid == "" || wakeStateDir() == "" {
		return 0
	}
	db, err := openBusDB()
	if err != nil {
		return 0
	}
	defer db.Close()
	t, err := flowdb.TaskBySessionID(db, sid)
	if err != nil || t == nil || isAutoRunSession(t) {
		return 0
	}
	if t.Harness.Valid && t.Harness.String != "" && t.Harness.String != string(harness.NameClaude) {
		return 0
	}
	// One waiter per session: SessionStart and every Stop re-fire this
	// hook, and a live waiter already covers the session.
	lock := rewakeLockPath(sid)
	if raw, err := os.ReadFile(lock); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid != os.Getpid() && claude.ProcessAlive(pid) {
			return 0
		}
	}
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		return 0
	}
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return 0
	}
	defer func() {
		if raw, err := os.ReadFile(lock); err == nil && strings.TrimSpace(string(raw)) == strconv.Itoa(os.Getpid()) {
			_ = os.Remove(lock)
		}
	}()
	parent, _ := strconv.Atoi(os.Getenv("CLAUDE_PID"))
	// idle mirrors the sender's decision (sessionIdleNow), with the
	// harness cross-check of a quiet "busy" session throttled to one per
	// wakeBusyRecheck.
	var lastCheck time.Time
	idle := func(now time.Time) bool {
		st, _ := flowdb.GetSessionState(db, t.Slug)
		if st.State != flowdb.SessionBusy {
			return true
		}
		if now.Sub(st.LastActivity) < wakeBusyRecheck || now.Sub(lastCheck) < wakeBusyRecheck {
			return now.Sub(st.LastActivity) >= wakeStaleBusy
		}
		lastCheck = now
		w := wakeAdapterFor(t)
		if w == nil {
			return now.Sub(st.LastActivity) >= wakeStaleBusy
		}
		return sessionIdleNow(db, t.Slug, t.SessionID.String, w)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if taskClosed(db, t.Slug) || !wakeActive() || (parent > 0 && !claude.ProcessAlive(parent)) {
			return 0
		}
		if now := time.Now(); hasUnannouncedBefore(db, t.Slug, now.Add(-rewakeGrace)) && idle(now) {
			if won, _ := flowdb.ClaimUnsurfacedOlderThan(db, t.Slug, now.Add(-rewakeGrace)); len(won) > 0 {
				_ = flowdb.SetSessionState(db, t.Slug, flowdb.SessionBusy, time.Now())
				n, _ := flowdb.PendingCountForTask(db, t.Slug)
				if n < len(won) {
					n = len(won)
				}
				fmt.Fprintln(os.Stderr, wakeNotice(n))
				return 2
			}
		}
		time.Sleep(rewakePoll)
	}
	return 0
}

// hasUnannouncedBefore reports whether slug has pending mail no path has
// announced yet, created before cutoff (a cheap read; no claim).
func hasUnannouncedBefore(db *sql.DB, slug string, cutoff time.Time) bool {
	ms, err := flowdb.PendingUnsurfacedForTask(db, slug)
	if err != nil {
		return false
	}
	for _, m := range ms {
		if ts, err := time.Parse(time.RFC3339, m.CreatedAt); err != nil || ts.Before(cutoff) {
			return true
		}
	}
	return false
}
