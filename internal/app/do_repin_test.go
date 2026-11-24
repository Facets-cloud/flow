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
		prev[0].Reason != "repin" || prev[0].SessionStarted != "2026-11-01T00:00:00Z" {
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
