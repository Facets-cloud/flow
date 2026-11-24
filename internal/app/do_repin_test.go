package app

import (
	"flow/internal/flowdb"
	"strings"
	"testing"
)

const repinOldSID = "658bf2be-5ae3-4842-a8a4-e0d0b785514d"

func seedClaudeBoundTask(t *testing.T, slug string) {
	t.Helper()
	seedTask(t, slug)
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', harness='claude', session_id=?, session_started='2026-11-01T00:00:00Z' WHERE slug=?`,
		repinOldSID, slug,
	); err != nil {
		t.Fatal(err)
	}
}

// `flow do <slug> --harness codex --fresh` re-pins a bootstrapped claude
// task: a NEW codex session, harness column switched, old session kept in
// history.
func TestCmdDoHarnessFreshRepins(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "repin-me")
	stubPS(t, "  PID COMMAND\n")
	const newSID = "019ff4b1-6162-7263-974f-f5f1866ad0fe"
	stubCodexProbe(t, newSID)
	_, script := stubITerm(t)

	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"repin-me", "--harness", "codex", "--fresh"}) })
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	if !strings.Contains(out, "re-pinned") || !strings.Contains(out, "claude") || !strings.Contains(out, "codex") {
		t.Errorf("output should announce the re-pin: %s", out)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "repin-me")
	if err != nil {
		t.Fatal(err)
	}
	if task.Harness.String != "codex" || task.SessionID.String != newSID {
		t.Errorf("task = harness %q session %q, want codex/%s", task.Harness.String, task.SessionID.String, newSID)
	}
	if !strings.Contains(script(), " resume "+newSID) {
		t.Errorf("spawn did not open the new codex session: %q", script())
	}
	prev, err := flowdb.PriorSessions(db, "repin-me")
	if err != nil {
		t.Fatal(err)
	}
	if len(prev) != 1 || prev[0].Harness != "claude" || prev[0].SessionID != repinOldSID ||
		prev[0].Reason != "repin" || prev[0].SessionStarted == nil || *prev[0].SessionStarted != "2026-11-01T00:00:00Z" {
		t.Errorf("prior sessions = %+v", prev)
	}
}

// Without --fresh the refusal stays, and now names the re-pin lane.
func TestCmdDoHarnessWithoutFreshStillRefusesAndHints(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "pinned")
	_, _ = stubITerm(t)
	stderr := captureStderr(t)
	if rc := cmdDo([]string{"pinned", "--harness", "codex"}); rc == 0 {
		t.Fatal("want refusal")
	}
	got := stderr()
	if !strings.Contains(got, "only selects the first session") || !strings.Contains(got, "--fresh") {
		t.Errorf("stderr = %q", got)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "pinned")
	if task.Harness.String != "claude" || task.SessionID.String != repinOldSID {
		t.Errorf("refused command changed the row: %+v", task)
	}
}

// A plain --fresh (same harness) also keeps the discarded session in
// history, with reason "fresh".
func TestCmdDoFreshRecordsPriorSession(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "fresh-hist")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	stubPS(t, "  PID COMMAND\n")
	const newSID = "11111111-2222-3333-4444-555555555555"
	stubNewUUID(t, newSID)
	_, _ = stubITerm(t)
	if rc := cmdDo([]string{"fresh-hist", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "fresh-hist")
	if task.Harness.String != "claude" || task.SessionID.String != newSID {
		t.Errorf("task = %q/%q", task.Harness.String, task.SessionID.String)
	}
	prev, _ := flowdb.PriorSessions(db, "fresh-hist")
	if len(prev) != 1 || prev[0].SessionID != repinOldSID || prev[0].Reason != "fresh" {
		t.Errorf("prior sessions = %+v", prev)
	}
}

// --harness <same> --fresh on a bootstrapped task is just a fresh session.
func TestCmdDoSameHarnessFreshIsPlainFresh(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "same-h")
	stubPS(t, "  PID COMMAND\n")
	const newSID = "22222222-2222-3333-4444-555555555555"
	stubNewUUID(t, newSID)
	_, _ = stubITerm(t)
	if rc := cmdDo([]string{"same-h", "--harness", "claude", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	prev, _ := flowdb.PriorSessions(db, "same-h")
	if len(prev) != 1 || prev[0].Reason != "fresh" {
		t.Errorf("prior sessions = %+v", prev)
	}
}

// An unknown harness on re-pin is rejected before touching the row.
func TestCmdDoRepinUnknownHarness(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "bad-h")
	_, _ = stubITerm(t)
	stderr := captureStderr(t)
	if rc := cmdDo([]string{"bad-h", "--harness", "nope", "--fresh"}); rc == 0 {
		t.Fatal("want error")
	}
	if !strings.Contains(stderr(), "invalid --harness") {
		t.Error("missing invalid --harness error")
	}
	db := openFlowDB(t)
	prev, _ := flowdb.PriorSessions(db, "bad-h")
	if len(prev) != 0 {
		t.Errorf("history written on refusal: %+v", prev)
	}
}

// In background mode ($FLOW_TERM=bg) a re-pin spawns the new harness's
// background session, switches the pin and keeps the old session.
func TestCmdDoBackgroundRepin(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bg-repin")
	db := openFlowDB(t)
	const oldPrx = "019f8f30-194b-7c83-8564-bff77c5e0147"
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', harness='prx', session_id=?, session_started='2026-11-02T00:00:00Z' WHERE slug='bg-repin'`, oldPrx); err != nil {
		t.Fatal(err)
	}
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bg-repin", "--harness", "claude", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.spawn != 1 {
		t.Fatalf("claude bg spawn calls = %d, want 1", c.spawn)
	}
	task, _ := flowdb.GetTask(db, "bg-repin")
	if task.Harness.String != "claude" || task.SessionID.String != bgFullSID {
		t.Errorf("task = %q/%q", task.Harness.String, task.SessionID.String)
	}
	prev, _ := flowdb.PriorSessions(db, "bg-repin")
	if len(prev) != 1 || prev[0].Harness != "prx" || prev[0].SessionID != oldPrx || prev[0].Reason != "repin" {
		t.Errorf("prior sessions = %+v", prev)
	}
}

// CodeRabbit #200: the re-pin decision must come from the row read inside
// the transaction. If another command re-pins the task to codex between
// flow do's snapshot and its transaction, `--harness claude --fresh` must
// still leave a claude session under a claude pin (never a claude session
// under a codex pin).
func TestCmdDoRepinDecidedFromRowInTx(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "race-pin")
	stubPS(t, "  PID COMMAND\n")
	const newSID = "33333333-2222-3333-4444-555555555555"
	stubNewUUID(t, newSID)
	_, _ = stubITerm(t)
	old := doBeforeTxHook
	doBeforeTxHook = func(slug string) {
		db := openFlowDB(t)
		if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug=?`, slug); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { doBeforeTxHook = old })

	if rc := cmdDo([]string{"race-pin", "--harness", "claude", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "race-pin")
	if task.Harness.String != "claude" || task.SessionID.String != newSID {
		t.Errorf("task = harness %q session %q, want claude/%s", task.Harness.String, task.SessionID.String, newSID)
	}
	prev, _ := flowdb.PriorSessions(db, "race-pin")
	if len(prev) != 1 || prev[0].Harness != "codex" || prev[0].Reason != "repin" {
		t.Errorf("prior sessions = %+v, want the codex-pinned session recorded as a repin", prev)
	}
}

// Without --harness, a pin changed by another command between the snapshot
// and the transaction is refused (the session would be minted by the
// wrong adapter), leaving the row as the other command left it.
func TestCmdDoFreshRefusesPinChangedConcurrently(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "race-fresh")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	stubPS(t, "  PID COMMAND\n")
	stubNewUUID(t, "44444444-2222-3333-4444-555555555555")
	_, _ = stubITerm(t)
	old := doBeforeTxHook
	doBeforeTxHook = func(slug string) {
		db := openFlowDB(t)
		db.Exec(`UPDATE tasks SET harness='codex' WHERE slug=?`, slug)
	}
	t.Cleanup(func() { doBeforeTxHook = old })
	stderr := captureStderr(t)
	if rc := cmdDo([]string{"race-fresh", "--fresh"}); rc == 0 {
		t.Fatal("want refusal")
	}
	if got := stderr(); !strings.Contains(got, "changed") {
		t.Errorf("stderr = %q", got)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "race-fresh")
	if task.Harness.String != "codex" || task.SessionID.String != repinOldSID {
		t.Errorf("row changed: %q/%q", task.Harness.String, task.SessionID.String)
	}
}

// CodeRabbit #200: in background mode the history row and the new binding
// are written in one transaction. If either write fails, flow keeps the
// old binding, stops the agent it just started (it would otherwise run
// unbound), and fails.
func TestCmdDoBackgroundRepinAtomic(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bg-atomic")
	db := openFlowDB(t)
	const oldPrx = "019f8f30-194b-7c83-8564-bff77c5e0147"
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', harness='prx', session_id=?, session_started='2026-11-02T00:00:00Z' WHERE slug='bg-atomic'`, oldPrx); err != nil {
		t.Fatal(err)
	}
	// Make the history insert fail.
	if _, err := db.Exec(`CREATE TRIGGER fail_hist BEFORE INSERT ON task_session_history BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bg-atomic", "--harness", "claude", "--fresh"}); rc == 0 {
		t.Fatal("want failure when the history write fails")
	}
	if c.spawn != 1 || c.stop != 1 {
		t.Errorf("spawns=%d stops=%d, want the started agent stopped", c.spawn, c.stop)
	}
	task, _ := flowdb.GetTask(db, "bg-atomic")
	if task.Harness.String != "prx" || task.SessionID.String != oldPrx {
		t.Errorf("binding changed despite failure: %q/%q", task.Harness.String, task.SessionID.String)
	}
}

// CodeRabbit #200 (2nd review): a plain resume must also refuse a pin that
// changed between flow do's snapshot and its transaction (it would resume
// the other harness's session with the stale adapter).
func TestCmdDoResumeRefusesPinChangedConcurrently(t *testing.T) {
	setupFlowRoot(t)
	seedClaudeBoundTask(t, "race-resume")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	stubPS(t, "  PID COMMAND\n")
	stubClaudeStatOK(t)
	count, _ := stubITerm(t)
	old := doBeforeTxHook
	doBeforeTxHook = func(slug string) {
		db := openFlowDB(t)
		db.Exec(`UPDATE tasks SET harness='codex', session_id='019ff4b1-6162-7263-974f-f5f1866ad0fe' WHERE slug=?`, slug)
	}
	t.Cleanup(func() { doBeforeTxHook = old })
	stderr := captureStderr(t)
	if rc := cmdDo([]string{"race-resume"}); rc == 0 {
		t.Fatal("want refusal")
	}
	if got := stderr(); !strings.Contains(got, "changed") {
		t.Errorf("stderr = %q", got)
	}
	if *count != 0 {
		t.Errorf("opened %d tabs, want none", *count)
	}
}

// CodeRabbit #200 (2nd review): the background binding tx re-reads the row.
// Another command bound the task to a claude session after this one's
// snapshot (prx); `--harness prx --fresh` must re-pin to prx and record the
// session it actually replaces (the claude one), not the stale snapshot's.
func TestCmdDoBackgroundBindRereadsRow(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bg-reread")
	db := openFlowDB(t)
	const oldPrx = "019f8f30-194b-7c83-8564-bff77c5e0147"
	const otherClaude = "55555555-2222-3333-4444-555555555555"
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', harness='prx', session_id=?, session_started='2026-11-02T00:00:00Z' WHERE slug='bg-reread'`, oldPrx); err != nil {
		t.Fatal(err)
	}
	stubBGMode(t)
	stubITerm(t)
	// The snapshot says prx, so this command's re-pin flag was computed
	// as a prx->claude re-pin; the row it actually replaces is claude.
	reg := bgAgentsJSON(bgFullSID)
	stubBGCommand(t, &reg)
	old := doBeforeTxHook
	doBeforeTxHook = func(slug string) {
		d := openFlowDB(t)
		d.Exec(`UPDATE tasks SET harness='claude', session_id=?, session_started='2026-11-03T00:00:00Z' WHERE slug=?`, otherClaude, slug)
	}
	t.Cleanup(func() { doBeforeTxHook = old })

	if rc := cmdDo([]string{"bg-reread", "--harness", "claude", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	task, _ := flowdb.GetTask(db, "bg-reread")
	if task.Harness.String != "claude" || task.SessionID.String != bgFullSID {
		t.Errorf("task = %q/%q", task.Harness.String, task.SessionID.String)
	}
	prev, _ := flowdb.PriorSessions(db, "bg-reread")
	if len(prev) != 1 || prev[0].SessionID != otherClaude || prev[0].Harness != "claude" || prev[0].Reason != "fresh" {
		t.Errorf("prior sessions = %+v, want the session the tx replaced (claude, fresh)", prev)
	}
}

// Without --harness, a background spawn whose pin changed concurrently is
// refused at binding time: the old binding stays and the agent is stopped.
func TestCmdDoBackgroundRefusesPinChangedConcurrently(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bg-race")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', harness='claude', session_id=? WHERE slug='bg-race'`, repinOldSID); err != nil {
		t.Fatal(err)
	}
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	old := doBeforeTxHook
	doBeforeTxHook = func(slug string) {
		d := openFlowDB(t)
		d.Exec(`UPDATE tasks SET harness='prx' WHERE slug=?`, slug)
	}
	t.Cleanup(func() { doBeforeTxHook = old })
	if rc := cmdDo([]string{"bg-race", "--fresh"}); rc == 0 {
		t.Fatal("want refusal")
	}
	if c.stop != 1 {
		t.Errorf("stops = %d, want the new agent stopped", c.stop)
	}
	task, _ := flowdb.GetTask(db, "bg-race")
	if task.Harness.String != "prx" || task.SessionID.String != repinOldSID {
		t.Errorf("row = %q/%q", task.Harness.String, task.SessionID.String)
	}
}
