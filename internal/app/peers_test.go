package app

import (
	"os"
	"path/filepath"
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
