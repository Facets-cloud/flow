package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// WakeTarget receives batches of actionable inbox entries for a task.
type WakeTarget interface {
	WakeTask(ctx context.Context, slug string, entries []InboxEntry) error
}

type InboxMonitorOptions struct {
	PollInterval time.Duration
}

// InboxMonitor scans one task inbox and wakes a target when new actionable
// rows appear after the monitor cursor.
type InboxMonitor struct {
	slug         string
	target       WakeTarget
	pollInterval time.Duration
}

func NewInboxMonitor(slug string, target WakeTarget, opts InboxMonitorOptions) *InboxMonitor {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &InboxMonitor{slug: slug, target: target, pollInterval: interval}
}

func (m *InboxMonitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		if err := m.ScanOnce(ctx); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *InboxMonitor) ScanOnce(ctx context.Context) error {
	offset, err := ReadInboxMonitorCursor(m.slug)
	if err != nil {
		return err
	}

	f, err := os.Open(InboxPath(m.slug))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	var actionable []InboxEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry InboxEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Meta.Source == "" {
			entry.Meta = ClassifyInboxEvent(entry.Event)
		}
		if entry.Meta.Actionable {
			actionable = append(actionable, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	newOffset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if len(actionable) > 0 {
		if err := m.target.WakeTask(ctx, m.slug, actionable); err != nil {
			return err
		}
	}
	return WriteInboxMonitorCursor(m.slug, newOffset)
}
