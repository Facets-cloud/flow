package flowdb

import (
	"database/sql"
	"fmt"
)

// taskSessionHistoryDDL records the sessions a task has LEFT: every time
// `flow do --fresh` (optionally with --harness to re-pin) replaces a
// task's session, the old (harness, session_id) lands here so its
// transcript stays findable. The live session stays on tasks.session_id.
const taskSessionHistoryDDL = `
CREATE TABLE IF NOT EXISTS task_session_history (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    task_slug        TEXT NOT NULL,
    harness          TEXT NOT NULL,
    session_id       TEXT NOT NULL,
    session_started  TEXT,
    ended_at         TEXT NOT NULL,
    reason           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_task_session_history_task ON task_session_history(task_slug);`

// PriorSession is one replaced session of a task.
type PriorSession struct {
	Harness        string  `json:"harness"`
	SessionID      string  `json:"session_id"`
	SessionStarted *string `json:"session_started"` // null when unknown
	EndedAt        string  `json:"ended_at"`
	Reason         string  `json:"reason"` // "fresh" | "repin"
}

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// RecordPriorSession appends a replaced session to the task's history.
// harness "" is stored as "claude" (the NULL-harness back-compat rule).
func RecordPriorSession(db execer, taskSlug, harness, sessionID, sessionStarted, reason string) error {
	if sessionID == "" {
		return nil
	}
	if harness == "" {
		harness = "claude"
	}
	if _, err := db.Exec(
		`INSERT INTO task_session_history (task_slug, harness, session_id, session_started, ended_at, reason)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		taskSlug, harness, sessionID, NullIfEmpty(sessionStarted), NowISO(), reason,
	); err != nil {
		return fmt.Errorf("record prior session: %w", err)
	}
	return nil
}

// PriorSessions lists a task's replaced sessions, oldest first. Never nil.
func PriorSessions(db *sql.DB, taskSlug string) ([]PriorSession, error) {
	rows, err := db.Query(
		`SELECT harness, session_id, session_started, ended_at, reason
		 FROM task_session_history WHERE task_slug = ? ORDER BY id`, taskSlug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PriorSession{}
	for rows.Next() {
		var p PriorSession
		var started sql.NullString
		if err := rows.Scan(&p.Harness, &p.SessionID, &started, &p.EndedAt, &p.Reason); err != nil {
			return nil, err
		}
		if started.Valid && started.String != "" {
			s := started.String
			p.SessionStarted = &s
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
