package app

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// cmdReview is `flow review <ref> accept|reject [--reason "…"] [--note "…"]`:
// the driver's explicit verdict on a child's result. driver.md tells a
// driver to verify each child's output itself; without this command that
// verdict lived only in the driver's transcript, and rework could only be
// guessed from message traffic.
//
//   - accept records a `review` event (verdict accept, optional note).
//   - reject requires --reason. It records the verdict; if the task is
//     done, reopens it (a `reopen` event, status → in-progress); and
//     messages the task's session with the reason, so the next run starts
//     from it.
//
// Anyone may review any task; the event's actor says who did (driver when
// the caller is bound to the task's parent).
func cmdReview(args []string) int {
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		fmt.Fprintln(os.Stderr, `error: usage: flow review <ref> accept|reject [--reason "<why>"] [--note "<text>"]`)
		return 2
	}
	query, verdict := args[0], args[1]
	fs := flagSet("review")
	reason := fs.String("reason", "", "why the result is rejected (required for reject)")
	note := fs.String("note", "", "optional note recorded with an accept")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	switch verdict {
	case "accept":
	case "reject":
		if strings.TrimSpace(*reason) == "" {
			fmt.Fprintln(os.Stderr, `error: reject needs --reason "<what is wrong>" — it is sent to the task and recorded`)
			return 2
		}
	default:
		fmt.Fprintf(os.Stderr, "error: verdict must be accept or reject, got %q\n", verdict)
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}
	if task.ArchivedAt.Valid {
		fmt.Fprintf(os.Stderr, "error: task %q is archived; unarchive it before reviewing\n", task.Slug)
		return 1
	}

	detail := map[string]string{"verdict": verdict}
	if verdict == "accept" {
		if *note != "" {
			detail["note"] = *note
		}
		recordTaskEvent(db, task, flowdb.EventReview, detail)
		fmt.Printf("review: accepted %s\n", task.Slug)
		return 0
	}

	detail["reason"] = *reason
	recordTaskEvent(db, task, flowdb.EventReview, detail)
	if task.Status == "done" {
		if err := reopenForReview(db, task, *reason); err != nil {
			fmt.Fprintf(os.Stderr, "error: reopen %s: %v\n", task.Slug, err)
			return 1
		}
		task.Status = "in-progress"
		fmt.Printf("review: rejected %s — reopened (in-progress)\n", task.Slug)
	} else {
		fmt.Printf("review: rejected %s\n", task.Slug)
	}
	if err := messageReviewedTask(db, task, *reason); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reason not sent to %s: %v\n", task.Slug, err)
	}
	fmt.Printf("re-run it with: flow do %s --auto --with \"<what to fix>\"   (or flow do %s to work it interactively)\n",
		task.Slug, task.Slug)
	return 0
}

// reopenForReview flips a done task back to in-progress and records the
// reopen with the rejection as its reason. Conditional on status, so a
// concurrent reopen is not recorded twice.
func reopenForReview(db *sql.DB, task *flowdb.Task, reason string) error {
	now := flowdb.NowISO()
	res, err := db.Exec(
		`UPDATE tasks SET status='in-progress', status_changed_at=?, updated_at=? WHERE slug=? AND status='done'`,
		now, now, task.Slug,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		recordStatusChange(db, task, "done", "in-progress", "review rejected: "+reason)
	}
	return nil
}

// messageReviewedTask sends the rejection reason to the task's session
// inbox, so a resumed or re-run session pops it first. Skipped when the
// caller IS that session (it already knows).
func messageReviewedTask(db *sql.DB, task *flowdb.Task, reason string) error {
	s := currentBusSender()
	if s.TaskSlug == task.Slug {
		return nil
	}
	if err := deliverableTask(task); err != nil {
		return err
	}
	to := busUser
	if task.Assignee.Valid && task.Assignee.String != "" {
		to = canonicalAssignee(task.Assignee.String)
	}
	body := "review: your result was rejected — " + reason
	if len(body) > busBodyMax {
		body = body[:busBodyMax]
	}
	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: s.Assignee, FromTaskSlug: s.TaskSlug, SenderSessionID: s.SessionID,
		ToAssignee: to, ToTaskSlug: task.Slug, Body: body,
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		return err
	}
	maybeDispatch(m)
	_ = flowdb.SweepBus(db, time.Now())
	return nil
}
