package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenDBCreatesSchema(t *testing.T) {
	db := openTempDB(t)

	// Query sqlite_master to confirm tables were created.
	for _, tbl := range []string{"projects", "tasks", "workdirs"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", tbl, err)
		}
	}
}

func TestOpenDBIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db1, err := OpenDB(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()
	db2, err := OpenDB(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	db2.Close()
}

func insertProject(t *testing.T, db *sql.DB, slug, name, wd, priority string) {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, 'active', ?, ?, ?, ?)`,
		slug, name, priority, wd, now, now)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
}

func insertTask(t *testing.T, db *sql.DB, slug, name, status, priority, wd string, project any) {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(`INSERT INTO tasks (slug, name, project_slug, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, name, project, status, priority, wd, now, now)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
}

func TestProjectCRUD(t *testing.T) {
	db := openTempDB(t)
	insertProject(t, db, "alpha", "Alpha Project", "/tmp/alpha", "high")

	got, err := GetProject(db, "alpha")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Slug != "alpha" || got.Name != "Alpha Project" || got.Priority != "high" || got.WorkDir != "/tmp/alpha" {
		t.Errorf("unexpected project: %+v", got)
	}
	if got.Status != "active" {
		t.Errorf("expected default status active, got %q", got.Status)
	}

	// Missing project → sql.ErrNoRows.
	if _, err := GetProject(db, "nope"); err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows, got %v", err)
	}
}

func TestListProjectsFilters(t *testing.T) {
	db := openTempDB(t)
	insertProject(t, db, "alpha", "Alpha", "/tmp/alpha", "high")
	insertProject(t, db, "beta", "Beta", "/tmp/beta", "medium")
	// Mark beta done.
	if _, err := db.Exec(`UPDATE projects SET status='done' WHERE slug='beta'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Archive alpha.
	if _, err := db.Exec(`UPDATE projects SET archived_at=? WHERE slug='alpha'`, nowISO()); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Default: no archived → only beta.
	got, err := ListProjects(db, ProjectFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "beta" {
		t.Errorf("default filter: got %v", got)
	}

	// Include archived → both.
	got, err = ListProjects(db, ProjectFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("include archived: got %d", len(got))
	}

	// Status filter.
	got, err = ListProjects(db, ProjectFilter{Status: "done", IncludeArchived: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "beta" {
		t.Errorf("status filter: got %v", got)
	}
}

func TestTaskCRUD(t *testing.T) {
	db := openTempDB(t)
	insertProject(t, db, "proj", "Proj", "/tmp/proj", "medium")
	insertTask(t, db, "work", "Some Work", "backlog", "medium", "/tmp/proj", "proj")

	got, err := GetTask(db, "work")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Slug != "work" || !got.ProjectSlug.Valid || got.ProjectSlug.String != "proj" {
		t.Errorf("unexpected task: %+v", got)
	}
	if got.SessionID.Valid {
		t.Errorf("expected null session_id, got %+v", got.SessionID)
	}

	// Floating task (nil project).
	insertTask(t, db, "float", "Floating", "in-progress", "high", "/tmp/float", nil)
	floating, err := GetTask(db, "float")
	if err != nil {
		t.Fatalf("GetTask floating: %v", err)
	}
	if floating.ProjectSlug.Valid {
		t.Errorf("expected null project_slug for floating, got %+v", floating.ProjectSlug)
	}
}

func TestListTasksFilters(t *testing.T) {
	db := openTempDB(t)
	insertProject(t, db, "proj", "Proj", "/tmp/proj", "medium")
	insertTask(t, db, "a-low", "A", "backlog", "low", "/tmp/proj", "proj")
	insertTask(t, db, "b-high", "B", "in-progress", "high", "/tmp/proj", "proj")
	insertTask(t, db, "c-med", "C", "backlog", "medium", "/tmp/proj", "proj")
	insertTask(t, db, "d-float", "D", "backlog", "high", "/tmp/float", nil)

	// No filter → all 4, sorted by priority (high, medium, low), then slug.
	all, err := ListTasks(db, TaskFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d tasks, want 4", len(all))
	}
	// Priority order: b-high, d-float (both high), c-med, a-low.
	wantOrder := []string{"b-high", "d-float", "c-med", "a-low"}
	for i, w := range wantOrder {
		if all[i].Slug != w {
			t.Errorf("pos %d: got %s, want %s", i, all[i].Slug, w)
		}
	}

	// Project filter.
	projOnly, err := ListTasks(db, TaskFilter{Project: "proj"})
	if err != nil {
		t.Fatalf("list proj: %v", err)
	}
	if len(projOnly) != 3 {
		t.Errorf("proj-only: got %d, want 3", len(projOnly))
	}

	// Status filter.
	inProg, err := ListTasks(db, TaskFilter{Status: "in-progress"})
	if err != nil {
		t.Fatalf("list in-progress: %v", err)
	}
	if len(inProg) != 1 || inProg[0].Slug != "b-high" {
		t.Errorf("in-progress: got %v", inProg)
	}

	// Priority filter.
	highs, err := ListTasks(db, TaskFilter{Priority: "high"})
	if err != nil {
		t.Fatalf("list high: %v", err)
	}
	if len(highs) != 2 {
		t.Errorf("high: got %d, want 2", len(highs))
	}

	// Include archived off by default.
	if _, err := db.Exec(`UPDATE tasks SET archived_at=? WHERE slug='a-low'`, nowISO()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	nonArch, err := ListTasks(db, TaskFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nonArch) != 3 {
		t.Errorf("non-archived: got %d, want 3", len(nonArch))
	}
	withArch, err := ListTasks(db, TaskFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if len(withArch) != 4 {
		t.Errorf("with-archived: got %d, want 4", len(withArch))
	}
}

func TestCheckConstraintsProjects(t *testing.T) {
	db := openTempDB(t)
	now := nowISO()
	// Invalid status for projects.
	_, err := db.Exec(`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at) VALUES ('x', 'X', 'weird', 'medium', '/tmp/x', ?, ?)`, now, now)
	if err == nil {
		t.Error("expected CHECK violation for invalid project status")
	}
	// Invalid priority.
	_, err = db.Exec(`INSERT INTO projects (slug, name, priority, work_dir, created_at, updated_at) VALUES ('y', 'Y', 'urgent', '/tmp/y', ?, ?)`, now, now)
	if err == nil {
		t.Error("expected CHECK violation for invalid project priority")
	}
}

func TestCheckConstraintsTasks(t *testing.T) {
	db := openTempDB(t)
	now := nowISO()
	_, err := db.Exec(`INSERT INTO tasks (slug, name, status, priority, work_dir, created_at, updated_at) VALUES ('t', 'T', 'blocked', 'medium', '/tmp', ?, ?)`, now, now)
	if err == nil {
		t.Error("expected CHECK violation for 'blocked' status (removed in v2)")
	}
}

func TestWorkdirUpsert(t *testing.T) {
	db := openTempDB(t)

	if err := UpsertWorkdir(db, "/tmp/repo", "repo", "", "git@github.com:foo/bar.git"); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	got, err := GetWorkdir(db, "/tmp/repo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Name.Valid || got.Name.String != "repo" {
		t.Errorf("name: got %+v", got.Name)
	}
	if !got.GitRemote.Valid || got.GitRemote.String != "git@github.com:foo/bar.git" {
		t.Errorf("git_remote: got %+v", got.GitRemote)
	}
	firstUsed := got.LastUsedAt.String
	firstCreated := got.CreatedAt

	// Second upsert bumps last_used_at but preserves created_at.
	// Using a nonzero time delay would be fragile; use a manual override.
	if _, err := db.Exec(`UPDATE workdirs SET last_used_at='2000-01-01T00:00:00Z' WHERE path=?`, "/tmp/repo"); err != nil {
		t.Fatalf("manual reset: %v", err)
	}
	if err := UpsertWorkdir(db, "/tmp/repo", "", "", ""); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got2, err := GetWorkdir(db, "/tmp/repo")
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if got2.CreatedAt != firstCreated {
		t.Errorf("created_at changed: %s → %s", firstCreated, got2.CreatedAt)
	}
	if got2.LastUsedAt.String == "2000-01-01T00:00:00Z" {
		t.Errorf("last_used_at was not bumped: %s", got2.LastUsedAt.String)
	}
	// Name and git_remote should still be set (coalesce preserved them).
	if !got2.Name.Valid || got2.Name.String != "repo" {
		t.Errorf("name dropped on second upsert: %+v", got2.Name)
	}
	if !got2.GitRemote.Valid {
		t.Errorf("git_remote dropped on second upsert: %+v", got2.GitRemote)
	}
	_ = firstUsed
}

func TestListWorkdirs(t *testing.T) {
	db := openTempDB(t)
	if err := UpsertWorkdir(db, "/tmp/a", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := UpsertWorkdir(db, "/tmp/b", "", "", ""); err != nil {
		t.Fatal(err)
	}
	// Force a to be older so b comes first in DESC order.
	if _, err := db.Exec(`UPDATE workdirs SET last_used_at='2000-01-01T00:00:00Z' WHERE path='/tmp/a'`); err != nil {
		t.Fatal(err)
	}
	list, err := ListWorkdirs(db)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d workdirs, want 2", len(list))
	}
	if list[0].Path != "/tmp/b" || list[1].Path != "/tmp/a" {
		t.Errorf("order: got %v %v", list[0].Path, list[1].Path)
	}
}

func TestNowISO(t *testing.T) {
	s := nowISO()
	if len(s) < 19 {
		t.Errorf("nowISO too short: %q", s)
	}
}

func TestMigrationAddsDueDateAndStatusChangedAt(t *testing.T) {
	// Simulate a pre-temporal DB by creating a DB, then checking columns.
	db := openTempDB(t)
	for _, col := range []string{"due_date", "status_changed_at"} {
		has, err := columnExists(db, "tasks", col)
		if err != nil {
			t.Fatalf("columnExists(%s): %v", col, err)
		}
		if !has {
			t.Errorf("column %s should exist after migration", col)
		}
	}
}

func TestTaskDueDateAndStatusChangedAt(t *testing.T) {
	db := openTempDB(t)
	now := nowISO()
	_, err := db.Exec(`INSERT INTO tasks (slug, name, status, priority, work_dir, due_date, status_changed_at, created_at, updated_at)
		VALUES ('temporal', 'Temporal', 'backlog', 'medium', '/tmp', '2026-06-01', ?, ?, ?)`, now, now, now)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	task, err := GetTask(db, "temporal")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !task.DueDate.Valid || task.DueDate.String != "2026-06-01" {
		t.Errorf("due_date: got %+v", task.DueDate)
	}
	if !task.StatusChangedAt.Valid || task.StatusChangedAt.String != now {
		t.Errorf("status_changed_at: got %+v", task.StatusChangedAt)
	}
}

func TestTaskNullDueDate(t *testing.T) {
	db := openTempDB(t)
	insertTask(t, db, "nodue", "No Due", "backlog", "medium", "/tmp", nil)
	task, err := GetTask(db, "nodue")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.DueDate.Valid {
		t.Errorf("expected NULL due_date, got %+v", task.DueDate)
	}
}
