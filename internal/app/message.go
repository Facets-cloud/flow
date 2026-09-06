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

// cmdMessage implements the directed half of the message bus:
//
//	flow message <assignee>[/<task-slug>] "<body>" [--urgent]
//	flow message <assignee> --body "<body>" [--urgent]   (equivalent)
//
// Flags are order-independent and the body may be given positionally or
// via --body/-m/--message; unknown flags are rejected rather than being
// silently swallowed into the body.
//
// A bare assignee (`user` = the local human) addresses a person: the
// message stays pending until answered (their next reply in the sending
// session acks it, or they `flow inbox pop`). `<assignee>/<task-slug>`
// addresses the session bound to that task. A sender can never message
// its own address.
//
// flow draws no attention itself — notification UX is the user's own
// polling of `flow inbox` (see skill references/messaging.md).
func cmdMessage(args []string) int {
	// Tolerant, order-independent parsing so the natural ways agents write
	// this all work: `... <addr> "body" --urgent`, `... <addr> --urgent
	// "body"`, and the named `... <addr> --body "body"` (agents invent
	// --body constantly). Previously the body was a blind positional, so a
	// flag written before it became the literal body ("--urgent") and the
	// real text was silently dropped. Unknown flags now error instead.
	usage := `usage: flow message <assignee>[/<task-slug>] "<body>" [--urgent] [--reply-to <id>]   (body also accepts --body "<text>")`
	var urgent, haveBody, rest bool
	var bodyFlag, replyTo string
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case rest:
			pos = append(pos, a)
		case a == "--":
			rest = true
		case a == "--urgent" || a == "-urgent":
			urgent = true
		case a == "--body" || a == "-body" || a == "--message" || a == "-m" || a == "-b":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --body needs a value")
				return 2
			}
			bodyFlag, haveBody, i = args[i+1], true, i+1
		case strings.HasPrefix(a, "--body="):
			bodyFlag, haveBody = a[len("--body="):], true
		case strings.HasPrefix(a, "--message="):
			bodyFlag, haveBody = a[len("--message="):], true
		case a == "--reply-to" || a == "-reply-to":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --reply-to needs a value")
				return 2
			}
			replyTo, i = args[i+1], i+1
		case strings.HasPrefix(a, "--reply-to="):
			replyTo = a[len("--reply-to="):]
		case a != "-" && strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "error: unknown flag %q — the body is positional or use --body \"...\"; only --urgent/--body/--reply-to are flags\n%s\n", a, usage)
			return 2
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	addr := pos[0]
	var body string
	switch {
	case haveBody:
		body = strings.TrimSpace(bodyFlag)
		if len(pos) > 1 {
			fmt.Fprintln(os.Stderr, "error: body given twice (a positional and --body) — use one")
			return 2
		}
	case len(pos) >= 2:
		body = strings.TrimSpace(pos[1])
		if len(pos) > 2 {
			fmt.Fprintln(os.Stderr, `error: too many arguments — quote the whole body: flow message <addr> "your message" [--urgent]`)
			return 2
		}
	}
	if body == "" {
		fmt.Fprintln(os.Stderr, "error: empty message body\n"+usage)
		return 2
	}
	if len(body) > busBodyMax {
		fmt.Fprintf(os.Stderr, "error: message body is %d chars (max %d) — messages are short; mention a task slug or update-file path in the body for context\n",
			len(body), busBodyMax)
		return 2
	}

	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	toAssignee, toSlug, err := parseBusAddress(db, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	s := currentBusSender()
	if toSlug != "" && toSlug == s.TaskSlug {
		fmt.Fprintf(os.Stderr, "error: %s/%s is THIS session's own inbox — you are the one who would pop it. Message `user` for the human, or another task's address.\n", toAssignee, toSlug)
		return 2
	}
	if toSlug == "" && s.TaskSlug == "" && toAssignee == s.Assignee {
		fmt.Fprintln(os.Stderr, "error: that is your own queue — you would be messaging yourself. Use a task update or note instead.")
		return 2
	}
	if replyTo != "" {
		// Validate the parent exists so a typo'd lineage id is caught at
		// send time rather than silently dangling on the receiver.
		parent, err := flowdb.GetBusMessageByID(db, replyTo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if parent == nil {
			fmt.Fprintf(os.Stderr, "error: no message %q to reply to\n", replyTo)
			return 2
		}
	}

	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: s.Assignee, FromTaskSlug: s.TaskSlug, SenderSessionID: s.SessionID,
		ToAssignee: toAssignee, ToTaskSlug: toSlug, Body: body, Urgent: urgent,
		ReplyTo: replyTo,
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if toSlug == "" {
		if toAssignee != busUser {
			fmt.Printf("note: no cross-user transport yet — %q will see this only on this machine\n", toAssignee)
		}
		fmt.Printf("messaged %s [%s] — pending until answered (their reply in this session, or `flow inbox pop`)\n",
			toAssignee, m.ID)
	} else {
		fmt.Printf("messaged session %s/%s [%s] — delivered on its next activity or pop\n",
			toAssignee, toSlug, m.ID)
	}
	_ = flowdb.SweepBus(db, time.Now())
	return 0
}

// busUser is the reserved assignee for the local human ("user" — the
// person driving flow). Tasks with a NULL assignee belong to them;
// multi-user addressing rides on tasks.assignee. Deliberately not
// "self": that spelling confused agents into reading `message self` as
// talking to themselves.
const busUser = "user"

const busBodyMax = 200

func openBusDB() (*sql.DB, error) {
	path, err := flowDBPath()
	if err != nil {
		return nil, err
	}
	return flowdb.OpenDB(path)
}

func newBusID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b)
}

// busSender describes who/where a bus command runs.
type busSender struct {
	Assignee  string // always "self" locally; workspace transport may widen this
	TaskSlug  string // bound flow task, "" if unbound
	SessionID string
}

func currentBusSender() busSender {
	return busSender{
		Assignee:  busUser,
		TaskSlug:  lookupBoundTaskSlug(),
		SessionID: currentSessionID(),
	}
}

// busIdentity is the address the current invocation consumes as:
// "self/<task-slug>" in a bound session, else "self" (the human).
func (s busSender) identity() string {
	if s.TaskSlug != "" {
		return s.Assignee + "/" + s.TaskSlug
	}
	return s.Assignee
}

// parseBusAddress resolves an address string to (assignee, taskSlug).
// taskSlug == "" means the human.
func parseBusAddress(db *sql.DB, addr string) (string, string, error) {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		assignee, slug := addr[:i], addr[i+1:]
		if assignee == "" || slug == "" {
			return "", "", fmt.Errorf("bad address %q (want <assignee>[/<task-slug>])", addr)
		}
		t, err := flowdb.GetTask(db, slug)
		if err != nil || t == nil {
			return "", "", fmt.Errorf("no task %q for address %q", slug, addr)
		}
		if err := deliverableTask(t); err != nil {
			return "", "", err
		}
		return assignee, slug, nil
	}
	// Bare token: a task slug is sugar for self/<slug>; else an assignee.
	if t, _ := flowdb.GetTask(db, addr); t != nil {
		if err := deliverableTask(t); err != nil {
			return "", "", err
		}
		return busUser, addr, nil
	}
	return addr, "", nil
}

// deliverableTask rejects addressing a session that can never read the
// message: done/archived tasks had their bus footprint cleaned at
// close-out and will not bind a session again — a row addressed there
// would be pending forever while the sender believes it was delivered.
func deliverableTask(t *flowdb.Task) error {
	if t.Status == "done" {
		return fmt.Errorf("task %q is done — its session will never pop this; message the human instead (flow message self ...)", t.Slug)
	}
	if t.ArchivedAt.Valid {
		return fmt.Errorf("task %q is archived — its session will never pop this; message the human instead (flow message self ...)", t.Slug)
	}
	return nil
}

func busFrom(m *flowdb.BusMessage) string {
	if m.FromTaskSlug != "" {
		return m.FromTaskSlug
	}
	return m.FromAssignee
}

func busAge(createdAt string, now time.Time) string {
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

func fmtBusWait(s float64) string {
	return busAge(time.Now().Add(-time.Duration(s*float64(time.Second))).Format(time.RFC3339), time.Now())
}
