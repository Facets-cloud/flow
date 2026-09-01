package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		b.WriteString(" flow-bus listener discipline: arm your Monitor tool on " +
			"`flow inbox pop --wait` NOW (Monitor is PREFERRED — it waits on the command " +
			"without holding a shell and wakes you the moment it exits with a message); if " +
			"Monitor is unavailable, run it as a background Bash command instead. RE-ARM " +
			"immediately after every wake so the next message also reaches you. Also " +
			"subscribe to what this task depends on: `flow watch <task-or-project-slug>` " +
			"for anything you are waiting on, coordinating with, or any task you create " +
			"from this session.")
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
	if stopHookActive(os.Stdin) {
		return 0
	}
	db, err := openBusDB()
	if err != nil {
		return 0
	}
	defer db.Close()
	s := currentBusSender()
	slug := s.TaskSlug
	if slug == "" {
		return 0
	}

	// Stranded-mail delivery: the turn is ending and mail is pending
	// with no live listener to catch it — hand it over NOW rather than
	// letting it wait for the user's next prompt. Real mail, so it
	// bypasses the post-nudge backoff; it self-limits because the drain
	// consumes the rows. A live `pop --wait` listener takes precedence
	// (it delivers within its poll tick; don't steal its message).
	var inboxCtx string
	if !listenerAlive(db, s.identity()) {
		if drained := drainTaskInbox(db, slug); drained != "" {
			inboxCtx = drained + " Act on these now if they change anything, then arm your " +
				"Monitor tool (preferred; else a background Bash command) on " +
				"`flow inbox pop --wait` so future mail wakes you instead of " +
				"waiting for a turn end."
		}
	}

	nudge := stopPostNudge(db, slug)
	ctx := strings.TrimSpace(inboxCtx + nudge)
	if ctx == "" {
		return 0
	}
	return emitHookContext("Stop", ctx)
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

// stopPostNudge returns the watcher post-nudge context ("" when any
// gate says stay quiet): watchers exist, no post in 30m, and the
// declined-nudge backoff has elapsed.
func stopPostNudge(db *sql.DB, slug string) string {
	t, err := flowdb.GetTask(db, slug)
	if err != nil || t == nil {
		return ""
	}
	watchers, err := flowdb.WatchersOf(db, taskTopics(t))
	if err != nil || len(watchers) == 0 {
		return "" // no audience — a nudge would be noise
	}
	last, _ := flowdb.LastPostAt(db, slug)
	if last != "" {
		if ts, err := time.Parse(time.RFC3339, last); err == nil && time.Since(ts) < 30*time.Minute {
			return "" // posted recently — stay quiet
		}
	}
	// Declined-nudge backoff: ask again only after 30m·2^(declines-1),
	// capped at 4h. The agent's judgment stands in between; a post
	// resets the cycle.
	if nudgedAt, attempts, _ := flowdb.GetNudgeState(db, slug); nudgedAt != "" {
		if ts, err := time.Parse(time.RFC3339, nudgedAt); err == nil &&
			time.Since(ts) < nudgeBackoffFor(attempts) {
			return ""
		}
	}
	_ = flowdb.RecordNudge(db, slug)
	return fmt.Sprintf(
		" flow-bus: %d watcher(s) follow this task and your last post is %s. If this turn "+
			"completed meaningful work, broadcast a one-liner now: flow post \"<what changed>\". "+
			"Skip if nothing notable happened.",
		len(watchers), lastPostDesc(last))
}

// stopHookActive reads the Stop-hook stdin payload and reports whether
// this stop is a hook-driven continuation (stop_hook_active). Any read
// or parse failure counts as active — when in doubt, don't block.
func stopHookActive(r io.Reader) bool {
	var payload struct {
		StopHookActive bool `json:"stop_hook_active"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return true
	}
	return payload.StopHookActive
}

func lastPostDesc(last string) string {
	if last == "" {
		return "(none yet)"
	}
	return busAge(last, time.Now()) + " old"
}
