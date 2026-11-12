package flowdb

import (
	"database/sql"
	"time"
)

// Harness wake state (see internal/app/wake.go for the delivery side).
//
// Two pieces of shared state let every notice path agree:
//
//   - bus_surfaced (bus.go) is the ATOMIC CLAIM. A path that wants to tell
//     a session "you have mail" first INSERT OR IGNOREs each pending row's
//     id; only the rows whose insert it won are its to announce. The sender
//     wake, the Stop-hook check, PreToolUse, SessionStart/UserPromptSubmit
//     and the Claude asyncRewake waiter all claim through here, so a
//     message is announced exactly once whichever path gets there first.
//   - bus_session_state tracks whether the task's session is mid-turn
//     (busy) or waiting for input (idle), written by flow's own hooks:
//     UserPromptSubmit/PreToolUse → busy (+activity), Stop → idle. A busy
//     session is left to its hooks; only an idle one is pushed a notice.
const wakeDDL = `
CREATE TABLE IF NOT EXISTS bus_session_state (
    task_slug      TEXT PRIMARY KEY,
    state          TEXT NOT NULL,
    changed_at     TEXT NOT NULL,
    last_activity  TEXT NOT NULL
);`

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

// ClaimUnsurfaced atomically claims every pending, not-yet-announced row
// addressed to slug's session and returns the rows THIS caller won,
// oldest first. An empty result means there is nothing for the caller to
// announce (no mail, or another path already announced it).
func ClaimUnsurfaced(db *sql.DB, slug string) ([]*BusMessage, error) {
	return claimUnsurfaced(db, slug, time.Time{})
}

// ClaimUnsurfacedOlderThan is ClaimUnsurfaced restricted to rows created
// before cutoff — the fallback waiter's form, which leaves young rows to
// the sender's immediate push.
func ClaimUnsurfacedOlderThan(db *sql.DB, slug string, cutoff time.Time) ([]*BusMessage, error) {
	return claimUnsurfaced(db, slug, cutoff)
}

func claimUnsurfaced(db *sql.DB, slug string, cutoff time.Time) ([]*BusMessage, error) {
	cands, err := PendingUnsurfacedForTask(db, slug)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	var won []*BusMessage
	for _, m := range cands {
		if !cutoff.IsZero() {
			ts, err := time.Parse(time.RFC3339, m.CreatedAt)
			if err == nil && !ts.Before(cutoff) {
				continue
			}
		}
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

// ReleaseSurfaced undoes a claim whose notice could not be delivered, so
// a fallback path (a hook, the waiter) can claim and announce the rows.
func ReleaseSurfaced(db *sql.DB, slug string, ids []string) error {
	for _, id := range ids {
		if _, err := db.Exec(`DELETE FROM bus_surfaced WHERE task_slug=? AND message_id=?`, slug, id); err != nil {
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
