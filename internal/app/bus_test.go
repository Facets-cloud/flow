package app

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// mkBusTask creates a task and optionally binds a session id.
func mkBusTask(t *testing.T, db *sql.DB, slug, sid string) {
	t.Helper()
	if rc := cmdAdd([]string{"task", slug, "--slug", slug, "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatalf("add task %s rc=%d", slug, rc)
	}
	if sid != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET session_id=?, status='in-progress' WHERE slug=?`, sid, slug); err != nil {
			t.Fatal(err)
		}
	}
}

func busHookContext(t *testing.T, out string) string {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		return ""
	}
	var parsed struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse hook output: %v\nraw: %s", err, out)
	}
	return parsed.HookSpecificOutput.AdditionalContext
}

func TestMessageHumanLifecycle(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-msg-1")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-msg-1")

	out := captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", "need release approval"}); rc != 0 {
			t.Fatalf("message rc != 0")
		}
	})
	if !strings.Contains(out, "messaged user") {
		t.Errorf("send output: %s", out)
	}

	// The user replying in the sender session acks + reports the wait.
	out = captureStdout(t, func() {
		if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
			t.Fatalf("ups hook rc != 0")
		}
	})
	ctx := busHookContext(t, out)
	if !strings.Contains(ctx, "answered after") || !strings.Contains(ctx, "need release approval") {
		t.Errorf("ack context: %s", ctx)
	}

	out = captureStdout(t, func() { _ = cmdInbox([]string{"stats"}) })
	if !strings.Contains(out, "answered messages : 1") {
		t.Errorf("stats: %s", out)
	}
}

func TestInboxPopConsumesOneAtATime(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-p") // messages come from a bound session
	db := openFlowDB(t)
	mkBusTask(t, db, "task-p", "sid-p")

	captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", "first"}); rc != 0 {
			t.Fatal("msg1")
		}
		if rc := cmdMessage([]string{"user", "second"}); rc != 0 {
			t.Fatal("msg2")
		}
	})

	out := captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop", "--as", "user"}); rc != 0 {
			t.Fatalf("pop rc != 0")
		}
	})
	if !strings.Contains(out, "first") || strings.Contains(out, "second") {
		t.Errorf("pop should consume exactly the oldest: %s", out)
	}
	// Popping a human-directed message ACKS it.
	s, _ := flowdb.GetBusStats(db, "user")
	if s.Acked != 1 || s.Pending != 1 {
		t.Errorf("after one pop: %+v", s)
	}
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop", "--as", "user"}); rc != 0 {
			t.Fatalf("pop2 rc != 0")
		}
	})
	// Empty inbox: exit 1 (script/Monitor friendly).
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop", "--as", "user"}); rc != 1 {
			t.Errorf("empty pop should exit 1")
		}
	})
}

func TestInboxAllListsReadAndUnread(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-all")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-all", "sid-all")

	captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", "first msg"}); rc != 0 {
			t.Fatal("msg1")
		}
		if rc := cmdMessage([]string{"user", "second msg"}); rc != 0 {
			t.Fatal("msg2")
		}
	})
	// Consume the oldest (acks it).
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop", "--as", "user"}); rc != 0 {
			t.Fatal("pop")
		}
	})
	// Default inbox shows only the remaining unread one.
	out := captureStdout(t, func() {
		if rc := cmdInbox([]string{"--as", "user", "--json"}); rc != 0 {
			t.Fatal("inbox --json")
		}
	})
	var unread []busMsgJSON
	if err := json.Unmarshal([]byte(out), &unread); err != nil || len(unread) != 1 {
		t.Fatalf("default inbox = %v, %v\nraw: %s", unread, err, out)
	}
	// --all shows both, labelled read/unread.
	out = captureStdout(t, func() {
		if rc := cmdInbox([]string{"--all", "--as", "user", "--json"}); rc != 0 {
			t.Fatal("inbox --all --json")
		}
	})
	var all []busMsgJSON
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all) != 2 {
		t.Fatalf("inbox --all = %v, %v\nraw: %s", all, err, out)
	}
	var reads, unreads int
	for _, m := range all {
		switch m.Mail {
		case "read":
			reads++
		case "unread":
			unreads++
		default:
			t.Errorf("unexpected mail label %q", m.Mail)
		}
	}
	if reads != 1 || unreads != 1 {
		t.Errorf("--all mail labels: read=%d unread=%d", reads, unreads)
	}
}

func TestInboxReadByID(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-rd")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-rd", "sid-rd")

	captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", "older"}); rc != 0 {
			t.Fatal("msg1")
		}
	})
	out := captureStdout(t, func() { _ = cmdInbox([]string{"--as", "user", "--json"}) })
	var rows []busMsgJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("inbox json: %v %v", rows, err)
	}
	id := rows[0].ID

	// read <id> marks it read out of pop order.
	out = captureStdout(t, func() {
		if rc := cmdInbox([]string{"read", id}); rc != 0 {
			t.Fatal("read rc != 0")
		}
	})
	if !strings.Contains(out, "marked read") || !strings.Contains(out, "older") {
		t.Errorf("read output: %s", out)
	}
	if s, _ := flowdb.GetBusStats(db, "user"); s.Acked != 1 || s.Pending != 0 {
		t.Errorf("read did not ack: %+v", s)
	}
	// Reading again is display-only.
	out = captureStdout(t, func() {
		if rc := cmdInbox([]string{"read", id}); rc != 0 {
			t.Fatal("read2")
		}
	})
	if !strings.Contains(out, "already read") {
		t.Errorf("second read output: %s", out)
	}
	// Unknown id exits 1.
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"read", "deadbeef"}); rc != 1 {
			t.Errorf("read of missing id should exit 1")
		}
	})
}

func TestInboxPopKeepUnread(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-ku")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-ku", "sid-ku")

	captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", "forward me"}); rc != 0 {
			t.Fatal("msg")
		}
	})
	// pop --keep-unread returns the message WITHOUT acking it.
	out := captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop", "--keep-unread", "--as", "user"}); rc != 0 {
			t.Fatal("keep-unread pop rc != 0")
		}
	})
	if !strings.Contains(out, "forward me") || !strings.Contains(out, "kept unread") {
		t.Errorf("keep-unread output: %s", out)
	}
	// It must NOT be acked (not answered): a reader only forwarded it.
	if s, _ := flowdb.GetBusStats(db, "user"); s.Acked != 0 {
		t.Errorf("keep-unread acked the message: %+v", s)
	}
	// It's now delivered → a second keep-unread pop won't re-return it (no
	// hot loop for the relay), and plain inbox no longer lists it as unread.
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop", "--keep-unread", "--as", "user"}); rc != 1 {
			t.Errorf("delivered message was re-returned by keep-unread")
		}
	})
	// But it survives in --all and can still be answered by id.
	out = captureStdout(t, func() { _ = cmdInbox([]string{"--all", "--as", "user", "--json"}) })
	var all []busMsgJSON
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all) != 1 {
		t.Fatalf("--all after keep-unread: %v %v", all, err)
	}
	if _, _, err := flowdb.ReadMessageByID(db, all[0].ID, "read"); err != nil {
		t.Fatal(err)
	}
	if s, _ := flowdb.GetBusStats(db, "user"); s.Acked != 1 {
		t.Errorf("forwarded message could not be answered by id: %+v", s)
	}
}

func TestMessageReplyToLineage(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-r")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-r")
	mkBusTask(t, db, "task-b", "")

	// Parent question to the human.
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", "which region for the DB?"}); rc != 0 {
			t.Fatal("parent")
		}
	})
	parents, _ := flowdb.PendingForHuman(db, "user")
	if len(parents) != 1 {
		t.Fatalf("expected parent in human queue: %v", parents)
	}
	parentID := parents[0].ID

	// Reply routed to a task session, stamped with the parent id.
	out := captureStdout(t, func() {
		if rc := cmdMessage([]string{"task-b", "us-east-1", "--reply-to", parentID}); rc != 0 {
			t.Fatal("reply rc != 0")
		}
	})
	_ = out
	// The reply carries the parent id on the receiver's row...
	rowsB, _ := flowdb.PendingForTask(db, "task-b")
	if len(rowsB) != 1 || rowsB[0].ReplyTo != parentID {
		t.Fatalf("reply-to not stamped on receiver: %+v", rowsB)
	}
	// ...and the lineage is shown when the message is displayed.
	out = captureStdout(t, func() {
		if rc := cmdInbox([]string{"read", rowsB[0].ID}); rc != 0 {
			t.Fatal("read reply rc != 0")
		}
	})
	if !strings.Contains(out, "in reply to") || !strings.Contains(out, parentID) ||
		!strings.Contains(out, "which region") {
		t.Errorf("lineage not shown to receiver: %s", out)
	}

	// Unknown parent id is rejected at send time.
	out = captureStdout(t, func() {
		if rc := cmdMessage([]string{"task-b", "orphan", "--reply-to", "deadbeef"}); rc != 2 {
			t.Errorf("reply to missing parent should rc=2")
		}
	})
	if !strings.Contains(out, "no message") {
		t.Errorf("expected missing-parent error: %s", out)
	}
}

func TestMessageBodyCapAndAddressErrors(t *testing.T) {
	setupFlowRoot(t)
	long := strings.Repeat("x", busBodyMax+1)
	out := captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", long}); rc != 2 {
			t.Errorf("overlong body rc != 2")
		}
	})
	if !strings.Contains(out, "messages are short") {
		t.Errorf("cap message: %s", out)
	}
	out = captureStdout(t, func() {
		if rc := cmdMessage([]string{"user/nope-not-a-task", "hi"}); rc != 2 {
			t.Errorf("bad task address rc != 2")
		}
	})
	if !strings.Contains(out, "no task") {
		t.Errorf("address error: %s", out)
	}
}

func TestBroadcastFanoutToWatchers(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-sender")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-sender") // sender (bound)
	mkBusTask(t, db, "task-b", "")           // recipient session
	mkBusTask(t, db, "task-c", "")           // watcher session

	// Direct message to a task: bare slug is sugar for self/<slug>.
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"task-b", "state file moved"}); rc != 0 {
			t.Fatalf("message task rc != 0")
		}
	})
	rows, err := flowdb.PendingForTask(db, "task-b")
	if err != nil || len(rows) != 1 || rows[0].Kind != "message" {
		t.Fatalf("task-b inbox = %v, %v", rows, err)
	}

	for _, w := range [][2]string{
		{"user/task-c", "task-a"},
		{"user", "task-a"},
		{"user/task-a", "task-a"}, // self-watch: must be skipped on fan-out
	} {
		if err := flowdb.AddWatch(db, w[0], w[1]); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdout(t, func() {
		if rc := cmdBroadcast([]string{"imports done, 3 drifts left"}); rc != 0 {
			t.Fatalf("broadcast rc != 0")
		}
	})
	if !strings.Contains(out, "2 watcher(s)") {
		t.Errorf("broadcast output: %s", out)
	}
	rows, _ = flowdb.PendingForTask(db, "task-c")
	if len(rows) != 1 || rows[0].Kind != "broadcast" || rows[0].FromTaskSlug != "task-a" {
		t.Errorf("task-c inbox = %+v", rows)
	}
	if human, _ := flowdb.PendingForHuman(db, "user"); len(human) != 1 {
		t.Errorf("human feed = %+v", human)
	}
	if self, _ := flowdb.PendingForTask(db, "task-a"); len(self) != 0 {
		t.Errorf("broadcaster received its own broadcast")
	}
}

func TestInboxAsAssigneeAndJSON(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "")

	// Queue a message for another assignee (local queue, no transport).
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"shashwat", "your infra PR needs a rebase"}); rc != 0 {
			t.Fatalf("message rc != 0")
		}
	})
	// self's inbox must NOT see it; --as shashwat must.
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop"}); rc != 1 {
			t.Errorf("self pop should find nothing")
		}
	})
	out := captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop", "--as", "shashwat", "--json"}); rc != 0 {
			t.Fatalf("pop --as rc != 0")
		}
	})
	var m busMsgJSON
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("pop --json parse: %v\nraw: %s", err, out)
	}
	if m.To.Assignee != "shashwat" || m.Kind != "message" || m.Body == "" {
		t.Errorf("json roundtrip: %+v", m)
	}
	if m.Status != "pending" { // rendered row is pre-claim snapshot
		t.Logf("status field: %s", m.Status)
	}

	// list --as --json for a fresh message to shashwat.
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"shashwat", "second thing"}); rc != 0 {
			t.Fatalf("message rc != 0")
		}
	})
	out = captureStdout(t, func() {
		if rc := cmdInbox([]string{"--as", "shashwat", "--json"}); rc != 0 {
			t.Fatalf("inbox --as --json rc != 0")
		}
	})
	var arr []busMsgJSON
	if err := json.Unmarshal([]byte(out), &arr); err != nil || len(arr) != 1 {
		t.Fatalf("inbox json = %v, %v\nraw: %s", arr, err, out)
	}
}

func TestSelfSendGates(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-g")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-g", "sid-g")

	// A bound session must not message its own inbox.
	out := captureStdout(t, func() {
		if rc := cmdMessage([]string{"user/task-g", "note to myself"}); rc != 2 {
			t.Errorf("own-inbox send should rc=2")
		}
	})
	if !strings.Contains(out, "own inbox") {
		t.Errorf("gate message: %s", out)
	}
	// An unbound human must not message their own queue.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	out = captureStdout(t, func() {
		if rc := cmdMessage([]string{"user", "hi me"}); rc != 2 {
			t.Errorf("own-queue send should rc=2")
		}
	})
	if !strings.Contains(out, "yourself") {
		t.Errorf("gate message: %s", out)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&n)
	if n != 0 {
		t.Errorf("gated sends inserted rows")
	}
}

func TestWatchAsSelfSubscribesHuman(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-w")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-w")

	captureStdout(t, func() {
		if rc := cmdWatch([]string{"task-a", "--as", "user"}); rc != 0 {
			t.Fatalf("watch --as self rc != 0")
		}
	})
	ws, _ := flowdb.ListWatches(db, "user")
	if len(ws) != 1 || ws[0] != "task-a" {
		t.Errorf("--as self should subscribe as self: %v", ws)
	}
}

// withStopHookStdin points os.Stdin at a real Stop-hook payload for the
// duration of fn — cmdHookStop reads stop_hook_active from it and
// fail-safes to silence when the payload is missing or malformed.
func withStopHookStdin(t *testing.T, payload string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	fn()
}

func stopHookOnce(t *testing.T, active bool) string {
	t.Helper()
	payload := `{"stop_hook_active": false}`
	if active {
		payload = `{"stop_hook_active": true}`
	}
	var out string
	withStopHookStdin(t, payload, func() {
		out = captureStdout(t, func() {
			if rc := cmdHookStop(nil); rc != 0 {
				t.Fatalf("rc != 0")
			}
		})
	})
	return out
}

func TestHookStopNudgesPostOnlyWithWatchers(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-a")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-a")

	if out := stopHookOnce(t, false); strings.TrimSpace(out) != "" {
		t.Errorf("no-watcher stop hook should emit nothing, got: %s", out)
	}

	if err := flowdb.AddWatch(db, "user", "task-a"); err != nil {
		t.Fatal(err)
	}
	ctx := busHookContext(t, stopHookOnce(t, false))
	if !strings.Contains(ctx, "flow broadcast") || !strings.Contains(ctx, "1 watcher(s)") {
		t.Errorf("stop nudge: %s", ctx)
	}

	// A declined nudge must not re-fire on the very next turn end —
	// the nudge itself backs off (wake-loop guard).
	if out := stopHookOnce(t, false); strings.TrimSpace(out) != "" {
		t.Errorf("nudge re-fired within cooldown: %s", out)
	}

	captureStdout(t, func() {
		if rc := cmdBroadcast([]string{"posted the thing"}); rc != 0 {
			t.Fatalf("broadcast rc != 0")
		}
	})
	if out := stopHookOnce(t, false); strings.TrimSpace(out) != "" {
		t.Errorf("recent post should silence the nudge, got: %s", out)
	}
}

func TestHooksInformButNeverConsume(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-b")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-b", "sid-b")

	if err := flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: "msg00001", CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: "user", FromTaskSlug: "task-a",
		ToAssignee: "user", ToTaskSlug: "task-b", Body: "your PR is unblocked",
	}); err != nil {
		t.Fatal(err)
	}

	// UserPromptSubmit: informs with a COUNT, never the body, and the
	// row must remain pending (hooks never consume).
	out := captureStdout(t, func() {
		if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
			t.Fatalf("ups rc != 0")
		}
	})
	ctx := busHookContext(t, out)
	if !strings.Contains(ctx, "1 pending message(s)") || !strings.Contains(ctx, "flow inbox pop") {
		t.Errorf("prompt-submit notice: %s", ctx)
	}
	if strings.Contains(ctx, "your PR is unblocked") {
		t.Errorf("hook leaked message body (should inform only): %s", ctx)
	}
	if rows, _ := flowdb.PendingForTask(db, "task-b"); len(rows) != 1 || rows[0].Status != "pending" {
		t.Errorf("hook consumed mail — must stay pending: %+v", rows)
	}

	// Stop: inbox mail alone must NOT wake the turn (no watchers here).
	if out := stopHookOnce(t, false); strings.TrimSpace(out) != "" {
		t.Errorf("stop emitted for inbox mail: %s", out)
	}
	if rows, _ := flowdb.PendingForTask(db, "task-b"); len(rows) != 1 {
		t.Errorf("stop consumed mail")
	}
}

// preToolUseOnce runs the PreToolUse hook and returns its
// additionalContext ("" when the hook stays silent).
func preToolUseOnce(t *testing.T) string {
	t.Helper()
	return busHookContext(t, captureStdout(t, func() {
		if rc := cmdHookPreToolUse(nil); rc != 0 {
			t.Fatalf("pre-tool-use rc=%d", rc)
		}
	}))
}

// insertTaskMessage drops a directed message into a task's inbox.
func insertTaskMessage(t *testing.T, db *sql.DB, id, toSlug, body string, urgent bool) {
	t.Helper()
	if err := flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: id, CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: "user", FromTaskSlug: "task-src",
		ToAssignee: "user", ToTaskSlug: toSlug, Body: body, Urgent: urgent,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestHookPreToolUseDeltaGates is the core contract: a just-arrived
// directed message is surfaced once before a tool call, then never again
// for the same message (so per-call spam is impossible), while a NEWLY
// arrived message still breaks through. Inform-only throughout.
func TestHookPreToolUseDeltaGates(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-pt")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-pt", "sid-pt")

	// Empty inbox → silent.
	if out := preToolUseOnce(t); out != "" {
		t.Errorf("empty inbox should be silent, got: %s", out)
	}

	insertTaskMessage(t, db, "ptmsg001", "task-pt", "stop, do not deploy", false)

	// First tool call surfaces the message (with an excerpt) but does not
	// consume it.
	ctx := preToolUseOnce(t)
	if !strings.Contains(ctx, "directed message") || !strings.Contains(ctx, "stop, do not deploy") {
		t.Errorf("first pre-tool-use should surface the message: %s", ctx)
	}
	if !strings.Contains(ctx, "ptmsg001") || !strings.Contains(ctx, "flow inbox pop") {
		t.Errorf("surface should name the id and the consume path: %s", ctx)
	}
	if rows, _ := flowdb.PendingForTask(db, "task-pt"); len(rows) != 1 || rows[0].Status != "pending" {
		t.Errorf("pre-tool-use consumed mail — must stay pending: %+v", rows)
	}

	// Every subsequent tool call with nothing new stays silent — the
	// delta-gate, not consumption, is what prevents re-nudging.
	for i := 0; i < 3; i++ {
		if out := preToolUseOnce(t); out != "" {
			t.Errorf("re-nudged the same pending message on call %d: %s", i, out)
		}
	}

	// A NEW message breaks the silence exactly once.
	insertTaskMessage(t, db, "ptmsg002", "task-pt", "actually, proceed", false)
	ctx = preToolUseOnce(t)
	if !strings.Contains(ctx, "actually, proceed") || strings.Contains(ctx, "stop, do not deploy") {
		t.Errorf("only the new message should surface: %s", ctx)
	}
	if out := preToolUseOnce(t); out != "" {
		t.Errorf("new message re-nudged after first surface: %s", out)
	}
}

// TestHookPreToolUseIgnoresBroadcasts pins that broadcasts (FYIs) never
// interrupt a tool call — only directed messages, which can change or
// cancel the pending action, do.
func TestHookPreToolUseIgnoresBroadcasts(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-pb")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-pb", "sid-pb")
	if err := flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: "bc000001", CreatedAt: flowdb.NowISO(), Kind: "broadcast",
		FromAssignee: "user", FromTaskSlug: "task-src",
		ToAssignee: "user", ToTaskSlug: "task-pb", Body: "fyi: imports done",
	}); err != nil {
		t.Fatal(err)
	}
	if out := preToolUseOnce(t); out != "" {
		t.Errorf("broadcast must not fire the pre-tool-use nudge: %s", out)
	}
}

// TestHookPreToolUsePrefersUrgent verifies the surfaced excerpt leads
// with an urgent message and the count flags how many are urgent.
func TestHookPreToolUsePrefersUrgent(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-pu")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-pu", "sid-pu")
	insertTaskMessage(t, db, "pumsg001", "task-pu", "routine note", false)
	insertTaskMessage(t, db, "pumsg002", "task-pu", "ABORT the release now", true)

	ctx := preToolUseOnce(t)
	if !strings.Contains(ctx, "URGENT") {
		t.Errorf("urgent count should be flagged: %s", ctx)
	}
	if !strings.Contains(ctx, "pumsg002") || !strings.Contains(ctx, "ABORT the release now") {
		t.Errorf("lead excerpt should be the urgent message: %s", ctx)
	}
}

// TestHookPreToolUseUnboundSilent confirms an unbound session never emits.
func TestHookPreToolUseUnboundSilent(t *testing.T) {
	setupFlowRoot(t)
	// No CLAUDE_CODE_SESSION_ID bound to any task.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-unbound")
	openFlowDB(t)
	if out := preToolUseOnce(t); out != "" {
		t.Errorf("unbound session should be silent, got: %s", out)
	}
}

func TestHookStopSilentDuringHookContinuation(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-a")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-a")
	if err := flowdb.AddWatch(db, "user", "task-a"); err != nil {
		t.Fatal(err)
	}
	// Even with every nudge condition met, stop_hook_active must win.
	if out := stopHookOnce(t, true); strings.TrimSpace(out) != "" {
		t.Errorf("nudge fired during hook continuation: %s", out)
	}
	// Missing/garbage payload fail-safes to silence too.
	var out string
	withStopHookStdin(t, "not json", func() {
		out = captureStdout(t, func() { _ = cmdHookStop(nil) })
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("nudge fired on malformed payload: %s", out)
	}
}

func TestMessageRejectsFlagAddressAndClosedTasks(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-z")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-z", "sid-z")

	// A KNOWN flag before the address is now fine (order-independent): the
	// first positional is the address, --urgent is recognized as a flag.
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"--urgent", "user", "release blocked"}); rc != 0 {
			t.Errorf("known flag before address should send, rc=%d", rc)
		}
	})
	var urgent int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE urgent=1 AND body='release blocked'`).Scan(&urgent)
	if urgent != 1 {
		t.Errorf("flag-first urgent message not stored correctly")
	}

	// An UNKNOWN flag is a usage error (never a bogus '--bogus' queue) and
	// inserts nothing — this is what stops garbage from reaching the bus.
	var before int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&before)
	out := captureStdout(t, func() {
		if rc := cmdMessage([]string{"--bogus", "user", "x"}); rc != 2 {
			t.Errorf("unknown flag should rc=2")
		}
	})
	if !strings.Contains(out, "unknown flag") {
		t.Errorf("expected unknown-flag error: %s", out)
	}
	var after int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&after)
	if after != before {
		t.Errorf("unknown-flag send inserted a row")
	}

	// Done/archived tasks are undeliverable addresses.
	if _, err := db.Exec(`UPDATE tasks SET status='done', session_id='sid-done' WHERE slug='task-z'`); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if rc := cmdMessage([]string{"user/task-z", "too late"}); rc != 2 {
			t.Errorf("done task should rc=2")
		}
	})
	if !strings.Contains(out, "done") {
		t.Errorf("expected done-task error: %s", out)
	}
}

// A flag written before the body (the natural order, and the --body agents
// invent) must never become the literal body. Regression for the silent
// "body stored as --urgent / --body" corruption.
func TestMessageParsingToleratesFlagOrderAndBodyFlag(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-src")
	db := openFlowDB(t)
	mkBusTask(t, db, "src", "sid-src")
	mkBusTask(t, db, "tgt", "sid-tgt")

	send := func(args ...string) int {
		var rc int
		_ = captureStdout(t, func() { rc = cmdMessage(args) })
		return rc
	}

	if rc := send("tgt", "--urgent", "flag first body"); rc != 0 {
		t.Fatalf("flag-first rc=%d", rc)
	}
	if rc := send("tgt", "--body", "named flag body"); rc != 0 {
		t.Fatalf("--body rc=%d", rc)
	}
	if rc := send("tgt", "positional body", "--urgent"); rc != 0 {
		t.Fatalf("positional rc=%d", rc)
	}

	got := map[string]bool{}
	rows, _ := flowdb.PendingForTask(db, "tgt")
	for _, m := range rows {
		got[m.Body] = m.Urgent
		if strings.HasPrefix(m.Body, "-") {
			t.Fatalf("a flag leaked into the body: %q", m.Body)
		}
	}
	if u, ok := got["flag first body"]; !ok || !u {
		t.Fatalf("flag-first body/urgent wrong: %+v", got)
	}
	if _, ok := got["named flag body"]; !ok {
		t.Fatalf("--body not stored: %+v", got)
	}
	if u, ok := got["positional body"]; !ok || !u {
		t.Fatalf("positional body/urgent wrong: %+v", got)
	}

	// unknown flag errors and stores nothing new
	before, _ := flowdb.PendingForTask(db, "tgt")
	if rc := send("tgt", "--nope", "x"); rc == 0 {
		t.Fatalf("unknown flag should error, not send")
	}
	after, _ := flowdb.PendingForTask(db, "tgt")
	if len(after) != len(before) {
		t.Fatalf("unknown-flag send stored a message")
	}
}
