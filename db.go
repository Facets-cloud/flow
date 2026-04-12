package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// schemaDDL is the full DDL for flow.db. Each statement is idempotent
// (CREATE ... IF NOT EXISTS) so OpenDB can run this on every startup.
//
// Note on NULL-safe equality: SQLite's `IS` operator treats NULLs as
// equal (NULL IS NULL → true, 'x' IS 'x' → true). Phase 2 code that
// needs optimistic-lock updates against a preSessionID that may be
// NULL should use `WHERE session_id IS ?` rather than `= ?`.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS projects (
    slug          TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','done')),
    priority      TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
    work_dir      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    archived_at   TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    slug                  TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    project_slug          TEXT REFERENCES projects(slug),
    status                TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog','in-progress','done')),
    priority              TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
    work_dir              TEXT NOT NULL,
    waiting_on            TEXT,
    due_date              TEXT,
    status_changed_at     TEXT,
    session_id            TEXT,
    session_started       TEXT,
    session_last_resumed  TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    archived_at           TEXT
);

CREATE TABLE IF NOT EXISTS workdirs (
    path          TEXT PRIMARY KEY,
    name          TEXT,
    description   TEXT,
    git_remote    TEXT,
    last_used_at  TEXT,
    created_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_project    ON tasks(project_slug);
CREATE INDEX IF NOT EXISTS idx_tasks_status     ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_updated_at ON tasks(updated_at);
`

// Project mirrors the projects table.
type Project struct {
	Slug       string
	Name       string
	Status     string
	Priority   string
	WorkDir    string
	CreatedAt  string
	UpdatedAt  string
	ArchivedAt sql.NullString
}

// Task mirrors the tasks table. ProjectSlug is nullable for floating tasks.
type Task struct {
	Slug               string
	Name               string
	ProjectSlug        sql.NullString
	Status             string
	Priority           string
	WorkDir            string
	WaitingOn          sql.NullString
	DueDate            sql.NullString
	StatusChangedAt    sql.NullString
	SessionID          sql.NullString
	SessionStarted     sql.NullString
	SessionLastResumed sql.NullString
	CreatedAt          string
	UpdatedAt          string
	ArchivedAt         sql.NullString
}

// Workdir mirrors the workdirs convenience registry.
type Workdir struct {
	Path        string
	Name        sql.NullString
	Description sql.NullString
	GitRemote   sql.NullString
	LastUsedAt  sql.NullString
	CreatedAt   string
}

// TaskFilter holds optional filters for ListTasks. Empty string fields are
// ignored. IncludeArchived=false (the default) filters out archived rows.
type TaskFilter struct {
	Status          string
	Project         string
	Priority        string
	Since           string // RFC3339 or "" for no lower bound
	IncludeArchived bool
}

// ProjectFilter is the equivalent for ListProjects.
type ProjectFilter struct {
	Status          string
	IncludeArchived bool
}

// nowISO returns the current time formatted as RFC3339. All timestamp
// columns in flow.db use this format.
func nowISO() string {
	return time.Now().Format(time.RFC3339)
}

// OpenDB opens (or creates) the SQLite database at path, ensures the
// schema is present, and runs idempotent migrations for columns added
// after the initial v2 release.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Enforce foreign keys on every connection in this pool.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign_keys: %w", err)
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// runMigrations adds columns introduced after the initial schema. Each
// step checks for the column's presence and ALTER TABLEs it in if absent.
// Idempotent — safe to call on every open.
func runMigrations(db *sql.DB) error {
	has, err := columnExists(db, "workdirs", "description")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE workdirs ADD COLUMN description TEXT`); err != nil {
			return fmt.Errorf("add workdirs.description: %w", err)
		}
	}

	// Temporal awareness columns (due_date, status_changed_at).
	has, err = columnExists(db, "tasks", "due_date")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN due_date TEXT`); err != nil {
			return fmt.Errorf("add tasks.due_date: %w", err)
		}
	}
	has, err = columnExists(db, "tasks", "status_changed_at")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN status_changed_at TEXT`); err != nil {
			return fmt.Errorf("add tasks.status_changed_at: %w", err)
		}
	}

	return nil
}

// columnExists returns whether `table.column` is present. Uses PRAGMA
// table_info which is the portable way to inspect columns in SQLite.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ---------- project helpers ----------

func scanProject(row interface {
	Scan(dest ...any) error
}) (*Project, error) {
	var p Project
	err := row.Scan(
		&p.Slug,
		&p.Name,
		&p.Status,
		&p.Priority,
		&p.WorkDir,
		&p.CreatedAt,
		&p.UpdatedAt,
		&p.ArchivedAt,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

const projectCols = "slug, name, status, priority, work_dir, created_at, updated_at, archived_at"

// GetProject fetches a single project by slug. Returns (nil, sql.ErrNoRows)
// if missing.
func GetProject(db *sql.DB, slug string) (*Project, error) {
	row := db.QueryRow("SELECT "+projectCols+" FROM projects WHERE slug = ?", slug)
	p, err := scanProject(row)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListProjects returns all projects matching filter. Sorted by slug.
func ListProjects(db *sql.DB, filter ProjectFilter) ([]*Project, error) {
	var where []string
	var args []any
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if !filter.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	q := "SELECT " + projectCols + " FROM projects"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY slug"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- task helpers ----------

const taskCols = "slug, name, project_slug, status, priority, work_dir, waiting_on, due_date, status_changed_at, session_id, session_started, session_last_resumed, created_at, updated_at, archived_at"

func scanTask(row interface {
	Scan(dest ...any) error
}) (*Task, error) {
	var t Task
	err := row.Scan(
		&t.Slug,
		&t.Name,
		&t.ProjectSlug,
		&t.Status,
		&t.Priority,
		&t.WorkDir,
		&t.WaitingOn,
		&t.DueDate,
		&t.StatusChangedAt,
		&t.SessionID,
		&t.SessionStarted,
		&t.SessionLastResumed,
		&t.CreatedAt,
		&t.UpdatedAt,
		&t.ArchivedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetTask fetches a single task by slug.
func GetTask(db *sql.DB, slug string) (*Task, error) {
	row := db.QueryRow("SELECT "+taskCols+" FROM tasks WHERE slug = ?", slug)
	return scanTask(row)
}

// ListTasks returns all tasks matching filter, sorted by priority (high,
// medium, low) then slug.
func ListTasks(db *sql.DB, filter TaskFilter) ([]*Task, error) {
	var where []string
	var args []any
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.Project != "" {
		where = append(where, "project_slug = ?")
		args = append(args, filter.Project)
	}
	if filter.Priority != "" {
		where = append(where, "priority = ?")
		args = append(args, filter.Priority)
	}
	if filter.Since != "" {
		where = append(where, "updated_at >= ?")
		args = append(args, filter.Since)
	}
	if !filter.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	q := "SELECT " + taskCols + " FROM tasks"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// Sort priority high → medium → low via CASE, then by slug.
	q += ` ORDER BY CASE priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 ELSE 3 END, slug`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---------- workdir helpers ----------

const workdirCols = "path, name, description, git_remote, last_used_at, created_at"

func scanWorkdir(row interface {
	Scan(dest ...any) error
}) (*Workdir, error) {
	var w Workdir
	err := row.Scan(
		&w.Path,
		&w.Name,
		&w.Description,
		&w.GitRemote,
		&w.LastUsedAt,
		&w.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// GetWorkdir fetches a single workdir row by absolute path.
func GetWorkdir(db *sql.DB, path string) (*Workdir, error) {
	row := db.QueryRow("SELECT "+workdirCols+" FROM workdirs WHERE path = ?", path)
	return scanWorkdir(row)
}

// ListWorkdirs returns all workdirs sorted by last_used_at descending
// (NULLs last), then path.
func ListWorkdirs(db *sql.DB) ([]*Workdir, error) {
	q := "SELECT " + workdirCols + " FROM workdirs ORDER BY last_used_at IS NULL, last_used_at DESC, path"
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("list workdirs: %w", err)
	}
	defer rows.Close()
	var out []*Workdir
	for rows.Next() {
		w, err := scanWorkdir(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workdir: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpsertWorkdir inserts or replaces a workdir row, bumping last_used_at to
// now. If the row is new, created_at is set to now as well; if it exists,
// the original created_at is preserved. Empty strings for name, description,
// or gitRemote are treated as "don't overwrite" — pass a non-empty value to
// replace the existing one.
func UpsertWorkdir(db *sql.DB, path, name, description, gitRemote string) error {
	now := nowISO()
	_, err := db.Exec(`
		INSERT INTO workdirs (path, name, description, git_remote, last_used_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			name         = COALESCE(NULLIF(excluded.name, ''),        workdirs.name),
			description  = COALESCE(NULLIF(excluded.description, ''), workdirs.description),
			git_remote   = COALESCE(NULLIF(excluded.git_remote, ''),  workdirs.git_remote),
			last_used_at = excluded.last_used_at
	`, path, nullIfEmpty(name), nullIfEmpty(description), nullIfEmpty(gitRemote), now, now)
	if err != nil {
		return fmt.Errorf("upsert workdir %s: %w", path, err)
	}
	return nil
}

// nullIfEmpty returns a *string pointing to s, or nil if s is empty.
// database/sql treats a nil interface value as SQL NULL.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
