package flowdb

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

type MonitorEvent struct {
	ID          string
	Source      string
	Kind        string
	SourceID    string
	Title       string
	Body        sql.NullString
	URL         sql.NullString
	Severity    string
	Status      string
	FirstSeenAt string
	LastSeenAt  string
	RawJSON     sql.NullString
}

type MonitorNotification struct {
	ID        string
	EventID   string
	Title     string
	Body      sql.NullString
	Level     string
	Status    string
	CreatedAt string
}

type AutomationRule struct {
	ID        string
	Source    string
	Kind      string
	Mode      string
	CreatedAt string
	UpdatedAt string
}

type MonitorEventInput struct {
	Source   string
	Kind     string
	SourceID string
	Title    string
	Body     string
	URL      string
	Severity string
	RawJSON  string
}

var defaultAutomationRules = []AutomationRule{
	{Source: "github", Kind: "review_requested", Mode: "approval"},
	{Source: "github", Kind: "ci_failed", Mode: "auto_agent"},
	{Source: "github", Kind: "assigned_issue", Mode: "notify"},
	{Source: "github", Kind: "notification", Mode: "notify"},
	{Source: "slack", Kind: "mention", Mode: "approval"},
	{Source: "slack", Kind: "dm", Mode: "approval"},
	{Source: "slack", Kind: "channel_message", Mode: "notify"},
	{Source: "calendar", Kind: "meeting_ended", Mode: "notify"},
	{Source: "avoma", Kind: "transcript_ready", Mode: "summarize"},
}

func EnsureDefaultAutomationRules(db *sql.DB) error {
	now := NowISO()
	for _, rule := range defaultAutomationRules {
		id := AutomationRuleID(rule.Source, rule.Kind)
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO automation_rules (id, source, kind, mode, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, rule.Source, rule.Kind, rule.Mode, now, now,
		); err != nil {
			return fmt.Errorf("insert default automation rule %s/%s: %w", rule.Source, rule.Kind, err)
		}
	}
	return nil
}

func AutomationRuleID(source, kind string) string {
	return normalizeMonitorPart(source) + "." + normalizeMonitorPart(kind)
}

func MonitorEventID(source, sourceID string) string {
	sum := sha1.Sum([]byte(source + ":" + sourceID))
	return normalizeMonitorPart(source) + "-" + hex.EncodeToString(sum[:])[:16]
}

func UpsertMonitorEvent(db *sql.DB, input MonitorEventInput) (*MonitorEvent, bool, error) {
	source := normalizeMonitorPart(input.Source)
	kind := normalizeMonitorPart(input.Kind)
	sourceID := strings.TrimSpace(input.SourceID)
	title := strings.TrimSpace(input.Title)
	if source == "" || kind == "" || sourceID == "" || title == "" {
		return nil, false, fmt.Errorf("monitor event requires source, kind, source_id, and title")
	}
	severity := normalizeMonitorPart(input.Severity)
	if severity != "low" && severity != "medium" && severity != "high" {
		severity = "medium"
	}
	now := NowISO()
	id := MonitorEventID(source, sourceID)
	res, err := db.Exec(
		`INSERT INTO monitor_events (
			id, source, kind, source_id, title, body, url, severity, status,
			first_seen_at, last_seen_at, raw_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'new', ?, ?, ?)
		ON CONFLICT(source, source_id) DO UPDATE SET
			kind = excluded.kind,
			title = excluded.title,
			body = excluded.body,
			url = excluded.url,
			severity = excluded.severity,
			last_seen_at = excluded.last_seen_at,
			raw_json = excluded.raw_json`,
		id, source, kind, sourceID, title, NullString(input.Body), NullString(input.URL),
		severity, now, now, NullString(input.RawJSON),
	)
	if err != nil {
		return nil, false, fmt.Errorf("upsert monitor event: %w", err)
	}
	affected, _ := res.RowsAffected()
	event, err := GetMonitorEvent(db, id)
	return event, affected > 0 && event != nil && event.FirstSeenAt == now, err
}

func GetMonitorEvent(db *sql.DB, id string) (*MonitorEvent, error) {
	row := db.QueryRow(
		`SELECT id, source, kind, source_id, title, body, url, severity, status, first_seen_at, last_seen_at, raw_json
		 FROM monitor_events WHERE id = ?`,
		id,
	)
	return scanMonitorEvent(row)
}

func ListMonitorEvents(db *sql.DB, limit int) ([]MonitorEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(
		`SELECT id, source, kind, source_id, title, body, url, severity, status, first_seen_at, last_seen_at, raw_json
		 FROM monitor_events
		 ORDER BY last_seen_at DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list monitor events: %w", err)
	}
	defer rows.Close()
	var out []MonitorEvent
	for rows.Next() {
		event, err := scanMonitorEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *event)
	}
	return out, rows.Err()
}

func ListMonitorNotifications(db *sql.DB, limit int) ([]MonitorNotification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(
		`SELECT id, event_id, title, body, level, status, created_at
		 FROM monitor_notifications
		 WHERE status != 'dismissed'
		 ORDER BY CASE status WHEN 'unread' THEN 0 WHEN 'read' THEN 1 ELSE 2 END, created_at DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list monitor notifications: %w", err)
	}
	defer rows.Close()
	var out []MonitorNotification
	for rows.Next() {
		n, err := scanMonitorNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func CreateNotificationForEvent(db *sql.DB, event MonitorEvent, level string) error {
	level = normalizeMonitorPart(level)
	if level == "" {
		level = "info"
	}
	id := "notif-" + event.ID
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO monitor_notifications (id, event_id, title, body, level, status, created_at)
		 VALUES (?, ?, ?, ?, ?, 'unread', ?)`,
		id, event.ID, event.Title, event.Body, level, NowISO(),
	); err != nil {
		return fmt.Errorf("create monitor notification: %w", err)
	}
	_, err := db.Exec(`UPDATE monitor_events SET status = 'notified' WHERE id = ? AND status = 'new'`, event.ID)
	return err
}

func UpdateNotificationStatus(db *sql.DB, id, status string) error {
	status = normalizeMonitorPart(status)
	switch status {
	case "unread", "read", "dismissed", "actioned":
	default:
		return fmt.Errorf("invalid notification status %q", status)
	}
	_, err := db.Exec(`UPDATE monitor_notifications SET status = ? WHERE id = ?`, status, id)
	return err
}

func UpdateMonitorEventStatus(db *sql.DB, id, status string) error {
	status = normalizeMonitorPart(status)
	switch status {
	case "new", "notified", "approved", "ignored", "started", "done":
	default:
		return fmt.Errorf("invalid monitor event status %q", status)
	}
	_, err := db.Exec(`UPDATE monitor_events SET status = ? WHERE id = ?`, status, id)
	return err
}

func ListAutomationRules(db *sql.DB) ([]AutomationRule, error) {
	if err := EnsureDefaultAutomationRules(db); err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, source, kind, mode, created_at, updated_at
		 FROM automation_rules
		 ORDER BY source, kind`,
	)
	if err != nil {
		return nil, fmt.Errorf("list automation rules: %w", err)
	}
	defer rows.Close()
	var out []AutomationRule
	for rows.Next() {
		r, err := scanAutomationRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func AutomationModeFor(db *sql.DB, source, kind string) (string, error) {
	if err := EnsureDefaultAutomationRules(db); err != nil {
		return "", err
	}
	var mode string
	err := db.QueryRow(
		`SELECT mode FROM automation_rules WHERE source = ? AND kind = ?`,
		normalizeMonitorPart(source), normalizeMonitorPart(kind),
	).Scan(&mode)
	if err == sql.ErrNoRows {
		return "notify", nil
	}
	return mode, err
}

func SetAutomationRuleMode(db *sql.DB, source, kind, mode string) error {
	source = normalizeMonitorPart(source)
	kind = normalizeMonitorPart(kind)
	mode = normalizeMonitorPart(mode)
	switch mode {
	case "off", "log", "notify", "approval", "auto_task", "auto_agent", "auto_agent_draft_only", "summarize":
	default:
		return fmt.Errorf("invalid automation mode %q", mode)
	}
	now := NowISO()
	_, err := db.Exec(
		`INSERT INTO automation_rules (id, source, kind, mode, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source, kind) DO UPDATE SET mode = excluded.mode, updated_at = excluded.updated_at`,
		AutomationRuleID(source, kind), source, kind, mode, now, now,
	)
	return err
}

func scanMonitorEvent(row interface{ Scan(dest ...any) error }) (*MonitorEvent, error) {
	var e MonitorEvent
	if err := row.Scan(&e.ID, &e.Source, &e.Kind, &e.SourceID, &e.Title, &e.Body, &e.URL, &e.Severity, &e.Status, &e.FirstSeenAt, &e.LastSeenAt, &e.RawJSON); err != nil {
		return nil, err
	}
	return &e, nil
}

func scanMonitorNotification(row interface{ Scan(dest ...any) error }) (*MonitorNotification, error) {
	var n MonitorNotification
	if err := row.Scan(&n.ID, &n.EventID, &n.Title, &n.Body, &n.Level, &n.Status, &n.CreatedAt); err != nil {
		return nil, err
	}
	return &n, nil
}

func scanAutomationRule(row interface{ Scan(dest ...any) error }) (*AutomationRule, error) {
	var r AutomationRule
	if err := row.Scan(&r.ID, &r.Source, &r.Kind, &r.Mode, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func normalizeMonitorPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

func NullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	return sql.NullString{String: s, Valid: s != ""}
}
