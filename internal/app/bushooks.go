package app

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// Message-bus hook handlers — strictly CLI/context primitives, no UI:
//
//	SessionStart      busSessionStartContext(): pending-message notice,
//	                  inbox drain, and the listener discipline — park
//	                  `flow inbox pop --wait` now, re-arm after each
//	                  wake, and watch the tasks you care about.
//	UserPromptSubmit  busPromptSubmitContext(): the human replying in a
//	                  session ACKS that session's pending human-directed
//	                  messages (wait recorded + injected) and drains the
//	                  bound task's inbox (fallback delivery for sessions
//	                  with no listener parked).
//	Stop              cmdHookStop(): nudges the agent to `flow post` a
//	                  one-liner — only when the task has watchers, and
//	                  with exponential backoff on declined nudges
//	                  (30m, 1h, 2h.. capped 4h; a post resets it).
//
// There is deliberately NO PostToolUse hook: per-tool-call delivery cost
// is not worth it when a parked `pop --wait` listener delivers instantly
// and the prompt-submit drain covers listener-less sessions.
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
		b.WriteString(" flow-bus listener discipline: park `flow inbox pop --wait` as a BACKGROUND " +
			"Bash command (or via your Monitor tool) NOW — it blocks until a message or watched " +
			"post arrives, prints it, and exits, which wakes you; RE-ARM it immediately after " +
			"every wake so the next message also reaches you. Also subscribe to what this task " +
			"depends on: `flow watch <task-or-project-slug>` for anything you are waiting on, " +
			"coordinating with, or any task you create from this session.")
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

// nudgeBackoffFor returns the cooldown before the next post-nudge is
// allowed, given how many consecutive nudges went unanswered: 30m, 1h,
// 2h, capped at 4h. A post resets the counter (flowdb.ResetNudges).
func nudgeBackoffFor(attempts int) time.Duration {
	d := 30 * time.Minute
	for i := 1; i < attempts && d < 4*time.Hour; i++ {
		d *= 2
	}
	if d > 4*time.Hour {
		d = 4 * time.Hour
	}
	return d
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
	// Declined-nudge backoff: ask again only after 30m·2^(declines-1),
	// capped at 4h. The agent's judgment stands in between; a post
	// resets the cycle.
	if nudgedAt, attempts, _ := flowdb.GetNudgeState(db, slug); nudgedAt != "" {
		if ts, err := time.Parse(time.RFC3339, nudgedAt); err == nil &&
			time.Since(ts) < nudgeBackoffFor(attempts) {
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
