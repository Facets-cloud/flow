package flowdb

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// Schema version 1, "baseline": every schema check and data fix that
// OpenDB used to run on every open before versioned migrations landed,
// moved here unchanged in meaning. It runs once per DB (inside the
// migrator's transaction, foreign_keys OFF) and must accept every
// historical flow.db shape — any release tag's schema, plus legacy
// columns from unreleased branches (tasks.deleted_at, forked_from_slug).
// The probes are idempotent, so a fresh DB and a years-old DB both end
// at the same shape. Never edit it again: later changes are new
// migrations appended in migrate.go.

// schemaDDL is the core tables' DDL as of the baseline. Each statement is
// idempotent (CREATE ... IF NOT EXISTS): on a fresh DB it creates the
// tables, on an old one it is a no-op and the ALTERs below fill the gaps.
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
    archived_at   TEXT,
    terminal_group TEXT
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
    harness               TEXT,
    auto_run_status       TEXT,
    auto_run_pid          INTEGER,
    auto_run_started      TEXT,
    auto_run_finished     TEXT,
    auto_run_log          TEXT,
    parent_slug           TEXT REFERENCES tasks(slug),
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

CREATE TABLE IF NOT EXISTS owners (
    slug              TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    work_dir          TEXT NOT NULL,
    project_slug      TEXT REFERENCES projects(slug),
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','retired')),
    every             TEXT NOT NULL,
    next_wake_at      TEXT,
    last_tick_at      TEXT,
    last_tick_status  TEXT,
    tick_pid          INTEGER,
    tick_started      TEXT,
    harness           TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    archived_at       TEXT
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

// busDDL is the message-bus tables' DDL; idempotent, applied after the
// core-table steps.
const busDDL = `
CREATE TABLE IF NOT EXISTS bus_messages (
    id                 TEXT PRIMARY KEY,
    created_at         TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('message','broadcast')),
    from_assignee      TEXT NOT NULL,
    from_task_slug     TEXT,
    sender_session_id  TEXT,
    to_assignee        TEXT NOT NULL,
    to_task_slug       TEXT,
    body               TEXT NOT NULL,
    urgent             INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','acked')),
    delivered_at       TEXT,
    acked_at           TEXT,
    waited_s           REAL,
    acked_by           TEXT,
    reply_to           TEXT,
    seen_at            TEXT,
    consumed_by_identity TEXT,
    consumed_by_session  TEXT,
    options            TEXT,
    fyi                INTEGER NOT NULL DEFAULT 0,
    ping               INTEGER NOT NULL DEFAULT 0,
    notified_at        TEXT,
    answered_at        TEXT
);
CREATE INDEX IF NOT EXISTS idx_bus_messages_inbox
    ON bus_messages(to_assignee, to_task_slug, status);
CREATE INDEX IF NOT EXISTS idx_bus_messages_sender
    ON bus_messages(sender_session_id, status);

DROP TABLE IF EXISTS bus_listeners;

CREATE TABLE IF NOT EXISTS bus_watches (
    watcher     TEXT NOT NULL,
    watched     TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (watcher, watched)
);

CREATE TABLE IF NOT EXISTS bus_nudges (
    task_slug  TEXT PRIMARY KEY,
    nudged_at  TEXT NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS bus_human_surfaced (
    session_id  TEXT NOT NULL,
    message_id  TEXT NOT NULL,
    surfaced_at TEXT NOT NULL,
    PRIMARY KEY (session_id, message_id)
);

CREATE TABLE IF NOT EXISTS bus_surfaced (
    task_slug   TEXT NOT NULL,
    message_id  TEXT NOT NULL,
    PRIMARY KEY (task_slug, message_id)
);
`

// wakeDDL: the wake tables (design in wake.go).
const wakeDDL = `
CREATE TABLE IF NOT EXISTS bus_session_state (
    task_slug      TEXT PRIMARY KEY,
    state          TEXT NOT NULL,
    changed_at     TEXT NOT NULL,
    last_activity  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS bus_wake_nudged (
    task_slug   TEXT NOT NULL,
    message_id  TEXT NOT NULL,
    nudged_at   TEXT NOT NULL,
    renudged    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (task_slug, message_id)
);`

// sessionVersionDDL: upgrade-notice state (design in sessionver.go).
const sessionVersionDDL = `
CREATE TABLE IF NOT EXISTS session_versions (
    session_id    TEXT PRIMARY KEY,
    seen_version  TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);`

// sessionLineageDDL: session fork lineage (design in lineage.go).
const sessionLineageDDL = `
CREATE TABLE IF NOT EXISTS session_lineage (
    session_id         TEXT PRIMARY KEY,
    source_session_id  TEXT NOT NULL,
    task_slug          TEXT,
    created_at         TEXT NOT NULL
);`

// peersDDL: the peer contact book (design in peers.go).
const peersDDL = `
CREATE TABLE IF NOT EXISTS peers (
    name          TEXT PRIMARY KEY,
    connector     TEXT NOT NULL,
    at            TEXT NOT NULL,
    blocked       INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT,
    UNIQUE (connector, at)
);
`

// peerBusIndexDDL runs after the peer columns exist: a duplicate inbound
// delivery of the same (peer, origin_ref) is refused by the database.
const peerBusIndexDDL = `
CREATE UNIQUE INDEX IF NOT EXISTS idx_bus_messages_peer_origin
    ON bus_messages(peer, origin_ref) WHERE origin_ref IS NOT NULL;
`

// schemaMetaDDL holds the migrator's compat floor (see migrate.go).
const schemaMetaDDL = `
CREATE TABLE IF NOT EXISTS schema_meta (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    min_binary_schema  INTEGER NOT NULL
);`

// baseline is migration v1. Step order is the order the pre-migrator
// OpenDB ran them in; later steps depend on columns earlier ones add.
func baseline(q querier) error {
	if _, err := q.Exec(schemaDDL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := runMigrations(q); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if _, err := q.Exec(busDDL); err != nil {
		return fmt.Errorf("apply bus schema: %w", err)
	}
	if err := applyWakeSchema(q); err != nil {
		return fmt.Errorf("apply wake schema: %w", err)
	}
	if _, err := q.Exec(sessionVersionDDL); err != nil {
		return fmt.Errorf("apply session version schema: %w", err)
	}
	if _, err := q.Exec(sessionLineageDDL); err != nil {
		return fmt.Errorf("apply session lineage schema: %w", err)
	}
	if err := migrateBusSelfToUser(q); err != nil {
		return fmt.Errorf("migrate bus assignee: %w", err)
	}
	if err := migrateBusColumns(q); err != nil {
		return err
	}
	if err := migratePeers(q); err != nil {
		return fmt.Errorf("migrate peers: %w", err)
	}
	if _, err := q.Exec(schemaMetaDDL); err != nil {
		return fmt.Errorf("apply schema_meta: %w", err)
	}
	return nil
}

// addColumnIfMissing runs ddl (an ALTER TABLE ... ADD COLUMN) unless
// table already has column. Reports whether it added the column.
func addColumnIfMissing(q querier, table, column, ddl string) (bool, error) {
	has, err := columnExists(q, table, column)
	if err != nil {
		return false, err
	}
	if has {
		return false, nil
	}
	if _, err := q.Exec(ddl); err != nil {
		return false, fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return true, nil
}

// migrateBusColumns backfills bus columns that arrived after their tables
// shipped. All nullable or defaulted, no data, except seen_at (below).
func migrateBusColumns(q querier) error {
	for _, c := range []struct{ table, col, ddl string }{
		// bus_nudges.attempts arrived after the table shipped.
		{"bus_nudges", "attempts", `ALTER TABLE bus_nudges ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`},
		// reply_to arrived with `flow message --reply-to`.
		{"bus_messages", "reply_to", `ALTER TABLE bus_messages ADD COLUMN reply_to TEXT`},
		// consumed_by_{identity,session} record who consumed a row; no
		// backfill (old consumers are unknowable).
		{"bus_messages", "consumed_by_identity", `ALTER TABLE bus_messages ADD COLUMN consumed_by_identity TEXT`},
		{"bus_messages", "consumed_by_session", `ALTER TABLE bus_messages ADD COLUMN consumed_by_session TEXT`},
		// Human-connector columns: ask options (JSON array), the --fyi flag
		// and the send-time ping decision, when a connector accepted the
		// ping, and when a connector recorded the human's answer.
		{"bus_messages", "options", "ALTER TABLE bus_messages ADD COLUMN options TEXT"},
		{"bus_messages", "fyi", "ALTER TABLE bus_messages ADD COLUMN fyi INTEGER NOT NULL DEFAULT 0"},
		{"bus_messages", "ping", "ALTER TABLE bus_messages ADD COLUMN ping INTEGER NOT NULL DEFAULT 0"},
		{"bus_messages", "notified_at", "ALTER TABLE bus_messages ADD COLUMN notified_at TEXT"},
		{"bus_messages", "answered_at", "ALTER TABLE bus_messages ADD COLUMN answered_at TEXT"},
	} {
		if _, err := addColumnIfMissing(q, c.table, c.col, c.ddl); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", c.table, c.col, err)
		}
	}
	// bus_messages.seen_at arrived with the human-mail fixes: human mail is
	// unread until a human has seen it. On first add, stamp every existing
	// human row seen — the old mail is left behind rather than resurfaced
	// as a backlog of stale asks and broadcasts.
	added, err := addColumnIfMissing(q, "bus_messages", "seen_at", `ALTER TABLE bus_messages ADD COLUMN seen_at TEXT`)
	if err != nil {
		return fmt.Errorf("migrate bus_messages.seen_at: %w", err)
	}
	if added {
		if _, err := q.Exec(`UPDATE bus_messages SET seen_at=? WHERE to_task_slug IS NULL AND to_assignee=?`,
			NowISO(), BusUser); err != nil {
			return fmt.Errorf("stamp legacy seen_at: %w", err)
		}
	}
	return nil
}

func runMigrations(q querier) error {
	for _, c := range []struct{ table, col, ddl string }{
		{"workdirs", "description", `ALTER TABLE workdirs ADD COLUMN description TEXT`},
		{"tasks", "due_date", `ALTER TABLE tasks ADD COLUMN due_date TEXT`},
		{"tasks", "status_changed_at", `ALTER TABLE tasks ADD COLUMN status_changed_at TEXT`},
		// playbooks table itself comes from schemaDDL. SQLite doesn't allow
		// CHECK constraints on ADD COLUMN; kind's CHECK is only enforced
		// for fresh tables, so app code validates enum values.
		{"tasks", "kind", `ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'regular'`},
		{"tasks", "playbook_slug", `ALTER TABLE tasks ADD COLUMN playbook_slug TEXT REFERENCES playbooks(slug)`},
		{"tasks", "assignee", `ALTER TABLE tasks ADD COLUMN assignee TEXT`},
		// tasks.harness: nullable, no backfill. NULL is the back-compat
		// signal for "claude".
		{"tasks", "harness", `ALTER TABLE tasks ADD COLUMN harness TEXT`},
	} {
		if _, err := addColumnIfMissing(q, c.table, c.col, c.ddl); err != nil {
			return err
		}
	}

	// Session-id invariant: any non-backlog task must have a session_id.
	// Adds a CHECK to the tasks table; old DBs need a table rebuild.
	if err := migrateTasksSessionInvariant(q); err != nil {
		return fmt.Errorf("migrate session invariant: %w", err)
	}

	// Autonomous-run bookkeeping columns (flow do --auto), parent_slug
	// (the task whose bound session created this one; some live DBs
	// already carry a legacy, unused parent_slug of this exact shape,
	// hence exists-checks rather than blind ALTERs), projects.
	// terminal_group (where `flow do` opens a project's tabs), and the
	// owners live-tick columns. All nullable, no CHECK.
	for _, c := range []struct{ table, col, ddl string }{
		{"tasks", "auto_run_status", "ALTER TABLE tasks ADD COLUMN auto_run_status TEXT"},
		{"tasks", "auto_run_pid", "ALTER TABLE tasks ADD COLUMN auto_run_pid INTEGER"},
		{"tasks", "auto_run_started", "ALTER TABLE tasks ADD COLUMN auto_run_started TEXT"},
		{"tasks", "auto_run_finished", "ALTER TABLE tasks ADD COLUMN auto_run_finished TEXT"},
		{"tasks", "auto_run_log", "ALTER TABLE tasks ADD COLUMN auto_run_log TEXT"},
		{"tasks", "parent_slug", "ALTER TABLE tasks ADD COLUMN parent_slug TEXT REFERENCES tasks(slug)"},
		{"projects", "terminal_group", `ALTER TABLE projects ADD COLUMN terminal_group TEXT`},
		{"owners", "tick_pid", "ALTER TABLE owners ADD COLUMN tick_pid INTEGER"},
		{"owners", "tick_started", "ALTER TABLE owners ADD COLUMN tick_started TEXT"},
	} {
		if _, err := addColumnIfMissing(q, c.table, c.col, c.ddl); err != nil {
			return err
		}
	}

	// Indexes that depend on columns added above.
	if _, err := q.Exec(indexesPostMigrate); err != nil {
		return fmt.Errorf("create post-migrate indexes: %w", err)
	}

	// Session-id uniqueness: dedupe, then the partial unique index.
	if err := migrateTasksSessionIDUnique(q); err != nil {
		return fmt.Errorf("migrate session-id uniqueness: %w", err)
	}
	return nil
}

// migrateTasksSessionIDUnique creates the partial unique index on
// tasks(session_id) WHERE session_id IS NOT NULL. Older DBs may have two
// tasks sharing a session_id (the old `flow update task --session-id`
// flag could overwrite a binding without clearing the prior owner), so it
// first deduplicates: per shared session_id, the carrier with the newest
// updated_at (then lowest slug) keeps it; the rest get session_id=NULL,
// session_started=NULL, status='backlog', with a stderr summary.
// Skipped when the index already exists.
func migrateTasksSessionIDUnique(q querier) error {
	var existing sql.NullString
	err := q.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_tasks_session_id'`,
	).Scan(&existing)
	if err == nil && existing.Valid {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("probe unique index: %w", err)
	}

	dupedSIDs, err := queryStrings(q,
		`SELECT session_id FROM tasks
		 WHERE session_id IS NOT NULL
		 GROUP BY session_id
		 HAVING COUNT(*) > 1
		 ORDER BY session_id`)
	if err != nil {
		return fmt.Errorf("scan duplicates: %w", err)
	}

	if len(dupedSIDs) > 0 {
		fmt.Fprintf(os.Stderr,
			"flow migration: deduplicating %d session_id(s) shared across multiple tasks (only one task may carry a given session_id):\n",
			len(dupedSIDs))
		now := NowISO()
		for _, sid := range dupedSIDs {
			tRows, err := q.Query(
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
				if _, err := q.Exec(
					`UPDATE tasks SET session_id=NULL, session_started=NULL, status='backlog', updated_at=? WHERE slug=?`,
					now, l.slug,
				); err != nil {
					return fmt.Errorf("demote duplicate %s: %w", l.slug, err)
				}
			}
		}
	}

	if _, err := q.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_session_id ON tasks(session_id) WHERE session_id IS NOT NULL`,
	); err != nil {
		return fmt.Errorf("create unique index: %w", err)
	}
	return nil
}

// tasksRebuildCols is the column list of the session-invariant rebuild,
// in order. Columns that later baseline steps add by ALTER are listed
// too, so a DB that already has them (legacy parent_slug, or a column
// shipped before the rebuild ran on that DB) keeps their exact
// definition; they are only emitted when the old table has them.
var tasksRebuildCols = []struct {
	name, def string
	always    bool
}{
	{"slug", "TEXT PRIMARY KEY", true},
	{"name", "TEXT NOT NULL", true},
	{"project_slug", "TEXT REFERENCES projects(slug)", true},
	{"status", "TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog','in-progress','done'))", true},
	{"kind", "TEXT NOT NULL DEFAULT 'regular' CHECK (kind IN ('regular','playbook_run'))", true},
	{"playbook_slug", "TEXT REFERENCES playbooks(slug)", true},
	{"priority", "TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low'))", true},
	{"work_dir", "TEXT NOT NULL", true},
	{"waiting_on", "TEXT", true},
	{"due_date", "TEXT", true},
	{"assignee", "TEXT", true},
	{"status_changed_at", "TEXT", true},
	{"session_id", "TEXT", true},
	{"session_started", "TEXT", true},
	{"session_last_resumed", "TEXT", true},
	{"harness", "TEXT", true},
	{"auto_run_status", "TEXT", false},
	{"auto_run_pid", "INTEGER", false},
	{"auto_run_started", "TEXT", false},
	{"auto_run_finished", "TEXT", false},
	{"auto_run_log", "TEXT", false},
	{"parent_slug", "TEXT REFERENCES tasks(slug)", false},
	{"created_at", "TEXT NOT NULL", true},
	{"updated_at", "TEXT NOT NULL", true},
	{"archived_at", "TEXT", true},
}

type tableCol struct {
	name, ctype string
	notnull     bool
	dflt        sql.NullString
}

func tableColumns(q querier, table string) ([]tableCol, error) {
	rows, err := q.Query(`SELECT name, type, "notnull", dflt_value FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()
	var out []tableCol
	for rows.Next() {
		var c tableCol
		var nn int
		if err := rows.Scan(&c.name, &c.ctype, &nn, &c.dflt); err != nil {
			return nil, err
		}
		c.notnull = nn != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// migrateTasksSessionInvariant rebuilds the tasks table to enforce
// `CHECK (status = 'backlog' OR session_id IS NOT NULL)`. SQLite can't
// add a CHECK via ALTER TABLE, so it uses the documented procedure
// (CREATE new, copy, DROP old, RENAME); the caller has foreign_keys OFF
// and a transaction open. Skipped when the tasks DDL already has the
// CHECK. Rows that violate it are demoted to backlog first (stderr
// summary): there is no way to invent a session_id after the fact.
//
// Every column of the old table survives, including ones this code
// doesn't know about (legacy tasks.deleted_at): the copy list comes from
// pragma_table_info, not a hard-coded list.
func migrateTasksSessionInvariant(q querier) error {
	var ddl string
	if err := q.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'`,
	).Scan(&ddl); err != nil {
		return fmt.Errorf("inspect tasks ddl: %w", err)
	}
	if strings.Contains(ddl, "session_id IS NOT NULL") {
		return nil
	}

	type violator struct{ slug, prevStatus string }
	var vs []violator
	rows, err := q.Query(
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
		if _, err := q.Exec(
			`UPDATE tasks SET status='backlog', updated_at=? WHERE status != 'backlog' AND session_id IS NULL`,
			NowISO(),
		); err != nil {
			return fmt.Errorf("demote violators: %w", err)
		}
	}

	old, err := tableColumns(q, "tasks")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, c := range old {
		have[c.name] = true
	}
	known := map[string]bool{}
	var defs []string
	for _, c := range tasksRebuildCols {
		known[c.name] = true
		if c.always || have[c.name] {
			defs = append(defs, c.name+" "+c.def)
		}
	}
	var copyCols []string
	for _, c := range old {
		if !known[c.name] {
			// Unknown column: keep its declared type, NOT NULL and default.
			d := quoteIdent(c.name)
			if c.ctype != "" {
				d += " " + c.ctype
			}
			if c.notnull {
				d += " NOT NULL"
			}
			if c.dflt.Valid {
				d += " DEFAULT " + c.dflt.String
			}
			defs = append(defs, d)
		}
		copyCols = append(copyCols, quoteIdent(c.name))
	}
	defs = append(defs, "CHECK (status = 'backlog' OR session_id IS NOT NULL)")

	if _, err := q.Exec("CREATE TABLE tasks_new (\n    " + strings.Join(defs, ",\n    ") + "\n)"); err != nil {
		return fmt.Errorf("create tasks_new: %w", err)
	}
	cols := strings.Join(copyCols, ", ")
	if _, err := q.Exec(`INSERT INTO tasks_new (` + cols + `) SELECT ` + cols + ` FROM tasks`); err != nil {
		return fmt.Errorf("copy rows: %w", err)
	}
	if _, err := q.Exec(`DROP TABLE tasks`); err != nil {
		return fmt.Errorf("drop old tasks: %w", err)
	}
	if _, err := q.Exec(`ALTER TABLE tasks_new RENAME TO tasks`); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	// Indexes from schemaDDL that lived on the dropped table (the rest are
	// recreated by indexesPostMigrate / migrateTasksSessionIDUnique).
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_tasks_project    ON tasks(project_slug)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status     ON tasks(status)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_updated_at ON tasks(updated_at)`,
	} {
		if _, err := q.Exec(idx); err != nil {
			return fmt.Errorf("recreate index: %w", err)
		}
	}
	return nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func queryStrings(q querier, query string, args ...any) ([]string, error) {
	rows, err := q.Query(query, args...)
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

func columnExists(q querier, table, column string) (bool, error) {
	var n int
	if err := q.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return false, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	return n > 0, nil
}

// migrateBusSelfToUser renames the reserved assignee 'self' to 'user'
// in existing rows (the old spelling confused agents into reading
// "message self" as talking to themselves).
func migrateBusSelfToUser(q querier) error {
	for _, s := range []string{
		`UPDATE bus_messages SET to_assignee='user' WHERE to_assignee='self'`,
		`UPDATE bus_messages SET from_assignee='user' WHERE from_assignee='self'`,
		`UPDATE bus_watches SET watcher='user' WHERE watcher='self'`,
		`UPDATE bus_watches SET watcher='user' || substr(watcher, 5) WHERE watcher LIKE 'self/%'`,
	} {
		if _, err := q.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// applyWakeSchema creates the wake tables. On the first open after an
// upgrade from a release where bus_surfaced was one claim shared by wake
// and hooks (alpha.38), a pending row's mark may be a push that never
// arrived: free every pending row's mark so the hooks announce it again.
// Worst case is one harmless duplicate PreToolUse line. Keyed on the new
// table's absence.
func applyWakeSchema(q querier) error {
	var n int
	if err := q.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bus_wake_nudged'`).Scan(&n); err != nil {
		return err
	}
	if _, err := q.Exec(wakeDDL); err != nil {
		return err
	}
	if n == 0 {
		if _, err := q.Exec(`DELETE FROM bus_surfaced
            WHERE message_id IN (SELECT id FROM bus_messages WHERE status='pending')`); err != nil {
			return err
		}
	}
	return nil
}

// migratePeers creates the peers table and backfills the peer columns on
// bus_messages (nullable, no data) plus their unique index. attachments
// (JSON array of {name,size,sha256}) arrived with peer desk primitives.
func migratePeers(q querier) error {
	if _, err := q.Exec(peersDDL); err != nil {
		return fmt.Errorf("peers table: %w", err)
	}
	for _, col := range []string{"peer", "origin_ref", "delivery", "delivery_error", "attachments"} {
		if _, err := addColumnIfMissing(q, "bus_messages", col, `ALTER TABLE bus_messages ADD COLUMN `+col+` TEXT`); err != nil {
			return fmt.Errorf("bus_messages.%s: %w", col, err)
		}
	}
	if _, err := q.Exec(peerBusIndexDDL); err != nil {
		return fmt.Errorf("peer origin index: %w", err)
	}
	return nil
}
