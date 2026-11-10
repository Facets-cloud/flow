package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
)

// A connector process (no bound task) holding its FLOW_CONNECTOR_TOKEN may
// notify the human with `flow message user ...`; the row's sender is
// connector:<name>. Without a valid token the old refusal stands, and a
// connector can never address tasks, peers or other assignees this way.
func TestConnectorMayNotifyHuman(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "worker", "sid-worker")
	ids := stubDispatchLauncher(t, nil)
	installFakeConnector(t, "flow-p2p", `"ask"`, "exit 0")
	secret := withSecret(t, "flow-p2p")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	// No token / wrong token: still "your own queue" (exit 2), no row.
	for _, tok := range []string{"", "wrong"} {
		t.Setenv("FLOW_CONNECTOR_TOKEN", tok)
		if rc := msgRC(t, "user", "--fyi", "nope "+tok); rc != 2 {
			t.Errorf("token %q: rc=%d, want 2", tok, rc)
		}
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&n)
	if n != 0 {
		t.Fatalf("refused sends left %d rows", n)
	}

	t.Setenv("FLOW_CONNECTOR_TOKEN", secret)

	// --fyi: allowed, sender connector:flow-p2p, never pings.
	if rc := msgRC(t, "user", "--fyi", "alice wants to pair"); rc != 0 {
		t.Fatalf("fyi rc=%d", rc)
	}
	fyi := rowByBody(t, db, "alice wants to pair")
	if fyi.FromAssignee != "connector:flow-p2p" || fyi.FromTaskSlug != "" || fyi.ToAssignee != busUser ||
		fyi.ToTaskSlug != "" || !fyi.FYI || fyi.Ping || fyi.SenderSessionID != "" {
		t.Errorf("fyi row = %+v", fyi)
	}
	if got := busFrom(fyi); got != "connector flow-p2p" {
		t.Errorf("busFrom = %q, want %q", got, "connector flow-p2p")
	}

	// default: pings (and dispatches); --urgent allowed and pings.
	if rc := msgRC(t, "user", "deliveries to rohit keep failing"); rc != 0 {
		t.Fatalf("default rc=%d", rc)
	}
	if m := rowByBody(t, db, "deliveries to rohit keep failing"); !m.Ping || m.FromAssignee != "connector:flow-p2p" {
		t.Errorf("default row = %+v", m)
	}
	if rc := msgRC(t, "user", "syncthing died", "--urgent"); rc != 0 {
		t.Fatalf("urgent rc=%d", rc)
	}
	if m := rowByBody(t, db, "syncthing died"); !m.Ping || !m.Urgent {
		t.Errorf("urgent row = %+v", m)
	}
	if len(*ids) != 2 {
		t.Errorf("dispatches = %v, want 2 (the fyi never pings)", *ids)
	}

	// Only `user`: tasks, other assignees, peers → exit 2, no row.
	if err := flowdb.AddPeer(db, &flowdb.Peer{Name: "bob", Connector: "flow-p2p", At: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"user/worker", "rohit", "bob", "bob/x"} {
		if rc := msgRC(t, addr, "--fyi", "leak "+addr); rc != 2 {
			t.Errorf("connector → %s rc=%d, want 2", addr, rc)
		}
	}
	db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE body LIKE 'leak %'`).Scan(&n)
	if n != 0 {
		t.Errorf("non-user targets left %d rows", n)
	}
	// A connector notifies; it cannot ask (an answer has nowhere to go).
	if rc := msgRC(t, "user", "pick one", "--options", "A|B"); rc != 2 {
		t.Errorf("connector --options rc=%d, want 2", rc)
	}

	// The human sees who it is from.
	out := captureStdout(t, func() { _ = cmdInbox([]string{"--as", "user"}) })
	if !strings.Contains(out, "connector flow-p2p: alice wants to pair") {
		t.Errorf("inbox does not label the connector:\n%s", out)
	}
}

// The ping payload names the connector origin so a dialog can label it.
func TestConnectorNotifyPayloadCarriesOrigin(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	orig := connectorTimeout
	connectorTimeout = 3 * time.Second
	t.Cleanup(func() { connectorTimeout = orig })

	got := filepath.Join(t.TempDir(), "got")
	installFakeConnector(t, "desk", `"ask"`, "cat > "+got)
	withSecret(t, "desk")
	installFakeConnector(t, "flow-p2p", `"ask"`, "exit 0")
	secret := withSecret(t, "flow-p2p")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("FLOW_CONNECTOR_TOKEN", secret)

	if rc := msgRC(t, "user", "pairing failed"); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	m := rowByBody(t, db, "pairing failed")
	t.Setenv("FLOW_CONNECTOR_TOKEN", "")
	captureStdout(t, func() { _ = cmdConnector([]string{"dispatch", m.ID}) })
	raw, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("desk not invoked: %v\n%s", err, readDispatchLog(t))
	}
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("payload: %v %q", err, raw)
	}
	if p["from_connector"] != "flow-p2p" || p["from_task"] != "" {
		t.Errorf("payload = %v", p)
	}
}
