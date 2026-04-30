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
