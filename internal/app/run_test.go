package app

import (
	"database/sql"
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
