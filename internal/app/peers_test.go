package app

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"flow/internal/connector"
	"flow/internal/flowdb"
)

// Peer hubs v0 — one block per behavior of the spec.

// installRoleConnector writes a contract-2 connector with the given roles
// (JSON list body, e.g. `"peer"`) straight into $FLOW_ROOT/connectors and
// stores its secret. Returns the folder.
func installRoleConnector(t *testing.T, name, roles, events, script string) string {
	t.Helper()
	root, err := connectorsRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","contract":2,"roles":[` + roles + `],"exec":"./run","capabilities":["send"],"events":[` + events + `]}`
	if err := os.WriteFile(filepath.Join(dir, "connector.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withSecret(t, name)
	return dir
}

// P2: human pings (#119 + #123 tiers/escalation) go only to role "human";
// a peer-only connector never sees a human ask.
func TestPeerRoleNeverGetsHumanPings(t *testing.T) {
	marks := prioSetup(t)
	markerConnector(t, marks, "desk", `"ask","urgent"`, 0) // contract 1 = human
	installRoleConnector(t, "pl", `"peer"`, ``,
		"cat >/dev/null; [ \"$1\" = send ] && echo x >> "+filepath.Join(marks, "pl"))
	installRoleConnector(t, "duo", `"human","peer"`, `"ask"`,
		"cat >/dev/null; [ \"$1\" = send ] && echo x >> "+filepath.Join(marks, "duo"))

	ask := askRow(t, "ship?")
	dispatch(t, ask.ID)
	if fired(marks, "desk") != 1 || fired(marks, "duo") != 1 {
		t.Errorf("human connectors desk=%d duo=%d, want 1/1", fired(marks, "desk"), fired(marks, "duo"))
	}
	if fired(marks, "pl") != 0 {
		t.Errorf("peer-only connector was pinged for a human ask")
	}
}

// P2: with only a peer-role connector installed, a pinging send spawns no
// human dispatch (exactly as with no connectors).
func TestPeerOnlyConnectorSpawnsNoHumanDispatch(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")
	spawned := stubDispatchLauncher(t, nil)
	installRoleConnector(t, "pl", `"peer"`, ``, "exit 0")
	if rc := msgRC(t, "user", "anyone?", "--urgent"); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(*spawned) != 0 {
		t.Errorf("human dispatch spawned with only a peer connector: %v", *spawned)
	}
	out := captureStdout(t, func() { _ = cmdConnector([]string{"list"}) })
	if !strings.Contains(out, "roles=peer") {
		t.Errorf("connector list lacks roles:\n%s", out)
	}
}

func peerRC(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var rc int
	out := captureStdout(t, func() { rc = cmdPeer(args) })
	return rc, out
}

// P3: flow peer add | list | show | rm | block | unblock.
func TestPeerCRUDCommands(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	installRoleConnector(t, "pl", `"peer"`, ``, "exit 0")
	installFakeConnector(t, "desk", `"ask"`, "exit 0") // contract 1 = human only
	withSecret(t, "desk")

	if rc, out := peerRC(t, "add", "bob", "--via", "pl", "--at", "/homes/b"); rc != 0 {
		t.Fatalf("add rc=%d %s", rc, out)
	}
	for name, c := range map[string]struct {
		args []string
		rc   int
	}{
		"human-only connector": {[]string{"add", "carol", "--via", "desk", "--at", "x"}, 1},
		"not installed":        {[]string{"add", "carol", "--via", "nope", "--at", "x"}, 1},
		"duplicate name":       {[]string{"add", "bob", "--via", "pl", "--at", "/homes/other"}, 1},
		"duplicate endpoint":   {[]string{"add", "robert", "--via", "pl", "--at", "/homes/b"}, 1},
		"reserved user":        {[]string{"add", "user", "--via", "pl", "--at", "y"}, 2},
		"reserved self":        {[]string{"add", "self", "--via", "pl", "--at", "y"}, 2},
		"slash in name":        {[]string{"add", "a/b", "--via", "pl", "--at", "y"}, 2},
		"no --via":             {[]string{"add", "dan", "--at", "y"}, 2},
		"no --at":              {[]string{"add", "dan", "--via", "pl"}, 2},
		"no name":              {[]string{"add", "--via", "pl", "--at", "y"}, 2},
		"show unknown":         {[]string{"show", "nobody"}, 1},
		"rm unknown":           {[]string{"rm", "nobody"}, 1},
		"block unknown":        {[]string{"block", "nobody"}, 1},
		"bad verb":             {[]string{"frob"}, 2},
		"bare":                 {nil, 2},
	} {
		if rc, out := peerRC(t, c.args...); rc != c.rc {
			t.Errorf("%s: rc=%d want %d (%s)", name, rc, c.rc, out)
		}
	}
	if ps, _ := flowdbListPeers(t); len(ps) != 1 {
		t.Fatalf("peers after rejections = %d, want 1", len(ps))
	}

	if rc, out := peerRC(t, "list"); rc != 0 || !strings.Contains(out, "bob") || !strings.Contains(out, "pl") || !strings.Contains(out, "/homes/b") {
		t.Errorf("list rc=%d:\n%s", rc, out)
	}
	if rc, out := peerRC(t, "block", "bob"); rc != 0 || !strings.Contains(out, "blocked") {
		t.Errorf("block rc=%d %s", rc, out)
	}
	if rc, out := peerRC(t, "show", "bob"); rc != 0 || !strings.Contains(out, "blocked:    yes") || !strings.Contains(out, "via:        pl") {
		t.Errorf("show rc=%d:\n%s", rc, out)
	}
	if rc, _ := peerRC(t, "unblock", "bob"); rc != 0 {
		t.Errorf("unblock rc=%d", rc)
	}
	if rc, out := peerRC(t, "show", "bob"); rc != 0 || !strings.Contains(out, "blocked:    no") {
		t.Errorf("show after unblock:\n%s", out)
	}
	if rc, _ := peerRC(t, "rm", "bob"); rc != 0 {
		t.Errorf("rm rc=%d", rc)
	}
	if rc, out := peerRC(t, "list"); rc != 0 || strings.Contains(out, "bob") {
		t.Errorf("list after rm:\n%s", out)
	}
	_ = db
}

func flowdbListPeers(t *testing.T) ([]*flowdb.Peer, error) {
	t.Helper()
	return flowdb.ListPeers(openFlowDB(t))
}

// peerHub sets up a flow home with a peer-role connector "pl" whose run
// script records stdin to <marks>/pl.<n> and exits rc, plus peer "bob".
func peerHub(t *testing.T, rc string) (marks string) {
	t.Helper()
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "t", "sid-t")
	marks = t.TempDir()
	installRoleConnector(t, "pl", `"peer"`, ``,
		`[ "$1" = send ] || exit 0; n=$(ls `+marks+` | wc -l | tr -d ' '); cat > `+marks+`/pl.$n; printf '{"remote_ref":"r-%s"}' "$n"; echo oops >&2; exit `+rc)
	if rc, out := peerRC(t, "add", "bob", "--via", "pl", "--at", "/homes/b"); rc != 0 {
		t.Fatalf("add peer: %d %s", rc, out)
	}
	return marks
}

func readMark(t *testing.T, marks string, n int) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(marks, "pl."+strconv.Itoa(n)))
	if err != nil {
		t.Fatalf("connector never received send #%d: %v", n, err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload %q: %v", b, err)
	}
	return p
}

// P4: flow message <peer> stores an outbound row and dispatches it to the
// peer's connector; delivery becomes sent (exit 0) or failed (+error).
func TestMessagePeerOutbound(t *testing.T) {
	marks := peerHub(t, "0")
	db := openFlowDB(t)
	spawned := stubDispatchLauncher(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-t")

	rc, out := 0, ""
	out = captureStdout(t, func() { rc = cmdMessage([]string{"bob", "ship?", "--options", "Yes|No"}) })
	if rc != 0 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	m := rowByBody(t, db, "ship?")
	if m.Peer != "bob" || m.ToAssignee != "bob" || m.ToTaskSlug != "" || m.FromTaskSlug != "t" ||
		m.Delivery != "dispatched" || m.Ping || m.OriginRef != "" {
		t.Fatalf("outbound row = %+v", m)
	}
	if strings.Contains(out, "no cross-user transport") || !strings.Contains(out, "peer bob") {
		t.Errorf("send output: %q", out)
	}
	if len(*spawned) != 1 || (*spawned)[0] != m.ID {
		t.Fatalf("dispatch spawned = %v, want [%s]", *spawned, m.ID)
	}

	dispatch(t, m.ID)
	p := readMark(t, marks, 0)
	if p["id"] != m.ID || p["peer"] != "bob" || p["at"] != "/homes/b" || p["kind"] != "ask" ||
		p["body"] != "ship?" || p["urgent"] != false || p["reply_to_ref"] != "" || p["created_at"] != m.CreatedAt {
		t.Errorf("payload = %v", p)
	}
	if opts, _ := p["options"].([]any); len(opts) != 2 || opts[0] != "Yes" {
		t.Errorf("payload options = %v", p["options"])
	}
	got, _ := flowdb.GetBusMessageByID(db, m.ID)
	if got.Delivery != "sent" || got.DeliveryError != "" {
		t.Errorf("after send: delivery=%q err=%q", got.Delivery, got.DeliveryError)
	}
	if !strings.Contains(readDispatchLog(t), "r-0") {
		t.Errorf("remote_ref not logged: %s", readDispatchLog(t))
	}

	// --fyi travels as kind fyi; an unbound plain terminal may send too.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if rc := msgRC(t, "bob", "heads up", "--fyi"); rc != 0 {
		t.Fatalf("fyi rc=%d", rc)
	}
	f := rowByBody(t, db, "heads up")
	if f.FromTaskSlug != "" || !f.FYI {
		t.Errorf("fyi row = %+v", f)
	}
	dispatch(t, f.ID)
	if p := readMark(t, marks, 1); p["kind"] != "fyi" {
		t.Errorf("fyi kind = %v", p["kind"])
	}
}

func TestMessagePeerDeliveryFailed(t *testing.T) {
	peerHub(t, "3")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-t")
	if rc := msgRC(t, "bob", "anyone?"); rc != 0 {
		t.Fatal(rc)
	}
	m := rowByBody(t, db, "anyone?")
	dispatch(t, m.ID)
	got, _ := flowdb.GetBusMessageByID(db, m.ID)
	if got.Delivery != "failed" || !strings.Contains(got.DeliveryError, "oops") {
		t.Errorf("failed delivery = %q err=%q", got.Delivery, got.DeliveryError)
	}
	out := captureStdout(t, func() { _ = cmdPeer([]string{"show", "bob"}) })
	if !strings.Contains(out, "failed") || !strings.Contains(out, "anyone?") {
		t.Errorf("peer show lacks recent delivery:\n%s", out)
	}
}

// P4: remote task names are unknowable — <peer>/<task> is a usage error.
func TestMessagePeerTaskAddressRejected(t *testing.T) {
	peerHub(t, "0")
	stubDispatchLauncher(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-t")
	var rc int
	out := captureStdout(t, func() { rc = cmdMessage([]string{"bob/x", "hi"}) })
	if rc != 2 || !strings.Contains(out, "peer") {
		t.Errorf("bob/x rc=%d out=%q", rc, out)
	}
}

// deliverRC runs `flow peer deliver` with the given connector token.
func deliverRC(t *testing.T, token string, args ...string) (int, string) {
	t.Helper()
	t.Setenv("FLOW_CONNECTOR_TOKEN", token)
	defer t.Setenv("FLOW_CONNECTOR_TOKEN", "")
	return peerRC(t, append([]string{"deliver"}, args...)...)
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// P5: flow peer deliver — the only inbound door.
func TestPeerDeliverToHumanMail(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	spawned := stubDispatchLauncher(t, nil)
	installFakeConnector(t, "desk", `"ask","urgent"`, "exit 0") // human pinger
	withSecret(t, "desk")
	tok := "secret-pl"

	rc, out := deliverRC(t, tok, "--at", "/homes/b", "--ref", "b-1", "--options", "Yes|No", "ship?")
	if rc != 0 {
		t.Fatalf("deliver rc=%d %s", rc, out)
	}
	m := rowByBody(t, db, "ship?")
	if m.ToAssignee != "user" || m.ToTaskSlug != "" || m.FromAssignee != "bob" || m.FromTaskSlug != "" ||
		m.Peer != "bob" || m.OriginRef != "b-1" || !m.Ping || m.FYI || len(m.Options) != 2 || m.Options[1] != "No" {
		t.Fatalf("inbound row = %+v", m)
	}
	if len(*spawned) != 1 || (*spawned)[0] != m.ID {
		t.Errorf("human ping not dispatched: %v", *spawned)
	}
	if p, _ := flowdb.GetPeer(db, "bob"); p.LastSeenAt == "" {
		t.Errorf("last_seen_at not stamped")
	}

	// Duplicate delivery of the same ref: exit 0, no new row, no ping.
	n := countRows(t, db)
	if rc, out := deliverRC(t, tok, "--at", "/homes/b", "--ref", "b-1", "ship?"); rc != 0 {
		t.Errorf("dup rc=%d %s", rc, out)
	}
	if countRows(t, db) != n || len(*spawned) != 1 {
		t.Errorf("duplicate delivery stored or pinged")
	}

	// --kind fyi: stored, never pinged.
	if rc, out := deliverRC(t, tok, "--at", "/homes/b", "--ref", "b-2", "--kind", "fyi", "deployed v2"); rc != 0 {
		t.Fatalf("fyi rc=%d %s", rc, out)
	}
	if f := rowByBody(t, db, "deployed v2"); !f.FYI || f.Ping {
		t.Errorf("fyi row = %+v", f)
	}
	if len(*spawned) != 1 {
		t.Errorf("fyi pinged")
	}
}

func TestPeerDeliverRejections(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	installFakeConnector(t, "desk", `"ask"`, "exit 0")
	desk := withSecret(t, "desk")
	installRoleConnector(t, "pl2", `"peer"`, ``, "exit 0")

	for name, tok := range map[string]string{"no token": "", "bad token": "nope"} {
		if rc, _ := deliverRC(t, tok, "--at", "/homes/b", "--ref", "x1", "hi"); rc != 1 {
			t.Errorf("%s: rc=%d, want 1", name, rc)
		}
	}
	// A valid token of another connector: (that connector, at) is unknown.
	if rc, _ := deliverRC(t, "secret-pl2", "--at", "/homes/b", "--ref", "x2", "hi"); rc != 1 {
		t.Errorf("other connector's endpoint accepted")
	}
	if rc, _ := deliverRC(t, desk, "--at", "/homes/b", "--ref", "x3", "hi"); rc != 1 {
		t.Errorf("human connector token accepted for bob's endpoint")
	}
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/stranger", "--ref", "x4", "hi"); rc != 1 {
		t.Errorf("unknown at accepted")
	}
	root, _ := connectorsRoot()
	log, _ := os.ReadFile(filepath.Join(root, "inbound.log"))
	if !strings.Contains(string(log), "/homes/stranger") {
		t.Errorf("unknown at not logged to inbound.log: %q", log)
	}
	if rc, _ := peerRC(t, "block", "bob"); rc != 0 {
		t.Fatal(rc)
	}
	rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "x5", "hi")
	if rc != 1 || !strings.Contains(out, "rejected") {
		t.Errorf("blocked peer rc=%d out=%q", rc, out)
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("rejections stored %d rows", n)
	}
	if p, _ := flowdb.GetPeer(db, "bob"); p.LastSeenAt != "" {
		t.Errorf("rejected delivery stamped last_seen_at")
	}
	if rc, _ := peerRC(t, "unblock", "bob"); rc != 0 {
		t.Fatal(rc)
	}
	for name, args := range map[string][]string{
		"no at":        {"--ref", "y", "hi"},
		"no ref":       {"--at", "/homes/b", "hi"},
		"no body":      {"--at", "/homes/b", "--ref", "y"},
		"bad kind":     {"--at", "/homes/b", "--ref", "y", "--kind", "shout", "hi"},
		"fyi+options":  {"--at", "/homes/b", "--ref", "y", "--kind", "fyi", "--options", "A|B", "hi"},
		"unknown flag": {"--at", "/homes/b", "--ref", "y", "--urgent", "hi"},
	} {
		if rc, _ := deliverRC(t, "secret-pl", args...); rc != 2 {
			t.Errorf("%s: rc=%d, want 2", name, rc)
		}
	}
}

// P5 step 4: an answer to one of OUR outbound messages lands in the asking
// task's inbox; when that task is closed (or there was none) it falls back
// to the human's mail, labelled.
func TestPeerDeliverReplyRouting(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	spawned := stubDispatchLauncher(t, nil)
	mkBusTask(t, db, "gone", "sid-gone")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-t")
	if rc := msgRC(t, "bob", "ship?"); rc != 0 {
		t.Fatal(rc)
	}
	ask := rowByBody(t, db, "ship?")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-gone")
	if rc := msgRC(t, "bob", "old question"); rc != 0 {
		t.Fatal(rc)
	}
	old := rowByBody(t, db, "old question")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if rc := msgRC(t, "bob", "from terminal"); rc != 0 {
		t.Fatal(rc)
	}
	term := rowByBody(t, db, "from terminal")
	*spawned = nil

	if rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "b-9", "--reply-to-ref", ask.ID, "Yes"); rc != 0 {
		t.Fatalf("reply rc=%d %s", rc, out)
	}
	r := rowByBody(t, db, "Yes")
	if r.ToAssignee != "user" || r.ToTaskSlug != "t" || r.ReplyTo != ask.ID || r.FromAssignee != "bob" ||
		r.Peer != "bob" || r.OriginRef != "b-9" || r.Ping || r.Status != "pending" {
		t.Fatalf("reply row = %+v", r)
	}

	// Asking task done → the human's mail, labelled, pinged.
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='gone'`); err != nil {
		t.Fatal(err)
	}
	if rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "b-10", "--reply-to-ref", old.ID, "Too late"); rc != 0 {
		t.Fatalf("late reply rc=%d %s", rc, out)
	}
	late, _ := flowdb.InboundByRef(db, "bob", "b-10")
	if late.ToTaskSlug != "" || late.ToAssignee != "user" || late.ReplyTo != old.ID ||
		!strings.Contains(late.Body, "answer to "+old.ID) || !strings.Contains(late.Body, "Too late") || !late.Ping {
		t.Errorf("fallback row = %+v", late)
	}
	// An ask sent from a plain terminal had no task: human mail, labelled.
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "b-11", "--reply-to-ref", term.ID, "Sure"); rc != 0 {
		t.Fatal("terminal reply")
	}
	if tr, _ := flowdb.InboundByRef(db, "bob", "b-11"); tr.ToTaskSlug != "" || !strings.Contains(tr.Body, "answer to "+term.ID) {
		t.Errorf("terminal-ask reply = %+v", tr)
	}
	// A reply-to-ref that is not our outbound row to bob: plain human mail.
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "b-12", "--reply-to-ref", "nope", "Huh"); rc != 0 {
		t.Fatal("unknown reply-to-ref")
	}
	if h, _ := flowdb.InboundByRef(db, "bob", "b-12"); h.ToTaskSlug != "" || h.ReplyTo != "" || h.Body != "Huh" {
		t.Errorf("unmatched reply-to-ref row = %+v", h)
	}
}

// inboundAsk delivers one peer message to this hub's human and returns it.
func inboundAsk(t *testing.T, db *sql.DB, ref, body string, extra ...string) *flowdb.BusMessage {
	t.Helper()
	args := append([]string{"--at", "/homes/b", "--ref", ref}, extra...)
	if rc, out := deliverRC(t, "secret-pl", append(args, body)...); rc != 0 {
		t.Fatalf("deliver rc=%d %s", rc, out)
	}
	m, _ := flowdb.InboundByRef(db, "bob", ref)
	return m
}

// P6: flow human reply on an inbound peer row — same local semantics
// (seen + answered) plus an outbound send to that peer carrying
// reply_to_ref = the inbound row's origin_ref.
func TestHumanReplyRoutesToPeer(t *testing.T) {
	marks := peerHub(t, "0")
	db := openFlowDB(t)
	spawned := stubDispatchLauncher(t, nil)
	installFakeConnector(t, "desk", `"ask"`, "exit 0")
	desk := withSecret(t, "desk")
	in := inboundAsk(t, db, "b-1", "ship?", "--options", "Yes|No")
	*spawned = nil

	t.Setenv("FLOW_CONNECTOR_TOKEN", desk)
	rc, out := humanRC(t, "reply", in.ID, "Yes")
	t.Setenv("FLOW_CONNECTOR_TOKEN", "")
	if rc != 0 {
		t.Fatalf("reply rc=%d %s", rc, out)
	}
	got, _ := flowdb.GetBusMessageByID(db, in.ID)
	if got.SeenAt == "" || got.AnsweredAt == "" {
		t.Errorf("inbound not seen+answered: %+v", got)
	}
	var outID string
	if err := db.QueryRow(`SELECT id FROM bus_messages WHERE peer='bob' AND origin_ref IS NULL AND reply_to=?`, in.ID).Scan(&outID); err != nil {
		t.Fatalf("no outbound reply row: %v", err)
	}
	o, _ := flowdb.GetBusMessageByID(db, outID)
	if o.ToAssignee != "bob" || o.Body != "Yes" || o.Delivery != "dispatched" || o.ToTaskSlug != "" {
		t.Errorf("outbound reply = %+v", o)
	}
	if len(*spawned) != 1 || (*spawned)[0] != outID {
		t.Fatalf("reply not dispatched: %v", *spawned)
	}
	dispatch(t, outID)
	p := readMark(t, marks, 0)
	if p["kind"] != "reply" || p["reply_to_ref"] != "b-1" || p["body"] != "Yes" || p["peer"] != "bob" {
		t.Errorf("reply payload = %v", p)
	}
	// Idempotent: a second reply sends nothing.
	t.Setenv("FLOW_CONNECTOR_TOKEN", desk)
	humanRC(t, "reply", in.ID, "No")
	t.Setenv("FLOW_CONNECTOR_TOKEN", "")
	if len(*spawned) != 1 {
		t.Errorf("repeat reply dispatched again")
	}
}

// P6: an agent (or a plain terminal) answers with flow message <peer>
// --reply-to <id>: routed out with reply_to_ref; replying to another
// peer's message is refused.
func TestMessageReplyToInboundPeer(t *testing.T) {
	marks := peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	in := inboundAsk(t, db, "b-7", "status?")
	if rc, _ := peerRC(t, "add", "carol", "--via", "pl", "--at", "/homes/c"); rc != 0 {
		t.Fatal(rc)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-t")
	if rc := msgRC(t, "bob", "all green", "--reply-to", in.ID); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	o := rowByBody(t, db, "all green")
	dispatch(t, o.ID)
	if p := readMark(t, marks, 0); p["kind"] != "reply" || p["reply_to_ref"] != "b-7" {
		t.Errorf("payload = %v", p)
	}
	if got, _ := flowdb.GetBusMessageByID(db, in.ID); got.SeenAt != "" {
		t.Errorf("an agent's reply marked the human's mail seen (#118)")
	}
	if rc := msgRC(t, "carol", "wrong thread", "--reply-to", in.ID); rc != 2 {
		t.Errorf("reply to bob's message via carol rc=%d, want 2", rc)
	}
}

// P7: the human-ping payload names the peer a message came from
// ("from_peer"; empty for local senders).
func TestHumanPingPayloadFromPeer(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	installFakeConnector(t, "desk", `"ask","urgent"`, "exit 0")
	withSecret(t, "desk")
	var payloads []map[string]any
	orig := runConnector
	t.Cleanup(func() { runConnector = orig })
	runConnector = func(m *connector.Manifest, verb string, payload []byte, env []string, timeout time.Duration) error {
		var p map[string]any
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("payload %s: %v", payload, err)
		}
		payloads = append(payloads, p)
		return nil
	}

	in := inboundAsk(t, db, "b-1", "ship?")
	dispatch(t, in.ID)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-t")
	local := askRow(t, "local ask")
	dispatch(t, local.ID)
	if len(payloads) != 2 {
		t.Fatalf("payloads = %v", payloads)
	}
	if v, ok := payloads[0]["from_peer"]; !ok || v != "bob" {
		t.Errorf("inbound ping from_peer = %v (present=%v)", v, ok)
	}
	if v, ok := payloads[1]["from_peer"]; !ok || v != "" {
		t.Errorf("local ping from_peer = %v (present=%v), want empty string", v, ok)
	}
}

// P12: the skill (lean core = one trigger line), references, CLI usage,
// README and CHANGELOG document peer hubs and contract 2.
func TestPeersDocs(t *testing.T) {
	core := strings.Join(strings.Fields(readSkillFile(t, "SKILL.md")), " ")
	for _, want := range []string{"references/peers.md", "flow message <peer>"} {
		if !strings.Contains(core, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
	peers := strings.Join(strings.Fields(readSkillFile(t, "references/peers.md")), " ")
	for _, want := range []string{
		"flow peer add <name> --via <connector> --at <endpoint>", "flow peer block",
		`flow message <peer> "<body>"`, "--reply-to <id>", "<peer>/<task>",
		"flow peer deliver", "reply_to_ref", "origin_ref", "delivery", "dispatched", "sent", "failed",
		"inbound.log", "Hand to agent", "peer-local", "FLOW_SECRET_STORE=file", "#118",
	} {
		if !strings.Contains(peers, want) {
			t.Errorf("references/peers.md lacks %q", want)
		}
	}
	conn := strings.Join(strings.Fields(readSkillFile(t, "references/connectors.md")), " ")
	for _, want := range []string{`"contract": 2`, `"roles"`, `["human","peer"]`, "from_peer",
		"reply_to_ref", "remote_ref", "flow peer deliver", "references/peers.md", "FLOW_SECRET_STORE=file"} {
		if !strings.Contains(conn, want) {
			t.Errorf("references/connectors.md lacks %q", want)
		}
	}
	usage := captureStdout(t, printUsage)
	for _, want := range []string{"flow peer add <name> --via <connector> --at <endpoint>", "flow peer deliver", `flow message <peer>`} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	readme, _ := os.ReadFile("../../README.md")
	for _, want := range []string{"### Peer hubs", "flow peer add bob --via peer-local", "flow connector add peer-local"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README lacks %q", want)
		}
	}
	changelog, _ := os.ReadFile("../../CHANGELOG.md")
	for _, want := range []string{"Peer hubs", "flow peer add", "flow peer deliver", "contract 2", "from_peer", "FLOW_SECRET_STORE=file", "peer-local", "Hand to agent"} {
		if !strings.Contains(string(changelog), want) {
			t.Errorf("CHANGELOG lacks %q", want)
		}
	}
}
