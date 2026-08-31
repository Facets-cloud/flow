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
// A bare assignee (e.g. `self`) addresses the human: the message stays
// pending, on an escalating notify schedule (`flow inbox due`), until
// the user answers (their next reply in the sending session acks it,
// or `flow inbox ack`/`pop`). `<assignee>/<task-slug>` addresses the
// session bound to that task: delivered into its context via hooks or
// `flow inbox pop`, no escalation.
//
// flow draws no attention itself — notification UX is user scripting
// on top of `flow inbox due` (see skill references/messaging.md).
func cmdMessage(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: flow message <assignee>[/<task-slug>] "<body>" [--urgent]`)
		return 2
	}
	addr, body := args[0], strings.TrimSpace(args[1])
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

	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: s.Assignee, FromTaskSlug: s.TaskSlug, SenderSessionID: s.SessionID,
		ToAssignee: toAssignee, ToTaskSlug: toSlug, Body: body, Urgent: *urgent,
	}
	if toSlug == "" {
		// Human-directed: due for notification immediately, then backoff.
		m.NextNotifyAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if toSlug == "" {
		if toAssignee != busSelf {
			fmt.Printf("note: no cross-user transport yet — %q will see this only on this machine\n", toAssignee)
		}
		fmt.Printf("messaged %s [%s] — pending until answered (their reply in this session, `flow inbox pop`, or `flow inbox ack`)\n",
			toAssignee, m.ID)
	} else {
		fmt.Printf("messaged session %s/%s [%s] — delivered on its next activity or pop\n",
			toAssignee, toSlug, m.ID)
	}
	_ = flowdb.SweepBus(db, time.Now())
	return 0
}

// busSelf is the assignee name for the local user. Tasks with a NULL
// assignee belong to self; multi-user addressing rides on tasks.assignee.
const busSelf = "self"

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
		Assignee:  busSelf,
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
		if t, err := flowdb.GetTask(db, slug); err != nil || t == nil {
			return "", "", fmt.Errorf("no task %q for address %q", slug, addr)
		}
		return assignee, slug, nil
	}
	// Bare token: a task slug is sugar for self/<slug>; else an assignee.
	if t, _ := flowdb.GetTask(db, addr); t != nil {
		return busSelf, addr, nil
	}
	return addr, "", nil
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
