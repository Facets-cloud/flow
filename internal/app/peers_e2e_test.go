package app

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
)

// Peer hubs v0 acceptance, end to end with the REAL flow binary: two temp
// flow homes A (alice's) and B (bob's) on this machine, linked by the
// embedded peer-local sample, secrets in files (FLOW_SECRET_STORE=file).
// Every hop — dispatch, the connector, the other home's `peer deliver`,
// its human ping — runs as real detached processes. POSIX only (no
// osascript/python), so it runs on Linux CI.
//
//	A (alice)                                   B (bob)
//	task t ─ flow message bob ─► peer-local ─► peer deliver ─► user mail ─► desk (ping)
//	user/t ◄─ peer deliver ◄─ peer-local ◄─ flow human reply <idB> ◄──────┘

type hub struct {
	t    *testing.T
	bin  string
	root string
	home string
}

func (h *hub) env(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + h.home,
		"TMPDIR=" + os.TempDir(),
		"FLOW_ROOT=" + h.root,
		"FLOW_SECRET_STORE=file",
	}
	return append(env, extra...)
}

// run executes the real binary in this home; extra env like
// CLAUDE_CODE_SESSION_ID=… makes it an agent invocation.
func (h *hub) run(extra []string, args ...string) (int, string) {
	h.t.Helper()
	cmd := exec.Command(h.bin, args...)
	cmd.Env = h.env(extra...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String()
	} else if err != nil {
		h.t.Fatalf("run %v: %v", args, err)
	}
	return 0, out.String()
}

func (h *hub) must(extra []string, args ...string) string {
	h.t.Helper()
	rc, out := h.run(extra, args...)
	if rc != 0 {
		h.t.Fatalf("flow %s (in %s): rc=%d\n%s", strings.Join(args, " "), filepath.Base(h.root), rc, out)
	}
	return out
}

func (h *hub) db() *sql.DB {
	h.t.Helper()
	db, err := flowdb.OpenDB(filepath.Join(h.root, "flow.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { db.Close() })
	return db
}

func (h *hub) secret(name string) string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, "secrets", name))
	if err != nil {
		h.t.Fatalf("secret %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

// waitRow polls db until query (returning one id) yields a row.
func waitRow(t *testing.T, db *sql.DB, what, query string, args ...any) *flowdb.BusMessage {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var id string
		if err := db.QueryRow(query, args...).Scan(&id); err == nil {
			m, err := flowdb.GetBusMessageByID(db, id)
			if err == nil && m != nil {
				return m
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func buildFlowBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "flow")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func bindTask(t *testing.T, db *sql.DB, slug, sid string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE tasks SET session_id=?, status='in-progress' WHERE slug=?`, sid, slug); err != nil {
		t.Fatal(err)
	}
}

func TestPeersTwoHomeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and drives the real binary")
	}
	bin := buildFlowBinary(t)
	base := t.TempDir()
	a := &hub{t: t, bin: bin, root: filepath.Join(base, "A"), home: filepath.Join(base, "homeA")}
	b := &hub{t: t, bin: bin, root: filepath.Join(base, "B"), home: filepath.Join(base, "homeB")}
	pings := filepath.Join(base, "desk-pings")
	desk := filepath.Join(base, "desk-src")
	if err := os.MkdirAll(desk, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fake HUMAN connector for B: records every ping payload.
	if err := os.WriteFile(filepath.Join(desk, "connector.json"),
		[]byte(`{"name":"desk","contract":1,"exec":"./run","capabilities":["send"],"events":["ask","urgent"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desk, "run"),
		[]byte("#!/bin/sh\n{ cat; echo; } >> "+pings+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	none := []string(nil)
	for _, h := range []*hub{a, b} {
		for _, d := range []string{h.root, h.home} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		h.must(none, "init")
		h.must(none, "connector", "add", "peer-local")
	}
	b.must(none, "connector", "add", desk)
	a.must(none, "peer", "add", "bob", "--via", "peer-local", "--at", b.root)
	b.must(none, "peer", "add", "alice", "--via", "peer-local", "--at", a.root)
	adb, bdb := a.db(), b.db()

	a.must(none, "add", "task", "t", "--slug", "t", "--work-dir", base)
	bindTask(t, adb, "t", "sid-a-t")
	b.must(none, "add", "task", "u", "--slug", "u", "--work-dir", base)
	bindTask(t, bdb, "u", "sid-b-u")
	agentA := []string{"CLAUDE_CODE_SESSION_ID=sid-a-t"}
	agentB := []string{"CLAUDE_CODE_SESSION_ID=sid-b-u"}

	// (1) A's task t asks bob with options → B's user mail from=alice,
	// pinged through B's human connector, options intact; A's row sent.
	a.must(agentA, "message", "bob", "ship?", "--options", "Yes|No")
	askA := rowByBody(t, adb, "ship?")
	inB := waitRow(t, bdb, "ship? in B", `SELECT id FROM bus_messages WHERE peer='alice' AND origin_ref=?`, askA.ID)
	if inB.ToAssignee != "user" || inB.ToTaskSlug != "" || inB.FromAssignee != "alice" || !inB.Ping ||
		len(inB.Options) != 2 || inB.Options[0] != "Yes" || inB.Options[1] != "No" {
		t.Fatalf("(1) B's row = %+v", inB)
	}
	waitFor(t, "A's delivery=sent", func() bool {
		m, _ := flowdb.GetBusMessageByID(adb, askA.ID)
		return m.Delivery == "sent"
	})
	waitFor(t, "B's human ping", func() bool {
		p, _ := os.ReadFile(pings)
		return strings.Contains(string(p), `"id":"`+inB.ID+`"`) && strings.Contains(string(p), `"from_peer":"alice"`)
	})
	if out := b.must(none, "inbox"); !strings.Contains(out, "alice") || !strings.Contains(out, "ship?") {
		t.Errorf("(1) B's inbox:\n%s", out)
	}

	// (6) #118 across hubs: an agent on B can neither pop nor ack it.
	b.run(agentB, "inbox", "pop", "--as", "user")
	b.run(agentB, "inbox", "read", inB.ID)
	b.run(agentB, "inbox", "read", inB.ID, "--as", "user")
	if m, _ := flowdb.GetBusMessageByID(bdb, inB.ID); m.SeenAt != "" || m.Status != "pending" {
		t.Fatalf("(6) an agent on B consumed alice's message: %+v", m)
	}

	// (2) B's human answers through the (fake) human connector → A's user/t.
	deskTok := []string{"FLOW_CONNECTOR_TOKEN=" + b.secret("desk")}
	b.must(deskTok, "human", "reply", inB.ID, "Yes")
	ans := waitRow(t, adb, "answer in A's user/t",
		`SELECT id FROM bus_messages WHERE to_task_slug='t' AND reply_to=?`, askA.ID)
	if ans.Body != "Yes" || ans.FromAssignee != "bob" || ans.Peer != "bob" {
		t.Errorf("(2) answer row = %+v", ans)
	}

	// (3) B's agent messages alice unprompted → A's user mail from=bob; A
	// answers from a plain terminal with --reply-to → B's user/u.
	b.must(agentB, "message", "alice", "need the staging creds?")
	askB := rowByBody(t, bdb, "need the staging creds?")
	inA := waitRow(t, adb, "B's ask in A", `SELECT id FROM bus_messages WHERE peer='bob' AND origin_ref=?`, askB.ID)
	if inA.ToAssignee != "user" || inA.ToTaskSlug != "" || inA.FromAssignee != "bob" {
		t.Fatalf("(3) A's row = %+v", inA)
	}
	a.must(none, "message", "bob", "no — use the vault", "--reply-to", inA.ID)
	back := waitRow(t, bdb, "A's answer in B's user/u",
		`SELECT id FROM bus_messages WHERE to_task_slug='u' AND reply_to=?`, askB.ID)
	if back.Body != "no — use the vault" || back.FromAssignee != "alice" {
		t.Errorf("(3) B's reply row = %+v", back)
	}

	// (4) The answer arrives after A's task t is done → A's user mail,
	// labelled with the id it answers.
	a.must(agentA, "message", "bob", "deploy friday?")
	askA2 := rowByBody(t, adb, "deploy friday?")
	inB2 := waitRow(t, bdb, "deploy in B", `SELECT id FROM bus_messages WHERE peer='alice' AND origin_ref=?`, askA2.ID)
	if _, err := adb.Exec(`UPDATE tasks SET status='done' WHERE slug='t'`); err != nil {
		t.Fatal(err)
	}
	b.must(deskTok, "human", "reply", inB2.ID, "sure")
	late := waitRow(t, adb, "late answer in A's user mail",
		`SELECT id FROM bus_messages WHERE peer='bob' AND to_task_slug IS NULL AND reply_to=?`, askA2.ID)
	if late.ToAssignee != "user" || !strings.Contains(late.Body, "answer to "+askA2.ID) || !strings.Contains(late.Body, "sure") {
		t.Errorf("(4) late answer = %+v", late)
	}
	if out := a.must(none, "inbox"); !strings.Contains(out, "answer to "+askA2.ID) {
		t.Errorf("(4) A's inbox lacks the labelled answer:\n%s", out)
	}

	// (7) Desk routing: the receiving hub's connector picks the task with
	// --to (flow keeps no peer→task map). (a) new mail with --to lands in
	// that task, not the human; (b) a reply whose asking task closed lands
	// in the --to task, reply_to kept.
	plTok := "FLOW_CONNECTOR_TOKEN=" + b.secret("peer-local")
	b.must([]string{plTok}, "peer", "deliver", "--at", a.root, "--ref", "desk-1", "--to", "u", "--", "triage this")
	if m, _ := flowdb.InboundByRef(bdb, "alice", "desk-1"); m == nil || m.ToTaskSlug != "u" || m.ToAssignee != "user" || m.Ping {
		t.Errorf("(7a) --to row = %+v", m)
	}
	if out := b.must(agentB, "inbox"); !strings.Contains(out, "triage this") {
		t.Errorf("(7a) task u inbox lacks its --to mail:\n%s", out)
	}
	a.must(none, "add", "task", "d", "--slug", "d", "--work-dir", base)
	bindTask(t, adb, "d", "sid-a-d")
	plTokA := []string{"FLOW_CONNECTOR_TOKEN=" + a.secret("peer-local")}
	a.must(plTokA, "peer", "deliver", "--at", b.root, "--ref", "late-2", "--reply-to-ref", askA2.ID, "--to", "d", "--", "still sure")
	if m, _ := flowdb.InboundByRef(adb, "bob", "late-2"); m == nil || m.ToTaskSlug != "d" || m.ReplyTo != askA2.ID || m.Body != "still sure" || m.Ping {
		t.Errorf("(7b) closed-task reply with --to = %+v", m)
	}

	// (7c) Attachments round-trip over peer-local: A's copy is handed to
	// B's deliver, B stores its own copy; sha256 matches end to end.
	srcDir := filepath.Join(base, "src dir")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "line one\nit's a 'quoted' file\n"
	if err := os.WriteFile(filepath.Join(srcDir, "bob's notes.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	a.must([]string{"CLAUDE_CODE_SESSION_ID=sid-a-d"}, "message", "bob", "see attached",
		"--attach", filepath.Join(srcDir, "bob's notes.txt"), "--attach", filepath.Join(srcDir, "bob's notes.txt"))
	sent := rowByBody(t, adb, "see attached")
	got := waitRow(t, bdb, "attachment in B", `SELECT id FROM bus_messages WHERE peer='alice' AND origin_ref=?`, sent.ID)
	if len(sent.Attachments) != 2 || len(got.Attachments) != 2 {
		t.Fatalf("(7c) attachments A=%+v B=%+v", sent.Attachments, got.Attachments)
	}
	for i := range sent.Attachments {
		sa, ga := sent.Attachments[i], got.Attachments[i]
		if sa.SHA256 != sha(content) || ga.SHA256 != sa.SHA256 || ga.Size != sa.Size {
			t.Errorf("(7c) #%d A=%+v B=%+v", i, sa, ga)
		}
		bpath := filepath.Join(b.root, "attachments", got.ID, ga.Name)
		if body, err := os.ReadFile(bpath); err != nil || string(body) != content {
			t.Errorf("(7c) B's copy %s: %q %v", bpath, body, err)
		}
	}
	if out := b.must(none, "inbox"); !strings.Contains(out, filepath.Join(b.root, "attachments", got.ID, "bob's notes-2.txt")) {
		t.Errorf("(7c) B's inbox lacks the attachment path:\n%s", out)
	}

	// (5) Rejections.
	count := func() int {
		var n int
		if err := bdb.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for name, c := range map[string]struct {
		env  []string
		args []string
	}{
		"no token":      {none, []string{"peer", "deliver", "--at", a.root, "--ref", "z1", "hi"}},
		"invalid token": {[]string{"FLOW_CONNECTOR_TOKEN=forged"}, []string{"peer", "deliver", "--at", a.root, "--ref", "z2", "hi"}},
		"unknown at":    {[]string{plTok}, []string{"peer", "deliver", "--at", "/somewhere/else", "--ref", "z3", "hi"}},
	} {
		if rc, out := b.run(c.env, c.args...); rc != 1 {
			t.Errorf("(5) %s: rc=%d, want 1\n%s", name, rc, out)
		}
	}
	if log, _ := os.ReadFile(filepath.Join(b.root, "connectors", "inbound.log")); !strings.Contains(string(log), "/somewhere/else") {
		t.Errorf("(5) unknown at not in inbound.log: %q", log)
	}
	b.must(none, "peer", "block", "alice")
	if rc, out := b.run([]string{plTok}, "peer", "deliver", "--at", a.root, "--ref", "z4", "hi"); rc != 1 || !strings.Contains(out, "rejected") {
		t.Errorf("(5) blocked: rc=%d %s", rc, out)
	}
	b.must(none, "peer", "unblock", "alice")
	if rc, out := b.run([]string{plTok}, "peer", "deliver", "--at", a.root, "--ref", askA.ID, "ship?"); rc != 0 {
		t.Errorf("(5) duplicate ref rc=%d, want 0 (no-op)\n%s", rc, out)
	}
	if n := count(); n != before {
		t.Errorf("(5) rejections/duplicate stored rows: %d → %d", before, n)
	}
	if rc, out := a.run(agentA, "message", "bob/x", "hi"); rc != 2 {
		t.Errorf("(5) bob/x rc=%d, want 2\n%s", rc, out)
	}
	if rc, out := b.run(none, "peer", "add", "carol", "--via", "desk", "--at", "x"); rc != 1 {
		t.Errorf("(5) peer via human-only connector rc=%d, want 1\n%s", rc, out)
	}

	// Drain: every detached sender and ping has finished before the temp
	// homes are removed.
	for _, db := range []*sql.DB{adb, bdb} {
		waitFor(t, "outbound peer rows settled", func() bool {
			var n int
			_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE delivery='dispatched'`).Scan(&n)
			return n == 0
		})
	}
	waitFor(t, "B's pings settled", func() bool {
		var n int
		_ = bdb.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE ping=1 AND notified_at IS NULL`).Scan(&n)
		return n == 0
	})
	time.Sleep(300 * time.Millisecond) // let the last dispatcher exit
}
