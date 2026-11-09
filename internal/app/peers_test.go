package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
