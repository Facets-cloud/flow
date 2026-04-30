package flowdb

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

func insertProject(t *testing.T, db *sql.DB, slug, name, wd, priority string) {
	t.Helper()
	now := NowISO()
	_, err := db.Exec(`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, 'active', ?, ?, ?, ?)`,
		slug, name, priority, wd, now, now)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
}

func insertTask(t *testing.T, db *sql.DB, slug, name, status, priority, wd string, project any) {
	t.Helper()
	now := NowISO()
	_, err := db.Exec(`INSERT INTO tasks (slug, name, project_slug, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, name, project, status, priority, wd, now, now)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
}

func TestOpenDBCreatesSchema(t *testing.T) {
	db := openTempDB(t)
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
	if _, err := GetProject(db, "nope"); err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows, got %v", err)
	}
}

func TestListProjectsFilters(t *testing.T) {
	db := openTempDB(t)
	insertProject(t, db, "alpha", "Alpha", "/tmp/alpha", "high")
	insertProject(t, db, "beta", "Beta", "/tmp/beta", "medium")
	if _, err := db.Exec(`UPDATE projects SET status='done' WHERE slug='beta'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := db.Exec(`UPDATE projects SET archived_at=? WHERE slug='alpha'`, NowISO()); err != nil {
		t.Fatalf("archive: %v", err)
	}
	got, err := ListProjects(db, ProjectFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "beta" {
		t.Errorf("default filter: got %v", got)
	}
	got, err = ListProjects(db, ProjectFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("include archived: got %d", len(got))
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
	insertTask(t, db, "float", "Floating", "in-progress", "high", "/tmp/float", nil)
	floating, err := GetTask(db, "float")
	if err != nil {
		t.Fatalf("GetTask floating: %v", err)
	}
	if floating.ProjectSlug.Valid {
		t.Errorf("expected null project_slug")
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
}

func TestNowISO(t *testing.T) {
	s := NowISO()
	if len(s) < 19 {
		t.Errorf("NowISO too short: %q", s)
	}
}

func TestMigrationAddsDueDateAndStatusChangedAt(t *testing.T) {
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

func TestPlaybooksTableExists(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "flow.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name='playbooks'`)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("playbooks table missing")
	}
	rows.Close()

	now := NowISO()
	wd := t.TempDir()
	if _, err := db.Exec(
		`INSERT INTO playbooks (slug, name, work_dir, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"p1", "Playbook 1", wd, now, now,
	); err != nil {
		t.Fatalf("insert playbook: %v", err)
	}
	var slug, name, gotWD string
	err = db.QueryRow(`SELECT slug, name, work_dir FROM playbooks WHERE slug='p1'`).Scan(&slug, &name, &gotWD)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Playbook 1" || gotWD != wd {
		t.Errorf("unexpected: slug=%q name=%q wd=%q", slug, name, gotWD)
	}
}

func TestMigrationAddsTasksKindAndPlaybookSlug(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "flow.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	hasKind, err := columnExists(db, "tasks", "kind")
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind {
		t.Error("tasks.kind column missing")
	}
	hasPB, err := columnExists(db, "tasks", "playbook_slug")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPB {
		t.Error("tasks.playbook_slug column missing")
	}

	// Default kind should be 'regular' for new rows.
	now := NowISO()
	wd := t.TempDir()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, 'backlog', 'medium', ?, ?, ?)`,
		"t1", "Task 1", wd, now, now,
	); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := db.QueryRow(`SELECT kind FROM tasks WHERE slug='t1'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "regular" {
		t.Errorf("default kind: got %q, want regular", kind)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "flow.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = OpenDB(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	db.Close()
}
