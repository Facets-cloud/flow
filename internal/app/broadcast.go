package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// cmdBroadcast implements the broadcast half of the message bus:
//
//	flow broadcast "<one-liner>" [--from <task-slug>]
//
// The broadcasting task never addresses recipients. At write time the
// one-liner fans out as one kind=broadcast row per CURRENT watcher of
// any of the task's topics (its slug, its project slug, its assignee) —
// a message to each. Broadcasts never escalate: session watchers get
// them via hooks or `flow inbox pop`; human watchers via `flow inbox`.
// No watchers = nothing delivered (the durable record of milestones is
// still a task update file).
func cmdBroadcast(args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, `usage: flow broadcast "<one-liner>" [--from <task-slug>]`)
		return 2
	}
	body := strings.TrimSpace(args[0])
	fs := flagSet("broadcast")
	from := fs.String("from", "", "broadcast as this task slug (default: the bound task)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if body == "" {
		fmt.Fprintln(os.Stderr, "error: empty broadcast")
		return 2
	}

	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	s := currentBusSender()
	slug := *from
	if slug == "" {
		slug = s.TaskSlug
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "error: flow broadcast needs a task identity — run in a bound session or pass --from <task-slug>")
		return 2
	}
	t, err := flowdb.GetTask(db, slug)
	if err != nil || t == nil {
		fmt.Fprintf(os.Stderr, "error: no task %q\n", slug)
		return 2
	}

	watchers, err := flowdb.WatchersOf(db, taskTopics(t))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	delivered := 0
	for _, w := range watchers {
		toAssignee, toSlug := splitWatcherAddr(w)
		if toSlug == slug {
			continue // never message yourself your own broadcast
		}
		m := &flowdb.BusMessage{
			ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "broadcast",
			FromAssignee: s.Assignee, FromTaskSlug: slug, SenderSessionID: s.SessionID,
			ToAssignee: toAssignee, ToTaskSlug: toSlug, Body: body,
		}
		if err := flowdb.InsertBusMessage(db, m); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		delivered++
	}
	_ = flowdb.ResetNudges(db, slug) // a broadcast restarts the Stop-nudge backoff cycle
	_ = flowdb.SweepBus(db, time.Now())
	if delivered == 0 {
		fmt.Printf("broadcast from %s — no watchers yet (subscribe with: flow watch %s)\n", slug, slug)
	} else {
		fmt.Printf("broadcast from %s to %d watcher(s)\n", slug, delivered)
	}
	return 0
}

// splitWatcherAddr parses a stored watcher address: "self" (human) or
// "self/<task-slug>" (session).
func splitWatcherAddr(w string) (assignee, taskSlug string) {
	if i := strings.IndexByte(w, '/'); i >= 0 {
		return w[:i], w[i+1:]
	}
	return w, ""
}

// taskTopics returns the broadcast topics a task publishes on: its own
// slug, its project slug, and its assignee (self when unassigned).
func taskTopics(t *flowdb.Task) []string {
	topics := []string{t.Slug}
	if t.ProjectSlug.Valid && t.ProjectSlug.String != "" {
		topics = append(topics, t.ProjectSlug.String)
	}
	assignee := busUser
	if t.Assignee.Valid && t.Assignee.String != "" {
		assignee = t.Assignee.String
	}
	return append(topics, assignee)
}
