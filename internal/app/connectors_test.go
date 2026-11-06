package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	connector.KeychainSet = func(string, string, string) error { return unavailable }
	connector.KeychainGet = func(string, string) (string, error) { return "", unavailable }
	connector.KeychainDelete = func(string, string) error { return unavailable }
}

// fakeAppKeychain installs an in-memory keychain for one test.
func fakeAppKeychain(t *testing.T) map[string]string {
	t.Helper()
	kc := map[string]string{}
	set, get, del := connector.KeychainSet, connector.KeychainGet, connector.KeychainDelete
	t.Cleanup(func() { connector.KeychainSet, connector.KeychainGet, connector.KeychainDelete = set, get, del })
	connector.KeychainSet = func(s, a, v string) error { kc[s+"@"+a] = v; return nil }
	connector.KeychainGet = func(s, a string) (string, error) {
		if v, ok := kc[s+"@"+a]; ok {
			return v, nil
		}
		return "", connector.ErrNoSecret
	}
	connector.KeychainDelete = func(s, a string) error { delete(kc, s+"@"+a); return nil }
	return kc
}

// kcKey is fakeAppKeychain's map key for connector name in flow root.
func kcKey(name, root string) string {
	return connector.KeychainService(name) + "@" + connector.KeychainAccount(root)
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
	secret := kc[kcKey("desk", root)]
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
	if _, ok := kc[kcKey("flaky", root)]; ok {
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

// withSecret stores a secret for an installed fake connector.
func withSecret(t *testing.T, name string) string {
	t.Helper()
	store, err := secretStore()
	if err != nil {
		t.Fatal(err)
	}
	secret := "secret-" + name
	if _, err := store.Put(name, secret); err != nil {
		t.Fatal(err)
	}
	return secret
}

func readDispatchLog(t *testing.T) string {
	t.Helper()
	root, _ := connectorsRoot()
	b, _ := os.ReadFile(filepath.Join(root, "dispatch.log"))
	return string(b)
}

// B4: dispatch runs `send` on every installed connector whose events match,
// in parallel with a per-connector timeout, then stamps notified_at.
func TestB4DispatchSendsToMatchingConnectors(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")
	stubDispatchLauncher(t, nil)
	orig := connectorTimeout
	connectorTimeout = 3 * time.Second // macOS can take a while to first-exec a fresh script
	t.Cleanup(func() { connectorTimeout = orig })

	got := filepath.Join(t.TempDir(), "got")
	urgentSeen := filepath.Join(t.TempDir(), "urgent")
	installFakeConnector(t, "good", `"ask","urgent"`, `echo "$1|$FLOW_CONNECTOR_TOKEN|$FLOW_BIN|$FLOW_ROOT|$CLAUDE_CODE_SESSION_ID" > `+got+`; cat >> `+got)
	installFakeConnector(t, "hang", `"ask"`, "sleep 30")
	installFakeConnector(t, "bad", `"ask"`, "echo kaput >&2; exit 1")
	installFakeConnector(t, "pager", `"urgent"`, "touch "+urgentSeen)
	goodSecret := withSecret(t, "good")
	for _, n := range []string{"hang", "bad", "pager"} {
		withSecret(t, n)
	}
	origBin := flowBinPath
	flowBinPath = func() string { return "/opt/flow/bin/flow" }
	t.Cleanup(func() { flowBinPath = origBin })

	if rc := msgRC(t, "user", "deploy now?", "--options", "Yes|No"); rc != 0 {
		t.Fatalf("send rc=%d", rc)
	}
	ask := rowByBody(t, db, "deploy now?")

	start := time.Now()
	var rc int
	captureStdout(t, func() { rc = cmdConnector([]string{"dispatch", ask.ID}) })
	if rc != 0 {
		t.Fatalf("dispatch rc=%d", rc)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("dispatch took %v — a hanging connector blocked it", d)
	}

	raw, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("good connector not invoked: %v\n%s", err, readDispatchLog(t))
	}
	head, body, _ := strings.Cut(string(raw), "\n")
	root, _ := flowRoot()
	if head != "send|"+goodSecret+"|/opt/flow/bin/flow|"+root+"|" {
		t.Errorf("verb/env = %q (session id must be stripped)", head)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("stdin not JSON: %v %q", err, body)
	}
	if p["id"] != ask.ID || p["from_task"] != "asker" || p["body"] != "deploy now?" ||
		p["kind"] != "ask" || p["urgent"] != false || p["created_at"] != ask.CreatedAt {
		t.Errorf("payload = %v", p)
	}
	if opts, _ := p["options"].([]any); len(opts) != 2 || opts[0] != "Yes" {
		t.Errorf("options = %v", p["options"])
	}
	if hint, _ := p["reply_hint"].(string); !strings.Contains(hint, "human reply "+ask.ID) {
		t.Errorf("reply_hint = %q", hint)
	}
	if _, err := os.Stat(urgentSeen); !os.IsNotExist(err) {
		t.Errorf("urgent-only connector got a normal ask")
	}
	if m, _ := flowdb.GetBusMessageByID(db, ask.ID); m.NotifiedAt == "" {
		t.Errorf("notified_at not set after a successful send")
	}
	log := readDispatchLog(t)
	for _, want := range []string{ask.ID + " good ok", ask.ID + " hang fail", "timed out", ask.ID + " bad fail", "kaput"} {
		if !strings.Contains(log, want) {
			t.Errorf("dispatch.log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "pager") {
		t.Errorf("non-matching connector logged:\n%s", log)
	}

	// An urgent ask reaches the urgent subscriber.
	if rc := msgRC(t, "user", "prod down", "--urgent"); rc != 0 {
		t.Fatal(rc)
	}
	u := rowByBody(t, db, "prod down")
	captureStdout(t, func() { rc = cmdConnector([]string{"dispatch", u.ID}) })
	if _, err := os.Stat(urgentSeen); err != nil {
		t.Errorf("urgent connector not invoked: %v", err)
	}

	if rc := func() int {
		var r int
		captureStdout(t, func() { r = cmdConnector([]string{"dispatch", "nope"}) })
		return r
	}(); rc != 1 {
		t.Errorf("unknown id rc=%d", rc)
	}
}

// B4: when every connector fails, notified_at stays unset.
func TestB4DispatchAllFailLeavesUnnotified(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")
	stubDispatchLauncher(t, nil)
	installFakeConnector(t, "bad", `"ask"`, "exit 1")
	withSecret(t, "bad")
	if rc := msgRC(t, "user", "q"); rc != 0 {
		t.Fatal(rc)
	}
	m := rowByBody(t, db, "q")
	captureStdout(t, func() { _ = cmdConnector([]string{"dispatch", m.ID}) })
	if m2, _ := flowdb.GetBusMessageByID(db, m.ID); m2.NotifiedAt != "" {
		t.Errorf("notified_at set though every connector failed")
	}
}

func humanRC(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var rc int
	out := captureStdout(t, func() { rc = cmdHuman(args) })
	return rc, out
}

// B7: flow human reply|seen are connector-only (FLOW_CONNECTOR_TOKEN).
func TestB7HumanReplyAndSeen(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	mkBusTask(t, db, "gone", "sid-gone")
	stubDispatchLauncher(t, nil)
	installFakeConnector(t, "desk", `"ask"`, "exit 0")
	secret := withSecret(t, "desk")

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")
	if rc := msgRC(t, "user", "ship?", "--options", "Yes|No"); rc != 0 {
		t.Fatal(rc)
	}
	if rc := msgRC(t, "user", "fyi-ish ask"); rc != 0 {
		t.Fatal(rc)
	}
	ask, other := rowByBody(t, db, "ship?"), rowByBody(t, db, "fyi-ish ask")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-gone")
	if rc := msgRC(t, "user", "orphan ask"); rc != 0 {
		t.Fatal(rc)
	}
	orphan := rowByBody(t, db, "orphan ask")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	// Without the token (an agent, a plain terminal) nothing changes.
	for _, tok := range []string{"", "wrong"} {
		t.Setenv("FLOW_CONNECTOR_TOKEN", tok)
		if rc, _ := humanRC(t, "reply", ask.ID, "Yes"); rc != 1 {
			t.Errorf("reply with token %q rc=%d, want 1", tok, rc)
		}
		if rc, _ := humanRC(t, "seen", ask.ID); rc != 1 {
			t.Errorf("seen with token %q rc=%d, want 1", tok, rc)
		}
	}
	if m, _ := flowdb.GetBusMessageByID(db, ask.ID); m.SeenAt != "" || m.AnsweredAt != "" {
		t.Fatalf("tokenless call changed the row: %+v", m)
	}

	t.Setenv("FLOW_CONNECTOR_TOKEN", secret)
	if rc, _ := humanRC(t, "seen", other.ID); rc != 0 {
		t.Fatalf("seen rc=%d", rc)
	}
	if m, _ := flowdb.GetBusMessageByID(db, other.ID); m.SeenAt == "" || m.ConsumedByIdentity != "connector:desk" || m.AnsweredAt != "" {
		t.Errorf("seen row: %+v", m)
	}
	if rc, _ := humanRC(t, "seen", other.ID); rc != 0 {
		t.Errorf("repeat seen rc=%d", rc)
	}

	if rc, out := humanRC(t, "reply", ask.ID, "Yes — after lunch"); rc != 0 {
		t.Fatalf("reply rc=%d %s", rc, out)
	}
	m, _ := flowdb.GetBusMessageByID(db, ask.ID)
	if m.SeenAt == "" || m.AnsweredAt == "" || m.Status != "acked" || m.ConsumedByIdentity != "connector:desk" {
		t.Errorf("answered row: %+v", m)
	}
	replies, err := flowdb.AllForTask(db, "asker")
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || replies[0].ReplyTo != ask.ID || replies[0].Body != "Yes — after lunch" ||
		replies[0].FromAssignee != "user" || replies[0].FromTaskSlug != "" || replies[0].Status != "pending" {
		t.Fatalf("reply rows = %+v", replies)
	}
	// Idempotent: a second reply records nothing new.
	if rc, _ := humanRC(t, "reply", ask.ID, "No"); rc != 0 {
		t.Errorf("repeat reply rc=%d", rc)
	}
	if again, _ := flowdb.AllForTask(db, "asker"); len(again) != 1 {
		t.Errorf("repeat reply delivered again: %d rows", len(again))
	}

	// Asking task closed: still answered, warning, exit 0, no undeliverable row.
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='gone'`); err != nil {
		t.Fatal(err)
	}
	rc, out := humanRC(t, "reply", orphan.ID, "too late") // captures stderr too
	if rc != 0 || !strings.Contains(out, "warning") || !strings.Contains(out, `"gone" is done`) {
		t.Errorf("orphan reply rc=%d out=%q", rc, out)
	}
	if m, _ := flowdb.GetBusMessageByID(db, orphan.ID); m.AnsweredAt == "" {
		t.Errorf("orphan not recorded answered")
	}
	if rows, _ := flowdb.AllForTask(db, "gone"); len(rows) != 0 {
		t.Errorf("reply delivered to a done task: %+v", rows)
	}

	if rc, _ := humanRC(t, "reply", "nope", "x"); rc != 1 {
		t.Errorf("unknown id rc=%d", rc)
	}
	for _, args := range [][]string{{}, {"reply", ask.ID}, {"reply", ask.ID, "  "}, {"seen"}, {"wat"}} {
		if rc, _ := humanRC(t, args...); rc != 2 {
			t.Errorf("%q rc=%d, want 2", args, rc)
		}
	}
}

// B9: the skill (source of truth) teaches asking with --options and --fyi
// for progress, and documents contract v1 in a reachable reference.
func TestB9SkillTeachesConnectors(t *testing.T) {
	core := strings.Join(strings.Fields(readSkillFile(t, "SKILL.md")), " ")
	for _, want := range []string{`--options "A|B"`, "--fyi", "reply_to", "references/connectors.md"} {
		if !strings.Contains(core, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
	msg := strings.Join(strings.Fields(readSkillFile(t, "references/messaging.md")), " ")
	for _, want := range []string{
		`flow message user "<q>" --options "A|B"`, "reply_to", "never block",
		"no connector is installed", "--fyi", "references/connectors.md",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("references/messaging.md lacks %q", want)
		}
	}
	ref := strings.Join(strings.Fields(readSkillFile(t, "references/connectors.md")), " ")
	for _, want := range []string{
		"connector.json", `"contract": 1`, "FLOW_CONNECTOR_TOKEN", "FLOW_BIN", "FLOW_ROOT",
		"run send", "run test", `"reply_hint"`, `"options"`, "human reply <id>", "human seen <id>",
		"flow connector add", "30s", "exit 0", "dispatch.log", "examples/connectors/macos",
		"flow connector add macos", "flow connector list --available",
		"account `flow-<12 hex of sha256 of the resolved FLOW_ROOT>`",
	} {
		if !strings.Contains(ref, want) {
			t.Errorf("references/connectors.md lacks %q", want)
		}
	}
}

// B9: flow's own FYI mail to the human is real --fyi mail, so it never
// pings a connector.
func TestB9FlowFYIsNeverPing(t *testing.T) {
	withParent := buildAutoBootstrapPrompt("kid", "regular", "", "papa")
	if !strings.Contains(withParent, `--fyi`) {
		t.Errorf("parented auto prompt's closed-parent FYI must use --fyi")
	}
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "papa", "sid-papa")
	mkBusTask(t, db, "kid", "")
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='papa' WHERE slug='kid'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='papa'`); err != nil {
		t.Fatal(err)
	}
	kid, _ := flowdb.GetTask(db, "kid")
	notifyParentOfDeadChild(db, kid)
	var fyi, ping int
	if err := db.QueryRow(`SELECT fyi, ping FROM bus_messages WHERE to_task_slug IS NULL`).Scan(&fyi, &ping); err != nil {
		t.Fatal(err)
	}
	if fyi != 1 || ping != 0 {
		t.Errorf("dead-child notice to the human: fyi=%d ping=%d, want 1/0", fyi, ping)
	}
}

// B10: the CLI usage, README and CHANGELOG document the connectors MVP.
func TestB10DocsCoverConnectors(t *testing.T) {
	usage := captureStdout(t, printUsage)
	for _, want := range []string{"--options", "--fyi", "flow connector add <dir|sample>", "flow connector list", "flow connector list --available",
		"flow connector remove <name>", "flow connector test [<name>]", "flow human reply <id>", "flow human seen <id>"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### Human connectors", `--options "Yes|No"`, "flow connector add macos", "flow connector list --available", "--fyi"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README lacks %q", want)
		}
	}
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	// Checked across the whole CHANGELOG: a release cut moves the entry out
	// of [Unreleased] into its version section.
	for _, want := range []string{"### Added", "connector", "--options", "--fyi", "flow human reply",
		"flow connector add macos", "--available"} {
		if !strings.Contains(string(changelog), want) {
			t.Errorf("CHANGELOG lacks %q", want)
		}
	}
}

// B4: the stdin payload is plain JSON without HTML escaping, so simple
// shell connectors see "<", ">" and "&" as typed.
func TestB4PayloadIsNotHTMLEscaped(t *testing.T) {
	setupFlowRoot(t)
	got := filepath.Join(t.TempDir(), "got")
	installFakeConnector(t, "raw", `"ask"`, "cat > "+got)
	withSecret(t, "raw")
	m := installedConnectors()[0]
	if err := invokeConnector(m, "send", connector.Payload{ID: "x", Body: "a<b && c>d", Kind: "ask"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(got)
	if !strings.Contains(string(b), `"body":"a<b && c>d"`) {
		t.Errorf("payload = %s", b)
	}
}

// fakeRunConnector replaces connector execution for one test and records
// each "<name> <verb> <exec-mode>" invocation.
func fakeRunConnector(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	orig := runConnector
	t.Cleanup(func() { runConnector = orig })
	runConnector = func(m *connector.Manifest, verb string, payload []byte, env []string, timeout time.Duration) error {
		fi, err := os.Stat(m.ExecPath())
		if err != nil {
			return err
		}
		calls = append(calls, m.Name+" "+verb+" "+fi.Mode().Perm().String())
		return nil
	}
	return &calls
}

// Packaging: a binary-only install (no repo checkout) can add a sample
// connector by name; the samples are embedded from examples/connectors.
func TestConnectorAddEmbeddedSample(t *testing.T) {
	root := setupFlowRoot(t)
	kc := fakeAppKeychain(t)
	calls := fakeRunConnector(t)
	t.Chdir(t.TempDir()) // no ./macos directory here: the name must resolve to the embedded copy

	rc, out := connectorRC(t, "list", "--available")
	if rc != 0 || !strings.Contains(out, "macos") {
		t.Fatalf("list --available rc=%d out=%s", rc, out)
	}

	if rc, out := connectorRC(t, "add", "macos"); rc != 0 {
		t.Fatalf("add macos rc=%d out=%s", rc, out)
	}
	inst := filepath.Join(root, "connectors", "macos")
	fi, err := os.Stat(filepath.Join(inst, "run"))
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("embedded run lacks exec bit: %v %v", fi, err)
	}
	for _, f := range []string{"connector.json", "README.md"} {
		if _, err := os.Stat(filepath.Join(inst, f)); err != nil {
			t.Errorf("%s not installed: %v", f, err)
		}
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "macos test -rwx") {
		t.Errorf("test invocation = %q", *calls)
	}
	if len(kc) != 1 {
		t.Errorf("no secret stored: %v", kc)
	}

	// Unknown name (and not a directory) → exit 1 naming what's available.
	rc, out = connectorRC(t, "add", "nosuch")
	if rc != 1 || !strings.Contains(out, "macos") {
		t.Errorf("unknown sample rc=%d out=%s", rc, out)
	}
}

// Two flow homes on one Mac keep separate keychain secrets for the same
// connector name; removing it in one leaves the other's intact.
func TestConnectorKeychainScopedPerFlowRoot(t *testing.T) {
	kc := fakeAppKeychain(t)
	fakeRunConnector(t)
	src := srcConnector(t, "desk", "exit 0")

	rootA := setupFlowRoot(t)
	if rc, out := connectorRC(t, "add", src); rc != 0 {
		t.Fatalf("add in A rc=%d %s", rc, out)
	}
	rootB := setupFlowRoot(t)
	if rootA == rootB {
		t.Fatal("setupFlowRoot reused the root")
	}
	if rc, out := connectorRC(t, "add", src); rc != 0 {
		t.Fatalf("add in B rc=%d %s", rc, out)
	}
	a, b := kc[kcKey("desk", rootA)], kc[kcKey("desk", rootB)]
	if a == "" || b == "" || a == b || len(kc) != 2 {
		t.Fatalf("want two distinct per-home secrets, got %v", kc)
	}
	if rc, _ := connectorRC(t, "remove", "desk"); rc != 0 { // in B (current FLOW_ROOT)
		t.Fatal("remove in B")
	}
	if _, ok := kc[kcKey("desk", rootB)]; ok {
		t.Errorf("B's secret survived remove")
	}
	if kc[kcKey("desk", rootA)] != a {
		t.Errorf("removing in B touched A's secret: %v", kc)
	}
}
