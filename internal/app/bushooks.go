package app

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// Message-bus hook handlers — strictly CLI/context primitives, no UI,
// and NEVER consuming: hooks only INFORM that mail is pending; the sole
// consumption paths are `flow inbox pop` and the agent's armed Monitor
// loop. (Consuming in a hook would mark rows delivered while the
// harness is free to drop the hook's output — silent mail loss.)
//
//	SessionStart      busSessionStartContext(): pending-count notices +
//	                  the listener discipline (arm a persistent Monitor
//	                  loop; watch what you depend on).
//	UserPromptSubmit  busPromptSubmitContext(): the human replying in a
//	                  session ACKS that session's own outbound
//	                  human-directed messages (that is the answering
//	                  mechanism, not inbox consumption) + a pending
//	                  count notice for the task's inbox.
//	PreToolUse        busPreToolUseContext(): delta-gated surface of any
//	                  just-arrived unread item (directed message OR
//	                  broadcast) right before a tool runs, so a mid-turn
//	                  "stop, don't do X" reaches the agent before the
//	                  action it would cancel. Announced once per item
//	                  (bus_surfaced high-water mark), so the same pending
//	                  set never re-nudges across a turn's many tool calls.
//	Stop              cmdHookStop(): broadcast nudge only — watchers
//	                  exist, no broadcast in 30m, declined-nudge
//	                  backoff elapsed. Inbox mail is deliberately NOT
//	                  handled at Stop: an armed Monitor picks it up
//	                  within seconds, and a listener-less session is
//	                  informed at its next prompt.
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
	b.WriteString(humanPendingNotice(db))
	if s.TaskSlug != "" {
		b.WriteString(pendingTaskNotice(db, s.TaskSlug))
		b.WriteString(" flow-bus listener discipline: arm ONE persistent Monitor NOW — the " +
			"loop is required (Monitor streams stdout lines as events): " +
			"Monitor(command: 'while true; do flow inbox pop --wait --timeout 300 --json " +
			"|| true; done', persistent: true). Every message then wakes you as an event, " +
			"no re-arming ever (--json is silent on timeouts). Only if Monitor is " +
			"unavailable, fall back to a background Bash `flow inbox pop --wait` and " +
			"re-arm it after every wake. Also subscribe to what this task depends on: " +
			"`flow watch <task-or-project-slug>` for anything you are waiting on, " +
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
		acked, _ := flowdb.AckHumanMessagesFromSession(db, s.SessionID, busUser, "prompt")
		for _, m := range acked {
			b.WriteString(fmt.Sprintf(
				" flow-bus: the user has just returned — your message [%s] (%q) was answered after %s. "+
					"Factor the elapsed time in; re-verify stale state after long waits.",
				m.ID, m.Body, fmtBusWait(m.WaitedS)))
		}
	}
	if s.TaskSlug != "" {
		b.WriteString(pendingTaskNotice(db, s.TaskSlug))
	}
	return b.String()
}

// busPreToolUseContext returns extra PreToolUse context (may be "").
// It surfaces UNREAD inbox mail of any kind — directed messages AND
// broadcasts — that arrived since it last announced, so a mid-turn item
// ("stop, don't do X") reaches the agent BEFORE it runs the tool that
// item might change or cancel.
//
// Delta-gated and inform-only. PreToolUse fires on every tool call, and
// hooks never consume (marking rows delivered here would silently lose
// mail the harness is free to drop), so a raw pending count would re-nudge
// the same set on every call. Instead it announces only rows absent
// from the bus_surfaced high-water mark, then records them — each item is
// surfaced at most once, and quiet tool calls stay silent. Broadcasts are
// included: any unread item can bear on the pending action, and the
// delta-gate keeps each to a single surface (no per-call spam).
func busPreToolUseContext() string {
	db, err := openBusDB()
	if err != nil {
		return ""
	}
	defer db.Close()
	s := currentBusSender()
	if s.TaskSlug == "" {
		return ""
	}
	msgs, err := flowdb.PendingUnsurfacedForTask(db, s.TaskSlug)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	// Prefer urgent: stable-partition urgent to the front (queryBusMsgs
	// already ordered oldest-first within each group) so the lead excerpt
	// and count lead with the blocking message.
	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].Urgent && !msgs[j].Urgent
	})
	ids := make([]string, len(msgs))
	urgent := 0
	for i, m := range msgs {
		ids[i] = m.ID
		if m.Urgent {
			urgent++
		}
	}
	// Advance the high-water mark before returning: whether or not the
	// agent acts on this surface, the next tool call must not re-announce
	// the same messages.
	_ = flowdb.MarkSurfaced(db, s.TaskSlug, ids)

	lead := msgs[0]
	subject := "A new message"
	if len(msgs) > 1 {
		subject = fmt.Sprintf("%d new messages", len(msgs))
	}
	urgentNote := ""
	if urgent > 0 {
		urgentNote = fmt.Sprintf(" (%d URGENT)", urgent)
	}
	return fmt.Sprintf(
		" flow-bus: %s%s just landed in this session's inbox — it may change or cancel the "+
			"action you're about to take. Top: [%s] %q. Read it BEFORE proceeding: "+
			"`flow inbox pop` (or your armed Monitor loop delivers it). Inform-only — you have "+
			"NOT consumed it; this notice will not repeat for these message(s).",
		subject, urgentNote, lead.ID, busSnippet(lead.Body))
}

// humanPendingNotice renders the inform-only "the USER has mail" line
// ("" when their queue is empty).
func humanPendingNotice(db *sql.DB) string {
	n, err := flowdb.PendingCountForHuman(db, busUser)
	if err != nil || n == 0 {
		return ""
	}
	return fmt.Sprintf(
		" flow-bus: %d pending message(s)/broadcast(s) await the USER — tell them at the "+
			"start of your reply; they consume via `flow inbox pop` (never consume these yourself).", n)
}

// busHumanPendingNotice is the standalone variant for the unbound
// SessionStart path (opens its own DB handle; silent on any error).
func busHumanPendingNotice() string {
	db, err := openBusDB()
	if err != nil {
		return ""
	}
	defer db.Close()
	return humanPendingNotice(db)
}

// pendingTaskNotice renders an inform-only pending-count line for a
// task's inbox ("" when empty). It reads counts only — never delivers.
func pendingTaskNotice(db *sql.DB, slug string) string {
	n, err := flowdb.PendingCountForTask(db, slug)
	if err != nil || n == 0 {
		return ""
	}
	return fmt.Sprintf(" flow-bus: %d pending message(s) in this session's inbox — consume with "+
		"`flow inbox pop` now (or your armed Monitor loop will deliver them).", n)
}

// nudgeBackoffFor returns the cooldown before the next broadcast-nudge
// is allowed, given how many consecutive nudges went unanswered: 30m,
// 1h, 2h, capped at 4h. A broadcast resets the counter.
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
// one-liner if this task has watchers and hasn't broadcast recently.
//
// Loop guard: Claude Code treats any Stop-hook output as blocking the
// turn from ending, re-invokes the hook on the follow-up stop with
// stop_hook_active=true in its stdin payload, and force-ends the turn
// after too many consecutive blocks. A nudge must therefore never fire
// on a stop that is already part of a hook-driven continuation — check
// the flag FIRST and stay silent while it's set.
func cmdHookStop(args []string) int {
	fs := flagSet("hook stop")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// A missing or malformed payload counts as active — when in doubt,
	// don't block.
	if p, ok := readHookPayload(); !ok || p.StopHookActive {
		return 0
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
	nudge := stopBroadcastNudge(db, slug)
	if nudge == "" {
		return 0
	}
	return emitHookContext("Stop", strings.TrimSpace(nudge))
}

// stopBroadcastNudge returns the watcher broadcast-nudge context (""
// when any gate says stay quiet): watchers exist, no broadcast in 30m,
// and the declined-nudge backoff has elapsed.
func stopBroadcastNudge(db *sql.DB, slug string) string {
	t, err := flowdb.GetTask(db, slug)
	if err != nil || t == nil {
		return ""
	}
	watchers, err := flowdb.WatchersOf(db, taskTopics(t))
	if err != nil || len(watchers) == 0 {
		return "" // no audience — a nudge would be noise
	}
	last, _ := flowdb.LastBroadcastAt(db, slug)
	if last != "" {
		if ts, err := time.Parse(time.RFC3339, last); err == nil && time.Since(ts) < 30*time.Minute {
			return "" // broadcast recently — stay quiet
		}
	}
	// Declined-nudge backoff: ask again only after 30m·2^(declines-1),
	// capped at 4h. The agent's judgment stands in between; a broadcast
	// resets the cycle.
	if nudgedAt, attempts, _ := flowdb.GetNudgeState(db, slug); nudgedAt != "" {
		if ts, err := time.Parse(time.RFC3339, nudgedAt); err == nil &&
			time.Since(ts) < nudgeBackoffFor(attempts) {
			return ""
		}
	}
	_ = flowdb.RecordNudge(db, slug)
	return fmt.Sprintf(
		"flow-bus: %d watcher(s) follow this task and your last broadcast is %s. If this turn "+
			"completed meaningful work, broadcast a one-liner now: flow broadcast \"<what changed>\". "+
			"Skip if nothing notable happened.",
		len(watchers), lastPostDesc(last))
}

func lastPostDesc(last string) string {
	if last == "" {
		return "(none yet)"
	}
	return busAge(last, time.Now()) + " old"
}
