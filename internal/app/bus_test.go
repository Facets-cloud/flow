package app

import (
	"database/sql"
	"encoding/json"
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
		if rc := cmdMessage([]string{"self", "need release approval"}); rc != 0 {
			t.Fatalf("message rc != 0")
		}
	})
	if !strings.Contains(out, "messaged self") {
		t.Errorf("send output: %s", out)
	}

	// Human-directed messages are due for notification immediately.
	out = captureStdout(t, func() {
		if rc := cmdInbox([]string{"due"}); rc != 0 {
			t.Fatalf("due rc != 0 on due message")
		}
	})
	if !strings.Contains(out, "need release approval") {
		t.Errorf("due output: %s", out)
	}
	// due advanced the schedule: nothing due now, exit 1.
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"due"}); rc != 1 {
			t.Errorf("second due should exit 1")
		}
	})

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
	t.Setenv("CLAUDE_CODE_SESSION_ID", "") // consume as the human
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "")

	captureStdout(t, func() {
		if rc := cmdMessage([]string{"self", "first"}); rc != 0 {
			t.Fatal("msg1")
		}
		if rc := cmdMessage([]string{"self", "second"}); rc != 0 {
			t.Fatal("msg2")
		}
	})

	out := captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop"}); rc != 0 {
			t.Fatalf("pop rc != 0")
		}
	})
	if !strings.Contains(out, "first") || strings.Contains(out, "second") {
		t.Errorf("pop should consume exactly the oldest: %s", out)
	}
	// Popping a human-directed message ACKS it.
	s, _ := flowdb.GetBusStats(db, "self")
	if s.Acked != 1 || s.Pending != 1 {
		t.Errorf("after one pop: %+v", s)
	}
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop"}); rc != 0 {
			t.Fatalf("pop2 rc != 0")
		}
	})
	// Empty inbox: exit 1 (script/Monitor friendly).
	captureStdout(t, func() {
		if rc := cmdInbox([]string{"pop"}); rc != 1 {
			t.Errorf("empty pop should exit 1")
		}
	})
}

func TestMessageBodyCapAndAddressErrors(t *testing.T) {
	setupFlowRoot(t)
	long := strings.Repeat("x", busBodyMax+1)
	out := captureStdout(t, func() {
		if rc := cmdMessage([]string{"self", long}); rc != 2 {
			t.Errorf("overlong body rc != 2")
		}
	})
	if !strings.Contains(out, "messages are short") {
		t.Errorf("cap message: %s", out)
	}
	out = captureStdout(t, func() {
		if rc := cmdMessage([]string{"self/nope-not-a-task", "hi"}); rc != 2 {
			t.Errorf("bad task address rc != 2")
		}
	})
	if !strings.Contains(out, "no task") {
		t.Errorf("address error: %s", out)
	}
}

func TestPostFanoutToWatchers(t *testing.T) {
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
		{"self/task-c", "task-a"},
		{"self", "task-a"},
		{"self/task-a", "task-a"}, // self-watch: must be skipped on fan-out
	} {
		if err := flowdb.AddWatch(db, w[0], w[1]); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdout(t, func() {
		if rc := cmdPost([]string{"imports done, 3 drifts left"}); rc != 0 {
			t.Fatalf("post rc != 0")
		}
	})
	if !strings.Contains(out, "2 watcher(s)") {
		t.Errorf("post output: %s", out)
	}
	rows, _ = flowdb.PendingForTask(db, "task-c")
	if len(rows) != 1 || rows[0].Kind != "post" || rows[0].FromTaskSlug != "task-a" {
		t.Errorf("task-c inbox = %+v", rows)
	}
	if human, _ := flowdb.PendingForHuman(db, "self"); len(human) != 1 {
		t.Errorf("human feed = %+v", human)
	}
	if self, _ := flowdb.PendingForTask(db, "task-a"); len(self) != 0 {
		t.Errorf("poster received its own post")
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

	// due --as --json for a fresh message to shashwat.
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"shashwat", "second thing"}); rc != 0 {
			t.Fatalf("message rc != 0")
		}
	})
	out = captureStdout(t, func() {
		if rc := cmdInbox([]string{"due", "--as", "shashwat", "--json"}); rc != 0 {
			t.Fatalf("due --as --json rc != 0")
		}
	})
	var arr []busMsgJSON
	if err := json.Unmarshal([]byte(out), &arr); err != nil || len(arr) != 1 {
		t.Fatalf("due json = %v, %v\nraw: %s", arr, err, out)
	}
}

func TestWatchAsSelfSubscribesHuman(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-w")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-w")

	captureStdout(t, func() {
		if rc := cmdWatch([]string{"task-a", "--as", "self"}); rc != 0 {
			t.Fatalf("watch --as self rc != 0")
		}
	})
	ws, _ := flowdb.ListWatches(db, "self")
	if len(ws) != 1 || ws[0] != "task-a" {
		t.Errorf("--as self should subscribe as self: %v", ws)
	}
}

func TestHookStopNudgesPostOnlyWithWatchers(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-a")
	db := openFlowDB(t)
	mkBusTask(t, db, "task-a", "sid-a")

	out := captureStdout(t, func() {
		if rc := cmdHookStop(nil); rc != 0 {
			t.Fatalf("rc != 0")
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("no-watcher stop hook should emit nothing, got: %s", out)
	}

	if err := flowdb.AddWatch(db, "self", "task-a"); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if rc := cmdHookStop(nil); rc != 0 {
			t.Fatalf("rc != 0")
		}
	})
	ctx := busHookContext(t, out)
	if !strings.Contains(ctx, "flow post") || !strings.Contains(ctx, "1 watcher(s)") {
		t.Errorf("stop nudge: %s", ctx)
	}

	// A declined nudge must not re-fire on the very next turn end —
	// the nudge itself has a 30m cooldown (wake-loop guard).
	out = captureStdout(t, func() {
		if rc := cmdHookStop(nil); rc != 0 {
			t.Fatalf("rc != 0")
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("nudge re-fired within cooldown: %s", out)
	}

	captureStdout(t, func() {
		if rc := cmdPost([]string{"posted the thing"}); rc != 0 {
			t.Fatalf("post rc != 0")
		}
	})
	out = captureStdout(t, func() {
		if rc := cmdHookStop(nil); rc != 0 {
			t.Fatalf("rc != 0")
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("recent post should silence the nudge, got: %s", out)
	}
}
