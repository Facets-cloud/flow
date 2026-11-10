package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"flow/internal/connector"
	"flow/internal/flowdb"
)

// Peer hubs: other flow homes in this hub's contact book, reached through
// an installed connector with role "peer". A peer is a local nickname for
// an opaque endpoint (`at`) that only that connector interprets. See the
// skill reference references/peers.md.
//
//	hub A                                   hub B
//	flow message bob "ship?" ──► row(peer=bob, delivery=dispatched)
//	   └ detached dispatch ──► A's peer connector `run send`
//	                              └──► B: flow peer deliver --at <A> --ref <idA>
//	                                         (B's connector token) ──► B's user mail
//	B: flow human reply <idB> "Yes" ──► row(peer=alice) ──► … ──►
//	   A: flow peer deliver --ref <idB> --reply-to-ref <idA> ──► A's user/<task>

const peerUsage = `usage: flow peer <add|list|show|rm|block|unblock> ...
  flow peer add <name> --via <connector> --at <endpoint>
                                  add another flow hub to your contact book (the
                                  connector must be installed with role "peer";
                                  <endpoint> is opaque — only the connector reads it)
  flow peer list                  your peers
  flow peer show <name>           one peer's details
  flow peer rm <name>             forget a peer
  flow peer block|unblock <name>  refuse / accept its inbound messages
  (message a peer: flow message <name> "<body>" — never <name>/<task>)`

var peerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// cmdPeer implements `flow peer ...`.
func cmdPeer(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, peerUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return cmdPeerAdd(args[1:])
	case "list", "ls":
		return cmdPeerList(args[1:])
	case "show":
		return cmdPeerShow(args[1:])
	case "rm", "remove":
		return cmdPeerRemove(args[1:])
	case "block":
		return cmdPeerBlock(args[1:], true)
	case "unblock":
		return cmdPeerBlock(args[1:], false)
	case "deliver":
		return cmdPeerDeliver(args[1:])
	case "-h", "--help", "help":
		fmt.Println(peerUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown peer subcommand %q\n%s\n", args[0], peerUsage)
		return 2
	}
}

// validPeerName rejects names that could collide with local addresses.
func validPeerName(name string) error {
	if isReservedBusName(name) {
		return fmt.Errorf("%q is reserved for the local human", name)
	}
	if !peerNameRe.MatchString(name) {
		return fmt.Errorf("peer name %q must be letters, digits, '.', '-' or '_' (no '/')", name)
	}
	return nil
}

// peerConnector returns the installed connector name with role "peer",
// or an error saying why it is not usable.
func peerConnector(name string) (*connector.Manifest, error) {
	for _, m := range installedConnectors() {
		if m.Name != name {
			continue
		}
		if !m.HasRole(connector.RolePeer) {
			return nil, fmt.Errorf("connector %q has no role \"peer\" (roles: %v) — it cannot reach other hubs", name, m.EffectiveRoles())
		}
		return m, nil
	}
	return nil, fmt.Errorf("no connector %q installed (add one with role \"peer\": flow connector add <dir|sample>)", name)
}

func cmdPeerAdd(args []string) int {
	const usage = "usage: flow peer add <name> --via <connector> --at <endpoint>"
	var name, via, at string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--via", "--at":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "error: %s needs a value\n%s\n", a, usage)
				return 2
			}
			if a == "--via" {
				via = args[i+1]
			} else {
				at = args[i+1]
			}
			i++
		default:
			if name != "" || (len(a) > 0 && a[0] == '-') {
				fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n%s\n", a, usage)
				return 2
			}
			name = a
		}
	}
	if name == "" || via == "" || at == "" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if err := validPeerName(name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	if _, err := peerConnector(via); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if p, _ := flowdb.GetPeer(db, name); p != nil {
		fmt.Fprintf(os.Stderr, "error: peer %q already exists (flow peer rm %s first)\n", name, name)
		return 1
	}
	if p, _ := flowdb.GetPeerByEndpoint(db, via, at); p != nil {
		fmt.Fprintf(os.Stderr, "error: %s endpoint %q is already peer %q\n", via, at, p.Name)
		return 1
	}
	if err := flowdb.AddPeer(db, &flowdb.Peer{Name: name, Connector: via, At: at}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("added peer %s via %s at %s\n", name, via, at)
	return 0
}

func cmdPeerList(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: flow peer list")
		return 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	ps, err := flowdb.ListPeers(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(ps) == 0 {
		fmt.Println("no peers — add another flow hub: flow peer add <name> --via <connector> --at <endpoint>")
		return 0
	}
	for _, p := range ps {
		state := ""
		if p.Blocked {
			state = "  BLOCKED"
		}
		seen := p.LastSeenAt
		if seen == "" {
			seen = "never"
		}
		fmt.Printf("%-16s via %-12s at %s  last seen %s%s\n", p.Name, p.Connector, p.At, seen, state)
	}
	return 0
}

// peerArg opens the bus and loads the named peer (rc != 0 on failure,
// message already printed). The caller closes db when rc == 0.
func peerArg(args []string, verb string) (*sql.DB, *flowdb.Peer, int) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: flow peer %s <name>\n", verb)
		return nil, nil, 2
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, nil, 1
	}
	p, err := flowdb.GetPeer(db, args[0])
	if err != nil {
		db.Close()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, nil, 1
	}
	if p == nil {
		db.Close()
		fmt.Fprintf(os.Stderr, "error: no peer %q (flow peer list)\n", args[0])
		return nil, nil, 1
	}
	return db, p, 0
}

func cmdPeerShow(args []string) int {
	db, p, rc := peerArg(args, "show")
	if rc != 0 {
		return rc
	}
	defer db.Close()
	yn := map[bool]string{true: "yes", false: "no"}
	seen := p.LastSeenAt
	if seen == "" {
		seen = "never"
	}
	fmt.Printf("peer:       %s\n", p.Name)
	fmt.Printf("via:        %s\n", p.Connector)
	fmt.Printf("at:         %s\n", p.At)
	fmt.Printf("blocked:    %s\n", yn[p.Blocked])
	fmt.Printf("added:      %s\n", p.CreatedAt)
	fmt.Printf("last seen:  %s\n", seen)
	if _, err := peerConnector(p.Connector); err != nil {
		fmt.Printf("warning:    %v\n", err)
	}
	recent, err := flowdb.RecentPeerMessages(db, p.Name, 5)
	if err == nil && len(recent) > 0 {
		fmt.Println("recent:")
		for _, m := range recent {
			dir, state := "←", ""
			if m.IsPeerOutbound() {
				dir, state = "→", " ["+m.Delivery+"]"
				if m.DeliveryError != "" {
					state = " [" + m.Delivery + ": " + m.DeliveryError + "]"
				}
			}
			fmt.Printf("  %s %s %s %s%s\n", dir, m.ID, m.CreatedAt, oneLine(m.Body, 60), state)
		}
	}
	return 0
}

// oneLine flattens and truncates a body for one-line listings.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

func cmdPeerRemove(args []string) int {
	db, p, rc := peerArg(args, "rm")
	if rc != 0 {
		return rc
	}
	defer db.Close()
	if _, err := flowdb.DeletePeer(db, p.Name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("removed peer %s\n", p.Name)
	return 0
}

func cmdPeerBlock(args []string, block bool) int {
	verb := map[bool]string{true: "block", false: "unblock"}[block]
	db, p, rc := peerArg(args, verb)
	if rc != 0 {
		return rc
	}
	defer db.Close()
	if _, err := flowdb.SetPeerBlocked(db, p.Name, block); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if block {
		fmt.Printf("blocked peer %s — its inbound messages are rejected\n", p.Name)
	} else {
		fmt.Printf("unblocked peer %s\n", p.Name)
	}
	return 0
}

// sendToPeer stores an outbound row to peer p and spawns the detached
// dispatch that hands it to p's connector. Human-role connectors are
// never involved. Called by cmdMessage once addr resolved to a peer.
func sendToPeer(db *sql.DB, p *flowdb.Peer, s busSender, body string, urgent, fyi bool, options []string, replyTo string) int {
	if replyTo != "" {
		parent, err := flowdb.GetBusMessageByID(db, replyTo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if parent == nil {
			fmt.Fprintf(os.Stderr, "error: no message %q to reply to\n", replyTo)
			return 2
		}
		if parent.IsPeerInbound() && parent.Peer != p.Name {
			fmt.Fprintf(os.Stderr, "error: %s came from peer %s, not %s — reply to %s instead\n", replyTo, parent.Peer, p.Name, parent.Peer)
			return 2
		}
	}
	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: s.Assignee, FromTaskSlug: s.TaskSlug, SenderSessionID: s.SessionID,
		ToAssignee: p.Name, Body: body, Urgent: urgent, ReplyTo: replyTo, Options: options,
		FYI: fyi, Peer: p.Name, Delivery: flowdb.DeliveryDispatched,
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	dispatchPeer(db, m)
	fmt.Printf("messaged peer %s [%s] via %s — delivery dispatched (flow peer show %s)\n", p.Name, m.ID, p.Connector, p.Name)
	return 0
}

// dispatchPeer spawns the detached sender for an outbound peer row; a
// spawn failure marks the row failed (never the caller's error).
func dispatchPeer(db *sql.DB, m *flowdb.BusMessage) {
	if err := dispatchLauncher(m.ID); err != nil {
		logDispatch("%s peer %s spawn-error %v", m.ID, m.Peer, err)
		_ = flowdb.SetDelivery(db, m.ID, flowdb.DeliveryFailed, "spawn: "+err.Error())
	}
}

// peerSend runs inside the detached `flow connector dispatch <id>` for an
// outbound peer row: one `run send` on the peer's connector. Exit 0 →
// delivery=sent; non-zero, timeout or a missing peer/connector →
// delivery=failed with the error. No auto-retry in v0.
func peerSend(db *sql.DB, m *flowdb.BusMessage) int {
	if m.Delivery == flowdb.DeliverySent {
		logDispatch("%s peer %s skip already sent", m.ID, m.Peer)
		return 0
	}
	fail := func(err error) int {
		logDispatch("%s peer %s fail %v", m.ID, m.Peer, err)
		_ = flowdb.SetDelivery(db, m.ID, flowdb.DeliveryFailed, err.Error())
		return 0
	}
	p, err := flowdb.GetPeer(db, m.Peer)
	if err != nil {
		return fail(err)
	}
	if p == nil {
		return fail(fmt.Errorf("no peer %q", m.Peer))
	}
	c, err := peerConnector(p.Connector)
	if err != nil {
		return fail(err)
	}
	pp := connector.PeerPayload{
		ID: m.ID, Peer: p.Name, At: p.At, Kind: "ask", Body: m.Body, Options: m.Options,
		Urgent: m.Urgent, CreatedAt: m.CreatedAt,
	}
	if pp.Options == nil {
		pp.Options = []string{}
	}
	if m.FYI {
		pp.Kind = "fyi"
	}
	if m.ReplyTo != "" {
		if parent, _ := flowdb.GetBusMessageByID(db, m.ReplyTo); parent != nil &&
			parent.IsPeerInbound() && parent.Peer == p.Name {
			pp.Kind, pp.ReplyToRef = "reply", parent.OriginRef
		}
	}
	payload, env, err := connectorCall(c, pp)
	if err != nil {
		return fail(err)
	}
	out, err := runPeerConnector(c, connector.VerbSend, payload, env, connectorTimeout)
	if err != nil {
		return fail(err)
	}
	var res struct {
		RemoteRef string `json:"remote_ref"`
	}
	_ = json.Unmarshal(out, &res) // optional, debugging only
	logDispatch("%s peer %s via %s ok remote_ref=%s", m.ID, p.Name, c.Name, res.RemoteRef)
	if err := flowdb.SetDelivery(db, m.ID, flowdb.DeliverySent, ""); err != nil {
		logDispatch("%s peer %s error mark sent: %v", m.ID, p.Name, err)
	}
	return 0
}

// logInbound appends one line to $FLOW_ROOT/connectors/inbound.log —
// rejected inbound deliveries (unknown endpoint, blocked peer).
// Best-effort: logging must never fail the caller.
func logInbound(format string, a ...any) {
	root, err := connectorsRoot()
	if err != nil {
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(root, "inbound.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

const deliverUsage = `usage: flow peer deliver --at <endpoint> --ref <their-id> [--reply-to-ref <our-id>]
                         [--to <task>] [--kind ask|fyi] [--options "A|B"] "<body>"   (connectors only)`

// cmdPeerDeliver is the only inbound door from another hub, callable only
// by an installed connector (FLOW_CONNECTOR_TOKEN):
//
//	token → connector C ── none/invalid ──► exit 1, nothing stored
//	(C, at) → peer P ───── unknown ──► exit 1 + connectors/inbound.log
//	                       blocked ──► exit 1 "rejected"
//	(P, ref) seen before ──────────► exit 0, no-op (dedupe)
//	--to X not an open task here ──► exit 1, nothing stored
//	routing (the connector chooses X; flow keeps no peer→task map):
//	  reply to our row → task T, T open        ──► user/T, reply_to=<our-id>
//	  reply to our row → T closed/gone, --to X ──► user/X, reply_to kept
//	  reply to our row → T closed/gone, no --to──► human mail, labelled
//	                                               "answer to <our-id>"
//	  not a reply (or unknown ref), --to X     ──► user/X
//	  not a reply, no --to                     ──► the human's mail
//	task mail never pings the human; human mail pings unless --kind fyi
//	P.last_seen_at = now
func cmdPeerDeliver(args []string) int {
	var at, ref, replyToRef, kind, optionsFlag, toTask string
	var haveOptions bool
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--at", "--ref", "--reply-to-ref", "--kind", "--options", "--to":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "error: %s needs a value\n%s\n", a, deliverUsage)
				return 2
			}
			v := args[i+1]
			i++
			switch a {
			case "--at":
				at = v
			case "--ref":
				ref = v
			case "--reply-to-ref":
				replyToRef = v
			case "--kind":
				kind = v
			case "--options":
				optionsFlag, haveOptions = v, true
			case "--to":
				toTask = v
			}
		case "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				fmt.Fprintf(os.Stderr, "error: unknown flag %q\n%s\n", a, deliverUsage)
				return 2
			}
			pos = append(pos, a)
		}
	}
	if at == "" || ref == "" || len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		fmt.Fprintln(os.Stderr, deliverUsage)
		return 2
	}
	body := strings.TrimSpace(pos[0])
	if len(body) > humanReplyMax {
		fmt.Fprintf(os.Stderr, "error: body is %d chars (max %d)\n", len(body), humanReplyMax)
		return 2
	}
	// "reply" is accepted as an alias of ask: the payload kind a sender's
	// connector may pass straight through; --reply-to-ref decides routing.
	fyi := false
	switch kind {
	case "", "ask", "reply":
	case "fyi":
		fyi = true
	default:
		fmt.Fprintf(os.Stderr, "error: --kind %q (want ask or fyi)\n", kind)
		return 2
	}
	var options []string
	if haveOptions {
		if options = parseAskOptions(optionsFlag); len(options) == 0 {
			fmt.Fprintln(os.Stderr, `error: --options has no choices`)
			return 2
		}
		if fyi {
			fmt.Fprintln(os.Stderr, "error: --kind fyi takes no --options")
			return 2
		}
	}

	conn := callingConnector()
	if conn == "" {
		fmt.Fprintln(os.Stderr, "error: not a connector — flow peer deliver needs FLOW_CONNECTOR_TOKEN from an installed connector")
		return 1
	}
	db, err := openBusDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	p, err := flowdb.GetPeerByEndpoint(db, conn, at)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if p == nil {
		logInbound("%s unknown at=%q ref=%s (not a peer — flow peer add <name> --via %s --at <endpoint>)", conn, at, ref, conn)
		fmt.Fprintf(os.Stderr, "error: no peer at %s endpoint %q\n", conn, at)
		return 1
	}
	if p.Blocked {
		logInbound("%s blocked peer=%s ref=%s", conn, p.Name, ref)
		fmt.Fprintf(os.Stderr, "error: rejected — peer %s is blocked\n", p.Name)
		return 1
	}
	if dup, err := flowdb.InboundByRef(db, p.Name, ref); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	} else if dup != nil {
		fmt.Printf("duplicate %s from %s — already delivered [%s]\n", ref, p.Name, dup.ID)
		return 0
	}
	if toTask != "" {
		t, err := flowdb.GetTask(db, toTask)
		if err != nil || t == nil {
			fmt.Fprintf(os.Stderr, "error: --to %s: no such task on this hub — nothing stored\n", toTask)
			return 1
		}
		if err := deliverableTask(t); err != nil {
			fmt.Fprintf(os.Stderr, "error: --to %s: %v — nothing stored\n", toTask, err)
			return 1
		}
	}

	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: p.Name, ToAssignee: busUser, Body: body, Options: options, FYI: fyi,
		Peer: p.Name, OriginRef: ref,
	}
	routeTo := toTask
	if replyToRef != "" {
		if ours, _ := flowdb.GetBusMessageByID(db, replyToRef); ours != nil &&
			ours.IsPeerOutbound() && ours.Peer == p.Name {
			m.ReplyTo = ours.ID
			if openTask(db, ours.FromTaskSlug) {
				routeTo = ours.FromTaskSlug
			} else if routeTo == "" {
				m.Body = fmt.Sprintf("[answer to %s] %s", ours.ID, body)
			}
		}
	}
	if routeTo != "" {
		// Task mail: delivered into that session, never a human ping.
		m.ToTaskSlug = routeTo
		m.Options, m.FYI = nil, false
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		if flowdb.IsUniqueViolation(err) {
			fmt.Printf("duplicate %s from %s — already delivered\n", ref, p.Name)
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	_ = flowdb.TouchPeerSeen(db, p.Name)
	maybeDispatch(m)
	fmt.Printf("delivered %s from %s → %s [%s]\n", ref, p.Name, recipientAddr(m), m.ID)
	return 0
}

// openTask reports whether slug names a task whose session can still
// read mail (exists, not done, not archived).
func openTask(db *sql.DB, slug string) bool {
	if slug == "" {
		return false
	}
	t, err := flowdb.GetTask(db, slug)
	return err == nil && t != nil && deliverableTask(t) == nil
}
