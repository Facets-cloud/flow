package app

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"flow/internal/flowdb"
)

// cmdInbox is the consumption surface of the message bus:
//
//	flow inbox                       list what's pending for you
//	flow inbox pop [--wait] [--timeout <s>]   consume the oldest, one at a time
//	flow inbox ack [<id>]            answer human-directed message(s) by hand
//	flow inbox due                   escalation primitive for notifier scripts
//	flow inbox stats                 wait-time metrics
//
// Identity is implicit: a bound session consumes as self/<task-slug>
// (its own mail); an unbound/human invocation consumes as self.
//
// `pop --wait` blocks until a message exists, pops exactly one, and
// exits 0 — built to be parked on by a Claude session's Monitor tool or
// a background Bash command, so mail arrival wakes the agent. Exit 1
// means nothing was popped (empty inbox, or --wait timed out).
func cmdInbox(args []string) int {
	if len(args) == 0 {
		return inboxList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "pop":
		return inboxPop(rest)
	case "ack":
		return inboxAck(rest)
	case "due":
		return inboxDue(rest)
	case "stats":
		return inboxStats(rest)
	case "ls", "list":
		return inboxList(rest)
	}
	return inboxList(args) // allow `flow inbox --me`
}

// inboxIdentity resolves the consuming identity, honoring --me (act as
// the human even inside a bound session).
func inboxIdentity(me bool) busSender {
	s := currentBusSender()
	if me {
		s.TaskSlug = ""
	}
	return s
}

// pendingForIdentity returns the pending rows for the current identity.
func pendingForIdentity(db *sql.DB, s busSender) ([]*flowdb.BusMessage, error) {
	if s.TaskSlug != "" {
		return flowdb.PendingForTask(db, s.TaskSlug)
	}
	return flowdb.PendingForHuman(db, s.Assignee)
}

func inboxList(args []string) int {
	fs := flagSet("inbox")
	me := fs.Bool("me", false, "act as the human (self), ignoring any session binding")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	s := inboxIdentity(*me)
	rows, err := pendingForIdentity(db, s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Printf("inbox empty (%s)\n", s.identity())
		return 0
	}
	now := time.Now()
	for _, m := range rows {
		mark := "✉"
		if m.Kind == "post" {
			mark = "↺"
		} else if m.Urgent {
			mark = "⚠"
		}
		extra := ""
		if m.Kind == "message" && m.ToTaskSlug == "" {
			extra = fmt.Sprintf("  (notified %dx)", m.Attempts)
		}
		fmt.Printf("%s [%s] %s%s  %s: %s\n", mark, m.ID, busAge(m.CreatedAt, now), extra, busFrom(m), m.Body)
	}
	fmt.Printf("\nconsume one at a time: flow inbox pop\n")
	return 0
}

// popOne consumes the single oldest pending message for the identity:
// posts and session-directed rows are marked delivered; human-directed
// messages are acked (popping IS answering). Returns nil when empty.
func popOne(db *sql.DB, s busSender) (*flowdb.BusMessage, error) {
	rows, err := pendingForIdentity(db, s)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	m := rows[0]
	if m.Kind == "message" && m.ToTaskSlug == "" {
		if _, err := flowdb.AckMessageByID(db, m.ID, "pop"); err != nil {
			return nil, err
		}
	} else {
		if err := flowdb.MarkDelivered(db, []string{m.ID}); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func printBusMessage(m *flowdb.BusMessage) {
	now := time.Now()
	fmt.Printf("[%s %s] from %s (%s ago): %s\n", m.Kind, m.ID, busFrom(m), busAge(m.CreatedAt, now), m.Body)
	if m.Urgent {
		fmt.Println("        marked URGENT by the sender")
	}
}

func inboxPop(args []string) int {
	fs := flagSet("inbox pop")
	wait := fs.Bool("wait", false, "block until a message arrives, then pop it")
	timeout := fs.Int("timeout", 3600, "seconds --wait blocks before giving up")
	me := fs.Bool("me", false, "act as the human (self), ignoring any session binding")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	s := inboxIdentity(*me)

	if !*wait {
		m, err := popOne(db, s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if m == nil {
			fmt.Printf("inbox empty (%s)\n", s.identity())
			return 1
		}
		printBusMessage(m)
		return 0
	}

	identity := s.identity()
	pid := os.Getpid()
	if err := flowdb.UpsertBusListener(db, identity, pid); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = flowdb.RemoveBusListener(db, identity, pid) }()
	fmt.Printf("waiting for mail as %s (pid %d, timeout %ds)\n", identity, pid, *timeout)
	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	for {
		_ = flowdb.TouchBusListener(db, identity, pid)
		m, err := popOne(db, s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if m != nil {
			printBusMessage(m)
			fmt.Println("re-arm: run `flow inbox pop --wait` again (backgrounded) to catch the next message")
			return 0
		}
		if time.Now().After(deadline) {
			fmt.Println("pop --wait timeout: no messages arrived")
			return 1
		}
		time.Sleep(2 * time.Second)
	}
}

func inboxAck(args []string) int {
	fs := flagSet("inbox ack")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if rest := fs.Args(); len(rest) == 1 {
		m, err := flowdb.AckMessageByID(db, rest[0], "manual")
		if err != nil || m == nil {
			fmt.Println("nothing to ack")
			return 0
		}
		fmt.Printf("acked [%s] after %s: %s\n", m.ID, fmtBusWait(m.WaitedS), m.Body)
		return 0
	}
	rows, _ := flowdb.PendingForHuman(db, busSelf)
	acked := 0
	for _, r := range rows {
		if r.Kind != "message" {
			continue
		}
		if m, _ := flowdb.AckMessageByID(db, r.ID, "manual"); m != nil {
			fmt.Printf("acked [%s] after %s: %s\n", m.ID, fmtBusWait(m.WaitedS), m.Body)
			acked++
		}
	}
	if acked == 0 {
		fmt.Println("nothing to ack")
	}
	return 0
}

// inboxDue is the escalation primitive for user notifier scripts: it
// prints human-directed messages whose notify deadline has passed (one
// per line, tab-separated: id, attempts, age, urgent, from, body) and
// advances each row's backoff schedule. Poll it from cron/a loop and
// pipe into whatever notification UX you want. Prints nothing (exit 1)
// when nothing is due.
func inboxDue(args []string) int {
	fs := flagSet("inbox due")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	now := time.Now()
	due, err := flowdb.DueBusMessages(db, busSelf, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(due) == 0 {
		return 1
	}
	for _, m := range due {
		urgent := "0"
		if m.Urgent {
			urgent = "1"
		}
		fmt.Printf("%s\t%d\t%s\t%s\t%s\t%s\n",
			m.ID, m.Attempts, busAge(m.CreatedAt, now), urgent, busFrom(m), m.Body)
		_ = flowdb.BumpNotifyAttempt(db, m.ID, m.Attempts, now)
	}
	return 0
}

func inboxStats(args []string) int {
	fs := flagSet("inbox stats")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	s, err := flowdb.GetBusStats(db, busSelf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("answered messages : %d\n", s.Acked)
	fmt.Printf("pending messages  : %d\n", s.Pending)
	fmt.Printf("posts on bus      : %d\n", s.Posts)
	if s.Acked > 0 {
		fmt.Printf("average wait      : %s\n", fmtBusWait(s.AvgWait))
		fmt.Printf("median wait       : %s\n", fmtBusWait(s.MedWait))
		fmt.Printf("worst wait        : %s\n", fmtBusWait(s.MaxWait))
	}
	return 0
}
