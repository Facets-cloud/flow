package app

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// Message-bus hook handlers — aggressive: every LLM turn touches the
// bus, but strictly through CLI/context primitives (no UI):
//
//	SessionStart      busSessionStartContext(): pending-message notice +
//	                  encouragement to park a Monitor / background Bash
//	                  on `flow inbox pop --wait`.
//	UserPromptSubmit  busPromptSubmitContext(): the human replying in a
//	                  session ACKS that session's pending human-directed
//	                  messages (wait recorded + injected) and drains the
//	                  bound task's inbox.
//	PostToolUse       cmdHookPostToolUse(): drains the inbox mid-turn on
//	                  every tool call; nudges pop --wait when mail
//	                  arrived with no live listener.
//	Stop              cmdHookStop(): nudges the agent to `flow post` a
//	                  one-liner when the task has watchers and no recent
//	                  post.
//
// Hook code must never fail loud — errors degrade to silence.

// busSessionStartContext returns extra SessionStart context (may be "").
func busSessionStartContext() string {
	db, err := openBusDB()
	if err != nil {
		return ""
	}
	defer db.Close()
	s := currentBusSender()

	var b strings.Builder
	if rows, _ := flowdb.PendingForHuman(db, busSelf); len(rows) > 0 {
		now := time.Now()
		b.WriteString(fmt.Sprintf(" flow-bus: %d pending message(s) for the user:", len(rows)))
		for _, m := range rows {
			b.WriteString(fmt.Sprintf(" [%s %s ago, %s: %s]", m.ID, busAge(m.CreatedAt, now), busFrom(m), m.Body))
		}
		b.WriteString(" Surface these to the user at the start of your reply (they consume via `flow inbox pop`).")
	}
	if s.TaskSlug != "" {
		b.WriteString(drainTaskInbox(db, s.TaskSlug))
		b.WriteString(" flow-bus: to be woken the moment a message or watched post arrives " +
			"(instead of waiting for your next tool call), park your Monitor tool — or a " +
			"background Bash command — on `flow inbox pop --wait` now, and re-arm it after " +
			"each wake. One listener per session.")
	}
	return b.String()
}

// busPromptSubmitContext returns extra UserPromptSubmit context (may be "").
func busPromptSubmitContext() string {
	db, err := openBusDB()
	if err != nil {
		return ""
	}
	defer db.Close()
	s := currentBusSender()

	var b strings.Builder
	if s.SessionID != "" {
		acked, _ := flowdb.AckHumanMessagesFromSession(db, s.SessionID, "prompt")
		for _, m := range acked {
			b.WriteString(fmt.Sprintf(
				" flow-bus: the user has just returned — your message [%s] (%q) was answered after %s. "+
					"Factor the elapsed time in; re-verify stale state after long waits.",
				m.ID, m.Body, fmtBusWait(m.WaitedS)))
		}
	}
	if s.TaskSlug != "" {
		b.WriteString(drainTaskInbox(db, s.TaskSlug))
	}
	return b.String()
}

// drainTaskInbox delivers pending messages for a task and renders them
// as hook context ("" if none).
func drainTaskInbox(db *sql.DB, slug string) string {
	rows, err := flowdb.PendingForTask(db, slug)
	if err != nil || len(rows) == 0 {
		return ""
	}
	ids := make([]string, len(rows))
	for i, m := range rows {
		ids[i] = m.ID
	}
	_ = flowdb.MarkDelivered(db, ids)
	now := time.Now()
	var b strings.Builder
	for _, m := range rows {
		verb := "message"
		if m.Kind == "post" {
			verb = "post (FYI broadcast)"
		}
		b.WriteString(fmt.Sprintf(" flow-bus %s from %s (%s ago): %s.", verb, busFrom(m), busAge(m.CreatedAt, now), m.Body))
	}
	return b.String()
}

// listenerAlive reports whether a live `flow inbox pop --wait` is
// consuming for this identity (recent heartbeat + pid still running).
func listenerAlive(db *sql.DB, identity string) bool {
	pid, hb, err := flowdb.GetBusListener(db, identity)
	if err != nil || pid == 0 || hb == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, hb)
	if err != nil || time.Since(t) > 30*time.Second {
		return false
	}
	return processAlive(pid) // shared liveness probe from auto.go
}

// cmdHookPostToolUse fires on every tool call: drain the inbox, nudge a
// listener into place.
func cmdHookPostToolUse(args []string) int {
	fs := flagSet("hook post-tool-use")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		return 0
	}
	defer db.Close()
	s := currentBusSender()
	if s.TaskSlug == "" {
		return 0
	}
	ctx := drainTaskInbox(db, s.TaskSlug)
	if ctx == "" {
		return 0
	}
	if !listenerAlive(db, s.identity()) {
		ctx += " flow-bus nudge: no listener is running — future messages wait for your next " +
			"tool call. Park your Monitor tool or a background Bash command on " +
			"`flow inbox pop --wait` to be woken instantly."
	}
	return emitHookContext("PostToolUse", strings.TrimSpace(ctx))
}

// cmdHookStop fires when a turn ends: nudge the agent to broadcast a
// one-liner if this task has watchers and hasn't posted recently.
func cmdHookStop(args []string) int {
	fs := flagSet("hook stop")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		return 0
	}
	defer db.Close()
	slug := lookupBoundTaskSlug()
	if slug == "" {
		return 0
	}
	t, err := flowdb.GetTask(db, slug)
	if err != nil || t == nil {
		return 0
	}
	watchers, err := flowdb.WatchersOf(db, taskTopics(t))
	if err != nil || len(watchers) == 0 {
		return 0 // no audience — a nudge would be noise
	}
	last, _ := flowdb.LastPostAt(db, slug)
	if last != "" {
		if ts, err := time.Parse(time.RFC3339, last); err == nil && time.Since(ts) < 30*time.Minute {
			return 0 // posted recently — stay quiet
		}
	}
	// Nudge cooldown: without it, every turn end re-nudges while the
	// last post stays old — including turns where the agent declined
	// the previous nudge, which wake-loops the session. Ask at most
	// once per 30m per task; the agent's judgment stands in between.
	if nudged, _ := flowdb.LastNudgeAt(db, slug); nudged != "" {
		if ts, err := time.Parse(time.RFC3339, nudged); err == nil && time.Since(ts) < 30*time.Minute {
			return 0
		}
	}
	_ = flowdb.RecordNudge(db, slug)
	msg := fmt.Sprintf(
		"flow-bus: %d watcher(s) follow this task and your last post is %s. If this turn "+
			"completed meaningful work, broadcast a one-liner now: flow post \"<what changed>\". "+
			"Skip if nothing notable happened.",
		len(watchers), lastPostDesc(last))
	return emitHookContext("Stop", msg)
}

func lastPostDesc(last string) string {
	if last == "" {
		return "(none yet)"
	}
	return busAge(last, time.Now()) + " old"
}
