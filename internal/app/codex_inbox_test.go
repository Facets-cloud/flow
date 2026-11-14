package app

import (
	"database/sql"
	"strings"
	"testing"
)

// An interactive Codex session has no Monitor tool, so it used to follow
// the "background Bash `flow inbox pop --wait`" fallback. Measured live on
// codex-cli 0.166 (task codex-interactive-inbox): Codex kills a
// backgrounded child within ~0.5s of the exec call returning, so the
// listener never listens. Worse, mail already pending when it is armed is
// acked at once into a log file the model never reads, and is lost.
// Interactive Codex must arm no listener and pop in the foreground.

func pinCodex(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug=?`, slug); err != nil {
		t.Fatal(err)
	}
}

func TestHookSessionStartInteractiveCodexArmsNoListener(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "cx", "sid-cx")
	pinCodex(t, db, "cx")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	ctx := hookRun(t, `{"session_id":"sid-cx"}`, cmdHookSessionStart)
	for _, bad := range []string{"Monitor(", "arm ONE inbox Monitor", "fall back to a background",
		"background Bash", "your inbox listener hears", "armed Monitor"} {
		if strings.Contains(ctx, bad) {
			t.Errorf("interactive Codex SessionStart still carries listener wording %q:\n%s", bad, ctx)
		}
	}
	for _, want := range []string{"do NOT arm an inbox listener", "`flow inbox pop` in the foreground",
		"Codex", "flow watch"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("interactive Codex SessionStart missing %q:\n%s", want, ctx)
		}
	}

	// Claude keeps the Monitor recipe as the fallback when wake is off.
	t.Setenv(wakeOffEnv, "1")
	mkBusTask(t, db, "cl", "sid-cl")
	ctx = hookRun(t, `{"session_id":"sid-cl"}`, cmdHookSessionStart)
	if !strings.Contains(ctx, "arm ONE inbox Monitor") {
		t.Errorf("Claude SessionStart lost the Monitor discipline:\n%s", ctx)
	}
}

// Mail pending at SessionStart: the count notice must not tell a Codex
// session that a Monitor loop will deliver it.
func TestPendingNoticeInteractiveCodexSaysForeground(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "cx", "sid-cx")
	pinCodex(t, db, "cx")
	captureStdout(t, func() { _ = cmdMessage([]string{"user/cx", "ping"}) })
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	for name, fn := range map[string]func([]string) int{
		"session-start": cmdHookSessionStart, "user-prompt-submit": cmdHookUserPromptSubmit,
	} {
		ctx := hookRun(t, `{"session_id":"sid-cx","prompt":"hi"}`, fn)
		if !strings.Contains(ctx, "1 pending message") {
			t.Errorf("%s: missing pending notice:\n%s", name, ctx)
		}
		if strings.Contains(ctx, "armed Monitor") || strings.Contains(ctx, "Monitor(") {
			t.Errorf("%s: Codex pending notice points at a Monitor:\n%s", name, ctx)
		}
		if !strings.Contains(ctx, "in the foreground") {
			t.Errorf("%s: Codex pending notice should say pop in the foreground:\n%s", name, ctx)
		}
	}
}

func TestPreToolUseInteractiveCodexSaysPopInForeground(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "cx", "sid-cx")
	pinCodex(t, db, "cx")
	captureStdout(t, func() { _ = cmdMessage([]string{"user/cx", "ping"}) })
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	ctx := hookRun(t, `{"session_id":"sid-cx"}`, cmdHookPreToolUse)
	if !strings.Contains(ctx, "ping") || !strings.Contains(ctx, "in the foreground now") {
		t.Errorf("Codex PreToolUse notice should say pop in the foreground:\n%s", ctx)
	}
	if strings.Contains(ctx, "Monitor") {
		t.Errorf("Codex PreToolUse notice still points at a Monitor:\n%s", ctx)
	}
}

// Guard: inside a Codex shell (CODEX_THREAD_ID set, not an auto run)
// `pop --wait` refuses with exit 3, so a stray listener loop stops and the
// pending message stays unread for the foreground pop.
func TestInboxPopWaitRefusedInInteractiveCodex(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "cx", "sid-cx")
	pinCodex(t, db, "cx")
	captureStdout(t, func() { _ = cmdMessage([]string{"user/cx", "ping"}) })
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "sid-cx")
	for _, args := range [][]string{
		{"--wait", "--timeout", "1", "--json"},
		{"--wait", "--timeout", "1", "--keep-unread"},
	} {
		var rc int
		out := captureStdout(t, func() { rc = inboxPop(args) })
		if rc != popExitNoListener {
			t.Fatalf("pop %v in Codex rc=%d, want %d; out=%s", args, rc, popExitNoListener, out)
		}
		if !strings.Contains(out, "Codex") || !strings.Contains(out, "one-shot `flow inbox pop` in the foreground") {
			t.Errorf("refusal must explain itself and give the foreground-pop rule, got: %s", out)
		}
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM bus_messages WHERE body='ping'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("refused pop changed the message status to %q", status)
	}
	var rc int
	out := captureStdout(t, func() { rc = inboxPop(nil) })
	if rc != 0 || !strings.Contains(out, "ping") {
		t.Fatalf("one-shot pop in Codex rc=%d out=%s", rc, out)
	}
}

// The driver/delegation blocks must not promise a listener a Codex
// session cannot have.
func TestDelegationBlocksDoNotPromiseListener(t *testing.T) {
	for name, s := range map[string]string{
		"driver": driverRoleBlock(), "delegation": driverDelegationBlock(),
	} {
		if strings.Contains(s, "your inbox listener hears it") {
			t.Errorf("%s block still promises an inbox listener: %s", name, s)
		}
	}
}

// `flow do --auto` from a Codex parent must not tell it to arm a Monitor.
func TestAutoSpawnParentReminderCodexSaysNoMonitor(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "sid-cx")
	out := parentListenerReminder()
	if strings.Contains(out, "Monitor") {
		t.Errorf("Codex parent reminder mentions a Monitor: %s", out)
	}
	if !strings.Contains(out, "foreground") {
		t.Errorf("Codex parent reminder should give the foreground pop rule: %s", out)
	}
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-cl")
	if out := parentListenerReminder(); strings.Contains(out, "Monitor(") || !strings.Contains(out, "foreground") {
		t.Errorf("Claude parent with wake on must not arm a Monitor: %s", out)
	}
	t.Setenv(wakeOffEnv, "1")
	if out := parentListenerReminder(); !strings.Contains(out, "Monitor(") {
		t.Errorf("Claude parent with wake off lost the Monitor fallback: %s", out)
	}
}

// Regular Codex sessions run in the workspace-write sandbox; only BARE
// flow bus/read commands match the exec-policy allow-list, so the hook
// must tell Codex to run flow commands bare.
func TestHookSessionStartCodexSaysRunFlowBare(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "cx", "sid-cx")
	pinCodex(t, db, "cx")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	ctx := hookRun(t, `{"session_id":"sid-cx"}`, cmdHookSessionStart)
	for _, want := range []string{"BARE", "readonly database", "approval"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("Codex SessionStart missing sandbox guidance %q:\n%s", want, ctx)
		}
	}
	mkBusTask(t, db, "cl", "sid-cl")
	ctx = hookRun(t, `{"session_id":"sid-cl"}`, cmdHookSessionStart)
	if strings.Contains(ctx, "readonly database") {
		t.Errorf("Claude SessionStart carries Codex sandbox guidance:\n%s", ctx)
	}
}
