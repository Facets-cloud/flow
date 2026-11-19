// Package db implements the SQLite data layer for flow.
package flowdb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

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
	// TerminalGroup is where `flow do` opens this project's task tabs in
	// terminals that group tabs (today: the Rex session label). NULL
	// means wherever flow ran from.
	TerminalGroup sql.NullString
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
	// Harness records which agent CLI (claude/codex/gemini/…) owns the
	// task's session. NULL/empty is treated as "claude" by callers — a
	// back-compat convention so pre-harness-column DBs Just Work.
	// Set on first `flow do` or `flow do --here` from the ambient
	// harness's session env var; immutable afterward.
	Harness sql.NullString
	// Autonomous-run bookkeeping (set by `flow do --auto`). AutoRunStatus
	// is NULL for tasks that were never run in --auto mode; otherwise one
	// of 'running' | 'completed' | 'dead'. AutoRunPID is the detached
	// supervisor process's PID (used for read-time liveness reconciliation
	// while status='running'); cleared on finalize.
	AutoRunStatus   sql.NullString
	AutoRunPID      sql.NullInt64
	AutoRunStarted  sql.NullString
	AutoRunFinished sql.NullString
	AutoRunLog      sql.NullString
	// ParentSlug is the task whose bound session ran `flow add task` to
	// create this one. NULL for tasks created from an unbound session.
	ParentSlug sql.NullString
	// Model and Effort are the harness model and effort level picked with
	// `--model` / `--effort` on a spawn or resume. Later launches with no
	// flag reuse them. NULL means the harness default.
	Model      sql.NullString
	Effort     sql.NullString
	CreatedAt  string
	UpdatedAt  string
	ArchivedAt sql.NullString
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
	Kind            string   // "regular" (default), "playbook_run", or "" for all
	PlaybookSlug    string   // optional; filter to runs of one playbook
	Tag             string   // optional; only tasks carrying this tag (already normalized)
	Tags            []string // optional; tasks must carry ALL of these (intersection; already normalized)
	Since           string   // RFC3339 or "" for no lower bound
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

// ---------- project queries ----------

const ProjectCols = "slug, name, status, priority, work_dir, created_at, updated_at, archived_at, terminal_group"

func ScanProject(row interface{ Scan(dest ...any) error }) (*Project, error) {
	var p Project
	err := row.Scan(&p.Slug, &p.Name, &p.Status, &p.Priority, &p.WorkDir, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt, &p.TerminalGroup)
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

const TaskCols = "slug, name, project_slug, status, kind, playbook_slug, priority, work_dir, waiting_on, due_date, assignee, status_changed_at, session_id, session_started, session_last_resumed, harness, auto_run_status, auto_run_pid, auto_run_started, auto_run_finished, auto_run_log, parent_slug, model, effort, created_at, updated_at, archived_at"

func ScanTask(row interface{ Scan(dest ...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(
		&t.Slug, &t.Name, &t.ProjectSlug, &t.Status, &t.Kind, &t.PlaybookSlug,
		&t.Priority, &t.WorkDir,
		&t.WaitingOn, &t.DueDate, &t.Assignee, &t.StatusChangedAt, &t.SessionID,
		&t.SessionStarted, &t.SessionLastResumed, &t.Harness,
		&t.AutoRunStatus, &t.AutoRunPID, &t.AutoRunStarted, &t.AutoRunFinished, &t.AutoRunLog,
		&t.ParentSlug, &t.Model, &t.Effort,
		&t.CreatedAt, &t.UpdatedAt, &t.ArchivedAt,
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

// ChildTasks returns the slugs of tasks whose parent_slug is slug,
// sorted by slug.
func ChildTasks(db *sql.DB, slug string) ([]string, error) {
	rows, err := db.Query(`SELECT slug FROM tasks WHERE parent_slug = ? ORDER BY slug`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
	// Intersection: each tag adds its own EXISTS-style subquery, ANDed
	// together, so a task must carry EVERY requested tag (e.g.
	// `--tag owner:x --tag question`).
	for _, t := range filter.Tags {
		if t == "" {
			continue
		}
		where = append(where, "slug IN (SELECT task_slug FROM task_tags WHERE tag = ?)")
		args = append(args, t)
	}
	if filter.Since != "" {
		// Compare instants, not strings: stored timestamps may carry any UTC
		// offset (local, "Z", a different zone after travel/DST), and RFC3339
		// strings with different offsets do not sort chronologically.
		where = append(where, "julianday(updated_at) >= julianday(?)")
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
