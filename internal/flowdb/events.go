package flowdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// task_events is an append-only log of what happened to a task: status
// transitions (done, reopen, start), autonomous-run launches and their
// terminal status, verification receipts, and driver verdicts. The tasks
// table keeps only the latest state — status_changed_at is overwritten on
// every flip and the auto_run_* columns are reset on every relaunch — so
// without this log a reopened task, an earlier failed run, or who closed a
// task cannot be recovered. Rows are never updated or deleted.
//
// Consumers (dashboards, self-improving harness loops) read it through
// `flow events --format json`, never by opening flow.db, so the schema
// can evolve behind the command.
const taskEventsDDL = `
CREATE TABLE IF NOT EXISTS task_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_slug   TEXT NOT NULL,
    at          TEXT NOT NULL,
    kind        TEXT NOT NULL,
    actor       TEXT NOT NULL,
    session_id  TEXT,
    detail      TEXT
);
CREATE INDEX IF NOT EXISTS idx_task_events_task ON task_events(task_slug, id);
CREATE INDEX IF NOT EXISTS idx_task_events_at   ON task_events(at);
`

// Event kinds. Validated in application code (SQLite cannot add a CHECK
// to an existing table, and new kinds must not need a migration).
const (
	EventStatus      = "status"       // detail: {"from","to"}
	EventDone        = "done"         // detail: {"from"}
	EventReopen      = "reopen"       // detail: {"from","to","reason"?}
	EventRunStarted  = "run-started"  // detail: {"harness","log"}
	EventRunFinished = "run-finished" // detail: {"status","waiting_on"?}
	EventVerify      = "verify"       // detail: VerifyReceipt
	EventReview      = "review"       // detail: {"verdict","reason"?}
)

// Event actors: who caused the event.
const (
	ActorAutoRun    = "auto-run"   // the task's own autonomous run (FLOW_AUTO_RUN == slug)
	ActorDriver     = "driver"     // the session bound to the task's parent
	ActorSession    = "session"    // some other bound harness session
	ActorHuman      = "human"      // no bound session: a person at a shell
	ActorSupervisor = "supervisor" // flow's own auto-run supervisor
)

// TaskEvent is one row of task_events. Detail is raw JSON (or empty).
type TaskEvent struct {
	ID        int64           `json:"id"`
	TaskSlug  string          `json:"task"`
	At        string          `json:"at"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	SessionID string          `json:"session_id,omitempty"`
	Detail    json.RawMessage `json:"detail,omitempty"`
}

// InsertTaskEvent appends one event. detail is marshalled to JSON; nil
// stores NULL. At defaults to now.
func InsertTaskEvent(db *sql.DB, slug, kind, actor, sessionID string, detail any) error {
	if slug == "" || kind == "" || actor == "" {
		return fmt.Errorf("task event needs slug, kind and actor")
	}
	var d any
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("marshal event detail: %w", err)
		}
		d = string(b)
	}
	_, err := db.Exec(
		`INSERT INTO task_events (task_slug, at, kind, actor, session_id, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		slug, NowISO(), kind, actor, NullIfEmpty(sessionID), d,
	)
	return err
}

// TaskEventFilter narrows ListTaskEvents. Zero values mean "any".
type TaskEventFilter struct {
	TaskSlug string // one task
	Since    string // RFC3339: events at or after
	AfterID  int64  // events with id > AfterID (cursor for incremental readers)
	Kinds    []string
	Limit    int
}

// ListTaskEvents returns events oldest first.
func ListTaskEvents(db *sql.DB, f TaskEventFilter) ([]*TaskEvent, error) {
	var where []string
	var args []any
	if f.TaskSlug != "" {
		where = append(where, "task_slug = ?")
		args = append(args, f.TaskSlug)
	}
	if f.Since != "" {
		where = append(where, "at >= ?")
		args = append(args, f.Since)
	}
	if f.AfterID > 0 {
		where = append(where, "id > ?")
		args = append(args, f.AfterID)
	}
	if len(f.Kinds) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Kinds)), ",")
		where = append(where, "kind IN ("+ph+")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	q := `SELECT id, task_slug, at, kind, actor, session_id, detail FROM task_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskEvent
	for rows.Next() {
		var e TaskEvent
		var sid, detail sql.NullString
		if err := rows.Scan(&e.ID, &e.TaskSlug, &e.At, &e.Kind, &e.Actor, &sid, &detail); err != nil {
			return nil, err
		}
		e.SessionID = sid.String
		if detail.Valid && detail.String != "" {
			e.Detail = json.RawMessage(detail.String)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
