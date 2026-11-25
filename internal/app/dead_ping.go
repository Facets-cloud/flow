package app

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// notifyHumanOfDeadOrphan pings the human when a PARENTLESS auto run ends
// dead (exited without `flow done`: crash, not logged in, timeout,
// killed). A parented run already reports to its parent
// (notifyParentOfDeadChild); without this, unattended work with no parent
// would fail silently. It is a normal, pinging message to the human (not
// --fyi): a dropped piece of work needs attention.
//
// Skipped for owner-managed tasks (tag owner:<slug>): the owner's next
// tick reviews what it dispatched and escalates itself, so pinging here
// would double-notify. Playbook runs are tasks too and are covered like
// any other parentless task.
//
// Callers invoke it only on the single transition to 'dead' (finalize
// owned the row, or the reconcile UPDATE matched), so a re-finalize never
// notifies twice.
func notifyHumanOfDeadOrphan(db *sql.DB, t *flowdb.Task) {
	if t.ParentSlug.Valid && t.ParentSlug.String != "" {
		return
	}
	if isOwnerManaged(db, t.Slug) {
		return
	}
	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: busUser, FromTaskSlug: t.Slug,
		ToAssignee: busUser,
		Body:       deadOrphanBody(t),
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		return
	}
	maybeDispatch(m)
	_ = flowdb.SweepBus(db, time.Now())
}

// deadOrphanBody is the human-facing notice for a dead parentless run.
func deadOrphanBody(t *flowdb.Task) string {
	logRef := "its auto-run log under tasks/" + t.Slug + "/auto-runs/"
	if t.AutoRunLog.Valid && t.AutoRunLog.String != "" {
		logRef = t.AutoRunLog.String
	}
	body := fmt.Sprintf("auto run %q (%s) died without finishing", t.Name, t.Slug)
	if first := briefFirstLine(t.Slug); first != "" && first != t.Name {
		body += fmt.Sprintf(" — %q", first)
	}
	return body + fmt.Sprintf(" — see %s; reopen with flow do %s or take it over", logRef, t.Slug)
}

// briefFirstLine returns the task brief's first non-empty line with any
// markdown heading marker stripped, capped so the notice stays short.
// "" when the brief is missing or empty.
func briefFirstLine(slug string) string {
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(root, "tasks", slug, "brief.md"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(sc.Text()), "#"))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > 160 {
			line = string(r[:157]) + "..."
		}
		return line
	}
	return ""
}

// isOwnerManaged reports whether the task carries an owner:<slug> tag.
func isOwnerManaged(db *sql.DB, slug string) bool {
	tags, err := flowdb.GetTaskTags(db, slug)
	if err != nil {
		return false
	}
	for _, tag := range tags {
		if isOwnerTag(tag) {
			return true
		}
	}
	return false
}

// notifyOfBlockedRun tells whoever must act that a run ended blocked on its
// BLOCKED: line. It goes to the same recipient as the dead-run alert: the
// parent task when it is deliverable, otherwise the human (an FYI when the
// parent is closed, nothing for an owner-managed parentless task). It is not
// worded as a crash, and it is skipped when the child already told that
// recipient it was blocked during this run.
func notifyOfBlockedRun(db *sql.DB, child *flowdb.Task, blockedLine string) {
	reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(blockedLine), "BLOCKED:"))
	if r := []rune(reason); len(r) > 160 {
		reason = string(r[:157]) + "..."
	}
	if reason == "" {
		reason = "no reason given"
	}
	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: busUser, FromTaskSlug: child.Slug,
		ToAssignee: busUser,
	}
	if child.ParentSlug.Valid && child.ParentSlug.String != "" {
		parent := child.ParentSlug.String
		if p, err := flowdb.GetTask(db, parent); err == nil && p != nil && deliverableTask(p) == nil {
			if childReportedBlocked(db, child, parent) {
				return
			}
			m.ToTaskSlug = parent
			m.Body = fmt.Sprintf("%s is blocked: %s; it's waiting on you", child.Slug, reason)
		} else {
			m.Body = fmt.Sprintf("FYI: %s is blocked: %s; it's waiting on you (parent %s closed)", child.Slug, reason, parent)
			m.FYI = true
		}
	} else {
		if isOwnerManaged(db, child.Slug) || childReportedBlocked(db, child, "") {
			return
		}
		m.Body = fmt.Sprintf("%s is blocked: %s; it's waiting on you", child.Slug, reason)
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		return
	}
	if m.ToTaskSlug == "" {
		maybeDispatch(m)
	}
	_ = flowdb.SweepBus(db, time.Now())
}

// childReportedBlocked reports whether child already sent a message this run
// whose body mentions "blocked" to toTask (to the human when toTask is "").
func childReportedBlocked(db *sql.DB, child *flowdb.Task, toTask string) bool {
	since := ""
	if child.AutoRunStarted.Valid {
		since = child.AutoRunStarted.String
	}
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM bus_messages WHERE from_task_slug=? AND kind='message'
		 AND to_assignee=? AND COALESCE(to_task_slug,'')=? AND created_at>=?
		 AND LOWER(body) LIKE '%blocked%'`,
		child.Slug, busUser, toTask, since,
	).Scan(&n)
	return err == nil && n > 0
}
