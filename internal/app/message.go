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
// session acks it, or they `flow inbox read <id>` from their terminal). `<assignee>/<task-slug>`
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
	usage := `usage: flow message <assignee>[/<task-slug>] "<body>" [--urgent] [--reply-to <id>] [--options "A|B"] [--fyi] [--attach <file> ...]   (body also accepts --body "<text>")`
	var urgent, haveBody, rest, fyi, haveOptions bool
	var bodyFlag, replyTo, optionsFlag string
	var pos, attach []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case rest:
			pos = append(pos, a)
		case a == "--":
			rest = true
		case isHelpArg(a):
			fmt.Fprintln(os.Stderr, usage)
			return 0
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
		case a == "--fyi" || a == "-fyi":
			fyi = true
		case a == "--options" || a == "-options":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, `error: --options needs a value, e.g. --options "Yes|No"`)
				return 2
			}
			optionsFlag, haveOptions, i = args[i+1], true, i+1
		case strings.HasPrefix(a, "--options="):
			optionsFlag, haveOptions = a[len("--options="):], true
		case a == "--attach" || a == "-attach":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --attach needs a file path")
				return 2
			}
			attach, i = append(attach, args[i+1]), i+1
		case strings.HasPrefix(a, "--attach="):
			attach = append(attach, a[len("--attach="):])
		case a != "-" && strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "error: unknown flag %q — the body is positional or use --body \"...\"; only --urgent/--body/--reply-to/--options/--fyi/--attach are flags\n%s\n", a, usage)
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
	// Peer mail may be longer (peerBodyMax); the local cap is applied
	// once the address is known not to be a peer.
	if len(body) > peerBodyMax {
		return bodyTooLong(body, peerBodyMax)
	}

	var options []string
	if haveOptions {
		if options = parseAskOptions(optionsFlag); len(options) == 0 {
			fmt.Fprintln(os.Stderr, `error: --options has no choices — use --options "Yes|No"`)
			return 2
		}
	}
	if fyi && urgent {
		fmt.Fprintln(os.Stderr, "error: --fyi and --urgent contradict — an FYI is never pinged, an urgent ask always is; pick one")
		return 2
	}
	if fyi && haveOptions {
		fmt.Fprintln(os.Stderr, "error: --fyi takes no --options — choices make it an ask; drop --fyi to ask")
		return 2
	}
	srcs, err := checkAttachments(attach)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v — nothing sent\n", err)
		return 2
	}

	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	// A connector process (no bound task, holding its installed secret in
	// FLOW_CONNECTOR_TOKEN) may notify the human — and only the human.
	s := currentBusSender()
	if s.TaskSlug == "" {
		if name := callingConnector(); name != "" {
			if canonicalAssignee(addr) != busUser {
				fmt.Fprintf(os.Stderr, "error: a connector may only notify the human (flow message user ...), not %q\n", addr)
				return 2
			}
			if haveOptions {
				fmt.Fprintln(os.Stderr, "error: a connector notifies, it cannot ask — drop --options (an answer would have nowhere to go)")
				return 2
			}
			s = busSender{Assignee: connectorSenderPrefix + name}
		}
	}

	// A bare name that is a peer goes to that hub; <peer>/<task> is never
	// valid (another hub's task names are unknowable).
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		if p, _ := flowdb.GetPeer(db, addr[:i]); p != nil {
			fmt.Fprintf(os.Stderr, "error: %q is a peer hub — its task names are unknowable; message the peer itself: flow message %s \"...\"\n", p.Name, p.Name)
			return 2
		}
	} else if p, _ := flowdb.GetPeer(db, addr); p != nil {
		return sendToPeer(db, p, s, body, urgent, fyi, options, replyTo, srcs)
	}
	if len(body) > busBodyMax {
		return bodyTooLong(body, busBodyMax)
	}

	toAssignee, toSlug, err := parseBusAddress(db, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	if toSlug != "" && (fyi || haveOptions) {
		fmt.Fprintln(os.Stderr, "error: --options/--fyi are for human-addressed mail only (flow message user ...), not a task's session")
		return 2
	}
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
		ReplyTo: replyTo, Options: options, FYI: fyi,
	}
	if err := insertWithAttachments(db, m, srcs); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	maybeDispatch(m)
	if toSlug == "" {
		if toAssignee != busUser {
			fmt.Printf("note: no cross-user transport yet — %q will see this only on this machine\n", toAssignee)
		}
		fmt.Printf("messaged %s [%s] — pending until answered (their reply in this session, or `flow inbox read <id>`)\n",
			toAssignee, m.ID)
	} else {
		fmt.Printf("messaged session %s/%s [%s] — delivered on its next activity or pop\n",
			toAssignee, toSlug, m.ID)
	}
	_ = flowdb.SweepBus(db, time.Now())
	pruneAttachments(db)
	return 0
}

// parseAskOptions splits `--options "A|B|C"` into trimmed, non-empty
// choices.
func parseAskOptions(s string) []string {
	var out []string
	for _, o := range strings.Split(s, "|") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// busUser is the reserved assignee for the local human ("user" — the
// person driving flow). Tasks with a NULL assignee belong to them;
// multi-user addressing rides on tasks.assignee. Deliberately not
// "self": agents misread that legacy spelling as talking to themselves
// (it is still accepted as an alias, see busSelfAlias).
const busUser = flowdb.BusUser

const busBodyMax = 500

// peerBodyMax caps a message to another hub (`flow message <peer>`) and
// an inbound `flow peer deliver` body: it may carry a human's own words,
// so it shares the human-reply cap. The local bus stays at busBodyMax.
const peerBodyMax = humanReplyMax

func bodyTooLong(body string, max int) int {
	fmt.Fprintf(os.Stderr, "error: message body is %d chars (max %d) — messages are short; mention a task slug or update-file path in the body for context\n",
		len(body), max)
	return 2
}

// busSelfAlias is the legacy spelling of busUser. It is accepted wherever
// an address or --as identity is read, and always means the human.
const busSelfAlias = "self"

// isReservedBusName reports whether a name is a reserved bus address that
// must never be a task slug (a task slugged `user` would capture the
// human's mail).
func isReservedBusName(s string) bool {
	return s == busUser || s == busSelfAlias
}

// canonicalAssignee maps the legacy `self` spelling to `user`.
func canonicalAssignee(a string) string {
	if a == busSelfAlias {
		return busUser
	}
	return a
}

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
	Assignee  string // always "user" locally; workspace transport may widen this
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

// consumer is the flowdb.Consumer record for a state change by s.
func (s busSender) consumer(by string) flowdb.Consumer {
	return flowdb.Consumer{By: by, Identity: s.identity(), Session: s.SessionID}
}

// identity is the address the current invocation consumes as:
// "user/<task-slug>" in a bound session, else "user" (the human).
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
		assignee, slug := canonicalAssignee(addr[:i]), addr[i+1:]
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
	// A bare token is ALWAYS an assignee (a person's queue); sessions are
	// addressed only as <assignee>/<task-slug>. Treating a bare token as a
	// task slug first let a task slugged after a person capture their mail.
	return canonicalAssignee(addr), "", nil
}

// deliverableTask rejects addressing a session that can never read the
// message: done/archived tasks had their bus footprint cleaned at
// close-out and will not bind a session again — a row addressed there
// would be pending forever while the sender believes it was delivered.
func deliverableTask(t *flowdb.Task) error {
	if t.Status == "done" {
		return fmt.Errorf("task %q is done — its session will never pop this; message the human instead (flow message user ...)", t.Slug)
	}
	if t.ArchivedAt.Valid {
		return fmt.Errorf("task %q is archived — its session will never pop this; message the human instead (flow message user ...)", t.Slug)
	}
	return nil
}

// connectorSenderPrefix marks from_assignee on mail a connector sent the
// human: "connector:<name>".
const connectorSenderPrefix = "connector:"

// fromConnector returns the connector that sent m, or "".
func fromConnector(m *flowdb.BusMessage) string {
	if m.FromTaskSlug == "" && strings.HasPrefix(m.FromAssignee, connectorSenderPrefix) {
		return strings.TrimPrefix(m.FromAssignee, connectorSenderPrefix)
	}
	return ""
}

func busFrom(m *flowdb.BusMessage) string {
	if m.FromTaskSlug != "" {
		return m.FromTaskSlug
	}
	if c := fromConnector(m); c != "" {
		return "connector " + c
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
