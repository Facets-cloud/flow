package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
)

func TestRunSlugBasic(t *testing.T) {
	db := openTempDB(t)
	now := time.Date(2026, 4, 30, 10, 30, 45, 0, time.UTC)
	got, err := generateRunSlug(db, "triage-cs", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "triage-cs--2026-04-30-10-30"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunSlugMinuteCollision(t *testing.T) {
	db := openTempDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p", Name: "P", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 30, 10, 30, 45, 0, time.UTC)

	first, _ := generateRunSlug(db, "p", now)
	insertRunTaskForSlug(t, db, first, "p", wd)

	second, err := generateRunSlug(db, "p", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "p--2026-04-30-10-30-45"
	if second != want {
		t.Errorf("got %q, want %q", second, want)
	}
}

func TestRunSlugSecondCollision(t *testing.T) {
	db := openTempDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p", Name: "P", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 30, 10, 30, 45, 0, time.UTC)
	insertRunTaskForSlug(t, db, "p--2026-04-30-10-30", "p", wd)
	insertRunTaskForSlug(t, db, "p--2026-04-30-10-30-45", "p", wd)
	got, err := generateRunSlug(db, "p", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "p--2026-04-30-10-30-45-2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunSlugUTCNormalization(t *testing.T) {
	db := openTempDB(t)
	loc, _ := time.LoadLocation("Asia/Kolkata") // UTC+5:30
	local := time.Date(2026, 4, 30, 16, 0, 45, 0, loc) // 10:30 UTC
	got, err := generateRunSlug(db, "p", local)
	if err != nil {
		t.Fatal(err)
	}
	want := "p--2026-04-30-10-30"
	if got != want {
		t.Errorf("got %q, want %q (UTC normalization)", got, want)
	}
}

func insertRunTaskForSlug(t *testing.T, db *sql.DB, slug, pbSlug, wd string) {
	t.Helper()
	now := flowdb.NowISO()
	_, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, created_at, updated_at)
		 VALUES (?, ?, 'backlog', 'playbook_run', ?, 'medium', ?, ?, ?)`,
		slug, slug, pbSlug, wd, now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCmdRunPlaybookCreatesRunTask(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}

	_, lastScript := stubITerm(t)

	if rc := cmdRun([]string{"playbook", "tri"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	rows, err := db.Query(`SELECT slug FROM tasks WHERE kind='playbook_run' AND playbook_slug='tri'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var runSlug string
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&runSlug); err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 run task, got %d", count)
	}
	if !strings.HasPrefix(runSlug, "tri--") {
		t.Errorf("expected slug prefix 'tri--', got %q", runSlug)
	}

	// brief.md should be a copy of playbook brief.md.
	root, _ := flowRoot()
	pbBrief, _ := os.ReadFile(filepath.Join(root, "playbooks", "tri", "brief.md"))
	runBrief, err := os.ReadFile(filepath.Join(root, "tasks", runSlug, "brief.md"))
	if err != nil {
		t.Errorf("run brief.md missing: %v", err)
	}
	if string(pbBrief) != string(runBrief) {
		t.Errorf("run brief should be verbatim copy of playbook brief")
	}

	// iTerm should have been called with a 'claude' command.
	script := lastScript()
	if !strings.Contains(script, "claude --session-id ") {
		t.Errorf("expected claude session-id in spawn script, got: %q", script)
	}
}

func TestCmdRunPlaybookSnapshotIsolation(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	pbBriefPath := filepath.Join(root, "playbooks", "p", "brief.md")
	if err := os.WriteFile(pbBriefPath, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubITerm(t)

	if rc := cmdRun([]string{"playbook", "p"}); rc != 0 {
		t.Fatal()
	}

	db := openFlowDB(t)
	var runSlug string
	if err := db.QueryRow(`SELECT slug FROM tasks WHERE kind='playbook_run' AND playbook_slug='p'`).Scan(&runSlug); err != nil {
		t.Fatal(err)
	}

	// Mutate the playbook brief.
	if err := os.WriteFile(pbBriefPath, []byte("MUTATED"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run brief should still be ORIGINAL.
	runBrief, _ := os.ReadFile(filepath.Join(root, "tasks", runSlug, "brief.md"))
	if string(runBrief) != "ORIGINAL" {
		t.Errorf("snapshot leaked: got %q, want ORIGINAL", string(runBrief))
	}
}

func TestCmdRunPlaybookMissing(t *testing.T) {
	setupFlowRoot(t)
	stubITerm(t)
	if rc := cmdRun([]string{"playbook", "no-such"}); rc == 0 {
		t.Errorf("expected non-zero rc for missing playbook")
	}
}

// ---------- flow run playbook --here ----------

// TestCmdRunPlaybookHereHappyPath pins the in-session bind contract
// for playbook runs: with $CLAUDE_CODE_SESSION_ID set and a valid
// playbook, --here creates the run-task row, snapshots the brief,
// binds the current session, and flips status to in-progress —
// without spawning a tab.
func TestCmdRunPlaybookHereHappyPath(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	const sid = "f00ba111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	count, _ := stubITerm(t)

	if rc := cmdRun([]string{"playbook", "tri", "--here"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 0 {
		t.Errorf("--here should not spawn; got %d spawns", *count)
	}

	db := openFlowDB(t)
	var runSlug, status, sessionID string
	err := db.QueryRow(
		`SELECT slug, status, COALESCE(session_id,'') FROM tasks WHERE kind='playbook_run' AND playbook_slug='tri'`,
	).Scan(&runSlug, &status, &sessionID)
	if err != nil {
		t.Fatalf("query run task: %v", err)
	}
	if sessionID != sid {
		t.Errorf("session_id = %q, want %s", sessionID, sid)
	}
	if status != "in-progress" {
		t.Errorf("status = %q, want in-progress", status)
	}

	// Brief snapshot is still produced on the --here path — bootstrap
	// contract reads tasks/<run-slug>/brief.md, not the live playbook.
	root, _ := flowRoot()
	if _, err := os.ReadFile(filepath.Join(root, "tasks", runSlug, "brief.md")); err != nil {
		t.Errorf("run brief.md missing on --here path: %v", err)
	}
}

// TestCmdRunPlaybookHereNoEnvVar pins that --here without a Claude
// session in env refuses with rc=1 AND does not create a dangling
// run-task row. Early validation before INSERT is the contract.
func TestCmdRunPlaybookHereNoEnvVar(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	count, _ := stubITerm(t)

	if rc := cmdRun([]string{"playbook", "p", "--here"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when CLAUDE_CODE_SESSION_ID unset", rc)
	}
	if *count != 0 {
		t.Errorf("no spawn expected; got %d", *count)
	}

	db := openFlowDB(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE kind='playbook_run'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("playbook_run rows after refused --here: got %d, want 0 (no dangling row)", n)
	}
}

// TestCmdRunPlaybookHereInvalidUUID pins UUID-shape validation. An
// env var that isn't a v4 UUID is rejected; no dangling row created.
func TestCmdRunPlaybookHereInvalidUUID(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "not-a-uuid")
	stubITerm(t)

	if rc := cmdRun([]string{"playbook", "p", "--here"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when env is not a valid UUID", rc)
	}
	db := openFlowDB(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE kind='playbook_run'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no run row should be created on UUID validation failure; got %d", n)
	}
}

// TestCmdRunPlaybookHereRejectsCurrentSessionAlreadyBoundElsewhere
// pins the session_id uniqueness invariant: if the current session
// is already bound to another task, --here refuses (and does NOT
// create a dangling run-task). Mirrors cmdDoHere semantics.
func TestCmdRunPlaybookHereRejectsCurrentSessionAlreadyBoundElsewhere(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	if rc := cmdAdd([]string{"task", "owner-task"}); rc != 0 {
		t.Fatal()
	}
	const sid = "f00ba111-2222-4333-8444-555555555555"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, status='in-progress' WHERE slug='owner-task'`,
		sid, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	stubITerm(t)

	if rc := cmdRun([]string{"playbook", "p", "--here"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when current session bound elsewhere", rc)
	}

	db = openFlowDB(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE kind='playbook_run'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no run row should be created when session bound elsewhere; got %d", n)
	}
	var ownerSID string
	if err := db.QueryRow(`SELECT session_id FROM tasks WHERE slug='owner-task'`).Scan(&ownerSID); err != nil {
		t.Fatal(err)
	}
	if ownerSID != sid {
		t.Errorf("owner-task session_id changed: got %q, want %s", ownerSID, sid)
	}
}

// TestCmdRunPlaybookHereIgnoresDangerSkip pins that
// --dangerously-skip-permissions is silently dropped on the --here
// path (no claude process is spawned, so the flag is meaningless).
// The bind should still succeed.
func TestCmdRunPlaybookHereIgnoresDangerSkip(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	const sid = "f00ba111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	count, _ := stubITerm(t)

	if rc := cmdRun([]string{"playbook", "p", "--here", "--dangerously-skip-permissions"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 0 {
		t.Errorf("--here should not spawn even with --dangerously-skip-permissions; got %d", *count)
	}
}

func TestCmdRunPlaybookForwardsWith(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri-with", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	_, lastScript := stubITerm(t)
	if rc := cmdRun([]string{"playbook", "tri-with", "--with", "also check the Acme deal"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	script := lastScript()
	if !strings.Contains(script, "[via flow do --with]") {
		t.Errorf("playbook run with --with should carry the marker: %s", script)
	}
	if !strings.Contains(script, "also check the Acme deal") {
		t.Errorf("playbook run with --with should inject the body: %s", script)
	}
	if !strings.Contains(script, "playbook `tri-with`") {
		t.Errorf("playbook bootstrap must remain intact: %s", script)
	}
}

func TestCmdRunPlaybookWithEmptyRejectedBeforeRowInsert(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri-empty", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	stubITerm(t)
	if rc := cmdRun([]string{"playbook", "tri-empty", "--with", "   "}); rc != 2 {
		t.Errorf("rc=%d, want 2 for empty --with", rc)
	}
	db := openFlowDB(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE kind='playbook_run' AND playbook_slug='tri-empty'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rejected --with should not insert a run row; got %d rows", n)
	}
}
