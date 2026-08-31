package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// cmdPost implements the broadcast half of the attention bus:
//
//   flow post "<one-liner>"
//
// The posting task never addresses recipients. At write time the post
// fans out as one kind=post row per CURRENT watcher of any of the
// task's topics (its slug, its project slug, its assignee) — a DM to
// each. Posts never interrupt: session watchers get them injected into
// context (hooks / listen); human watchers see them in `flow page` and
// `flow watch --follow`. No watchers = nothing delivered (the durable
// record of milestones is still a task update file).
func cmdPost(args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, `usage: flow post "<one-liner>" [--from <task-slug>]`)
		return 2
	}
	body := strings.TrimSpace(args[0])
	fs := flagSet("post")
	from := fs.String("from", "", "post as this task slug (default: the bound task)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if body == "" {
		fmt.Fprintln(os.Stderr, "error: empty post")
		return 2
	}

	db, err := openPagerDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	s := currentSender()
	slug := *from
	if slug == "" {
		slug = s.TaskSlug
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "error: flow post needs a task identity — run in a bound session or pass --from <task-slug>")
		return 2
	}
	t, err := flowdb.GetTask(db, slug)
	if err != nil || t == nil {
		fmt.Fprintf(os.Stderr, "error: no task %q\n", slug)
		return 2
	}
	registerEndpoint(db, s)

	watchers, err := flowdb.WatchersOf(db, taskTopics(t))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	delivered := 0
	for _, w := range watchers {
		toAssignee, toSlug := splitWatcherAddr(w)
		if toSlug == slug {
			continue // never DM yourself your own post
		}
		m := &flowdb.PageMessage{
			ID: newPageID(), CreatedAt: flowdb.NowISO(), Kind: "post",
			FromAssignee: s.Assignee, FromTaskSlug: slug, SenderSessionID: s.SessionID,
			ToAssignee: toAssignee, ToTaskSlug: toSlug, Body: body,
		}
		if err := flowdb.InsertPageMessage(db, m); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if toSlug != "" {
			if ep, _ := flowdb.GetPageEndpoint(db, toSlug); ep != nil {
				setBadge(ep.TTY, "\U0001F4E8")
			}
		}
		delivered++
	}
	_ = flowdb.SweepPages(db, time.Now())
	if delivered == 0 {
		fmt.Printf("posted from %s — no watchers yet (subscribe with: flow watch %s)\n", slug, slug)
	} else {
		fmt.Printf("posted from %s to %d watcher(s)\n", slug, delivered)
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
	assignee := pagerSelf
	if t.Assignee.Valid && t.Assignee.String != "" {
		assignee = t.Assignee.String
	}
	return append(topics, assignee)
}
