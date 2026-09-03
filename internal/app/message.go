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
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: flow message <assignee>[/<task-slug>] "<body>" [--urgent]`)
		return 2
	}
	addr, body := args[0], strings.TrimSpace(args[1])
	if strings.HasPrefix(addr, "-") {
		// A flag in the address slot would otherwise become a bogus
		// assignee queue nobody ever reads. Positionals come first.
		fmt.Fprintln(os.Stderr, `usage: flow message <assignee>[/<task-slug>] "<body>" [--urgent] (address and body before flags)`)
		return 2
	}
	fs := flagSet("message")
	urgent := fs.Bool("urgent", false, "mark as urgent (notifier scripts may escalate harder)")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	if body == "" {
		fmt.Fprintln(os.Stderr, "error: empty message body")
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

	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: s.Assignee, FromTaskSlug: s.TaskSlug, SenderSessionID: s.SessionID,
		ToAssignee: toAssignee, ToTaskSlug: toSlug, Body: body, Urgent: *urgent,
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
