package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// cmdPage implements the directed half of the attention bus:
//
//   flow page <address> "<body>" [--urgent] [--re <task-slug>]
//   flow page                        pending pages addressed to you
//   flow page listen [--timeout <s>] [--follow]
//   flow page ack [<id>]
//   flow page stats
//
// Address grammar: "<assignee>" pages the human (notification + backoff
// escalation until acked); "<assignee>/<task-slug>" messages the session
// bound to that task (context delivery, no interruption). "self" is the
// local user. A bare task slug is sugar for self/<slug>.
func cmdPage(args []string) int {
	if len(args) == 0 {
		return pageList()
	}
	switch args[0] {
	case "listen":
		return pageListen(args[1:])
	case "ack":
		return pageAck(args[1:])
	case "stats":
		return pageStats(args[1:])
	case "ls", "list":
		return pageList()
	}
	return pageSend(args)
}

// pagerSelf is the assignee name for the local user. Tasks with a NULL
// assignee belong to self; multi-user addressing rides on tasks.assignee.
const pagerSelf = "self"

const pageBodyMax = 200

func openPagerDB() (*sql.DB, error) {
	path, err := flowDBPath()
	if err != nil {
		return nil, err
	}
	return flowdb.OpenDB(path)
}

func newPageID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b)
}

// senderIdentity describes who/where a page command is running.
type senderIdentity struct {
	Assignee  string // always "self" locally; workspace transport may widen this
	TaskSlug  string // bound flow task, "" if unbound
	SessionID string
	TTY       string
	ItermUUID string
}

func currentSender() senderIdentity {
	return senderIdentity{
		Assignee:  pagerSelf,
		TaskSlug:  lookupBoundTaskSlug(),
		SessionID: currentSessionID(),
		TTY:       findSessionTTY(),
		ItermUUID: itermSessionUUID(),
	}
}

// registerEndpoint refreshes this session's reachability record so
// peers can badge/notify its tab. Best-effort.
func registerEndpoint(db *sql.DB, s senderIdentity) {
	if s.TaskSlug == "" {
		return
	}
	_ = flowdb.UpsertPageEndpoint(db, &flowdb.PageEndpoint{
		TaskSlug: s.TaskSlug, SessionID: s.SessionID,
		TTY: s.TTY, ItermUUID: s.ItermUUID,
	})
}

// parsePageAddress resolves an address string to (assignee, taskSlug).
// taskSlug == "" means the human.
func parsePageAddress(db *sql.DB, addr string) (string, string, error) {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		assignee, slug := addr[:i], addr[i+1:]
		if assignee == "" || slug == "" {
			return "", "", fmt.Errorf("bad address %q (want <assignee>[/<task-slug>])", addr)
		}
		if t, err := flowdb.GetTask(db, slug); err != nil || t == nil {
			return "", "", fmt.Errorf("no task %q for address %q", slug, addr)
		}
		return assignee, slug, nil
	}
	// Bare token: a task slug is sugar for self/<slug>; else an assignee.
	if t, _ := flowdb.GetTask(db, addr); t != nil {
		return pagerSelf, addr, nil
	}
	return addr, "", nil
}

func pageSend(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: flow page <assignee>[/<task-slug>] "<body>" [--urgent] [--re <slug>]`)
		return 2
	}
	addr, body := args[0], strings.TrimSpace(args[1])
	fs := flagSet("page")
	urgent := fs.Bool("urgent", false, "escalate: Dock bounces until the recipient focuses iTerm")
	re := fs.String("re", "", "task slug this page is about (context link)")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	if body == "" {
		fmt.Fprintln(os.Stderr, "error: empty page body")
		return 2
	}
	if len(body) > pageBodyMax {
		fmt.Fprintf(os.Stderr, "error: page body is %d chars (max %d) — pages are short; put context in --re <task-slug> or a task update\n",
			len(body), pageBodyMax)
		return 2
	}

	db, err := openPagerDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	toAssignee, toSlug, err := parsePageAddress(db, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	s := currentSender()
	registerEndpoint(db, s)

	m := &flowdb.PageMessage{
		ID: newPageID(), CreatedAt: flowdb.NowISO(), Kind: "page",
		FromAssignee: s.Assignee, FromTaskSlug: s.TaskSlug, SenderSessionID: s.SessionID,
		ToAssignee: toAssignee, ToTaskSlug: toSlug, Body: body, ReSlug: *re, Urgent: *urgent,
	}

	if toSlug == "" {
		// Human page: notify via the SENDER's tty (click focuses this tab)
		// and arm the backoff escalation.
		m.NextNotifyAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		if err := flowdb.InsertPageMessage(db, m); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		notifyOnTTY(s.TTY, senderLabel(s), body, *urgent)
		if toAssignee != pagerSelf {
			fmt.Printf("note: no cross-user transport yet — %q will see this page only on this machine\n", toAssignee)
		}
		fmt.Printf("paged %s [%s] — re-notifies with backoff until answered (your reply in this session acks it)\n",
			toAssignee, m.ID)
	} else {
		if err := flowdb.InsertPageMessage(db, m); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if ep, _ := flowdb.GetPageEndpoint(db, toSlug); ep != nil {
			setBadge(ep.TTY, "\U0001F4E8")
		}
		fmt.Printf("paged session %s/%s [%s] — delivered on its next activity or listen\n",
			toAssignee, toSlug, m.ID)
	}
	_ = flowdb.SweepPages(db, time.Now())
	return 0
}

func senderLabel(s senderIdentity) string {
	if s.TaskSlug != "" {
		return s.TaskSlug
	}
	return "a session"
}

func pageList() int {
	db, err := openPagerDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	pages, err := flowdb.PendingHumanPages(db, pagerSelf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	posts, err := flowdb.PendingPostsForHuman(db, pagerSelf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(pages) == 0 && len(posts) == 0 {
		fmt.Println("no pending pages")
		return 0
	}
	now := time.Now()
	for _, m := range pages {
		mark := "\U0001F4DE"
		if m.Urgent {
			mark = "\U0001F6A8"
		}
		fmt.Printf("%s [%s] %s  (notified %dx)  %s: %s\n",
			mark, m.ID, pageAge(m.CreatedAt, now), m.Attempts, pageFrom(m), m.Body)
	}
	for _, m := range posts {
		fmt.Printf("\U0001F4E3 [%s] %s  %s: %s\n", m.ID, pageAge(m.CreatedAt, now), pageFrom(m), m.Body)
	}
	fmt.Println("\nanswer a page by replying in its session (or: flow page ack <id>)")
	return 0
}

func pageFrom(m *flowdb.PageMessage) string {
	if m.FromTaskSlug != "" {
		return m.FromTaskSlug
	}
	return m.FromAssignee
}

func pageAge(createdAt string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// pageListen blocks until messages arrive for the current identity,
// prints them, and exits — run it as a BACKGROUND shell command so
// arrival wakes the agent. Bound sessions drain their task inbox;
// an unbound/human invocation streams the human's pages + posts.
func pageListen(args []string) int {
	fs := flagSet("page listen")
	timeout := fs.Int("timeout", 3600, "seconds to wait before giving up")
	follow := fs.Bool("follow", false, "keep streaming instead of exiting on first delivery")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openPagerDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	s := currentSender()
	human := s.TaskSlug == ""
	if human {
		fmt.Printf("listening as %s (pages + watched posts), timeout %ds\n", pagerSelf, *timeout)
	} else {
		fmt.Printf("listening as %s/%s, pid %d, timeout %ds\n", pagerSelf, s.TaskSlug, os.Getpid(), *timeout)
	}

	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	for {
		if s.TaskSlug != "" {
			_ = flowdb.UpsertPageEndpoint(db, &flowdb.PageEndpoint{
				TaskSlug: s.TaskSlug, SessionID: s.SessionID, TTY: s.TTY, ItermUUID: s.ItermUUID,
				ListenPID: os.Getpid(), ListenHeartbeat: flowdb.NowISO(),
			})
		}
		var rows []*flowdb.PageMessage
		if human {
			pages, _ := flowdb.PendingHumanPages(db, pagerSelf)
			posts, _ := flowdb.PendingPostsForHuman(db, pagerSelf)
			rows = append(pages, posts...)
		} else {
			rows, _ = flowdb.PendingForTask(db, s.TaskSlug)
		}
		if len(rows) > 0 {
			printPageRows(rows)
			ids := make([]string, 0, len(rows))
			for _, m := range rows {
				if human && m.Kind == "page" {
					_, _ = flowdb.AckPageByID(db, m.ID, "listen")
					continue
				}
				ids = append(ids, m.ID)
			}
			_ = flowdb.MarkDelivered(db, ids)
			setBadge(s.TTY, "")
			if !*follow {
				return 0
			}
		}
		if time.Now().After(deadline) {
			fmt.Println("listen timeout: no messages arrived")
			return 0
		}
		time.Sleep(2 * time.Second)
	}
}

func printPageRows(rows []*flowdb.PageMessage) {
	now := time.Now()
	for _, m := range rows {
		kind := "page"
		if m.Kind == "post" {
			kind = "post"
		}
		fmt.Printf("[%s %s] from %s (%s ago): %s\n", kind, m.ID, pageFrom(m), pageAge(m.CreatedAt, now), m.Body)
		if m.ReSlug != "" {
			fmt.Printf("         re: task %s (flow show task %s)\n", m.ReSlug, m.ReSlug)
		}
	}
}

func pageAck(args []string) int {
	fs := flagSet("page ack")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openPagerDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if rest := fs.Args(); len(rest) == 1 {
		m, err := flowdb.AckPageByID(db, rest[0], "manual")
		if err != nil || m == nil {
			fmt.Println("nothing to ack")
			return 0
		}
		fmt.Printf("acked [%s] after %s: %s\n", m.ID, fmtWait(m.WaitedS), m.Body)
		return 0
	}
	// No id: ack every pending human page (inbox-zero gesture).
	pages, _ := flowdb.PendingHumanPages(db, pagerSelf)
	if len(pages) == 0 {
		fmt.Println("nothing to ack")
		return 0
	}
	for _, p := range pages {
		if m, _ := flowdb.AckPageByID(db, p.ID, "manual"); m != nil {
			fmt.Printf("acked [%s] after %s: %s\n", m.ID, fmtWait(m.WaitedS), m.Body)
		}
	}
	return 0
}

func fmtWait(s float64) string {
	return pageAge(time.Now().Add(-time.Duration(s*float64(time.Second))).Format(time.RFC3339), time.Now())
}

func pageStats(args []string) int {
	fs := flagSet("page stats")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openPagerDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	s, err := flowdb.GetPageStats(db, pagerSelf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("answered pages : %d\n", s.Acked)
	fmt.Printf("pending pages  : %d\n", s.Pending)
	fmt.Printf("posts on bus   : %d\n", s.Posts)
	if s.Acked > 0 {
		fmt.Printf("average wait   : %s\n", fmtWait(s.AvgWait))
		fmt.Printf("median wait    : %s\n", fmtWait(s.MedWait))
		fmt.Printf("worst wait     : %s\n", fmtWait(s.MaxWait))
	}
	return 0
}

// runNotifyScan performs one backoff pass over due human pages,
// re-firing their notifications via the sender's recorded tty (falling
// back to the sender task's endpoint). Called opportunistically from
// hooks so any active session anywhere keeps escalation alive.
func runNotifyScan(db *sql.DB) {
	now := time.Now()
	due, err := flowdb.DueHumanPages(db, now)
	if err != nil {
		return
	}
	for _, m := range due {
		tty := ""
		if m.FromTaskSlug != "" {
			if ep, _ := flowdb.GetPageEndpoint(db, m.FromTaskSlug); ep != nil {
				tty = ep.TTY
			}
		}
		age := pageAge(m.CreatedAt, now)
		notifyOnTTY(tty, pageFrom(m), fmt.Sprintf("%s (waiting %s)", m.Body, age), m.Urgent)
		_ = flowdb.BumpNotifyAttempt(db, m.ID, m.Attempts, now)
	}
}
