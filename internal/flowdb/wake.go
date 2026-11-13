package flowdb

import (
	"context"
	"database/sql"
	"time"
)

// Harness wake state (see internal/app/wake.go for the delivery side).
//
// Two independent records, so a swallowed push can never hide mail:
//
//	bus_surfaced     HOOK record ("shown in this session's context").
//	(bus.go)         Written only by paths whose output is known to land in
//	                 context: PreToolUse, the Stop check, the SessionStart /
//	                 UserPromptSubmit pending notices, a delivered wake
//	                 prompt, and `flow inbox wait`. Hooks read only this.
//	bus_wake_nudged  WAKE record ("a push was sent for this row"). Written
//	                 by the sender's push and the asyncRewake waiter, used
//	                 only to coalesce pushes (one per batch) and to bound
//	                 the waiter's single re-nudge. A Claude socket push or
//	                 a `codex queue` can be swallowed silently, so this
//	                 record never decides whether the hooks announce mail.
//
// The one cross-read: the WAITER skips rows the hooks already showed (the
// session provably knows; a nudge would be a wasted turn). The sender's
// push reads only its own record.
//
// bus_session_state tracks whether the task's session is mid-turn (busy)
// or waiting for input (idle), written by flow's own hooks:
// UserPromptSubmit/PreToolUse → busy (+activity), Stop → idle. A busy
// session is left to its hooks; only an idle one is pushed a notice.
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

// applyWakeSchema creates the wake tables. On the first open after an
// upgrade from a release where bus_surfaced was one claim shared by wake
// and hooks (alpha.38), a pending row's mark may be a push that never
// arrived: free every pending row's mark so the hooks announce it again.
// Worst case is one harmless duplicate PreToolUse line. Keyed on the new
// table's absence, so it runs once.
func applyWakeSchema(db *sql.DB) error {
	exists := func(q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}) (bool, error) {
		var n int
		err := q.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bus_wake_nudged'`).Scan(&n)
		return n > 0, err
	}
	// Fast path (every open after the first): nothing to do, no write lock.
	if ok, err := exists(db); err != nil || ok {
		if err == nil {
			_, err = db.Exec(wakeDDL)
		}
		return err
	}
	// One IMMEDIATE transaction on one connection: the table's existence is
	// the "migrated" marker, so it never commits without the cleanup, and
	// concurrent first opens serialize on the write lock (busy_timeout)
	// instead of failing a deferred read→write upgrade.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	commit := false
	defer func() {
		if !commit {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	ok, err := exists(conn)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, wakeDDL); err != nil {
		return err
	}
	if !ok {
		if _, err := conn.ExecContext(ctx, `DELETE FROM bus_surfaced
            WHERE message_id IN (SELECT id FROM bus_messages WHERE status='pending')`); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	commit = true
	return nil
}

// Session states recorded by flow's hooks.
const (
	SessionBusy = "busy"
	SessionIdle = "idle"
)

// AfterInsert, when set, runs after every committed bus_messages insert
// (InsertBusMessage, and the reply AnswerHumanMessage stores). The app
// wires it to the harness wake; it must never block for long or fail the
// insert — it has no error return on purpose.
var AfterInsert func(db *sql.DB, m *BusMessage)

func afterInsert(db *sql.DB, m *BusMessage) {
	if AfterInsert != nil && m != nil {
		AfterInsert(db, m)
	}
}

// ClaimUnsurfaced atomically claims (in the HOOK record) every pending row
// addressed to slug's session that no hook-side path has shown yet, and
// returns the rows THIS caller won, oldest first. Only callers whose
// output reliably reaches the session's context may use it.
func ClaimUnsurfaced(db *sql.DB, slug string) ([]*BusMessage, error) {
	cands, err := PendingUnsurfacedForTask(db, slug)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	var won []*BusMessage
	for _, m := range cands {
		res, err := db.Exec(`INSERT OR IGNORE INTO bus_surfaced (task_slug, message_id) VALUES (?,?)`, slug, m.ID)
		if err != nil {
			return won, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			won = append(won, m)
		}
	}
	return won, nil
}

// pendingUnnudged lists pending rows for slug with no wake record,
// optionally excluding rows the hooks already showed.
func pendingUnnudged(db *sql.DB, slug string, skipHookSurfaced bool) ([]*BusMessage, error) {
	where := `status='pending' AND to_task_slug=?
         AND id NOT IN (SELECT message_id FROM bus_wake_nudged WHERE task_slug=?)`
	args := []any{slug, slug}
	if skipHookSurfaced {
		where += ` AND id NOT IN (SELECT message_id FROM bus_surfaced WHERE task_slug=?)`
		args = append(args, slug)
	}
	return queryBusMsgs(db, where, args...)
}

// nudgedAtLayout is fixed-width so that comparing nudged_at as text (the
// re-nudge cutoff) matches time order. RFC3339Nano trims trailing zeros,
// which would sort "...:00Z" after "...:00.5Z".
const nudgedAtLayout = "2006-01-02T15:04:05.000000000Z07:00"

func nudgedAtStamp(t time.Time) string { return t.UTC().Format(nudgedAtLayout) }

// ClaimWakeNudge atomically records a push for every pending row of slug
// not yet nudged (restricted to rows created before cutoff unless it is
// zero) and returns the rows THIS caller won. skipHookSurfaced is the
// waiter's form: rows the hooks already showed are left alone. now stamps
// nudged_at (the re-nudge clock).
func ClaimWakeNudge(db *sql.DB, slug string, cutoff time.Time, skipHookSurfaced bool, now time.Time) ([]*BusMessage, error) {
	cands, err := pendingUnnudged(db, slug, skipHookSurfaced)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	ts := nudgedAtStamp(now)
	var won []*BusMessage
	for _, m := range cands {
		if !cutoff.IsZero() {
			if c, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil && !c.Before(cutoff) {
				continue
			}
		}
		res, err := db.Exec(`INSERT OR IGNORE INTO bus_wake_nudged (task_slug, message_id, nudged_at) VALUES (?,?,?)`,
			slug, m.ID, ts)
		if err != nil {
			return won, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			won = append(won, m)
		}
	}
	return won, nil
}

// ClaimWakeRenudge claims the single re-nudge of rows that were pushed
// before nudgedBefore, are still unread, and that no hook has shown — the
// signature of a swallowed push. Each row is re-nudged at most once.
func ClaimWakeRenudge(db *sql.DB, slug string, nudgedBefore time.Time) ([]*BusMessage, error) {
	cands, err := queryBusMsgs(db, `status='pending' AND to_task_slug=?
         AND id NOT IN (SELECT message_id FROM bus_surfaced WHERE task_slug=?)
         AND id IN (SELECT message_id FROM bus_wake_nudged WHERE task_slug=? AND renudged=0)`,
		slug, slug, slug)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	cut := nudgedAtStamp(nudgedBefore)
	var won []*BusMessage
	for _, m := range cands {
		res, err := db.Exec(`UPDATE bus_wake_nudged SET renudged=1
            WHERE task_slug=? AND message_id=? AND renudged=0 AND nudged_at < ?`, slug, m.ID, cut)
		if err != nil {
			return won, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			won = append(won, m)
		}
	}
	return won, nil
}

// WakeDue is the waiter's cheap read (no claim): is there a row it would
// nudge (first nudge before cutoff) or re-nudge (pushed before
// renudgeBefore), ignoring rows the hooks already showed?
func WakeDue(db *sql.DB, slug string, cutoff, renudgeBefore time.Time) bool {
	ms, err := pendingUnnudged(db, slug, true)
	if err == nil {
		for _, m := range ms {
			if c, err := time.Parse(time.RFC3339, m.CreatedAt); err != nil || c.Before(cutoff) {
				return true
			}
		}
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_wake_nudged w JOIN bus_messages m ON m.id=w.message_id
        WHERE w.task_slug=? AND w.renudged=0 AND w.nudged_at < ? AND m.status='pending'
          AND w.message_id NOT IN (SELECT message_id FROM bus_surfaced WHERE task_slug=?)`,
		slug, nudgedAtStamp(renudgeBefore), slug).Scan(&n)
	return n > 0
}

// ReleaseWakeNudge undoes a wake claim whose push could not be sent, so a
// later push or the waiter can nudge the rows.
func ReleaseWakeNudge(db *sql.DB, slug string, ids []string) error {
	for _, id := range ids {
		if _, err := db.Exec(`DELETE FROM bus_wake_nudged WHERE task_slug=? AND message_id=?`, slug, id); err != nil {
			return err
		}
	}
	return nil
}

// SessionStateRow is a task session's hook-tracked turn state.
type SessionStateRow struct {
	State        string // SessionBusy | SessionIdle | "" (never recorded)
	ChangedAt    time.Time
	LastActivity time.Time
}

// Idle reports whether the session counts as waiting for input at now: it
// is recorded idle, was never recorded, or has been "busy" with no hook
// activity for staleAfter (an Esc-interrupt or a crash may end a turn
// without a Stop hook).
func (s SessionStateRow) Idle(now time.Time, staleAfter time.Duration) bool {
	if s.State != SessionBusy {
		return true
	}
	return now.Sub(s.LastActivity) >= staleAfter
}

// SetSessionState records a turn-state change (and counts as activity).
func SetSessionState(db *sql.DB, slug, state string, now time.Time) error {
	ts := now.UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO bus_session_state (task_slug, state, changed_at, last_activity)
        VALUES (?,?,?,?)
        ON CONFLICT(task_slug) DO UPDATE SET state=excluded.state, changed_at=excluded.changed_at,
            last_activity=excluded.last_activity`, slug, state, ts, ts)
	return err
}

// TouchSessionActivity bumps last_activity (a tool is about to run) and
// marks the session busy: a tool call only happens inside a turn.
func TouchSessionActivity(db *sql.DB, slug string, now time.Time) error {
	ts := now.UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO bus_session_state (task_slug, state, changed_at, last_activity)
        VALUES (?,'busy',?,?)
        ON CONFLICT(task_slug) DO UPDATE SET last_activity=excluded.last_activity,
            changed_at=CASE WHEN state='busy' THEN changed_at ELSE excluded.changed_at END,
            state='busy'`, slug, ts, ts)
	return err
}

// GetSessionState reads slug's turn state (zero value when never recorded).
func GetSessionState(db *sql.DB, slug string) (SessionStateRow, error) {
	var st, changed, act string
	err := db.QueryRow(`SELECT state, changed_at, last_activity FROM bus_session_state WHERE task_slug=?`, slug).
		Scan(&st, &changed, &act)
	if err == sql.ErrNoRows {
		return SessionStateRow{}, nil
	}
	if err != nil {
		return SessionStateRow{}, err
	}
	r := SessionStateRow{State: st}
	r.ChangedAt, _ = time.Parse(time.RFC3339Nano, changed)
	r.LastActivity, _ = time.Parse(time.RFC3339Nano, act)
	return r, nil
}
