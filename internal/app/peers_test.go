package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
