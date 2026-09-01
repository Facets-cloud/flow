package flowdb

import (
	"database/sql"
	"fmt"
	"time"
)

// Message bus data layer — flow's session↔human and session↔session
// messaging. CLI-only by design: flow stores, addresses, schedules
// escalation, and measures waits; it draws no attention itself. Users
// script notification UX on top of `flow inbox due` / `flow inbox`.
//
// Two kinds share one table and one delivery path (fan-out on write):
//
//   - kind=message: directed. To a human (to_task_slug NULL) it stays
//     pending until acked, with a backoff schedule exposed via DueBus-
//     Messages for user notifier scripts. To a task it is delivered
//     into that session's context (hooks or `flow inbox pop`).
//   - kind=post: broadcast one-liner, materialized at post time as one
//     row per current watcher. Never escalates.
//
// Addresses: "<assignee>" is a human, "<assignee>/<task-slug>" is the
// session bound to that task. self = the local user.

// busDDL is idempotent and applied on every OpenDB, after migrations.
const busDDL = `
CREATE TABLE IF NOT EXISTS bus_messages (
    id                 TEXT PRIMARY KEY,
    created_at         TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('message','post')),
    from_assignee      TEXT NOT NULL,
    from_task_slug     TEXT,
    sender_session_id  TEXT,
    to_assignee        TEXT NOT NULL,
    to_task_slug       TEXT,
    body               TEXT NOT NULL,
    urgent             INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','acked')),
    attempts           INTEGER NOT NULL DEFAULT 0,
    next_notify_at     TEXT,
    delivered_at       TEXT,
    acked_at           TEXT,
    waited_s           REAL,
    acked_by           TEXT
);
CREATE INDEX IF NOT EXISTS idx_bus_messages_inbox
    ON bus_messages(to_assignee, to_task_slug, status);
CREATE INDEX IF NOT EXISTS idx_bus_messages_sender
    ON bus_messages(sender_session_id, status);

CREATE TABLE IF NOT EXISTS bus_listeners (
    identity   TEXT PRIMARY KEY,
    pid        INTEGER NOT NULL,
    heartbeat  TEXT NOT NULL,
    started_at TEXT NOT NULL
);

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
`

// BusMessage mirrors one bus_messages row.
type BusMessage struct {
	ID              string
	CreatedAt       string
	Kind            string
	FromAssignee    string
	FromTaskSlug    string
	SenderSessionID string
	ToAssignee      string
	ToTaskSlug      string
	Body            string
	Urgent          bool
	Status          string
	Attempts        int
	NextNotifyAt    string
	WaitedS         float64
}

const busMsgCols = `id, created_at, kind, from_assignee, COALESCE(from_task_slug,''),
    COALESCE(sender_session_id,''), to_assignee, COALESCE(to_task_slug,''), body,
    urgent, status, attempts, COALESCE(next_notify_at,''),
    COALESCE(waited_s,0)`

func queryBusMsgs(db *sql.DB, where string, args ...any) ([]*BusMessage, error) {
	rows, err := db.Query(
		`SELECT `+busMsgCols+` FROM bus_messages WHERE `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BusMessage
	for rows.Next() {
		m := &BusMessage{}
		var urgent int
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Kind, &m.FromAssignee, &m.FromTaskSlug,
			&m.SenderSessionID, &m.ToAssignee, &m.ToTaskSlug, &m.Body,
			&urgent, &m.Status, &m.Attempts, &m.NextNotifyAt, &m.WaitedS); err != nil {
			return nil, err
		}
		m.Urgent = urgent == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// InsertBusMessage inserts one message row. next_notify_at should be
// set only for kind=message to a human (its escalation schedule).
func InsertBusMessage(db *sql.DB, m *BusMessage) error {
	urgent := 0
	if m.Urgent {
		urgent = 1
	}
	_, err := db.Exec(`INSERT INTO bus_messages
        (id, created_at, kind, from_assignee, from_task_slug, sender_session_id,
         to_assignee, to_task_slug, body, urgent, status, attempts, next_notify_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,'pending',0,?)`,
		m.ID, m.CreatedAt, m.Kind, m.FromAssignee, NullIfEmpty(m.FromTaskSlug),
		NullIfEmpty(m.SenderSessionID), m.ToAssignee, NullIfEmpty(m.ToTaskSlug),
		m.Body, urgent, NullIfEmpty(m.NextNotifyAt))
	return err
}

// PendingForHuman returns pending rows addressed to the human
// `assignee` — directed messages and fanned-out posts.
func PendingForHuman(db *sql.DB, assignee string) ([]*BusMessage, error) {
	return queryBusMsgs(db,
		`status='pending' AND to_assignee=? AND to_task_slug IS NULL`, assignee)
}

// PendingForTask returns undelivered rows addressed to a task's session.
func PendingForTask(db *sql.DB, slug string) ([]*BusMessage, error) {
	return queryBusMsgs(db, `status='pending' AND to_task_slug=?`, slug)
}

// MarkDelivered marks the given message ids delivered.
func MarkDelivered(db *sql.DB, ids []string) error {
	now := NowISO()
	for _, id := range ids {
		if _, err := db.Exec(
			`UPDATE bus_messages SET status='delivered', delivered_at=? WHERE id=? AND status='pending'`,
			now, id); err != nil {
			return err
		}
	}
	return nil
}

// ClaimDelivered atomically transitions one message pending→delivered.
// Returns false when another consumer claimed it first — the caller
// should move on to the next row. This is what makes concurrent `pop`
// consumers of one inbox (e.g. the user's terminal and a monitor task)
// safe: exactly one pops each message.
func ClaimDelivered(db *sql.DB, id string) (bool, error) {
	res, err := db.Exec(
		`UPDATE bus_messages SET status='delivered', delivered_at=? WHERE id=? AND status='pending'`,
		NowISO(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AckHumanMessagesFromSession acks every pending human-directed message
// sent by `sessionID`, recording the wait. Returns the acked rows.
func AckHumanMessagesFromSession(db *sql.DB, sessionID, by string) ([]*BusMessage, error) {
	rows, err := queryBusMsgs(db,
		`kind='message' AND status='pending' AND to_task_slug IS NULL AND sender_session_id=?`, sessionID)
	if err != nil || len(rows) == 0 {
		return rows, err
	}
	acked := rows[:0]
	for _, m := range rows {
		claimed, err := ClaimAcked(db, m, by)
		if err != nil {
			return nil, err
		}
		if claimed {
			acked = append(acked, m)
		}
	}
	return acked, nil
}

// AckMessageByID acks one message by id. Returns the row, or nil when
// it doesn't exist, isn't pending, or was claimed concurrently.
func AckMessageByID(db *sql.DB, id, by string) (*BusMessage, error) {
	rows, err := queryBusMsgs(db, `id=? AND status='pending'`, id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	claimed, err := ClaimAcked(db, rows[0], by)
	if err != nil || !claimed {
		return nil, err
	}
	return rows[0], nil
}

// ClaimAcked atomically transitions one message pending→acked,
// recording the wait. Returns false when another consumer claimed it
// first. Together with ClaimDelivered this makes concurrent consumers
// of one inbox safe: exactly one pops each message.
func ClaimAcked(db *sql.DB, m *BusMessage, by string) (bool, error) {
	m.WaitedS = waitedSeconds(m.CreatedAt, time.Now())
	res, err := db.Exec(
		`UPDATE bus_messages SET status='acked', acked_at=?, waited_s=?, acked_by=? WHERE id=? AND status='pending'`,
		NowISO(), m.WaitedS, by, m.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func waitedSeconds(createdAt string, now time.Time) float64 {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0
	}
	return now.Sub(t).Seconds()
}

// DueBusMessages returns pending human-directed messages whose
// next_notify_at has passed — the primitive user notifier scripts poll
// (`flow inbox due`). Callers bump each returned row.
func DueBusMessages(db *sql.DB, assignee string, now time.Time) ([]*BusMessage, error) {
	return queryBusMsgs(db,
		`kind='message' AND status='pending' AND to_task_slug IS NULL AND to_assignee=?
         AND next_notify_at IS NOT NULL AND next_notify_at <= ?`,
		assignee, now.UTC().Format(time.RFC3339))
}

// BumpNotifyAttempt records one escalation firing: attempts+1 and the
// next backoff deadline (base 1m doubling, capped at 30m).
func BumpNotifyAttempt(db *sql.DB, id string, attempts int, now time.Time) error {
	delay := time.Duration(60*(1<<uint(attempts+1))) * time.Second
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	_, err := db.Exec(`UPDATE bus_messages SET attempts=?, next_notify_at=? WHERE id=?`,
		attempts+1, now.Add(delay).UTC().Format(time.RFC3339), id)
	return err
}

// UpsertBusListener records a live blocking consumer (`flow inbox pop
// --wait`) for an identity.
func UpsertBusListener(db *sql.DB, identity string, pid int) error {
	now := NowISO()
	_, err := db.Exec(`INSERT INTO bus_listeners (identity, pid, heartbeat, started_at)
        VALUES (?,?,?,?)
        ON CONFLICT(identity) DO UPDATE SET
          pid=excluded.pid, heartbeat=excluded.heartbeat`,
		identity, pid, now, now)
	return err
}

// TouchBusListener refreshes a listener heartbeat.
func TouchBusListener(db *sql.DB, identity string, pid int) error {
	_, err := db.Exec(`UPDATE bus_listeners SET heartbeat=? WHERE identity=? AND pid=?`,
		NowISO(), identity, pid)
	return err
}

// RemoveBusListener drops a listener registration (on pop exit).
func RemoveBusListener(db *sql.DB, identity string, pid int) error {
	_, err := db.Exec(`DELETE FROM bus_listeners WHERE identity=? AND pid=?`, identity, pid)
	return err
}

// GetBusListener returns (pid, heartbeat) for an identity, or (0, "").
func GetBusListener(db *sql.DB, identity string) (int, string, error) {
	row := db.QueryRow(`SELECT pid, heartbeat FROM bus_listeners WHERE identity=?`, identity)
	var pid int
	var hb string
	err := row.Scan(&pid, &hb)
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	return pid, hb, nil
}

// AddWatch subscribes `watcher` (address form: "self" for the human,
// "self/<task-slug>" for a session) to `watched` (task slug, project
// slug, or assignee).
func AddWatch(db *sql.DB, watcher, watched string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO bus_watches (watcher, watched, created_at)
        VALUES (?,?,?)`, watcher, watched, NowISO())
	return err
}

// RemoveWatch unsubscribes. Returns whether a row was removed.
func RemoveWatch(db *sql.DB, watcher, watched string) (bool, error) {
	res, err := db.Exec(`DELETE FROM bus_watches WHERE watcher=? AND watched=?`, watcher, watched)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListWatches returns the watch targets for one watcher.
func ListWatches(db *sql.DB, watcher string) ([]string, error) {
	rows, err := db.Query(`SELECT watched FROM bus_watches WHERE watcher=? ORDER BY watched`, watcher)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WatchersOf returns the distinct watcher addresses subscribed to any
// of the given topics (a post's task slug, project slug, assignee).
func WatchersOf(db *sql.DB, topics []string) ([]string, error) {
	if len(topics) == 0 {
		return nil, nil
	}
	q := `SELECT DISTINCT watcher FROM bus_watches WHERE watched IN (?` +
		repeatPlaceholder(len(topics)-1) + `)`
	args := make([]any, len(topics))
	for i, t := range topics {
		args[i] = t
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func repeatPlaceholder(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += ",?"
	}
	return s
}

// GetNudgeState returns when the Stop hook last nudged a task to post
// ("" if never) and how many consecutive nudges have gone unanswered.
// Without a cooldown the nudge would re-fire on every turn end while
// the last post stays old — including turns where the agent just
// declined the previous nudge (a wake-loop). Declined nudges back off
// exponentially; ResetNudges (called on a successful post) starts the
// cycle over.
func GetNudgeState(db *sql.DB, taskSlug string) (nudgedAt string, attempts int, err error) {
	row := db.QueryRow(`SELECT nudged_at, attempts FROM bus_nudges WHERE task_slug=?`, taskSlug)
	err = row.Scan(&nudgedAt, &attempts)
	if err == sql.ErrNoRows {
		return "", 0, nil
	}
	return nudgedAt, attempts, err
}

// RecordNudge stamps the nudge cooldown for a task and increments its
// consecutive-decline counter.
func RecordNudge(db *sql.DB, taskSlug string) error {
	_, err := db.Exec(`INSERT INTO bus_nudges (task_slug, nudged_at, attempts) VALUES (?,?,1)
        ON CONFLICT(task_slug) DO UPDATE SET
          nudged_at=excluded.nudged_at, attempts=bus_nudges.attempts+1`,
		taskSlug, NowISO())
	return err
}

// ResetNudges clears a task's nudge backoff — called when it posts, so
// the next quiet stretch starts again from the base cooldown.
func ResetNudges(db *sql.DB, taskSlug string) error {
	_, err := db.Exec(`DELETE FROM bus_nudges WHERE task_slug=?`, taskSlug)
	return err
}

// LastPostAt returns the created_at of the most recent post authored by
// a task ("" if none). Used by the Stop-hook nudge heuristic.
func LastPostAt(db *sql.DB, fromTaskSlug string) (string, error) {
	row := db.QueryRow(`SELECT COALESCE(MAX(created_at),'') FROM bus_messages
        WHERE kind='post' AND from_task_slug=?`, fromTaskSlug)
	var ts string
	if err := row.Scan(&ts); err != nil {
		return "", err
	}
	return ts, nil
}

// BusStats aggregates wait metrics over acked human-directed messages.
type BusStats struct {
	Acked, Pending, Posts     int
	AvgWait, MedWait, MaxWait float64
}

// GetBusStats computes wait-time stats for messages to `assignee`.
func GetBusStats(db *sql.DB, assignee string) (*BusStats, error) {
	s := &BusStats{}
	row := db.QueryRow(`SELECT COUNT(*), COALESCE(AVG(waited_s),0), COALESCE(MAX(waited_s),0)
        FROM bus_messages WHERE kind='message' AND status='acked' AND to_assignee=? AND to_task_slug IS NULL`,
		assignee)
	if err := row.Scan(&s.Acked, &s.AvgWait, &s.MaxWait); err != nil {
		return nil, err
	}
	row = db.QueryRow(`SELECT COALESCE(waited_s,0) FROM bus_messages
        WHERE kind='message' AND status='acked' AND to_assignee=? AND to_task_slug IS NULL
        ORDER BY waited_s LIMIT 1 OFFSET ?`, assignee, s.Acked/2)
	_ = row.Scan(&s.MedWait) // best-effort; zero rows leaves 0
	row = db.QueryRow(`SELECT COUNT(*) FROM bus_messages
        WHERE kind='message' AND status='pending' AND to_assignee=? AND to_task_slug IS NULL`, assignee)
	if err := row.Scan(&s.Pending); err != nil {
		return nil, err
	}
	row = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE kind='post'`)
	if err := row.Scan(&s.Posts); err != nil {
		return nil, err
	}
	return s, nil
}

// CleanupTaskBus removes a closed task's bus footprint — called from
// `flow done` and `flow archive`. Deleted: PENDING rows addressed TO
// the task (undeliverable — no session will ever pop them; without this
// they'd live forever, since pending rows are exempt from SweepBus),
// its watch subscriptions (as watcher and as watched topic), its nudge
// stamp, and its listener row. Deliberately KEPT: messages the task
// sent to a human that are still pending — closing a task doesn't
// un-ask a question the user hasn't seen; those clear on pop/ack — and
// already-consumed rows, which age out via the normal 90d sweep.
func CleanupTaskBus(db *sql.DB, taskSlug string) error {
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM bus_messages WHERE to_task_slug=? AND status='pending'`, []any{taskSlug}},
		{`DELETE FROM bus_watches WHERE watched=? OR watcher LIKE '%/' || ?`, []any{taskSlug, taskSlug}},
		{`DELETE FROM bus_nudges WHERE task_slug=?`, []any{taskSlug}},
		{`DELETE FROM bus_listeners WHERE identity LIKE '%/' || ?`, []any{taskSlug}},
	}
	for _, s := range stmts {
		if _, err := db.Exec(s.q, s.args...); err != nil {
			return fmt.Errorf("cleanup task bus: %w", err)
		}
	}
	return nil
}

// SweepBus applies retention: delivered/acked rows older than 90 days
// are deleted (pending rows never expire); listeners with heartbeats
// older than an hour are pruned. Cheap enough to run opportunistically
// on any bus write.
func SweepBus(db *sql.DB, now time.Time) error {
	cut := now.AddDate(0, 0, -90).UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`DELETE FROM bus_messages WHERE status != 'pending' AND created_at < ?`, cut); err != nil {
		return fmt.Errorf("sweep messages: %w", err)
	}
	hbCut := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`DELETE FROM bus_listeners WHERE heartbeat < ?`, hbCut); err != nil {
		return fmt.Errorf("sweep listeners: %w", err)
	}
	return nil
}
