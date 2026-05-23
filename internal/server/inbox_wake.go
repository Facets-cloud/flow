package server

import (
	"context"
	"fmt"
	"strings"

	"flow/internal/monitor"
)

type inboxWakeTarget struct {
	terminals *terminalHub
}

func (w inboxWakeTarget) WakeTask(ctx context.Context, slug string, entries []monitor.InboxEntry) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if w.terminals == nil {
		return fmt.Errorf("terminal hub unavailable")
	}
	return w.terminals.wakeTask(slug, formatInboxWakePrompt(slug, entries))
}

func formatInboxWakePrompt(slug string, entries []monitor.InboxEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Flow task %s has %d new actionable inbox event(s).\n", slug, len(entries))
	b.WriteString("Read the new task inbox entries from inbox.jsonl, inspect the referenced source context, and continue the task in this same session.\n")
	for i, entry := range entries {
		if i >= 5 {
			fmt.Fprintf(&b, "- plus %d more event(s)\n", len(entries)-i)
			break
		}
		meta := entry.Meta
		if meta.Source == "" {
			meta = monitor.ClassifyInboxEvent(entry.Event)
		}
		fmt.Fprintf(&b, "- %s %s", meta.Source, entry.Event.Kind)
		if entry.Event.URL != "" {
			fmt.Fprintf(&b, " %s", entry.Event.URL)
		}
		if entry.Event.Text != "" {
			fmt.Fprintf(&b, ": %s", oneLine(entry.Event.Text, 240))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}
