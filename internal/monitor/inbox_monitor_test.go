package monitor

import (
	"context"
	"errors"
	"os"
	"testing"
)

type recordingWakeTarget struct {
	calls [][]InboxEntry
	err   error
}

func (r *recordingWakeTarget) WakeTask(ctx context.Context, slug string, entries []InboxEntry) error {
	if r.err != nil {
		return r.err
	}
	r.calls = append(r.calls, append([]InboxEntry(nil), entries...))
	return nil
}

func TestInboxMonitorScanOnce_WakesForActionableBatch(t *testing.T) {
	slug := inboxTestSlug(t)
	if err := AppendInboxEvent(slug, InboundEvent{Kind: "pr_review_comment", ChannelType: "github", Text: "fix this"}); err != nil {
		t.Fatalf("AppendInboxEvent() error = %v", err)
	}
	if err := AppendInboxEvent(slug, InboundEvent{Kind: "pr_merged", ChannelType: "github", Text: "merged"}); err != nil {
		t.Fatalf("AppendInboxEvent() error = %v", err)
	}
	target := &recordingWakeTarget{}
	m := NewInboxMonitor(slug, target, InboxMonitorOptions{})

	if err := m.ScanOnce(context.Background()); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}
	if len(target.calls) != 1 {
		t.Fatalf("wake calls = %d, want 1", len(target.calls))
	}
	if len(target.calls[0]) != 1 {
		t.Fatalf("woken entries = %d, want 1", len(target.calls[0]))
	}
	if got := target.calls[0][0].Event.Text; got != "fix this" {
		t.Fatalf("woken text = %q", got)
	}
	offset, err := ReadInboxMonitorCursor(slug)
	if err != nil {
		t.Fatalf("ReadInboxMonitorCursor() error = %v", err)
	}
	if offset == 0 {
		t.Fatalf("cursor = 0, want advanced offset")
	}
}

func TestInboxMonitorScanOnce_DoesNotAdvanceCursorWhenWakeFails(t *testing.T) {
	slug := inboxTestSlug(t)
	if err := AppendInboxEvent(slug, InboundEvent{Kind: "message", ChannelType: "slack", Text: "new reply"}); err != nil {
		t.Fatalf("AppendInboxEvent() error = %v", err)
	}
	target := &recordingWakeTarget{err: errors.New("terminal unavailable")}
	m := NewInboxMonitor(slug, target, InboxMonitorOptions{})

	if err := m.ScanOnce(context.Background()); err == nil {
		t.Fatalf("ScanOnce() error = nil, want error")
	}
	offset, err := ReadInboxMonitorCursor(slug)
	if err != nil {
		t.Fatalf("ReadInboxMonitorCursor() error = %v", err)
	}
	if offset != 0 {
		t.Fatalf("cursor = %d, want 0", offset)
	}
}

func TestInboxMonitorScanOnce_IgnoresAlreadyProcessedBytes(t *testing.T) {
	slug := inboxTestSlug(t)
	if err := AppendInboxEvent(slug, InboundEvent{Kind: "message", ChannelType: "slack", Text: "old reply"}); err != nil {
		t.Fatalf("AppendInboxEvent() error = %v", err)
	}
	info, err := os.Stat(InboxPath(slug))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if err := WriteInboxMonitorCursor(slug, info.Size()); err != nil {
		t.Fatalf("WriteInboxMonitorCursor() error = %v", err)
	}
	if err := AppendInboxEvent(slug, InboundEvent{Kind: "message", ChannelType: "slack", Text: "new reply"}); err != nil {
		t.Fatalf("AppendInboxEvent() error = %v", err)
	}
	target := &recordingWakeTarget{}
	m := NewInboxMonitor(slug, target, InboxMonitorOptions{})

	if err := m.ScanOnce(context.Background()); err != nil {
		t.Fatalf("ScanOnce() error = %v", err)
	}
	if len(target.calls) != 1 || len(target.calls[0]) != 1 {
		t.Fatalf("calls = %+v, want one new entry", target.calls)
	}
	if got := target.calls[0][0].Event.Text; got != "new reply" {
		t.Fatalf("woken text = %q, want new reply", got)
	}
}
