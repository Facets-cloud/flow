package app

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// Paging-bus hook handlers. Wired aggressively — every LLM turn touches
// the bus:
//
//   SessionStart      pageSessionStartContext(): pending-page notice +
//                     encouragement to background `flow page listen`.
//   UserPromptSubmit  pagePromptSubmitContext(): the human replying in a
//                     session ACKS that session's pending human pages
//                     (wait time recorded + injected) and drains the
//                     bound task's inbox.
//   PostToolUse       cmdHookPostToolUse(): drains the inbox mid-turn on
//                     every tool call, nudges `listen` when mail arrived
//                     with no live listener, and runs the backoff
//                     notify-scan so escalation stays alive machine-wide.
//   Stop              cmdHookStop(): nudges the agent to `flow post` a
//                     one-liner when the task has watchers and no recent
//                     post.
//
// Hook code must never fail loud — errors degrade to silence.

// pageSessionStartContext returns extra SessionStart context (may be "").
func pageSessionStartContext() string {
	db, err := openPagerDB()
	if err != nil {
		return ""
	}
	defer db.Close()
	s := currentSender()
	registerEndpoint(db, s)
	runNotifyScan(db)

	var b strings.Builder
	if pages, _ := flowdb.PendingHumanPages(db, pagerSelf); len(pages) > 0 {
		now := time.Now()
		b.WriteString(fmt.Sprintf(" page-bus: %d pending page(s) for the user:", len(pages)))
		for _, m := range pages {
			b.WriteString(fmt.Sprintf(" [%s %s ago, %s: %s]", m.ID, pageAge(m.CreatedAt, now), pageFrom(m), m.Body))
		}
		b.WriteString(" Surface these to the user at the start of your reply.")
	}
	if s.TaskSlug != "" {
		if inbox := drainTaskInbox(db, s.TaskSlug, s.TTY); inbox != "" {
			b.WriteString(inbox)
		}
		b.WriteString(" page-bus: to be woken the moment a page or watched post arrives " +
			"(instead of waiting for your next tool call), run `flow page listen` as a " +
			"BACKGROUND Bash command now and re-start it after each wake. One listener per session.")
	}
	return b.String()
}

// pagePromptSubmitContext returns extra UserPromptSubmit context (may be "").
func pagePromptSubmitContext() string {
	db, err := openPagerDB()
	if err != nil {
		return ""
	}
	defer db.Close()
	s := currentSender()
	registerEndpoint(db, s)
	runNotifyScan(db)

	var b strings.Builder
	if s.SessionID != "" {
		acked, _ := flowdb.AckHumanPagesFromSession(db, s.SessionID, "prompt")
		for _, m := range acked {
			setBadge(s.TTY, "")
			b.WriteString(fmt.Sprintf(
				" page-bus: the user has just returned — your page [%s] (%q) was answered after %s. "+
					"Factor the elapsed time in; re-verify stale state after long waits.",
				m.ID, m.Body, fmtWait(m.WaitedS)))
		}
	}
	if s.TaskSlug != "" {
		b.WriteString(drainTaskInbox(db, s.TaskSlug, s.TTY))
	}
	return b.String()
}

// drainTaskInbox delivers pending messages for a task and renders them
// as hook context ("" if none).
func drainTaskInbox(db *sql.DB, slug, tty string) string {
	rows, err := flowdb.PendingForTask(db, slug)
	if err != nil || len(rows) == 0 {
		return ""
	}
	ids := make([]string, len(rows))
	for i, m := range rows {
		ids[i] = m.ID
	}
	_ = flowdb.MarkDelivered(db, ids)
	setBadge(tty, "")
	now := time.Now()
	var b strings.Builder
	for _, m := range rows {
		verb := "message"
		if m.Kind == "post" {
			verb = "post (FYI broadcast)"
		}
		b.WriteString(fmt.Sprintf(" page-bus %s from %s (%s ago): %s.", verb, pageFrom(m), pageAge(m.CreatedAt, now), m.Body))
		if m.ReSlug != "" {
			b.WriteString(fmt.Sprintf(" (re: task %s)", m.ReSlug))
		}
	}
	return b.String()
}

// listenerAlive reports whether a live `flow page listen` process is
// watching this task (recent heartbeat + pid still running).
func listenerAlive(db *sql.DB, slug string) bool {
	ep, err := flowdb.GetPageEndpoint(db, slug)
	if err != nil || ep == nil || ep.ListenPID == 0 || ep.ListenHeartbeat == "" {
		return false
	}
	hb, err := time.Parse(time.RFC3339, ep.ListenHeartbeat)
	if err != nil || time.Since(hb) > 30*time.Second {
		return false
	}
	return processAlive(ep.ListenPID) // shared liveness probe from auto.go
}

// cmdHookPostToolUse fires on every tool call: drain inbox, keep
// escalation alive, nudge a listener into place.
func cmdHookPostToolUse(args []string) int {
	fs := flagSet("hook post-tool-use")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openPagerDB()
	if err != nil {
		return 0
	}
	defer db.Close()
	s := currentSender()
	runNotifyScan(db)
	if s.TaskSlug == "" {
		return 0
	}
	registerEndpoint(db, s)
	ctx := drainTaskInbox(db, s.TaskSlug, s.TTY)
	if ctx == "" {
		return 0
	}
	if !listenerAlive(db, s.TaskSlug) {
		ctx += " page-bus nudge: no listener is running — future messages wait for your next " +
			"tool call. Start `flow page listen` as a background Bash command to be woken instantly."
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
	db, err := openPagerDB()
	if err != nil {
		return 0
	}
	defer db.Close()
	runNotifyScan(db)
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
		if t, err := time.Parse(time.RFC3339, last); err == nil && time.Since(t) < 30*time.Minute {
			return 0 // posted recently — stay quiet
		}
	}
	msg := fmt.Sprintf(
		"page-bus: %d watcher(s) follow this task and your last post is %s. If this turn "+
			"completed meaningful work, broadcast a one-liner now: flow post \"<what changed>\". "+
			"Skip if nothing notable happened.",
		len(watchers), lastPostDesc(last))
	return emitHookContext("Stop", msg)
}

func lastPostDesc(last string) string {
	if last == "" {
		return "(none yet)"
	}
	return pageAge(last, time.Now()) + " old"
}
