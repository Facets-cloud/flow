package app

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/connector"
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

// Safety net: no app test may ever reach the real macOS keychain. Unless a
// test installs its own fake, the keychain is "unavailable" and secrets fall
// back to 0600 files under the temp FLOW_ROOT.
func init() {
	unavailable := errors.New("keychain disabled in tests")
	connector.KeychainSet = func(string, string) error { return unavailable }
	connector.KeychainGet = func(string) (string, error) { return "", unavailable }
	connector.KeychainDelete = func(string) error { return unavailable }
}

// fakeAppKeychain installs an in-memory keychain for one test.
func fakeAppKeychain(t *testing.T) map[string]string {
	t.Helper()
	kc := map[string]string{}
	set, get, del := connector.KeychainSet, connector.KeychainGet, connector.KeychainDelete
	t.Cleanup(func() { connector.KeychainSet, connector.KeychainGet, connector.KeychainDelete = set, get, del })
	connector.KeychainSet = func(s, v string) error { kc[s] = v; return nil }
	connector.KeychainGet = func(s string) (string, error) {
		if v, ok := kc[s]; ok {
			return v, nil
		}
		return "", connector.ErrNoSecret
	}
	connector.KeychainDelete = func(s string) error { delete(kc, s); return nil }
	return kc
}

// srcConnector writes an uninstalled connector folder (outside FLOW_ROOT)
// whose run appends "<verb> <token-len> <stdin>" lines to <dir>/calls.
func srcConnector(t *testing.T, name, runBody string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","contract":1,"exec":"./run","capabilities":["send"],"events":["ask","urgent"]}`
	if err := os.WriteFile(filepath.Join(dir, "connector.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "helper.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("#!/bin/sh\n"+runBody+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func connectorRC(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var rc int
	out := captureStdout(t, func() { rc = cmdConnector(args) })
	return rc, out
}

// B6: flow connector add | list | remove | test.
func TestB6ConnectorAddListRemoveTest(t *testing.T) {
	root := setupFlowRoot(t)
	kc := fakeAppKeychain(t)
	calls := filepath.Join(t.TempDir(), "calls")
	src := srcConnector(t, "desk", `echo "$1 token=${#FLOW_CONNECTOR_TOKEN} bin=$FLOW_BIN root=$FLOW_ROOT $(cat)" >> `+calls)

	if rc, out := connectorRC(t, "add", src); rc != 0 {
		t.Fatalf("add rc=%d out=%s", rc, out)
	}
	inst := filepath.Join(root, "connectors", "desk")
	if _, err := os.Stat(filepath.Join(inst, "lib", "helper.txt")); err != nil {
		t.Errorf("folder not copied: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(inst, "run")); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("exec bit lost: %v", err)
	}
	secret := kc[connector.KeychainService("desk")]
	if len(secret) < 32 {
		t.Fatalf("no keychain secret: %v", kc)
	}
	// add ran `test` with the test payload, the secret, FLOW_BIN and FLOW_ROOT.
	got, _ := os.ReadFile(calls)
	line := string(got)
	if !strings.HasPrefix(line, "test token=64 ") || !strings.Contains(line, `"id":"test"`) ||
		!strings.Contains(line, "root="+root) || !strings.Contains(line, "bin=/") {
		t.Errorf("test invocation = %q", line)
	}
	// No secret in the connector folder or flow.db.
	for _, p := range []string{inst, filepath.Join(root, "flow.db")} {
		filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				if b, _ := os.ReadFile(path); strings.Contains(string(b), secret) {
					t.Errorf("secret leaked into %s", path)
				}
			}
			return nil
		})
	}

	// Re-adding the same name is refused.
	if rc, _ := connectorRC(t, "add", src); rc != 1 {
		t.Errorf("duplicate add rc=%d, want 1", rc)
	}
	// An invalid manifest is refused without installing anything.
	bad := srcConnector(t, "bad", "exit 0")
	os.WriteFile(filepath.Join(bad, "connector.json"), []byte(`{"name":"bad","contract":9}`), 0o644)
	if rc, _ := connectorRC(t, "add", bad); rc != 1 {
		t.Errorf("invalid add rc=%d", rc)
	}
	// A connector whose test fails is rolled back (folder + secret).
	failing := srcConnector(t, "flaky", "echo broken >&2; exit 1")
	if rc, _ := connectorRC(t, "add", failing); rc != 1 {
		t.Errorf("failing-test add rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(root, "connectors", "flaky")); !os.IsNotExist(err) {
		t.Errorf("failed add left a folder")
	}
	if _, ok := kc[connector.KeychainService("flaky")]; ok {
		t.Errorf("failed add left a secret")
	}

	rc, out := connectorRC(t, "list")
	if rc != 0 || !strings.Contains(out, "desk") || !strings.Contains(out, "keychain") || !strings.Contains(out, "ask,urgent") {
		t.Errorf("list rc=%d out=%s", rc, out)
	}

	os.Remove(calls)
	if rc, out := connectorRC(t, "test", "desk"); rc != 0 || !strings.Contains(out, "ok") {
		t.Errorf("test rc=%d out=%s", rc, out)
	}
	if got, _ := os.ReadFile(calls); !strings.HasPrefix(string(got), "test ") {
		t.Errorf("test verb not run: %q", got)
	}
	if rc, _ := connectorRC(t, "test", "nope"); rc != 1 {
		t.Errorf("test unknown rc=%d", rc)
	}

	if rc, _ := connectorRC(t, "remove", "desk"); rc != 0 {
		t.Fatalf("remove rc=%d", rc)
	}
	if _, err := os.Stat(inst); !os.IsNotExist(err) {
		t.Errorf("remove left the folder")
	}
	if len(kc) != 0 {
		t.Errorf("remove left the secret: %v", kc)
	}
	if rc, _ := connectorRC(t, "remove", "desk"); rc != 1 {
		t.Errorf("second remove rc=%d", rc)
	}
	if rc, _ := connectorRC(t); rc != 2 {
		t.Errorf("bare connector rc=%d", rc)
	}
	if rc, _ := connectorRC(t, "add"); rc != 2 {
		t.Errorf("add without dir rc=%d", rc)
	}
}

// B6: without a keychain the secret falls back to a 0600 file.
func TestB6ConnectorSecretFileFallback(t *testing.T) {
	root := setupFlowRoot(t)
	src := srcConnector(t, "desk", "cat >/dev/null")
	if rc, out := connectorRC(t, "add", src); rc != 0 {
		t.Fatalf("add rc=%d %s", rc, out)
	}
	fi, err := os.Stat(filepath.Join(root, "secrets", "desk"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file: %v %v", fi, err)
	}
	if rc, _ := connectorRC(t, "remove", "desk"); rc != 0 {
		t.Fatal("remove")
	}
	if _, err := os.Stat(filepath.Join(root, "secrets", "desk")); !os.IsNotExist(err) {
		t.Errorf("secret file survived remove")
	}
}
