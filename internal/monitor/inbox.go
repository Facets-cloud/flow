package monitor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// InboxEventMeta stores derived routing metadata for a normalized source
// event. Older inbox.jsonl rows may omit it; readers treat an empty meta as
// legacy data and can reclassify from Event when needed.
type InboxEventMeta struct {
	Source     string `json:"source,omitempty"`
	Actionable bool   `json:"actionable,omitempty"`
}

// InboxEntry is the on-disk form of one source event appended to a task's
// inbox.jsonl. The schema is deliberately small and stable: a live agent
// session reads these entries to understand what happened in a monitored
// source after the task was created.
//
// EnqueuedAt is RFC3339 wall-clock at append time, distinct from the
// Slack event's TS (which uses Slack's own seconds.microseconds format
// and is global only within a channel).
type InboxEntry struct {
	EnqueuedAt string         `json:"enqueued_at"`
	Event      InboundEvent   `json:"event"`
	Meta       InboxEventMeta `json:"meta,omitempty"`
}

// ClassifyInboxEvent derives the source and actionability used by the
// same-session monitor. Source listeners still append all useful lifecycle
// events; only actionable entries wake a live task terminal.
func ClassifyInboxEvent(ev InboundEvent) InboxEventMeta {
	source := "unknown"
	if ev.ChannelType == "github" || strings.HasPrefix(ev.Kind, "pr_") || strings.HasPrefix(ev.Kind, "issue_") {
		source = "github"
	} else if ev.ChannelType == "slack" || ev.Kind == "message" || ev.Kind == "app_mention" || ev.Kind == "reaction_added" {
		source = "slack"
	} else if ev.ChannelType != "" {
		source = ev.ChannelType
	}

	actionable := false
	switch source {
	case "github":
		switch ev.Kind {
		case "pr_review_comment", "pr_review_changes_requested", "pr_head_updated",
			"pr_comment", "issue_comment":
			actionable = true
		}
	case "slack":
		switch ev.Kind {
		case "message", "app_mention":
			actionable = true
		}
	}

	return InboxEventMeta{Source: source, Actionable: actionable}
}

// TaskDir returns the absolute path to a task's directory under
// $FLOW_ROOT (or ~/.flow as the default). Returns "" when neither
// $FLOW_ROOT nor $HOME is resolvable, which short-circuits any
// subsequent file ops gracefully.
func TaskDir(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return ""
	}
	if root := strings.TrimSpace(os.Getenv("FLOW_ROOT")); root != "" {
		return filepath.Join(root, "tasks", slug)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".flow", "tasks", slug)
}

// InboxPath returns the inbox.jsonl path for the given task slug.
func InboxPath(slug string) string {
	dir := TaskDir(slug)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "inbox.jsonl")
}

// CursorPath returns the inbox.cursor path for the given task slug. The
// cursor file is a single line containing the latest Slack ts processed
// for the thread — used by the listener's bootstrap catch-up to know
// where to resume from after a restart.
func CursorPath(slug string) string {
	dir := TaskDir(slug)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "inbox.cursor")
}

// MonitorCursorPath returns the inbox.monitor.cursor path for the given task
// slug. Unlike inbox.cursor, this stores a byte offset for the same-session
// inbox monitor and must not be used by Slack catch-up code.
func MonitorCursorPath(slug string) string {
	dir := TaskDir(slug)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "inbox.monitor.cursor")
}

// AppendInboxEvent appends one event line to the task's inbox.jsonl,
// creating the file if needed. Caller is expected to have created the
// task directory (flow add task creates it). If the directory is missing
// this returns the underlying error rather than swallowing — silent
// drops would lose Slack events the user expects to see.
//
// The append is one syscall via O_APPEND so concurrent appends from
// multiple Slack events don't interleave their bytes (POSIX guarantees
// atomic writes under PIPE_BUF size, and a single JSON line of a Slack
// event is well under 4KB).
func AppendInboxEvent(slug string, ev InboundEvent) error {
	path := InboxPath(slug)
	if path == "" {
		return errors.New("monitor: cannot resolve inbox path (no FLOW_ROOT or HOME)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("monitor: mkdir task dir: %w", err)
	}
	entry := InboxEntry{
		EnqueuedAt: time.Now().UTC().Format(time.RFC3339),
		Event:      ev,
		Meta:       ClassifyInboxEvent(ev),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("monitor: marshal inbox entry: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("monitor: open inbox.jsonl: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("monitor: append inbox.jsonl: %w", err)
	}
	return nil
}

// ReadInboxEntries returns all entries currently in the task's
// inbox.jsonl, in append order. Missing file → empty slice + nil error.
// Malformed lines are skipped with no error; the spawned session's
// bootstrap doesn't need to choke on a single garbled line.
func ReadInboxEntries(slug string) ([]InboxEntry, error) {
	path := InboxPath(slug)
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []InboxEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry InboxEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		out = append(out, entry)
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// ReadInboxMonitorCursor returns the byte offset processed by the
// same-session monitor, or 0 when no cursor file exists.
func ReadInboxMonitorCursor(slug string) (int64, error) {
	path := MonitorCursorPath(slug)
	if path == "" {
		return 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return 0, nil
	}
	offset, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("monitor: parse inbox monitor cursor: %w", err)
	}
	if offset < 0 {
		return 0, fmt.Errorf("monitor: negative inbox monitor cursor %d", offset)
	}
	return offset, nil
}

// WriteInboxMonitorCursor atomically stores the byte offset processed by the
// same-session monitor.
func WriteInboxMonitorCursor(slug string, offset int64) error {
	if offset < 0 {
		return fmt.Errorf("monitor: negative inbox monitor cursor %d", offset)
	}
	path := MonitorCursorPath(slug)
	if path == "" {
		return errors.New("monitor: cannot resolve inbox monitor cursor path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("monitor: mkdir task dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(offset, 10)+"\n"), 0o644); err != nil {
		return fmt.Errorf("monitor: write inbox monitor cursor.tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("monitor: rename inbox monitor cursor: %w", err)
	}
	return nil
}

// SeedInboxMonitorCursorToEnd initializes a task's monitor cursor to the
// current end of its inbox.jsonl, but ONLY when no cursor exists yet. This
// makes a freshly-started monitor watch for events that arrive after it starts
// rather than replaying the entire historical backlog — without it, restoring
// monitors on boot would wake or respawn the agent for every old event. A task
// whose cursor already exists resumes from its saved position untouched.
func SeedInboxMonitorCursorToEnd(slug string) error {
	cursorPath := MonitorCursorPath(slug)
	if cursorPath == "" {
		return nil
	}
	if _, err := os.Stat(cursorPath); err == nil {
		return nil // cursor exists — resume from where we left off
	} else if !os.IsNotExist(err) {
		return err
	}
	size := int64(0)
	if info, err := os.Stat(InboxPath(slug)); err == nil {
		size = info.Size()
	} else if !os.IsNotExist(err) {
		return err
	}
	return WriteInboxMonitorCursor(slug, size)
}

// ReadInboxCursor returns the latest Slack ts processed for the task's
// thread, or "" when no cursor file exists. Used by the listener's
// catch-up sweep on startup to know where to resume.
func ReadInboxCursor(slug string) (string, error) {
	path := CursorPath(slug)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// WriteInboxCursor atomically replaces the cursor file with ts. Uses
// write-to-temp + rename so a crash mid-write leaves the prior cursor
// intact rather than corrupting it.
func WriteInboxCursor(slug, ts string) error {
	path := CursorPath(slug)
	if path == "" {
		return errors.New("monitor: cannot resolve cursor path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("monitor: mkdir task dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(ts)+"\n"), 0o644); err != nil {
		return fmt.Errorf("monitor: write cursor.tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("monitor: rename cursor: %w", err)
	}
	return nil
}
