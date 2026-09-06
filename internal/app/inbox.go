package app

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"flow/internal/flowdb"
)

// cmdInbox is the consumption surface of the message bus:
//
//	flow inbox [--all] [--as <assignee>] [--json]
//	flow inbox pop [--wait] [--timeout <s>] [--keep-unread] [--as <assignee>] [--json]
//	flow inbox read <id> [--json]
//	flow inbox stats [--as <assignee>]
//
// Mail model: `flow inbox` lists unread (pending); `--all` lists the whole
// retained queue (unread + read). `pop` consumes the oldest;
// `pop --keep-unread` wakes a reader/relay without acking; `read <id>`
// targets and acks a specific message out of arrival order.
//
// Identity is implicit: a bound session consumes as user/<task-slug>
// (its own mail); an unbound/human invocation consumes as user.
// Override: --as <assignee> targets a human queue directly — `--as
// user` is the human's own inbox even inside a bound session (e.g. a
// dedicated inbox-monitor task); any other assignee serves monitor/
// transport workers draining that queue. pop is the ONLY consumption
// API: it answers, delivers, and clears — one verb, loop it freely.
//
// `pop --wait` blocks until a message exists, pops exactly one, and
// exits 0 — built to be parked on by a Claude session's Monitor tool or
// a background Bash command, so mail arrival wakes the agent. Exit 1
// means nothing was popped (empty inbox, or --wait timed out). Pops are
// atomic claims: concurrent consumers of one inbox never double-pop.
// --json emits machine-readable rows for scripting.
func cmdInbox(args []string) int {
	if len(args) == 0 {
		return inboxList(nil)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "pop":
		return inboxPop(rest)
	case "read":
		return inboxRead(rest)
	case "stats":
		return inboxStats(rest)
	case "ls", "list":
		return inboxList(rest)
	}
	return inboxList(args) // allow `flow inbox --as x` / `--json` / `--all`
}

// consumerFlags registers the shared identity-override flag: --as
// <assignee> consumes that human queue instead of the ambient identity
// (`--as self` = the user's own inbox even inside a bound session; any
// other assignee = monitor/transport workers draining their queue).
func consumerFlags(fs *flag.FlagSet) (as *string) {
	return fs.String("as", "", "consume this assignee's queue instead of the ambient identity (e.g. --as self)")
}

// resolveConsumer applies the identity override to the ambient identity.
func resolveConsumer(as string) busSender {
	if as != "" {
		return busSender{Assignee: as}
	}
	return currentBusSender()
}

// pendingForIdentity returns the pending rows for an identity.
func pendingForIdentity(db *sql.DB, s busSender) ([]*flowdb.BusMessage, error) {
	if s.TaskSlug != "" {
		return flowdb.PendingForTask(db, s.TaskSlug)
	}
	return flowdb.PendingForHuman(db, s.Assignee)
}

// busMsgJSON is the stable machine-readable rendering of a message.
type busMsgJSON struct {
	ID        string   `json:"id"`
	CreatedAt string   `json:"created_at"`
	Kind      string   `json:"kind"`
	From      addrJSON `json:"from"`
	To        addrJSON `json:"to"`
	Body      string   `json:"body"`
	Urgent    bool     `json:"urgent"`
	Status    string   `json:"status"`
	Mail      string   `json:"mail"` // mail-model label: "unread" | "read"
	WaitedS   float64  `json:"waited_s,omitempty"`
	ReplyTo   string   `json:"reply_to,omitempty"`
}

type addrJSON struct {
	Assignee string `json:"assignee"`
	TaskSlug string `json:"task_slug,omitempty"`
}

func toMsgJSON(m *flowdb.BusMessage) busMsgJSON {
	return busMsgJSON{
		ID: m.ID, CreatedAt: m.CreatedAt, Kind: m.Kind,
		From: addrJSON{Assignee: m.FromAssignee, TaskSlug: m.FromTaskSlug},
		To:   addrJSON{Assignee: m.ToAssignee, TaskSlug: m.ToTaskSlug},
		Body: m.Body, Urgent: m.Urgent, Status: m.Status, Mail: mailLabel(m),
		WaitedS: m.WaitedS, ReplyTo: m.ReplyTo,
	}
}

// mailLabel maps the internal status to the mail-model nomenclature the
// bus presents to humans: pending = "unread", everything consumed
// (delivered/acked) = "read".
func mailLabel(m *flowdb.BusMessage) string {
	if m.Status == "pending" {
		return "unread"
	}
	return "read"
}

func emitJSON(v any) int {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func inboxList(args []string) int {
	fs := flagSet("inbox")
	as := consumerFlags(fs)
	asJSON := fs.Bool("json", false, "emit a JSON array instead of the human listing")
	all := fs.Bool("all", false, "list everything still retained (read + unread), not just unread")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	s := resolveConsumer(*as)
	rows, err := listForIdentity(db, s, *all)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if *asJSON {
		out := make([]busMsgJSON, len(rows))
		for i, m := range rows {
			out[i] = toMsgJSON(m)
		}
		return emitJSON(out)
	}
	if len(rows) == 0 {
		if *all {
			fmt.Printf("inbox empty (%s) — nothing retained\n", s.identity())
		} else {
			fmt.Printf("no unread mail (%s)\n", s.identity())
		}
		return 0
	}
	now := time.Now()
	for _, m := range rows {
		mark := "✉"
		if m.Kind == "broadcast" {
			mark = "↺"
		} else if m.Urgent {
			mark = "⚠"
		}
		extra := ""
		if *all {
			extra = "  " + mailLabel(m)
		}
		fmt.Printf("%s [%s] %s%s  %s: %s\n", mark, m.ID, busAge(m.CreatedAt, now), extra, busFrom(m), m.Body)
	}
	if *all {
		fmt.Printf("\nread one by id: flow inbox read <id>\n")
	} else {
		fmt.Printf("\nconsume one at a time: flow inbox pop\n")
	}
	return 0
}

// listForIdentity returns the rows an `inbox` listing should show: only
// unread (pending) by default, or the whole retained queue when all=true.
func listForIdentity(db *sql.DB, s busSender, all bool) ([]*flowdb.BusMessage, error) {
	if !all {
		return pendingForIdentity(db, s)
	}
	if s.TaskSlug != "" {
		return flowdb.AllForTask(db, s.TaskSlug)
	}
	return flowdb.AllForHuman(db, s.Assignee)
}

// popOne atomically claims the oldest pending message for the identity:
// posts and session-directed rows become delivered; human-directed
// messages become acked (popping IS answering). Rows lost to a
// concurrent consumer are skipped. Returns nil when nothing claimable.
//
// keepUnread is the reader/relay mode: it claims the row to delivered
// for EVERY kind (so a loop won't re-return it) but never acks — a
// human-directed message stays unanswered/unread in the mail model, so
// a forwarder can wake on it and pass it along without consuming the
// user's answer. The whole point of `pop --wait --keep-unread`.
func popOne(db *sql.DB, s busSender, keepUnread bool) (*flowdb.BusMessage, error) {
	rows, err := pendingForIdentity(db, s)
	if err != nil {
		return nil, err
	}
	for _, m := range rows {
		var claimed bool
		switch {
		case keepUnread:
			claimed, err = flowdb.ClaimDelivered(db, m.ID)
			if claimed {
				m.Status = "delivered"
			}
		case m.Kind == "message" && m.ToTaskSlug == "":
			claimed, err = flowdb.ClaimAcked(db, m, "pop")
		default:
			claimed, err = flowdb.ClaimDelivered(db, m.ID)
		}
		if err != nil {
			return nil, err
		}
		if claimed {
			return m, nil
		}
	}
	return nil, nil
}

func printBusMessage(db *sql.DB, m *flowdb.BusMessage) {
	now := time.Now()
	fmt.Printf("[%s %s] from %s (%s ago): %s\n", m.Kind, m.ID, busFrom(m), busAge(m.CreatedAt, now), m.Body)
	if line := replyToLine(db, m); line != "" {
		fmt.Println(line)
	}
	if m.Urgent {
		fmt.Println("        marked URGENT by the sender")
	}
}

// replyToLine renders the lineage of a reply ("" when the message has no
// parent). It shows the parent id and a body snippet so a routed reply —
// which the bus does not thread — arrives with the context it answers.
func replyToLine(db *sql.DB, m *flowdb.BusMessage) string {
	if m.ReplyTo == "" {
		return ""
	}
	parent, _ := flowdb.GetBusMessageByID(db, m.ReplyTo)
	if parent == nil {
		return fmt.Sprintf("        ↳ in reply to [%s]", m.ReplyTo)
	}
	return fmt.Sprintf("        ↳ in reply to [%s]: %s", m.ReplyTo, busSnippet(parent.Body))
}

// busSnippet trims a body to a short one-line context preview.
func busSnippet(body string) string {
	const max = 60
	if len(body) > max {
		return body[:max] + "…"
	}
	return body
}

func inboxPop(args []string) int {
	fs := flagSet("inbox pop")
	wait := fs.Bool("wait", false, "block until a message arrives, then pop it")
	timeout := fs.Int("timeout", 3600, "seconds --wait blocks before giving up")
	as := consumerFlags(fs)
	asJSON := fs.Bool("json", false, "emit the popped message as JSON")
	keepUnread := fs.Bool("keep-unread", false, "reader/relay mode: return the message without acking it (leave it unread)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	s := resolveConsumer(*as)

	emit := func(m *flowdb.BusMessage) int {
		if *asJSON {
			return emitJSON(toMsgJSON(m))
		}
		printBusMessage(db, m)
		if *keepUnread {
			fmt.Println("kept unread — not acked; ack later with `flow inbox read <id>` once the recipient has answered")
		}
		if *wait {
			fmt.Println("re-arm: point your Monitor tool (or a background shell) at `flow inbox pop --wait` again to catch the next message")
		}
		return 0
	}

	if !*wait {
		m, err := popOne(db, s, *keepUnread)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if m == nil {
			if !*asJSON {
				fmt.Printf("inbox empty (%s)\n", s.identity())
			}
			return 1
		}
		return emit(m)
	}

	if !*asJSON {
		fmt.Printf("waiting for mail as %s (timeout %ds)\n", s.identity(), *timeout)
	}
	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	for {
		m, err := popOne(db, s, *keepUnread)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if m != nil {
			return emit(m)
		}
		if time.Now().After(deadline) {
			if !*asJSON {
				fmt.Println("pop --wait timeout: no messages arrived")
			}
			return 1
		}
		time.Sleep(2 * time.Second)
	}
}

// inboxRead retrieves ONE message by id and marks it read:
//
//	flow inbox read <id> [--json]
//
// Unlike `pop` (oldest-first only), `read` targets any specific message,
// including ones already consumed — it displays those without changing
// them. A still-unread human message (or one a `--keep-unread` reader
// left delivered-but-unanswered) transitions to read/acked; a task or
// broadcast row transitions pending→delivered. This is how a relay acks
// the exact message the human just answered, out of arrival order.
func inboxRead(args []string) int {
	fs := flagSet("inbox read")
	asJSON := fs.Bool("json", false, "emit the message as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow inbox read <id>")
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	m, newly, err := flowdb.ReadMessageByID(db, rest[0], "read")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if m == nil {
		if !*asJSON {
			fmt.Printf("no message [%s]\n", rest[0])
		}
		return 1
	}
	if *asJSON {
		return emitJSON(toMsgJSON(m))
	}
	printBusMessage(db, m)
	if newly {
		fmt.Println("marked read")
	} else {
		fmt.Println("(already read)")
	}
	return 0
}

func inboxStats(args []string) int {
	fs := flagSet("inbox stats")
	as := consumerFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	assignee := resolveConsumer(*as).Assignee
	s, err := flowdb.GetBusStats(db, assignee)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("answered messages : %d\n", s.Acked)
	fmt.Printf("pending messages  : %d\n", s.Pending)
	fmt.Printf("broadcasts on bus : %d\n", s.Broadcasts)
	if s.Acked > 0 {
		fmt.Printf("average wait      : %s\n", fmtBusWait(s.AvgWait))
		fmt.Printf("median wait       : %s\n", fmtBusWait(s.MedWait))
		fmt.Printf("worst wait        : %s\n", fmtBusWait(s.MaxWait))
	}
	return 0
}
