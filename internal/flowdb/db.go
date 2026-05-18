// Package db implements the SQLite data layer for flow.
package flowdb

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// schemaDDL is the full DDL for flow.db. Each statement is idempotent
// (CREATE ... IF NOT EXISTS) so OpenDB can run this on every startup.
//
// Note on NULL-safe equality: SQLite's `IS` operator treats NULLs as
// equal (NULL IS NULL → true, 'x' IS 'x' → true). Code that needs
// optimistic-lock updates against a preSessionID that may be NULL
// should use `WHERE session_id IS ?` rather than `= ?`.
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

CREATE TABLE IF NOT EXISTS playbooks (
    slug          TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    project_slug  TEXT REFERENCES projects(slug),
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
    kind                  TEXT NOT NULL DEFAULT 'regular' CHECK (kind IN ('regular','playbook_run')),
    playbook_slug         TEXT REFERENCES playbooks(slug),
    priority              TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
    work_dir              TEXT NOT NULL,
    waiting_on            TEXT,
    due_date              TEXT,
    assignee              TEXT,
    status_changed_at     TEXT,
    session_id            TEXT,
    session_started       TEXT,
    session_last_resumed  TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    archived_at           TEXT,
    CHECK (status = 'backlog' OR session_id IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS workdirs (
    path          TEXT PRIMARY KEY,
    name          TEXT,
    description   TEXT,
    git_remote    TEXT,
    last_used_at  TEXT,
    created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_tags (
    task_slug   TEXT NOT NULL REFERENCES tasks(slug) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (task_slug, tag)
);

CREATE INDEX IF NOT EXISTS idx_tasks_project    ON tasks(project_slug);
CREATE INDEX IF NOT EXISTS idx_tasks_status     ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_updated_at ON tasks(updated_at);
CREATE INDEX IF NOT EXISTS idx_task_tags_tag    ON task_tags(tag);
`

// indexesPostMigrate are indexes that depend on columns added by
// runMigrations. Running them in schemaDDL before migrations would fail
// against an existing pre-migration DB ("no such column"), so they live
// here and run AFTER migrations land.
const indexesPostMigrate = `
CREATE INDEX IF NOT EXISTS idx_tasks_kind          ON tasks(kind);
CREATE INDEX IF NOT EXISTS idx_tasks_playbook_slug ON tasks(playbook_slug);
CREATE INDEX IF NOT EXISTS idx_playbooks_project   ON playbooks(project_slug);
`

// (idx_tasks_session_id is a partial UNIQUE index that requires a
// dedupe pass against existing data — a flat CREATE UNIQUE INDEX
// would fail on any DB that has two tasks sharing a session_id.
// migrateTasksSessionIDUnique handles both the dedupe and the index
// creation as one idempotent step.)

// ---------- models ----------

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
	Kind               string         // 'regular' | 'playbook_run'
	PlaybookSlug       sql.NullString // set when Kind='playbook_run'
	Priority           string
	WorkDir            string
	WaitingOn          sql.NullString
	DueDate            sql.NullString
	Assignee           sql.NullString
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

// TaskFilter holds optional filters for ListTasks.
type TaskFilter struct {
	Status          string
	Project         string
	Priority        string
	Kind            string // "regular" (default), "playbook_run", or "" for all
	PlaybookSlug    string // optional; filter to runs of one playbook
	Tag             string // optional; only tasks carrying this tag (already normalized)
	Since           string // RFC3339 or "" for no lower bound
	IncludeArchived bool
	ExcludeDone     bool // hide status=done; ignored if Status is set explicitly
}

// ProjectFilter is the equivalent for ListProjects.
type ProjectFilter struct {
	Status          string
	IncludeArchived bool
}

// ---------- lifecycle ----------

// NowISO returns the current time formatted as RFC3339.
func NowISO() string {
	return time.Now().Format(time.RFC3339)
}

// NullIfEmpty returns a *string pointing to s, or nil if s is empty.
func NullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// OpenDB opens (or creates) the SQLite database at path, ensures the
// schema is present, and runs idempotent migrations.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
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

	// playbooks table: created via schemaDDL on every OpenDB, so no ALTER needed
	// for the table itself. Just ensure tasks columns are present.

	has, err = columnExists(db, "tasks", "kind")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'regular'`); err != nil {
			return fmt.Errorf("add tasks.kind: %w", err)
		}
		// Note: SQLite doesn't allow CHECK constraints on ADD COLUMN; the
		// CHECK is only enforced for fresh tables (see schemaDDL). Application
		// code should validate enum values before insert.
	}

	has, err = columnExists(db, "tasks", "playbook_slug")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN playbook_slug TEXT REFERENCES playbooks(slug)`); err != nil {
			return fmt.Errorf("add tasks.playbook_slug: %w", err)
		}
	}

	has, err = columnExists(db, "tasks", "assignee")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN assignee TEXT`); err != nil {
			return fmt.Errorf("add tasks.assignee: %w", err)
		}
	}

	// Session-id invariant: any non-backlog task must have a session_id.
	// Adds a CHECK to the tasks table; old DBs need a table rebuild.
	if err := migrateTasksSessionInvariant(db); err != nil {
		return fmt.Errorf("migrate session invariant: %w", err)
	}

	// Indexes that depend on columns added above. Safe to run after every
	// migration pass — CREATE INDEX IF NOT EXISTS is idempotent, and by
	// this point all referenced columns exist.
	if _, err := db.Exec(indexesPostMigrate); err != nil {
		return fmt.Errorf("create post-migrate indexes: %w", err)
	}

	// Session-id uniqueness: dedupe any tasks sharing a session_id, then
	// create the partial unique index. Runs after the basic indexes
	// because it has its own dedupe-first contract; a naive
	// CREATE UNIQUE INDEX in indexesPostMigrate would fail on a DB with
	// pre-existing duplicates.
	if err := migrateTasksSessionIDUnique(db); err != nil {
		return fmt.Errorf("migrate session-id uniqueness: %w", err)
	}
	return nil
}

// migrateTasksSessionIDUnique creates the partial unique index on
// tasks(session_id) WHERE session_id IS NOT NULL. Older DBs may have
// two tasks sharing a session_id (the old `flow update task
// --session-id` flag could silently overwrite a binding without
// clearing the prior owner; or a user manually edited the row). A
// flat CREATE UNIQUE INDEX would fail on those DBs, so this function
// first deduplicates by:
//
//  1. Listing every session_id that appears on 2+ tasks.
//  2. For each such session_id, ordering the carrier tasks by
//     updated_at DESC, slug ASC. The first row keeps the binding.
//  3. The remaining rows get session_id=NULL, session_started=NULL,
//     and status='backlog' (the only state legal for a NULL
//     session_id under the invariant). A stderr summary explains
//     which task kept the session and which were demoted.
//
// Idempotent: probes sqlite_master for the index first; subsequent
// calls are no-ops once the index exists.
func migrateTasksSessionIDUnique(db *sql.DB) error {
	var existing sql.NullString
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_tasks_session_id'`,
	).Scan(&existing)
	if err == nil && existing.Valid {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("probe unique index: %w", err)
	}

	rows, err := db.Query(
		`SELECT session_id FROM tasks
		 WHERE session_id IS NOT NULL
		 GROUP BY session_id
		 HAVING COUNT(*) > 1
		 ORDER BY session_id`,
	)
	if err != nil {
		return fmt.Errorf("scan duplicates: %w", err)
	}
	var dupedSIDs []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return err
		}
		dupedSIDs = append(dupedSIDs, sid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(dupedSIDs) > 0 {
		fmt.Fprintf(os.Stderr,
			"flow migration: deduplicating %d session_id(s) shared across multiple tasks (only one task may carry a given session_id):\n",
			len(dupedSIDs))
		now := NowISO()
		for _, sid := range dupedSIDs {
			tRows, err := db.Query(
				`SELECT slug, status FROM tasks
				 WHERE session_id = ?
				 ORDER BY updated_at DESC, slug ASC`,
				sid,
			)
			if err != nil {
				return fmt.Errorf("scan duplicate group %s: %w", sid, err)
			}
			type tRow struct{ slug, status string }
			var carriers []tRow
			for tRows.Next() {
				var t tRow
				if err := tRows.Scan(&t.slug, &t.status); err != nil {
					tRows.Close()
					return err
				}
				carriers = append(carriers, t)
			}
			if err := tRows.Err(); err != nil {
				tRows.Close()
				return err
			}
			tRows.Close()
			if len(carriers) < 2 {
				continue
			}
			winner := carriers[0]
			fmt.Fprintf(os.Stderr,
				"  %s: keeping on %s (was %s); demoting to backlog with NULL session_id:\n",
				sid, winner.slug, winner.status)
			for _, l := range carriers[1:] {
				fmt.Fprintf(os.Stderr, "    - %s (was %s)\n", l.slug, l.status)
				if _, err := db.Exec(
					`UPDATE tasks SET session_id=NULL, session_started=NULL, status='backlog', updated_at=? WHERE slug=?`,
					now, l.slug,
				); err != nil {
					return fmt.Errorf("demote duplicate %s: %w", l.slug, err)
				}
			}
		}
	}

	if _, err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_session_id ON tasks(session_id) WHERE session_id IS NOT NULL`,
	); err != nil {
		return fmt.Errorf("create unique index: %w", err)
	}
	return nil
}

// migrateTasksSessionInvariant rebuilds the tasks table to enforce
// `CHECK (status = 'backlog' OR session_id IS NOT NULL)`. SQLite does
// not support adding a CHECK constraint to an existing table via
// ALTER TABLE, so the documented procedure (CREATE new, copy, DROP
// old, RENAME) is used. Idempotent: probes sqlite_master for the
// CHECK substring before doing any work, so subsequent calls are
// no-ops. Existing rows that violate the new constraint are demoted
// to backlog first (with a stderr summary), since there is no way to
// invent a session_id for them after the fact.
func migrateTasksSessionInvariant(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'`,
	).Scan(&ddl); err != nil {
		return fmt.Errorf("inspect tasks ddl: %w", err)
	}
	if strings.Contains(ddl, "session_id IS NOT NULL") {
		return nil
	}

	type violator struct{ slug, prevStatus string }
	var vs []violator
	rows, err := db.Query(
		`SELECT slug, status FROM tasks WHERE status != 'backlog' AND session_id IS NULL`,
	)
	if err != nil {
		return fmt.Errorf("scan violators: %w", err)
	}
	for rows.Next() {
		var v violator
		if err := rows.Scan(&v.slug, &v.prevStatus); err != nil {
			rows.Close()
			return err
		}
		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(vs) > 0 {
		fmt.Fprintf(os.Stderr,
			"flow migration: demoting %d task(s) without session_id back to backlog "+
				"(only backlog may have NULL session_id under the new invariant):\n",
			len(vs))
		for _, v := range vs {
			fmt.Fprintf(os.Stderr, "  %s (was %s)\n", v.slug, v.prevStatus)
		}
		if _, err := db.Exec(
			`UPDATE tasks SET status='backlog', updated_at=? WHERE status != 'backlog' AND session_id IS NULL`,
			NowISO(),
		); err != nil {
			return fmt.Errorf("demote violators: %w", err)
		}
	}

	// SQLite-recommended ALTER-by-rebuild procedure. PRAGMA must be set
	// outside the transaction.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable fk: %w", err)
	}
	defer func() { _, _ = db.Exec(`PRAGMA foreign_keys = ON`) }()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(`
		CREATE TABLE tasks_new (
			slug                  TEXT PRIMARY KEY,
			name                  TEXT NOT NULL,
			project_slug          TEXT REFERENCES projects(slug),
			status                TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog','in-progress','done')),
			kind                  TEXT NOT NULL DEFAULT 'regular' CHECK (kind IN ('regular','playbook_run')),
			playbook_slug         TEXT REFERENCES playbooks(slug),
			priority              TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
			work_dir              TEXT NOT NULL,
			waiting_on            TEXT,
			due_date              TEXT,
			assignee              TEXT,
			status_changed_at     TEXT,
			session_id            TEXT,
			session_started       TEXT,
			session_last_resumed  TEXT,
			created_at            TEXT NOT NULL,
			updated_at            TEXT NOT NULL,
			archived_at           TEXT,
			CHECK (status = 'backlog' OR session_id IS NOT NULL)
		)`); err != nil {
		return fmt.Errorf("create tasks_new: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO tasks_new (
			slug, name, project_slug, status, kind, playbook_slug, priority,
			work_dir, waiting_on, due_date, assignee, status_changed_at,
			session_id, session_started, session_last_resumed,
			created_at, updated_at, archived_at
		)
		SELECT
			slug, name, project_slug, status, kind, playbook_slug, priority,
			work_dir, waiting_on, due_date, assignee, status_changed_at,
			session_id, session_started, session_last_resumed,
			created_at, updated_at, archived_at
		FROM tasks`); err != nil {
		return fmt.Errorf("copy rows: %w", err)
	}

	if _, err := tx.Exec(`DROP TABLE tasks`); err != nil {
		return fmt.Errorf("drop old tasks: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE tasks_new RENAME TO tasks`); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	// Indexes from schemaDDL that lived on the dropped table.
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_tasks_project    ON tasks(project_slug)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status     ON tasks(status)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_updated_at ON tasks(updated_at)`,
	} {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("recreate index: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

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

// ---------- project queries ----------

const ProjectCols = "slug, name, status, priority, work_dir, created_at, updated_at, archived_at"

func ScanProject(row interface{ Scan(dest ...any) error }) (*Project, error) {
	var p Project
	err := row.Scan(&p.Slug, &p.Name, &p.Status, &p.Priority, &p.WorkDir, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func GetProject(db *sql.DB, slug string) (*Project, error) {
	row := db.QueryRow("SELECT "+ProjectCols+" FROM projects WHERE slug = ?", slug)
	return ScanProject(row)
}

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
	q := "SELECT " + ProjectCols + " FROM projects"
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
		p, err := ScanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- task queries ----------

const TaskCols = "slug, name, project_slug, status, kind, playbook_slug, priority, work_dir, waiting_on, due_date, assignee, status_changed_at, session_id, session_started, session_last_resumed, created_at, updated_at, archived_at"

func ScanTask(row interface{ Scan(dest ...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(
		&t.Slug, &t.Name, &t.ProjectSlug, &t.Status, &t.Kind, &t.PlaybookSlug,
		&t.Priority, &t.WorkDir,
		&t.WaitingOn, &t.DueDate, &t.Assignee, &t.StatusChangedAt, &t.SessionID,
		&t.SessionStarted, &t.SessionLastResumed, &t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func GetTask(db *sql.DB, slug string) (*Task, error) {
	row := db.QueryRow("SELECT "+TaskCols+" FROM tasks WHERE slug = ?", slug)
	return ScanTask(row)
}

// TaskBySessionID returns the task bound to the given Claude session
// UUID, or sql.ErrNoRows if none. The partial unique index on
// tasks(session_id) WHERE session_id IS NOT NULL guarantees at most
// one row. Empty sid is treated as "no binding" and returns ErrNoRows
// without hitting the DB.
func TaskBySessionID(db *sql.DB, sid string) (*Task, error) {
	if sid == "" {
		return nil, sql.ErrNoRows
	}
	row := db.QueryRow("SELECT "+TaskCols+" FROM tasks WHERE session_id = ? LIMIT 1", sid)
	return ScanTask(row)
}

func ListTasks(db *sql.DB, filter TaskFilter) ([]*Task, error) {
	var where []string
	var args []any
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	} else if filter.ExcludeDone {
		where = append(where, "status != 'done'")
	}
	if filter.Project != "" {
		where = append(where, "project_slug = ?")
		args = append(args, filter.Project)
	}
	if filter.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.PlaybookSlug != "" {
		where = append(where, "playbook_slug = ?")
		args = append(args, filter.PlaybookSlug)
	}
	if filter.Priority != "" {
		where = append(where, "priority = ?")
		args = append(args, filter.Priority)
	}
	if filter.Tag != "" {
		where = append(where, "slug IN (SELECT task_slug FROM task_tags WHERE tag = ?)")
		args = append(args, filter.Tag)
	}
	if filter.Since != "" {
		where = append(where, "updated_at >= ?")
		args = append(args, filter.Since)
	}
	if !filter.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	q := "SELECT " + TaskCols + " FROM tasks"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY CASE priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 ELSE 3 END, slug`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := ScanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---------- workdir queries ----------

const WorkdirCols = "path, name, description, git_remote, last_used_at, created_at"

func ScanWorkdir(row interface{ Scan(dest ...any) error }) (*Workdir, error) {
	var w Workdir
	err := row.Scan(&w.Path, &w.Name, &w.Description, &w.GitRemote, &w.LastUsedAt, &w.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func GetWorkdir(db *sql.DB, path string) (*Workdir, error) {
	row := db.QueryRow("SELECT "+WorkdirCols+" FROM workdirs WHERE path = ?", path)
	return ScanWorkdir(row)
}

func ListWorkdirs(db *sql.DB) ([]*Workdir, error) {
	q := "SELECT " + WorkdirCols + " FROM workdirs ORDER BY last_used_at IS NULL, last_used_at DESC, path"
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("list workdirs: %w", err)
	}
	defer rows.Close()
	var out []*Workdir
	for rows.Next() {
		w, err := ScanWorkdir(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workdir: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func UpsertWorkdir(db *sql.DB, path, name, description, gitRemote string) error {
	now := NowISO()
	_, err := db.Exec(`
		INSERT INTO workdirs (path, name, description, git_remote, last_used_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			name         = COALESCE(NULLIF(excluded.name, ''),        workdirs.name),
			description  = COALESCE(NULLIF(excluded.description, ''), workdirs.description),
			git_remote   = COALESCE(NULLIF(excluded.git_remote, ''),  workdirs.git_remote),
			last_used_at = excluded.last_used_at
	`, path, NullIfEmpty(name), NullIfEmpty(description), NullIfEmpty(gitRemote), now, now)
	if err != nil {
		return fmt.Errorf("upsert workdir %s: %w", path, err)
	}
	return nil
}

// ---------- playbook models ----------

// Playbook mirrors the playbooks table.
type Playbook struct {
	Slug        string
	Name        string
	ProjectSlug sql.NullString
	WorkDir     string
	CreatedAt   string
	UpdatedAt   string
	ArchivedAt  sql.NullString
}

// PlaybookFilter holds optional filters for ListPlaybooks.
type PlaybookFilter struct {
	Project         string
	IncludeArchived bool
}

// ---------- playbook queries ----------

const PlaybookCols = "slug, name, project_slug, work_dir, created_at, updated_at, archived_at"

func ScanPlaybook(row interface{ Scan(dest ...any) error }) (*Playbook, error) {
	var p Playbook
	err := row.Scan(&p.Slug, &p.Name, &p.ProjectSlug, &p.WorkDir, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func GetPlaybook(db *sql.DB, slug string) (*Playbook, error) {
	row := db.QueryRow("SELECT "+PlaybookCols+" FROM playbooks WHERE slug = ?", slug)
	return ScanPlaybook(row)
}

func ListPlaybooks(db *sql.DB, filter PlaybookFilter) ([]*Playbook, error) {
	var where []string
	var args []any
	if filter.Project != "" {
		where = append(where, "project_slug = ?")
		args = append(args, filter.Project)
	}
	if !filter.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	q := "SELECT " + PlaybookCols + " FROM playbooks"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY slug"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list playbooks: %w", err)
	}
	defer rows.Close()
	var out []*Playbook
	for rows.Next() {
		p, err := ScanPlaybook(rows)
		if err != nil {
			return nil, fmt.Errorf("scan playbook: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- task tag queries ----------

// NormalizeTag canonicalizes a tag for storage and comparison: trim
// whitespace, lowercase. Returns "" for input that contains nothing
// after trimming — callers should treat "" as invalid.
func NormalizeTag(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// AddTaskTag attaches a tag to a task. Idempotent: re-adding an existing
// (task_slug, tag) pair is a no-op via INSERT OR IGNORE.
func AddTaskTag(db *sql.DB, slug, tag string) error {
	t := NormalizeTag(tag)
	if t == "" {
		return fmt.Errorf("tag is empty")
	}
	_, err := db.Exec(
		`INSERT OR IGNORE INTO task_tags (task_slug, tag, created_at) VALUES (?, ?, ?)`,
		slug, t, NowISO(),
	)
	if err != nil {
		return fmt.Errorf("add tag %s on %s: %w", t, slug, err)
	}
	return nil
}

// RemoveTaskTag detaches a tag from a task. No error if the pair doesn't
// exist; caller can pre-check via GetTaskTags.
func RemoveTaskTag(db *sql.DB, slug, tag string) error {
	t := NormalizeTag(tag)
	if t == "" {
		return fmt.Errorf("tag is empty")
	}
	_, err := db.Exec(`DELETE FROM task_tags WHERE task_slug = ? AND tag = ?`, slug, t)
	if err != nil {
		return fmt.Errorf("remove tag %s from %s: %w", t, slug, err)
	}
	return nil
}

// ClearTaskTags removes all tags from a task.
func ClearTaskTags(db *sql.DB, slug string) error {
	_, err := db.Exec(`DELETE FROM task_tags WHERE task_slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("clear tags for %s: %w", slug, err)
	}
	return nil
}

// GetTaskTags returns the tags on a single task, sorted alphabetically.
func GetTaskTags(db *sql.DB, slug string) ([]string, error) {
	rows, err := db.Query(`SELECT tag FROM task_tags WHERE task_slug = ? ORDER BY tag`, slug)
	if err != nil {
		return nil, fmt.Errorf("get tags for %s: %w", slug, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTaskTagsBatch returns tags for many tasks in one query, keyed by
// task slug. Used by list output to avoid N+1 queries.
func GetTaskTagsBatch(db *sql.DB, slugs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(slugs))
	if len(slugs) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(slugs)-1) + "?"
	args := make([]any, 0, len(slugs))
	for _, s := range slugs {
		args = append(args, s)
	}
	q := `SELECT task_slug, tag FROM task_tags WHERE task_slug IN (` + placeholders + `) ORDER BY task_slug, tag`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("batch get tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slug, tag string
		if err := rows.Scan(&slug, &tag); err != nil {
			return nil, err
		}
		out[slug] = append(out[slug], tag)
	}
	return out, rows.Err()
}

// TagCount is the (tag, task-count) pair returned by ListAllTags.
type TagCount struct {
	Tag   string
	Count int
}

// ListAllTags returns every distinct tag in use, with the number of
// non-archived tasks that carry it. Sorted by count descending, then
// tag ascending — most-used tags first.
func ListAllTags(db *sql.DB) ([]TagCount, error) {
	rows, err := db.Query(`
		SELECT t.tag, COUNT(*) AS n
		FROM task_tags t
		JOIN tasks tk ON tk.slug = t.task_slug
		WHERE tk.archived_at IS NULL
		GROUP BY t.tag
		ORDER BY n DESC, t.tag ASC`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Tag, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// UpsertPlaybook inserts a new playbook or updates an existing row by slug.
// Updates touch name, project_slug, work_dir, updated_at; archived_at is
// not touched here (use a dedicated archive command).
func UpsertPlaybook(db *sql.DB, pb *Playbook) error {
	now := NowISO()
	if pb.CreatedAt == "" {
		pb.CreatedAt = now
	}
	pb.UpdatedAt = now
	_, err := db.Exec(`
		INSERT INTO playbooks (slug, name, project_slug, work_dir, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			name         = excluded.name,
			project_slug = excluded.project_slug,
			work_dir     = excluded.work_dir,
			updated_at   = excluded.updated_at
	`, pb.Slug, pb.Name, pb.ProjectSlug, pb.WorkDir, pb.CreatedAt, pb.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert playbook %s: %w", pb.Slug, err)
	}
	return nil
}
