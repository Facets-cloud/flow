package app

import (
	"database/sql"
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

// TestHarnessForTask covers the column → adapter lookup, including
// the back-compat fallback for NULL and unknown values.
func TestHarnessForTask(t *testing.T) {
	cases := []struct {
		name   string
		harness sql.NullString
		want   harness.Name
	}{
		{"null column → claude", sql.NullString{}, harness.NameClaude},
		{"empty string → claude", sql.NullString{Valid: true, String: ""}, harness.NameClaude},
		{"claude pin", sql.NullString{Valid: true, String: "claude"}, harness.NameClaude},
		{"unknown name → claude fallback", sql.NullString{Valid: true, String: "future"}, harness.NameClaude},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &flowdb.Task{Harness: tc.harness}
			if got := harnessForTask(task).Name(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCmdDoPersistsHarnessOnBootstrap pins the contract: the first
// `flow do` on a previously-unbound task writes the chosen harness
// to the tasks.harness column atomically with session_id. Used by
// future `flow do` invocations to look up the right adapter.
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
}

// TestCmdDoHerePersistsHarnessColumn pins that --here writes the
// harness column on bind, not just session_id.
func TestCmdDoHerePersistsHarnessColumn(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "here-harness")

	const sid = "11111111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

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

// TestHarnessForSpawnPrefersAmbientForUnpinned pins the convenience
// behavior: a brand-new task (no harness pinned) created from inside
// a known harness session adopts that harness on first `flow do`,
// not the static claude default.
//
// This test exercises the helper directly rather than running cmdDo
// end-to-end, because exercising "task born in codex" requires a
// codex env var probe that ambientHarness can match against a
// registered adapter. With only claude registered, we verify the
// general behavior: claude env set + null task harness ⇒ claude.
func TestHarnessForSpawnPrefersAmbientForUnpinned(t *testing.T) {
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	// Sanity: ambient is nil → fallback to claude.
	task := &flowdb.Task{}
	if got := harnessForSpawn(task).Name(); got != harness.NameClaude {
		t.Errorf("no ambient + null pin = %v, want claude", got)
	}

	// Ambient claude set → returns claude.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	if got := harnessForSpawn(task).Name(); got != harness.NameClaude {
		t.Errorf("ambient claude + null pin = %v, want claude", got)
	}

	// Pinned takes precedence over ambient (would matter once codex
	// is registered; here we exercise the precedence rule by setting
	// the column to claude explicitly).
	task.Harness = sql.NullString{Valid: true, String: "claude"}
	if got := harnessForSpawn(task).Name(); got != harness.NameClaude {
		t.Errorf("pinned claude = %v, want claude", got)
	}

	// Ensure claude adapter type is what we got (sanity).
	if _, ok := harnessForSpawn(task).(harness.Harness); !ok {
		t.Error("harnessForSpawn return doesn't satisfy harness.Harness")
	}
	_ = claude.New() // import-keep
}
