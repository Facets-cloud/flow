package app

import (
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// Peer desk primitives: a peer connector picks the local task an inbound
// message lands in (`peer deliver --to <task>`). flow stores and routes
// where told — it keeps no peer→task mapping of its own.

// F1: new (non-reply) mail with --to lands in that task's inbox, unpinged.
func TestPeerDeliverToTask(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	spawned := stubDispatchLauncher(t, nil)
	mkBusTask(t, db, "desk", "sid-desk")

	if rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "d-1", "--to", "desk", "need creds"); rc != 0 {
		t.Fatalf("deliver --to rc=%d %s", rc, out)
	}
	m, _ := flowdb.InboundByRef(db, "bob", "d-1")
	if m == nil || m.ToAssignee != "user" || m.ToTaskSlug != "desk" || m.FromAssignee != "bob" ||
		m.Peer != "bob" || m.Ping || m.Body != "need creds" || m.Status != "pending" {
		t.Fatalf("--to row = %+v", m)
	}
	if len(*spawned) != 0 {
		t.Errorf("task mail pinged the human: %v", *spawned)
	}
	// Dedupe (peer, ref) unchanged: a retry is a no-op.
	n := countRows(t, db)
	if rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "d-1", "--to", "desk", "need creds"); rc != 0 || countRows(t, db) != n {
		t.Errorf("dup --to rc=%d rows %d→%d %s", rc, n, countRows(t, db), out)
	}
	// Without --to: the human's mail, as before.
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "d-2", "plain"); rc != 0 {
		t.Fatal("plain deliver")
	}
	if h, _ := flowdb.InboundByRef(db, "bob", "d-2"); h.ToTaskSlug != "" || !h.Ping {
		t.Errorf("plain row = %+v", h)
	}
}

// F1: --to must name a task that can read mail here; else exit 1, nothing stored.
func TestPeerDeliverToRejections(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	mkBusTask(t, db, "arch", "sid-arch")
	mkBusTask(t, db, "fin", "sid-fin")
	if _, err := db.Exec(`UPDATE tasks SET archived_at=? WHERE slug='arch'`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='fin'`); err != nil {
		t.Fatal(err)
	}
	for i, to := range []string{"nope", "arch", "fin"} {
		rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "r-"+to, "--to", to, "hi")
		if rc != 1 || !strings.Contains(out, to) {
			t.Errorf("#%d --to %s: rc=%d, want 1\n%s", i, to, rc, out)
		}
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("rejected --to stored %d rows", n)
	}
	// --to needs a value; and still needs a valid connector token.
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "y", "hi", "--to"); rc != 2 {
		t.Errorf("--to without value rc=%d, want 2", rc)
	}
	mkBusTask(t, db, "desk", "sid-desk")
	if rc, _ := deliverRC(t, "", "--at", "/homes/b", "--ref", "y", "--to", "desk", "hi"); rc != 1 {
		t.Errorf("--to without token rc=%d, want 1", rc)
	}
}

// F1 routing table for replies:
//
//	reply → T open                 → user/T (--to ignored)
//	reply → T closed, --to X       → user/X, reply_to kept
//	reply → T closed, no --to      → human mail, labelled (unchanged)
func TestPeerDeliverReplyWithTo(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	mkBusTask(t, db, "gone", "sid-gone")
	mkBusTask(t, db, "desk", "sid-desk")
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

	// T open: the asking task wins over --to.
	if rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "b-1", "--reply-to-ref", ask.ID, "--to", "desk", "Yes"); rc != 0 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	if r, _ := flowdb.InboundByRef(db, "bob", "b-1"); r.ToTaskSlug != "t" || r.ReplyTo != ask.ID {
		t.Errorf("open-task reply = %+v", r)
	}
	// T closed + --to: the --to task, reply_to kept, unlabelled, unpinged.
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='gone'`); err != nil {
		t.Fatal(err)
	}
	if rc, out := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "b-2", "--reply-to-ref", old.ID, "--to", "desk", "Too late"); rc != 0 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	r, _ := flowdb.InboundByRef(db, "bob", "b-2")
	if r.ToAssignee != "user" || r.ToTaskSlug != "desk" || r.ReplyTo != old.ID || r.Body != "Too late" || r.Ping {
		t.Errorf("closed-task reply with --to = %+v", r)
	}
	// T closed, no --to: human mail, labelled (unchanged).
	if rc, _ := deliverRC(t, "secret-pl", "--at", "/homes/b", "--ref", "b-3", "--reply-to-ref", old.ID, "Later"); rc != 0 {
		t.Fatal("late reply")
	}
	if h, _ := flowdb.InboundByRef(db, "bob", "b-3"); h.ToTaskSlug != "" || !strings.Contains(h.Body, "answer to "+old.ID) {
		t.Errorf("closed-task reply without --to = %+v", h)
	}
}

// F2: a message to a peer may carry up to peerBodyMax (4000) chars; the
// local bus stays at busBodyMax (500).
func TestPeerMessageBodyCap(t *testing.T) {
	peerHub(t, "0")
	db := openFlowDB(t)
	stubDispatchLauncher(t, nil)
	mkBusTask(t, db, "other", "sid-other")
	long := strings.Repeat("x", 4000)
	if rc := msgRC(t, "bob", long); rc != 0 {
		t.Errorf("4000-char peer message rc=%d, want 0", rc)
	}
	if rc := msgRC(t, "bob", long+"y"); rc != 2 {
		t.Errorf("4001-char peer message rc=%d, want 2", rc)
	}
	if rc := msgRC(t, "user/other", strings.Repeat("z", 501)); rc != 2 {
		t.Errorf("501-char local message rc=%d, want 2", rc)
	}
	if rc := msgRC(t, "user", strings.Repeat("z", 501)); rc != 2 {
		t.Errorf("501-char human message rc=%d, want 2", rc)
	}
	if peerBodyMax != 4000 || busBodyMax != 500 {
		t.Errorf("caps = %d/%d", peerBodyMax, busBodyMax)
	}
}
