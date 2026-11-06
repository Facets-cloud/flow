package app

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// Human connectors MVP — one block per numbered behavior of the spec.

// msgRC runs `flow message <args...>` swallowing stdout and returns rc.
func msgRC(t *testing.T, args ...string) int {
	t.Helper()
	var rc int
	captureStdout(t, func() { rc = cmdMessage(args) })
	return rc
}

// rowByBody fetches the single bus row with this body.
func rowByBody(t *testing.T, db *sql.DB, body string) *flowdb.BusMessage {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM bus_messages WHERE body=?`, body).Scan(&id); err != nil {
		t.Fatalf("row %q: %v", body, err)
	}
	m, err := flowdb.GetBusMessageByID(db, id)
	if err != nil || m == nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return m
}

// B1: --options and --fyi on human-addressed mail only.
func TestB1MessageOptionsAndFYI(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	mkBusTask(t, db, "peer", "sid-peer")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")

	if rc := msgRC(t, "user", "ship it?", "--options", "Yes|No|Later today"); rc != 0 {
		t.Fatalf("options rc=%d", rc)
	}
	m := rowByBody(t, db, "ship it?")
	if got := m.Options; len(got) != 3 || got[0] != "Yes" || got[1] != "No" || got[2] != "Later today" {
		t.Errorf("options = %q", got)
	}
	if m.FYI {
		t.Errorf("plain ask stored as fyi")
	}

	if rc := msgRC(t, "user", "--fyi", "progress: 3/5 done"); rc != 0 {
		t.Fatalf("fyi rc=%d", rc)
	}
	if f := rowByBody(t, db, "progress: 3/5 done"); !f.FYI || len(f.Options) != 0 {
		t.Errorf("fyi row: fyi=%v options=%q", f.FYI, f.Options)
	}

	asks, fyi, err := flowdb.HumanUnreadCounts(db, "user")
	if err != nil {
		t.Fatal(err)
	}
	if asks != 1 || fyi != 1 {
		t.Errorf("unread counts asks=%d fyi=%d, want 1/1 (an --fyi message is FYI)", asks, fyi)
	}

	// Listed: the human's inbox shows the choices and the FYI marker, and
	// the JSON rendering carries both.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	out := captureStdout(t, func() { _ = cmdInbox(nil) })
	if !strings.Contains(out, "(Yes | No | Later today)") || !strings.Contains(out, "fyi") {
		t.Errorf("inbox listing lacks options/fyi:\n%s", out)
	}
	out = captureStdout(t, func() { _ = cmdInbox([]string{"--json"}) })
	if !strings.Contains(out, `"options":["Yes","No","Later today"]`) || !strings.Contains(out, `"fyi":true`) {
		t.Errorf("inbox json lacks options/fyi:\n%s", out)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")

	for name, args := range map[string][]string{
		"options to task":  {"user/peer", "hi", "--options", "A|B"},
		"fyi to task":      {"user/peer", "hi", "--fyi"},
		"fyi + urgent":     {"user", "x1", "--fyi", "--urgent"},
		"fyi + options":    {"user", "x2", "--fyi", "--options", "A|B"},
		"empty options":    {"user", "x3", "--options", " | "},
		"options no value": {"user", "x4", "--options"},
	} {
		if rc := msgRC(t, args...); rc != 2 {
			t.Errorf("%s: rc=%d, want 2", name, rc)
		}
	}
}

// B2: the ping decision is made at SEND time and stored on the row.
func TestB2PingDecisionAtSend(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	mkBusTask(t, db, "peer", "sid-peer")

	send := func(sid string, args ...string) *flowdb.BusMessage {
		t.Helper()
		t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
		if rc := msgRC(t, args...); rc != 0 {
			t.Fatalf("send %q rc=%d", args, rc)
		}
		return rowByBody(t, db, args[1])
	}
	want := func(m *flowdb.BusMessage, ping bool) {
		t.Helper()
		if m.Ping != ping {
			t.Errorf("%q: ping=%v, want %v", m.Body, m.Ping, ping)
		}
	}

	a1 := send("sid-asker", "user", "ask one")
	want(a1, true)
	want(send("sid-asker", "user", "ask two"), false) // one ping per task until answered
	u := send("sid-asker", "user", "urgent one", "--urgent")
	want(u, true) // --urgent always pings
	want(send("sid-asker", "user", "progress", "--fyi"), false)
	want(send("sid-peer", "user", "peer ask"), true) // per task, not global
	// A sender with no task (the CLI refuses unbound → user, but other
	// writers such as a workspace transport can) always pings.
	for _, id := range []string{"nt1", "nt2"} {
		m := &flowdb.BusMessage{ID: id, CreatedAt: flowdb.NowISO(), Kind: "message",
			FromAssignee: "user", ToAssignee: "user", Body: id}
		if err := flowdb.InsertBusMessage(db, m); err != nil {
			t.Fatal(err)
		}
		want(m, true)
	}
	want(send("sid-asker", "alice", "to alice"), false) // not the local human
	want(send("sid-asker", "user/peer", "task mail"), false)

	// Once the human has seen every pinged ask from the task, it may ping again.
	c := flowdb.Consumer{By: "test", Identity: "user"}
	if _, err := flowdb.MarkSeen(db, []string{a1.ID, u.ID}, c); err != nil {
		t.Fatal(err)
	}
	want(send("sid-asker", "user", "ask three"), true)

	// Broadcasts are never pinged.
	b := &flowdb.BusMessage{ID: "bcast1", CreatedAt: flowdb.NowISO(), Kind: "broadcast",
		FromAssignee: "user", ToAssignee: "user", Body: "b"}
	if err := flowdb.InsertBusMessage(db, b); err != nil {
		t.Fatal(err)
	}
	if m, _ := flowdb.GetBusMessageByID(db, "bcast1"); m.Ping || b.Ping {
		t.Errorf("broadcast pinged")
	}
}

// installFakeConnector writes a valid connector folder straight into
// $FLOW_ROOT/connectors/<name>/ whose run script is `script` (a /bin/sh
// body). Returns the folder.
func installFakeConnector(t *testing.T, name, events, script string) string {
	t.Helper()
	root, err := connectorsRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","contract":1,"exec":"./run","capabilities":["send"],"events":[` + events + `]}`
	if err := os.WriteFile(filepath.Join(dir, "connector.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stubDispatchLauncher records spawned dispatch ids instead of forking.
func stubDispatchLauncher(t *testing.T, fail error) *[]string {
	t.Helper()
	var ids []string
	orig := dispatchLauncher
	dispatchLauncher = func(id string) error {
		ids = append(ids, id)
		return fail
	}
	t.Cleanup(func() { dispatchLauncher = orig })
	return &ids
}

// B3: a pinging send spawns a detached dispatch — only when a connector is
// installed, and a spawn failure never fails the send.
func TestB3SendSpawnsDispatchOnlyWithConnectors(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")
	spawned := stubDispatchLauncher(t, nil)

	// No connectors installed: behaves exactly as today.
	if rc := msgRC(t, "user", "first ask"); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(*spawned) != 0 {
		t.Fatalf("dispatch spawned with no connectors: %v", *spawned)
	}
	if m := rowByBody(t, db, "first ask"); !m.Ping {
		t.Fatalf("first ask should still record ping=1")
	}

	installFakeConnector(t, "desk", `"ask","urgent"`, "exit 0")
	if rc := msgRC(t, "user", "urgent ask", "--urgent"); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	u := rowByBody(t, db, "urgent ask")
	if len(*spawned) != 1 || (*spawned)[0] != u.ID {
		t.Fatalf("spawned = %v, want [%s]", *spawned, u.ID)
	}
	// Non-pinging sends never spawn.
	for _, args := range [][]string{{"user", "second ask"}, {"user", "a progress note", "--fyi"}} {
		if rc := msgRC(t, args...); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	}
	if len(*spawned) != 1 {
		t.Fatalf("non-pinging sends spawned: %v", *spawned)
	}

	// Spawn errors are logged, never returned.
	stubDispatchLauncher(t, errors.New("fork exploded"))
	if rc := msgRC(t, "user", "boom", "--urgent"); rc != 0 {
		t.Fatalf("spawn failure leaked into rc=%d", rc)
	}
	root, _ := connectorsRoot()
	log, _ := os.ReadFile(filepath.Join(root, "dispatch.log"))
	if !strings.Contains(string(log), "fork exploded") {
		t.Errorf("dispatch.log lacks the spawn error: %q", log)
	}
}
