package app

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// mockPagerTTY neutralizes terminal side effects: no escape sequences
// reach a real tty and the process-tree walk finds none.
func mockPagerTTY(t *testing.T) {
	t.Helper()
	oldWrite, oldPS := writeTTY, psOutput
	writeTTY = func(tty, seq string) error { return nil }
	psOutput = func(pid int) (string, error) { return "0 ??", nil }
	t.Cleanup(func() { writeTTY, psOutput = oldWrite, oldPS })
	t.Setenv("ITERM_SESSION_ID", "")
}

// mkPagerTask creates a backlog task and optionally binds a session id.
func mkPagerTask(t *testing.T, db *sql.DB, slug, sid string) {
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

func hookContext(t *testing.T, out string) string {
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

func TestPageHumanLifecycle(t *testing.T) {
	setupFlowRoot(t)
	mockPagerTTY(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-page-1")
	db := openFlowDB(t)
	mkPagerTask(t, db, "task-a", "sid-page-1")

	out := captureStdout(t, func() {
		if rc := cmdPage([]string{"self", "need release approval"}); rc != 0 {
			t.Fatalf("page rc != 0")
		}
	})
	if !strings.Contains(out, "paged self") || !strings.Contains(out, "backoff") {
		t.Errorf("send output: %s", out)
	}

	out = captureStdout(t, func() { _ = cmdPage(nil) })
	if !strings.Contains(out, "need release approval") || !strings.Contains(out, "task-a") {
		t.Errorf("list output: %s", out)
	}

	// The user replying in the sender session acks the page and tells
	// the agent how long it waited.
	out = captureStdout(t, func() {
		if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
			t.Fatalf("ups hook rc != 0")
		}
	})
	ctx := hookContext(t, out)
	if !strings.Contains(ctx, "answered after") || !strings.Contains(ctx, "need release approval") {
		t.Errorf("ack context: %s", ctx)
	}

	out = captureStdout(t, func() { _ = cmdPage([]string{"stats"}) })
	if !strings.Contains(out, "answered pages : 1") {
		t.Errorf("stats: %s", out)
	}
}

func TestPageBodyCapAndAddressErrors(t *testing.T) {
	setupFlowRoot(t)
	mockPagerTTY(t)
	long := strings.Repeat("x", pageBodyMax+1)
	out := captureStdout(t, func() {
		if rc := cmdPage([]string{"self", long}); rc != 2 {
			t.Errorf("overlong body rc != 2")
		}
	})
	if !strings.Contains(out, "pages are short") {
		t.Errorf("cap message: %s", out)
	}
	out = captureStdout(t, func() {
		if rc := cmdPage([]string{"self/nope-not-a-task", "hi"}); rc != 2 {
			t.Errorf("bad task address rc != 2")
		}
	})
	if !strings.Contains(out, "no task") {
		t.Errorf("address error: %s", out)
	}
}

func TestPageToTaskAndPostFanout(t *testing.T) {
	setupFlowRoot(t)
	mockPagerTTY(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-sender")
	db := openFlowDB(t)
	mkPagerTask(t, db, "task-a", "sid-sender") // sender (bound)
	mkPagerTask(t, db, "task-b", "")           // recipient session
	mkPagerTask(t, db, "task-c", "")           // watcher session

	// Direct page to a task: bare slug is sugar for self/<slug>.
	captureStdout(t, func() {
		if rc := cmdPage([]string{"task-b", "state file moved"}); rc != 0 {
			t.Fatalf("page task rc != 0")
		}
	})
	rows, err := flowdb.PendingForTask(db, "task-b")
	if err != nil || len(rows) != 1 || rows[0].Kind != "page" {
		t.Fatalf("task-b inbox = %v, %v", rows, err)
	}

	// Watch + post: task-c and the human watch task-a; a post fans out
	// to both, never to the poster itself.
	if err := flowdb.AddWatch(db, "self/task-c", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.AddWatch(db, "self", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.AddWatch(db, "self/task-a", "task-a"); err != nil { // self-watch: must be skipped
		t.Fatal(err)
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
	human, _ := flowdb.PendingPostsForHuman(db, "self")
	if len(human) != 1 {
		t.Errorf("human feed = %+v", human)
	}
	self, _ := flowdb.PendingForTask(db, "task-a")
	if len(self) != 0 {
		t.Errorf("poster received its own post")
	}
}

func TestHookPostToolUseDrainsInboxWithListenNudge(t *testing.T) {
	setupFlowRoot(t)
	mockPagerTTY(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-b")
	db := openFlowDB(t)
	mkPagerTask(t, db, "task-b", "sid-b")

	// Empty inbox: hook is silent.
	out := captureStdout(t, func() {
		if rc := cmdHookPostToolUse(nil); rc != 0 {
			t.Fatalf("rc != 0")
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("empty inbox should emit nothing, got: %s", out)
	}

	if err := flowdb.InsertPageMessage(db, &flowdb.PageMessage{
		ID: "msg00001", CreatedAt: flowdb.NowISO(), Kind: "page",
		FromAssignee: "self", FromTaskSlug: "task-a",
		ToAssignee: "self", ToTaskSlug: "task-b", Body: "your PR is unblocked",
	}); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		if rc := cmdHookPostToolUse(nil); rc != 0 {
			t.Fatalf("rc != 0")
		}
	})
	ctx := hookContext(t, out)
	if !strings.Contains(ctx, "your PR is unblocked") {
		t.Errorf("drain missing message: %s", ctx)
	}
	if !strings.Contains(ctx, "flow page listen") {
		t.Errorf("expected listen nudge when no listener alive: %s", ctx)
	}
	if rows, _ := flowdb.PendingForTask(db, "task-b"); len(rows) != 0 {
		t.Errorf("hook did not mark delivered")
	}
}

func TestHookStopNudgesPostOnlyWithWatchers(t *testing.T) {
	setupFlowRoot(t)
	mockPagerTTY(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-a")
	db := openFlowDB(t)
	mkPagerTask(t, db, "task-a", "sid-a")

	// No watchers: silence — a nudge with no audience is noise.
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
	ctx := hookContext(t, out)
	if !strings.Contains(ctx, "flow post") || !strings.Contains(ctx, "1 watcher(s)") {
		t.Errorf("stop nudge: %s", ctx)
	}

	// After a fresh post the nudge stays quiet for 30m.
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
