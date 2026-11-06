package flowdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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
//   - kind=broadcast: one-liner fan-out, materialized at broadcast time
//     as one row per current watcher. Never escalates.
//
// Addresses: "<assignee>" is a human, "<assignee>/<task-slug>" is the
// session bound to that task. self = the local user.

// busDDL is idempotent and applied on every OpenDB, after migrations.
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
	WaitedS         float64
	ReplyTo         string // parent message id (`--reply-to`), "" if none
	SeenAt          string // when a HUMAN saw it (human mail only), "" if unseen
	// ConsumedByIdentity / ConsumedBySession record who last claimed,
	// acked or saw the row (bus address + harness session id).
	ConsumedByIdentity string
	ConsumedBySession  string
	// Options are the answer choices of a human ask (`--options "A|B"`),
	// stored as a JSON array; nil when none.
	Options []string
	// FYI marks human mail that is stored and listed but never pinged.
	FYI bool
	// Ping is the send-time decision to notify the human through the
	// installed connectors (see InsertBusMessage).
	Ping bool
	// NotifiedAt is when at least one connector accepted the ping.
	NotifiedAt string
	// AnsweredAt is when a connector recorded the human's answer.
	AnsweredAt string
}

// Consumer identifies who changes a row's state: By is the verb recorded
// in acked_by ('pop', 'prompt', 'read'); Identity is the consumer's bus
// address (user, user/<slug>); Session its harness session id ("" from a
// plain terminal). Every claim/ack/seen records Identity + Session so a
// swallowed message can always be traced to its consumer.
type Consumer struct {
	By, Identity, Session string
}

// BusUser is the reserved assignee for the local human. "Human mail" is
// every row to BusUser with no task — directed messages AND broadcasts.
const BusUser = "user"

// humanUnreadWhere is THE unread predicate for a human queue: the local
// human's mail is unread until a human has seen it (seen_at), whatever
// its delivery status; another assignee's queue (drained by a transport
// worker via --as) is unread while still pending. Shared by the listing,
// the counts, and the mail label so they never drift.
func humanUnreadWhere(assignee string) string {
	if assignee == BusUser {
		return `to_assignee=? AND to_task_slug IS NULL AND seen_at IS NULL`
	}
	return `to_assignee=? AND to_task_slug IS NULL AND status='pending'`
}

// IsUnread applies the same predicate to one row in Go.
func (m *BusMessage) IsUnread() bool {
	if m.ToTaskSlug == "" && m.ToAssignee == BusUser {
		return m.SeenAt == ""
	}
	return m.Status == "pending"
}

const busMsgCols = `id, created_at, kind, from_assignee, COALESCE(from_task_slug,''),
    COALESCE(sender_session_id,''), to_assignee, COALESCE(to_task_slug,''), body,
    urgent, status, COALESCE(waited_s,0), COALESCE(reply_to,''), COALESCE(seen_at,''),
    COALESCE(consumed_by_identity,''), COALESCE(consumed_by_session,''),
    COALESCE(options,''), fyi, ping, COALESCE(notified_at,''),
    COALESCE(answered_at,'')`

func queryBusMsgs(db *sql.DB, where string, args ...any) ([]*BusMessage, error) {
	// rowid tiebreak: created_at is second-granularity RFC3339, so
	// same-second rows need insertion order to keep pop oldest-first.
	rows, err := db.Query(
		`SELECT `+busMsgCols+` FROM bus_messages WHERE `+where+` ORDER BY created_at, rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BusMessage
	for rows.Next() {
		m := &BusMessage{}
		var urgent, fyi, ping int
		var options string
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Kind, &m.FromAssignee, &m.FromTaskSlug,
			&m.SenderSessionID, &m.ToAssignee, &m.ToTaskSlug, &m.Body,
			&urgent, &m.Status, &m.WaitedS, &m.ReplyTo, &m.SeenAt,
			&m.ConsumedByIdentity, &m.ConsumedBySession, &options, &fyi, &ping, &m.NotifiedAt, &m.AnsweredAt); err != nil {
			return nil, err
		}
		m.Urgent = urgent == 1
		m.FYI = fyi == 1
		m.Ping = ping == 1
		if options != "" {
			_ = json.Unmarshal([]byte(options), &m.Options)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// InsertBusMessage inserts one message row (always status=pending) and
// sets m.Ping to the send-time ping decision, made in the same statement
// so two concurrent asks from one task cannot both ping:
//
//   - only a directed message to the local human (no task) can ping;
//     broadcasts, task mail, other assignees and --fyi never do;
//   - --urgent always pings, and so does a sender with no task;
//   - otherwise one ping per sender task until the human has seen it:
//     no ping while that task has a pinged ask still unseen.
func InsertBusMessage(db *sql.DB, m *BusMessage) error {
	return insertBusMessage(db, m)
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func insertBusMessage(db queryRower, m *BusMessage) error {
	options, err := optionsJSON(m.Options)
	if err != nil {
		return err
	}
	candidate := m.Kind == "message" && m.ToTaskSlug == "" && m.ToAssignee == BusUser && !m.FYI
	forced := candidate && (m.Urgent || m.FromTaskSlug == "")
	var ping int
	err = db.QueryRow(`INSERT INTO bus_messages
        (id, created_at, kind, from_assignee, from_task_slug, sender_session_id,
         to_assignee, to_task_slug, body, urgent, status, reply_to, options, fyi, ping)
        VALUES (?,?,?,?,?,?,?,?,?,?,'pending',?,?,?,
          CASE WHEN ?=0 THEN 0 WHEN ?=1 THEN 1
               WHEN EXISTS (SELECT 1 FROM bus_messages
                            WHERE from_task_slug=? AND kind='message' AND to_task_slug IS NULL
                              AND to_assignee=? AND ping=1 AND seen_at IS NULL) THEN 0
               ELSE 1 END)
        RETURNING ping`,
		m.ID, m.CreatedAt, m.Kind, m.FromAssignee, NullIfEmpty(m.FromTaskSlug),
		NullIfEmpty(m.SenderSessionID), m.ToAssignee, NullIfEmpty(m.ToTaskSlug),
		m.Body, boolInt(m.Urgent), NullIfEmpty(m.ReplyTo), options, boolInt(m.FYI),
		boolInt(candidate), boolInt(forced), m.FromTaskSlug, BusUser).Scan(&ping)
	m.Ping = ping == 1
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// optionsJSON encodes ask options for the options column (NULL when none).
func optionsJSON(opts []string) (any, error) {
	if len(opts) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// MarkNotified stamps notified_at once a connector accepted the ping.
func MarkNotified(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE bus_messages SET notified_at=COALESCE(notified_at, ?) WHERE id=?`, NowISO(), id)
	return err
}

// AnswerHumanMessage records a connector-relayed human answer to message
// id, atomically: the row becomes seen, acked and answered (consumer c),
// and reply — when non-nil — is inserted in the same transaction. newly
// is false (and nothing is written) when the row was already answered,
// which makes a repeated reply idempotent.
func AnswerHumanMessage(db *sql.DB, id string, c Consumer, reply *BusMessage) (newly bool, err error) {
	m, err := GetBusMessageByID(db, id)
	if err != nil || m == nil {
		return false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	now := NowISO()
	res, err := tx.Exec(`UPDATE bus_messages SET answered_at=?, seen_at=COALESCE(seen_at, ?),
            status='acked', acked_at=COALESCE(acked_at, ?),
            waited_s=CASE WHEN status='acked' THEN waited_s ELSE ? END,
            acked_by=CASE WHEN status='acked' THEN acked_by ELSE ? END,
            consumed_by_identity=?, consumed_by_session=?
        WHERE id=? AND answered_at IS NULL`,
		now, now, now, waitedSeconds(m.CreatedAt, time.Now()), c.By,
		NullIfEmpty(c.Identity), NullIfEmpty(c.Session), id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		err = tx.Rollback()
		return false, err
	}
	if reply != nil {
		if err = insertBusMessage(tx, reply); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// PendingForHuman returns pending rows addressed to the human
// `assignee` — directed messages and fanned-out broadcasts.
func PendingForHuman(db *sql.DB, assignee string) ([]*BusMessage, error) {
	return queryBusMsgs(db,
		`status='pending' AND to_assignee=? AND to_task_slug IS NULL`, assignee)
}

// PendingForTask returns undelivered rows addressed to a task's session.
func PendingForTask(db *sql.DB, slug string) ([]*BusMessage, error) {
	return queryBusMsgs(db, `status='pending' AND to_task_slug=?`, slug)
}

// UnreadForHuman returns the human queue's unread rows (see
// humanUnreadWhere) — what `flow inbox --as <assignee>` lists.
func UnreadForHuman(db *sql.DB, assignee string) ([]*BusMessage, error) {
	return queryBusMsgs(db, humanUnreadWhere(assignee), assignee)
}

// HumanUnreadCounts splits a human queue's unread rows into asks
// (directed messages) and FYI (broadcasts and `--fyi` messages).
func HumanUnreadCounts(db *sql.DB, assignee string) (asks, fyi int, err error) {
	err = db.QueryRow(`SELECT
        COALESCE(SUM(CASE WHEN kind='message' AND fyi=0 THEN 1 ELSE 0 END),0),
        COALESCE(SUM(CASE WHEN kind='broadcast' OR fyi=1 THEN 1 ELSE 0 END),0)
        FROM bus_messages WHERE `+humanUnreadWhere(assignee), assignee).Scan(&asks, &fyi)
	return
}

// MarkSeen stamps seen_at on rows that are still unseen and returns the
// ids it newly stamped. A human seeing human mail is what makes it read.
func MarkSeen(db *sql.DB, ids []string, c Consumer) ([]string, error) {
	var out []string
	now := NowISO()
	for _, id := range ids {
		res, err := db.Exec(`UPDATE bus_messages SET seen_at=?, consumed_by_identity=?, consumed_by_session=?
            WHERE id=? AND seen_at IS NULL`, now, NullIfEmpty(c.Identity), NullIfEmpty(c.Session), id)
		if err != nil {
			return out, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out = append(out, id)
		}
	}
	return out, nil
}

// AllForHuman returns EVERY row still in the table (any status — unread,
// read, delivered) addressed to the human `assignee`. Backs `flow inbox
// --all`: the mail-model view over the whole retained queue, not just
// the unread head that PendingForHuman returns.
func AllForHuman(db *sql.DB, assignee string) ([]*BusMessage, error) {
	return queryBusMsgs(db, `to_assignee=? AND to_task_slug IS NULL`, assignee)
}

// AllForTask is the task-inbox equivalent of AllForHuman.
func AllForTask(db *sql.DB, slug string) ([]*BusMessage, error) {
	return queryBusMsgs(db, `to_task_slug=?`, slug)
}

// GetBusMessageByID returns one row by id regardless of status (works
// for already-consumed messages too), or nil when it doesn't exist.
func GetBusMessageByID(db *sql.DB, id string) (*BusMessage, error) {
	rows, err := queryBusMsgs(db, `id=?`, id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// ReadMessageByID marks one message read by id and returns (row, newly).
// It fixes pop's oldest-first limitation: any specific message can be
// acked/read directly, out of arrival order. A human-directed message
// that is still unread (pending) OR was handed to a reader (delivered,
// e.g. by `pop --keep-unread`) transitions to acked; a task/broadcast row
// transitions pending→delivered. `newly` is false when the row was
// already read (the caller still gets the row to display) or was claimed
// concurrently.
func ReadMessageByID(db *sql.DB, id string, c Consumer) (m *BusMessage, newly bool, err error) {
	m, err = GetBusMessageByID(db, id)
	if err != nil || m == nil {
		return nil, false, err
	}
	toHuman := m.ToTaskSlug == "" && m.Kind == "message"
	if m.ToTaskSlug == "" && m.ToAssignee == BusUser && m.Kind == "broadcast" {
		// A broadcast to the human is read once seen; no ack semantics.
		ids, err := MarkSeen(db, []string{m.ID}, c)
		if len(ids) > 0 {
			m.SeenAt = NowISO()
		}
		return m, len(ids) > 0, err
	}
	switch {
	case toHuman && (m.Status == "pending" || m.Status == "delivered"):
		claimed, err := claimAckedAnyState(db, m, c)
		return m, claimed, err
	case !toHuman && m.Status == "pending":
		claimed, err := ClaimDelivered(db, m.ID, c)
		if claimed {
			m.Status = "delivered"
		}
		return m, claimed, err
	default:
		return m, false, nil // already read/consumed — display only
	}
}

// claimAckedAnyState acks a human-directed message from pending OR
// delivered (a `--keep-unread` reader left it delivered-but-unanswered).
// ClaimAcked itself only fires on pending, so delivered rows need this
// widened guard. Atomic: exactly one caller wins.
func claimAckedAnyState(db *sql.DB, m *BusMessage, c Consumer) (bool, error) {
	m.WaitedS = waitedSeconds(m.CreatedAt, time.Now())
	res, err := db.Exec(
		`UPDATE bus_messages SET status='acked', acked_at=?, waited_s=?, acked_by=?,
             seen_at=COALESCE(seen_at, ?), consumed_by_identity=?, consumed_by_session=?
         WHERE id=? AND status IN ('pending','delivered')`,
		NowISO(), m.WaitedS, c.By, NowISO(), NullIfEmpty(c.Identity), NullIfEmpty(c.Session), m.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		m.Status = "acked"
	}
	return n > 0, nil
}

// ClaimDelivered atomically transitions one message pending→delivered.
// Returns false when another consumer claimed it first — the caller
// should move on to the next row. This is what makes concurrent `pop`
// consumers of one inbox (e.g. the user's terminal and a monitor task)
// safe: exactly one pops each message.
func ClaimDelivered(db *sql.DB, id string, c Consumer) (bool, error) {
	res, err := db.Exec(
		`UPDATE bus_messages SET status='delivered', delivered_at=?,
             consumed_by_identity=?, consumed_by_session=? WHERE id=? AND status='pending'`,
		NowISO(), NullIfEmpty(c.Identity), NullIfEmpty(c.Session), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AckHumanMessagesFromSession acks every pending message the session
// sent to the human `toAssignee`, recording the wait. Scoped to that
// one queue: a reply from the local user must never ack mail addressed
// to a DIFFERENT assignee (they haven't seen it). Returns acked rows.
func AckHumanMessagesFromSession(db *sql.DB, sessionID, toAssignee string, c Consumer) ([]*BusMessage, error) {
	rows, err := queryBusMsgs(db,
		`kind='message' AND status='pending' AND to_task_slug IS NULL
         AND sender_session_id=? AND to_assignee=?`, sessionID, toAssignee)
	if err != nil || len(rows) == 0 {
		return rows, err
	}
	acked := rows[:0]
	for _, m := range rows {
		claimed, err := ClaimAcked(db, m, c)
		if err != nil {
			return nil, err
		}
		if claimed {
			acked = append(acked, m)
		}
	}
	return acked, nil
}

// ClaimAcked atomically transitions one message pending→acked,
// recording the wait. Returns false when another consumer claimed it
// first. Together with ClaimDelivered this makes concurrent consumers
// of one inbox safe: exactly one pops each message.
func ClaimAcked(db *sql.DB, m *BusMessage, c Consumer) (bool, error) {
	m.WaitedS = waitedSeconds(m.CreatedAt, time.Now())
	res, err := db.Exec(
		`UPDATE bus_messages SET status='acked', acked_at=?, waited_s=?, acked_by=?,
             seen_at=COALESCE(seen_at, ?), consumed_by_identity=?, consumed_by_session=?
         WHERE id=? AND status='pending'`,
		NowISO(), m.WaitedS, c.By, NowISO(), NullIfEmpty(c.Identity), NullIfEmpty(c.Session), m.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		// Reflect the claim on the caller's copy so renderings (pop
		// --json) report the real post-claim state.
		m.Status = "acked"
		m.ConsumedByIdentity, m.ConsumedBySession = c.Identity, c.Session
		if m.SeenAt == "" {
			m.SeenAt = NowISO()
		}
	}
	return n > 0, nil
}

func waitedSeconds(createdAt string, now time.Time) float64 {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0
	}
	return now.Sub(t).Seconds()
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

// LastBroadcastAt returns the created_at of the most recent broadcast
// authored by a task ("" if none). Used by the Stop-hook nudge heuristic.
func LastBroadcastAt(db *sql.DB, fromTaskSlug string) (string, error) {
	row := db.QueryRow(`SELECT COALESCE(MAX(created_at),'') FROM bus_messages
        WHERE kind='broadcast' AND from_task_slug=?`, fromTaskSlug)
	var ts string
	if err := row.Scan(&ts); err != nil {
		return "", err
	}
	return ts, nil
}

// BusStats aggregates wait metrics over acked human-directed messages.
type BusStats struct {
	Acked, Pending, Broadcasts int
	AvgWait, MedWait, MaxWait  float64
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
	asks, _, err := HumanUnreadCounts(db, assignee)
	if err != nil {
		return nil, err
	}
	s.Pending = asks
	row = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE kind='broadcast'`)
	if err := row.Scan(&s.Broadcasts); err != nil {
		return nil, err
	}
	return s, nil
}

// PendingCountForTask returns how many undelivered rows await a task's
// session — the inform-only primitive hooks use (hooks never consume).
func PendingCountForTask(db *sql.DB, slug string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE status='pending' AND to_task_slug=?`, slug).Scan(&n)
	return n, err
}

// PendingCountForHuman is the human-queue equivalent.
func PendingCountForHuman(db *sql.DB, assignee string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE `+humanUnreadWhere(assignee), assignee).Scan(&n)
	return n, err
}

// PendingUnsurfacedForTask returns pending rows of ANY kind (directed
// messages AND broadcasts) addressed to a task's session that the
// PreToolUse delta-gate has not already announced (see MarkSurfaced). It
// backs the pre-tool-call nudge: raw pending counts re-announce the same
// set on every tool call (hooks never consume), so the delta-gate
// suppresses everything but genuinely new mail. Both kinds surface — any
// unread item, directed or broadcast, may bear on the action the agent is
// about to take, and the delta-gate keeps it to a single surface. Rows
// come oldest-first (via queryBusMsgs); the caller re-orders urgent-first.
func PendingUnsurfacedForTask(db *sql.DB, slug string) ([]*BusMessage, error) {
	return queryBusMsgs(db,
		`status='pending' AND to_task_slug=?
         AND id NOT IN (SELECT message_id FROM bus_surfaced WHERE task_slug=?)`,
		slug, slug)
}

// MarkSurfaced records that the PreToolUse delta-gate has announced these
// message ids for the task, so the same pending message is never surfaced
// twice at that hook point. Idempotent; a no-op on an empty id list.
func MarkSurfaced(db *sql.DB, slug string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO bus_surfaced (task_slug, message_id) VALUES (?,?)`,
			slug, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// RecordHumanSurfaced notes that a hook showed these human-mail ids in an
// interactive session, so the NEXT human-typed prompt there can mark them
// seen (TakeHumanSurfaced). Idempotent.
func RecordHumanSurfaced(db *sql.DB, sessionID string, ids []string) error {
	if sessionID == "" || len(ids) == 0 {
		return nil
	}
	now := NowISO()
	for _, id := range ids {
		if _, err := db.Exec(`INSERT OR IGNORE INTO bus_human_surfaced (session_id, message_id, surfaced_at)
            VALUES (?,?,?)`, sessionID, id, now); err != nil {
			return err
		}
	}
	return nil
}

// TakeHumanSurfaced returns and forgets the human-mail ids previously
// surfaced in a session.
func TakeHumanSurfaced(db *sql.DB, sessionID string) ([]string, error) {
	rows, err := db.Query(`SELECT message_id FROM bus_human_surfaced WHERE session_id=? ORDER BY surfaced_at`, sessionID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = db.Exec(`DELETE FROM bus_human_surfaced WHERE session_id=?`, sessionID)
	return ids, err
}

// AddWatch subscribes `watcher` (address form: "user" for the human,
// "user/<task-slug>" for a session) to `watched` (task slug, project
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
// of the given topics (a broadcast's task slug, project slug, assignee).
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

// CleanupTaskBus removes a closed task's bus footprint — called from
// `flow done` and `flow archive`. Deleted: PENDING rows addressed TO
// the task (undeliverable — no session will ever pop them; without this
// they'd live forever, since pending rows are exempt from SweepBus),
// its watch subscriptions (as watcher and as watched topic), and its
// nudge stamp. Deliberately KEPT: messages the task
// sent to a human that are still pending — closing a task doesn't
// un-ask a question the user hasn't seen; those clear on pop/ack — and
// already-consumed rows, which age out via the normal 90d sweep.
func CleanupTaskBus(db *sql.DB, taskSlug string) error {
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM bus_messages WHERE to_task_slug=? AND status='pending'`, []any{taskSlug}},
		{`DELETE FROM bus_watches WHERE watched=?`, []any{taskSlug}},
		{`DELETE FROM bus_nudges WHERE task_slug=?`, []any{taskSlug}},
		{`DELETE FROM bus_surfaced WHERE task_slug=?`, []any{taskSlug}},
	} {
		if _, err := db.Exec(stmt.q, stmt.args...); err != nil {
			return fmt.Errorf("cleanup task bus: %w", err)
		}
	}
	// Watches BY the closed task: match the address suffix exactly in Go
	// — a LIKE pattern would treat '_' or '%' in a slug as wildcards and
	// could delete a sibling task's subscriptions (db_sync vs db-sync).
	rows, err := db.Query(`SELECT DISTINCT watcher FROM bus_watches`)
	if err != nil {
		return fmt.Errorf("cleanup task bus: %w", err)
	}
	var doomed []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			rows.Close()
			return fmt.Errorf("cleanup task bus: %w", err)
		}
		if strings.HasSuffix(w, "/"+taskSlug) {
			doomed = append(doomed, w)
		}
	}
	rows.Close()
	for _, w := range doomed {
		if _, err := db.Exec(`DELETE FROM bus_watches WHERE watcher=?`, w); err != nil {
			return fmt.Errorf("cleanup task bus: %w", err)
		}
	}
	return nil
}

// busKeepRolled is how many rollable rows are retained — a rolling
// window by row count, newest first. Rollable = broadcasts of any status
// (an unread broadcast is an FYI, not a debt) PLUS acked rows of any kind
// PLUS delivered TASK-directed messages (a peer session consumed them).
// Immortal (never rolled) rows are the unanswered questions to a human:
// a human-directed message stays immortal while pending (unread) AND
// while delivered-but-unacked — a reader/relay that forwarded it with
// `pop --keep-unread` leaves it delivered, and that question is still
// outstanding until the human actually answers it. An unanswered
// question must never silently vanish, whoever forwarded it.
const busKeepRolled = 1000

// busRollable is the retention predicate: which rows roll off by count.
// Its inverse is the immortal set (unanswered human questions). Shared
// between the DELETE and its keep-newest subquery so they never drift.
const busRollable = `(kind='broadcast'
    OR status='acked'
    OR (status='delivered' AND to_task_slug IS NOT NULL)
    OR (to_task_slug IS NULL AND seen_at IS NOT NULL))`

// SweepBus applies retention: rollable rows roll by count (the newest
// busKeepRolled are kept, older ones deleted). Cheap enough to run
// opportunistically on any bus write.
func SweepBus(db *sql.DB, now time.Time) error {
	_ = now
	if _, err := db.Exec(`DELETE FROM bus_messages
        WHERE `+busRollable+`
        AND id NOT IN (SELECT id FROM bus_messages
                       WHERE `+busRollable+`
                       ORDER BY created_at DESC, rowid DESC LIMIT ?)`, busKeepRolled); err != nil {
		return fmt.Errorf("sweep messages: %w", err)
	}
	// Prune delta-gate marks for messages that have rolled off, so the
	// bus_surfaced table stays bounded by the live message set.
	if _, err := db.Exec(
		`DELETE FROM bus_surfaced WHERE message_id NOT IN (SELECT id FROM bus_messages)`); err != nil {
		return fmt.Errorf("sweep surfaced: %w", err)
	}
	if _, err := db.Exec(
		`DELETE FROM bus_human_surfaced WHERE message_id NOT IN (SELECT id FROM bus_messages)`); err != nil {
		return fmt.Errorf("sweep human surfaced: %w", err)
	}
	return nil
}

// migrateBusSelfToUser renames the reserved assignee 'self' to 'user'
// in existing rows (the old spelling confused agents into reading
// "message self" as talking to themselves). Idempotent and cheap; runs
// on every open.
func migrateBusSelfToUser(db *sql.DB) error {
	for _, q := range []string{
		`UPDATE bus_messages SET to_assignee='user' WHERE to_assignee='self'`,
		`UPDATE bus_messages SET from_assignee='user' WHERE from_assignee='self'`,
		`UPDATE bus_watches SET watcher='user' WHERE watcher='self'`,
		`UPDATE bus_watches SET watcher='user' || substr(watcher, 5) WHERE watcher LIKE 'self/%'`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
