package app

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

// TestAmbientHarness covers the env-var probe: returns the matching
// harness when its session-id env var is set, nil otherwise.
func TestAmbientHarness(t *testing.T) {
	// Unset every known harness env var so we control the starting state.
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	if got := ambientHarness(); got != nil {
		t.Errorf("ambientHarness with no env set = %v, want nil", got)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	got := ambientHarness()
	if got == nil {
		t.Fatal("ambientHarness with $CLAUDE_CODE_SESSION_ID set = nil, want claude")
	}
	if got.Name() != harness.NameClaude {
		t.Errorf("ambientHarness = %v, want claude", got.Name())
	}
}

// TestHarnessForTask covers the column → adapter lookup. NULL and
// empty resolve to claude+nil error (back-compat). Unknown
// non-empty names resolve to nil+error so callers refuse rather
// than silently coerce.
func TestHarnessForTask(t *testing.T) {
	cases := []struct {
		name    string
		harness sql.NullString
		want    harness.Name
		wantErr bool
	}{
		{"null column → claude", sql.NullString{}, harness.NameClaude, false},
		{"empty string → claude", sql.NullString{Valid: true, String: ""}, harness.NameClaude, false},
		{"claude pin", sql.NullString{Valid: true, String: "claude"}, harness.NameClaude, false},
		{"unknown name → error", sql.NullString{Valid: true, String: "future"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &flowdb.Task{Harness: tc.harness}
			h, err := harnessForTask(task)
			if tc.wantErr {
				if err == nil {
					t.Errorf("got nil error, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := h.Name(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCmdDoPersistsHarnessOnBootstrap pins the contract: the first
// `flow do` on a previously-unbound task writes the chosen harness
// AND the session_cwd to the tasks row atomically with session_id.
// Future `flow do` invocations read both back to look up the right
// adapter and find the transcript on disk.
func TestCmdDoPersistsHarnessOnBootstrap(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "harness-bootstrap")
	_, _ = stubITerm(t)

	// Clean env so ambient detection falls back to claude default.
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	if rc := cmdDo([]string{"harness-bootstrap"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "harness-bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if !task.Harness.Valid || task.Harness.String != "claude" {
		t.Errorf("task.harness after bootstrap = %+v, want claude", task.Harness)
	}
	// session_cwd should equal work_dir on fresh bootstrap.
	if !task.SessionCwd.Valid || task.SessionCwd.String != task.WorkDir {
		t.Errorf("task.session_cwd after bootstrap = %+v, want work_dir=%q",
			task.SessionCwd, task.WorkDir)
	}
}

// TestCmdDoRefusesUnsupportedHarnessPin pins the two
// related contracts:
//
//  1. A task pinned to a harness this binary doesn't register
//     must NOT spawn as the silent claude fallback. cmdDo returns
//     an error mentioning the unsupported name and the registered
//     alternatives — better that the user sees the issue
//     explicitly than that we silently run the wrong adapter.
//
//  2. The pin survives the refusal. cmdDo doesn't UPDATE the
//     harness column when it can't resolve the pin, so a future
//     binary that DOES register the pinned harness can still pick
//     up the task untouched.
//
// Together: today's binary refuses, tomorrow's binary works,
// downgrade is safe.
func TestCmdDoRefusesUnsupportedHarnessPin(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "future-pin")
	_, _ = stubITerm(t)

	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	// Simulate a future build having pinned the task.
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug='future-pin'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	stderr := captureStderr(t)
	rc := cmdDo([]string{"future-pin"})
	if rc == 0 {
		t.Fatalf("cmdDo rc=%d, want non-zero (unsupported pin should refuse)", rc)
	}
	got := stderr()
	if !strings.Contains(got, "codex") || !strings.Contains(got, "isn't supported") {
		t.Errorf("stderr should name the unsupported harness; got:\n%s", got)
	}

	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "future-pin")
	if err != nil {
		t.Fatal(err)
	}
	if task.Harness.String != "codex" {
		t.Errorf("refusal should preserve the pin; got %q, want codex",
			task.Harness.String)
	}
	if task.SessionID.Valid {
		t.Errorf("refusal should not allocate a session_id; got %+v", task.SessionID)
	}
}

// TestCmdDoHerePersistsHarnessColumn pins that --here writes the
// harness column AND session_cwd on bind, not just session_id.
// session_cwd captures the cwd of THIS flow process — equal to the
// cwd claude was started in, which is what determines the on-disk
// transcript path. Without recording it, --here-bound tasks whose
// claude session was started in a different cwd than work_dir
// (common with --force binds) couldn't have their transcript found.
func TestCmdDoHerePersistsHarnessColumn(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "here-harness")

	const sid = "11111111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	wantCwd, _ := os.Getwd()

	if rc := cmdDoHere("here-harness", false); rc != 0 {
		t.Fatalf("cmdDoHere rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "here-harness")
	if err != nil {
		t.Fatal(err)
	}
	if !task.Harness.Valid || task.Harness.String != "claude" {
		t.Errorf("task.harness after --here = %+v, want claude", task.Harness)
	}
	if task.SessionID.String != sid {
		t.Errorf("task.session_id after --here = %q, want %s", task.SessionID.String, sid)
	}
	if !task.SessionCwd.Valid || task.SessionCwd.String != wantCwd {
		t.Errorf("task.session_cwd after --here = %+v, want %q (the test process's cwd)",
			task.SessionCwd, wantCwd)
	}
}

// TestCmdDoHereRejectsCrossHarness pins the safety rail: a task
// pinned to harness X can't be --here-bound from a session of
// harness Y without --force. The check is purely string-based on
// the harness column, so we can exercise it without a second
// adapter implementation — just set the column directly.
func TestCmdDoHereRejectsCrossHarness(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "pinned-elsewhere")

	// Pin the task to a fictional "codex" harness by writing
	// directly to the column.
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug='pinned-elsewhere'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Ambient is claude.
	const sid = "11111111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	stderr := captureStderr(t)
	rc := cmdDoHere("pinned-elsewhere", false)
	if rc != 1 {
		t.Errorf("cmdDoHere across harnesses rc=%d, want 1", rc)
	}
	got := stderr()
	if !strings.Contains(got, "pinned to harness") {
		t.Errorf("stderr should explain harness mismatch; got:\n%s", got)
	}

	// Task should be unchanged.
	db = openFlowDB(t)
	task, _ := flowdb.GetTask(db, "pinned-elsewhere")
	if task.SessionID.Valid {
		t.Errorf("rejected --here should not touch session_id; got %v", task.SessionID)
	}
	if task.Harness.String != "codex" {
		t.Errorf("rejected --here should not touch harness column; got %q", task.Harness.String)
	}
}

// TestCmdDoHereForceSwitchesHarness pins the --force escape: when
// the user explicitly asks, we switch the task's harness pinning
// alongside the session_id rebind. The old harness's transcript
// stays on disk but flow no longer tracks it.
func TestCmdDoHereForceSwitchesHarness(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "force-switch")

	// Pre-pin to "codex" with an existing session_id.
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET harness='codex', session_id='old-codex-sid', session_started=? WHERE slug='force-switch'`,
		flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Ambient: claude.
	const sid = "22222222-3333-4444-8555-666666666666"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	if rc := cmdDoHere("force-switch", true); rc != 0 {
		t.Fatalf("cmdDoHere --force rc=%d, want 0", rc)
	}

	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "force-switch")
	if err != nil {
		t.Fatal(err)
	}
	if task.Harness.String != "claude" {
		t.Errorf("task.harness after --force switch = %q, want claude", task.Harness.String)
	}
	if task.SessionID.String != sid {
		t.Errorf("task.session_id after --force switch = %q, want %s", task.SessionID.String, sid)
	}
}

// TestHarnessForSpawn_AllPathsLandOnClaudeToday smoke-tests every
// branch of harnessForSpawn under the single-harness registry
// (claude is the only adapter today). Each of the three branches
// — null pin + no ambient, null pin + claude ambient, claude pin —
// lands on claude, which is the only assertion the test can make
// without a second registered adapter.
//
// The actual *precedence* properties (pinned > ambient > default)
// can't be exercised here: when ambient is claude and pin is empty,
// "matches ambient" and "fell through to default" are
// indistinguishable. Add real coverage once codex/gemini registers
// and a non-claude ambient is possible.
func TestHarnessForSpawn_AllPathsLandOnClaudeToday(t *testing.T) {
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	mustResolve := func(t *testing.T, task *flowdb.Task) harness.Harness {
		t.Helper()
		h, err := harnessForSpawn(task)
		if err != nil {
			t.Fatalf("harnessForSpawn: unexpected error: %v", err)
		}
		return h
	}

	// Branch 1: ambient nil + null pin → claude (fallback).
	task := &flowdb.Task{}
	if got := mustResolve(t, task).Name(); got != harness.NameClaude {
		t.Errorf("no ambient + null pin = %v, want claude", got)
	}

	// Branch 2: claude ambient + null pin → claude. Doesn't prove
	// ambient is *preferred over* fallback (same answer either way);
	// only proves the branch doesn't error.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	if got := mustResolve(t, task).Name(); got != harness.NameClaude {
		t.Errorf("ambient claude + null pin = %v, want claude", got)
	}

	// Branch 3: claude pin → claude. Doesn't prove pinned is
	// *preferred over* ambient (same as above).
	task.Harness = sql.NullString{Valid: true, String: "claude"}
	if got := mustResolve(t, task).Name(); got != harness.NameClaude {
		t.Errorf("pinned claude = %v, want claude", got)
	}

	// Sanity: returned value satisfies harness.Harness.
	if _, ok := mustResolve(t, task).(harness.Harness); !ok {
		t.Error("harnessForSpawn return doesn't satisfy harness.Harness")
	}
	_ = claude.New() // import-keep
}
