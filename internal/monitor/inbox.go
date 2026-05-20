package monitor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InboxEntry is the on-disk form of one Slack event appended to a task's
// inbox.jsonl. The schema is deliberately small and stable — a spawned
// Claude session reads these entries on bootstrap to understand "what
// happened in the Slack thread since I was last here."
//
// EnqueuedAt is RFC3339 wall-clock at append time, distinct from the
// Slack event's TS (which uses Slack's own seconds.microseconds format
// and is global only within a channel).
type InboxEntry struct {
	EnqueuedAt string       `json:"enqueued_at"`
	Event      InboundEvent `json:"event"`
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
