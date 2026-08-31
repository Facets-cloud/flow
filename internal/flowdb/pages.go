package flowdb

import (
	"database/sql"
	"fmt"
	"time"
)

// Paging/attention bus data layer. Two primitives share one messages
// table and one delivery path (fan-out on write):
//
//   - kind=page: directed interrupt. To a human (to_task_slug NULL):
//     native notification + exponential backoff until acked. To a task:
//     delivered into that session's context, no escalation.
//   - kind=post: broadcast one-liner. At post time it is materialized as
//     one row per current watcher (a DM to each) — non-interrupting,
//     auto-expiring, never escalates.
//
// Addresses use the skill's grammar: "<assignee>" is a human,
// "<assignee>/<task-slug>" is the session bound to that task. self =
// the local user (tasks with NULL assignee).

// pagesDDL is idempotent and applied on every OpenDB, after migrations.
const pagesDDL = `
CREATE TABLE IF NOT EXISTS page_messages (
    id                 TEXT PRIMARY KEY,
    created_at         TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('page','post')),
    from_assignee      TEXT NOT NULL,
    from_task_slug     TEXT,
    sender_session_id  TEXT,
    to_assignee        TEXT NOT NULL,
    to_task_slug       TEXT,
    body               TEXT NOT NULL,
    re_slug            TEXT,
    urgent             INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','acked')),
    attempts           INTEGER NOT NULL DEFAULT 0,
    next_notify_at     TEXT,
    delivered_at       TEXT,
    acked_at           TEXT,
    waited_s           REAL,
    acked_by           TEXT
);
CREATE INDEX IF NOT EXISTS idx_page_messages_inbox
    ON page_messages(to_assignee, to_task_slug, status);
CREATE INDEX IF NOT EXISTS idx_page_messages_sender
    ON page_messages(sender_session_id, status);

CREATE TABLE IF NOT EXISTS page_endpoints (
    task_slug         TEXT PRIMARY KEY,
    session_id        TEXT,
    tty               TEXT,
    iterm_uuid        TEXT,
    listen_pid        INTEGER,
    listen_heartbeat  TEXT,
    last_seen         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS page_watches (
    watcher     TEXT NOT NULL,
    watched     TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (watcher, watched)
);
`

// PageMessage mirrors one page_messages row.
type PageMessage struct {
	ID              string
	CreatedAt       string
	Kind            string
	FromAssignee    string
	FromTaskSlug    string
	SenderSessionID string
	ToAssignee      string
	ToTaskSlug      string
	Body            string
	ReSlug          string
	Urgent          bool
	Status          string
	Attempts        int
	NextNotifyAt    string
	WaitedS         float64
}

// PageEndpoint records how to reach a task's session right now.
type PageEndpoint struct {
	TaskSlug        string
	SessionID       string
	TTY             string
	ItermUUID       string
	ListenPID       int
	ListenHeartbeat string
	LastSeen        string
}

const pageMsgCols = `id, created_at, kind, from_assignee, COALESCE(from_task_slug,''),
    COALESCE(sender_session_id,''), to_assignee, COALESCE(to_task_slug,''), body,
    COALESCE(re_slug,''), urgent, status, attempts, COALESCE(next_notify_at,''),
    COALESCE(waited_s,0)`

func scanPageMsg(rows *sql.Rows) (*PageMessage, error) {
	m := &PageMessage{}
	var urgent int
	if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Kind, &m.FromAssignee, &m.FromTaskSlug,
		&m.SenderSessionID, &m.ToAssignee, &m.ToTaskSlug, &m.Body, &m.ReSlug,
		&urgent, &m.Status, &m.Attempts, &m.NextNotifyAt, &m.WaitedS); err != nil {
		return nil, err
	}
	m.Urgent = urgent == 1
	return m, nil
}

func queryPageMsgs(db *sql.DB, where string, args ...any) ([]*PageMessage, error) {
	rows, err := db.Query(
		`SELECT `+pageMsgCols+` FROM page_messages WHERE `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PageMessage
	for rows.Next() {
		m, err := scanPageMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// InsertPageMessage inserts one message row. next_notify_at should be
// set only for kind=page to a human.
func InsertPageMessage(db *sql.DB, m *PageMessage) error {
	urgent := 0
	if m.Urgent {
		urgent = 1
	}
	_, err := db.Exec(`INSERT INTO page_messages
        (id, created_at, kind, from_assignee, from_task_slug, sender_session_id,
         to_assignee, to_task_slug, body, re_slug, urgent, status, attempts, next_notify_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,'pending',0,?)`,
		m.ID, m.CreatedAt, m.Kind, m.FromAssignee, NullIfEmpty(m.FromTaskSlug),
		NullIfEmpty(m.SenderSessionID), m.ToAssignee, NullIfEmpty(m.ToTaskSlug),
		m.Body, NullIfEmpty(m.ReSlug), urgent, NullIfEmpty(m.NextNotifyAt))
	return err
}

// PendingHumanPages returns pending kind=page rows addressed to the
// human `assignee` (to_task_slug NULL).
func PendingHumanPages(db *sql.DB, assignee string) ([]*PageMessage, error) {
	return queryPageMsgs(db,
		`kind='page' AND status='pending' AND to_assignee=? AND to_task_slug IS NULL`, assignee)
}

// PendingForTask returns undelivered rows (both kinds) addressed to a
// task's session.
func PendingForTask(db *sql.DB, slug string) ([]*PageMessage, error) {
	return queryPageMsgs(db, `status='pending' AND to_task_slug=?`, slug)
}

// PendingPostsForHuman returns undelivered posts fanned out to a human
// watcher (kind=post, to_task_slug NULL).
func PendingPostsForHuman(db *sql.DB, assignee string) ([]*PageMessage, error) {
	return queryPageMsgs(db,
		`kind='post' AND status='pending' AND to_assignee=? AND to_task_slug IS NULL`, assignee)
}

// MarkDelivered marks the given message ids delivered.
func MarkDelivered(db *sql.DB, ids []string) error {
	now := NowISO()
	for _, id := range ids {
		if _, err := db.Exec(
			`UPDATE page_messages SET status='delivered', delivered_at=? WHERE id=? AND status='pending'`,
			now, id); err != nil {
			return err
		}
	}
	return nil
}

// AckHumanPagesFromSession acks every pending human-directed page sent
// by `sessionID`, recording the wait. Returns the acked rows.
func AckHumanPagesFromSession(db *sql.DB, sessionID, by string) ([]*PageMessage, error) {
	rows, err := queryPageMsgs(db,
		`kind='page' AND status='pending' AND to_task_slug IS NULL AND sender_session_id=?`, sessionID)
	if err != nil || len(rows) == 0 {
		return rows, err
	}
	now := time.Now()
	for _, m := range rows {
		m.WaitedS = waitedSeconds(m.CreatedAt, now)
		if _, err := db.Exec(
			`UPDATE page_messages SET status='acked', acked_at=?, waited_s=?, acked_by=? WHERE id=?`,
			NowISO(), m.WaitedS, by, m.ID); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// AckPageByID acks one page by id. Returns the row or nil.
func AckPageByID(db *sql.DB, id, by string) (*PageMessage, error) {
	rows, err := queryPageMsgs(db, `id=? AND status='pending'`, id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	m := rows[0]
	m.WaitedS = waitedSeconds(m.CreatedAt, time.Now())
	if _, err := db.Exec(
		`UPDATE page_messages SET status='acked', acked_at=?, waited_s=?, acked_by=? WHERE id=?`,
		NowISO(), m.WaitedS, by, m.ID); err != nil {
		return nil, err
	}
	return m, nil
}

func waitedSeconds(createdAt string, now time.Time) float64 {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0
	}
	return now.Sub(t).Seconds()
}

// DueHumanPages returns pending human pages whose next_notify_at has
// passed — candidates for backoff re-notification.
func DueHumanPages(db *sql.DB, now time.Time) ([]*PageMessage, error) {
	return queryPageMsgs(db,
		`kind='page' AND status='pending' AND to_task_slug IS NULL
         AND next_notify_at IS NOT NULL AND next_notify_at <= ?`,
		now.UTC().Format(time.RFC3339))
}

// BumpNotifyAttempt records one re-notification: attempts+1 and the
// next backoff deadline (base 1m doubling, capped at 30m).
func BumpNotifyAttempt(db *sql.DB, id string, attempts int, now time.Time) error {
	delay := time.Duration(60*(1<<uint(attempts+1))) * time.Second
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	_, err := db.Exec(`UPDATE page_messages SET attempts=?, next_notify_at=? WHERE id=?`,
		attempts+1, now.Add(delay).UTC().Format(time.RFC3339), id)
	return err
}

// UpsertPageEndpoint records/refreshes how to reach a task's session.
// Zero-valued fields are preserved from the existing row.
func UpsertPageEndpoint(db *sql.DB, e *PageEndpoint) error {
	_, err := db.Exec(`INSERT INTO page_endpoints
        (task_slug, session_id, tty, iterm_uuid, listen_pid, listen_heartbeat, last_seen)
        VALUES (?,?,?,?,?,?,?)
        ON CONFLICT(task_slug) DO UPDATE SET
          session_id       = COALESCE(NULLIF(excluded.session_id,''), page_endpoints.session_id),
          tty              = COALESCE(NULLIF(excluded.tty,''), page_endpoints.tty),
          iterm_uuid       = COALESCE(NULLIF(excluded.iterm_uuid,''), page_endpoints.iterm_uuid),
          listen_pid       = CASE WHEN excluded.listen_pid != 0 THEN excluded.listen_pid ELSE page_endpoints.listen_pid END,
          listen_heartbeat = COALESCE(NULLIF(excluded.listen_heartbeat,''), page_endpoints.listen_heartbeat),
          last_seen        = excluded.last_seen`,
		e.TaskSlug, e.SessionID, e.TTY, e.ItermUUID, e.ListenPID, e.ListenHeartbeat, NowISO())
	return err
}

// GetPageEndpoint returns the endpoint row for a task, or nil.
func GetPageEndpoint(db *sql.DB, slug string) (*PageEndpoint, error) {
	row := db.QueryRow(`SELECT task_slug, COALESCE(session_id,''), COALESCE(tty,''),
        COALESCE(iterm_uuid,''), COALESCE(listen_pid,0), COALESCE(listen_heartbeat,''), last_seen
        FROM page_endpoints WHERE task_slug=?`, slug)
	e := &PageEndpoint{}
	err := row.Scan(&e.TaskSlug, &e.SessionID, &e.TTY, &e.ItermUUID,
		&e.ListenPID, &e.ListenHeartbeat, &e.LastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// AddWatch subscribes `watcher` (address form: "self" for the human,
// "self/<task-slug>" for a session) to `watched` (task slug, project
// slug, or assignee).
func AddWatch(db *sql.DB, watcher, watched string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO page_watches (watcher, watched, created_at)
        VALUES (?,?,?)`, watcher, watched, NowISO())
	return err
}

// RemoveWatch unsubscribes. Returns whether a row was removed.
func RemoveWatch(db *sql.DB, watcher, watched string) (bool, error) {
	res, err := db.Exec(`DELETE FROM page_watches WHERE watcher=? AND watched=?`, watcher, watched)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListWatches returns the watch targets for one watcher.
func ListWatches(db *sql.DB, watcher string) ([]string, error) {
	rows, err := db.Query(`SELECT watched FROM page_watches WHERE watcher=? ORDER BY watched`, watcher)
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
	q := `SELECT DISTINCT watcher FROM page_watches WHERE watched IN (?` +
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

// LastPostAt returns the created_at of the most recent post authored by
// a task ("" if none). Used by the Stop-hook nudge heuristic.
func LastPostAt(db *sql.DB, fromTaskSlug string) (string, error) {
	row := db.QueryRow(`SELECT COALESCE(MAX(created_at),'') FROM page_messages
        WHERE kind='post' AND from_task_slug=?`, fromTaskSlug)
	var ts string
	if err := row.Scan(&ts); err != nil {
		return "", err
	}
	return ts, nil
}

// PageStats aggregates wait metrics over acked human pages.
type PageStats struct {
	Acked, Pending, Posts    int
	AvgWait, MedWait, MaxWait float64
}

// GetPageStats computes wait-time stats for pages to `assignee`.
func GetPageStats(db *sql.DB, assignee string) (*PageStats, error) {
	s := &PageStats{}
	row := db.QueryRow(`SELECT COUNT(*), COALESCE(AVG(waited_s),0), COALESCE(MAX(waited_s),0)
        FROM page_messages WHERE kind='page' AND status='acked' AND to_assignee=? AND to_task_slug IS NULL`,
		assignee)
	if err := row.Scan(&s.Acked, &s.AvgWait, &s.MaxWait); err != nil {
		return nil, err
	}
	row = db.QueryRow(`SELECT COALESCE(waited_s,0) FROM page_messages
        WHERE kind='page' AND status='acked' AND to_assignee=? AND to_task_slug IS NULL
        ORDER BY waited_s LIMIT 1 OFFSET ?`, assignee, s.Acked/2)
	_ = row.Scan(&s.MedWait) // best-effort; zero rows leaves 0
	row = db.QueryRow(`SELECT COUNT(*) FROM page_messages
        WHERE kind='page' AND status='pending' AND to_assignee=? AND to_task_slug IS NULL`, assignee)
	if err := row.Scan(&s.Pending); err != nil {
		return nil, err
	}
	row = db.QueryRow(`SELECT COUNT(*) FROM page_messages WHERE kind='post'`)
	if err := row.Scan(&s.Posts); err != nil {
		return nil, err
	}
	return s, nil
}

// SweepPages applies retention: delivered/acked rows older than 90 days
// are deleted (pending rows never expire); stale endpoints (unseen 30d)
// are pruned. Cheap enough to run opportunistically on any page write.
func SweepPages(db *sql.DB, now time.Time) error {
	cut := now.AddDate(0, 0, -90).UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`DELETE FROM page_messages WHERE status != 'pending' AND created_at < ?`, cut); err != nil {
		return fmt.Errorf("sweep messages: %w", err)
	}
	epCut := now.AddDate(0, 0, -30).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`DELETE FROM page_endpoints WHERE last_seen < ?`, epCut); err != nil {
		return fmt.Errorf("sweep endpoints: %w", err)
	}
	return nil
}
