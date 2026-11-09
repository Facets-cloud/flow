package app

import (
	"path/filepath"
	"strings"
	"testing"
)

// An auto run is bound, so the generic "arm ONE inbox Monitor" discipline
// used to reach it too. A Codex child then backgrounded a `flow inbox pop
// --wait` loop whose output nobody read: every message sent to the child
// was acked into a /tmp file and lost. Auto runs must get the opposite
// instruction instead.
func TestHookSessionStartAutoRunArmsNoListener(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "kid", "sid-kid")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("FLOW_AUTO_RUN", "kid")
	ctx := hookRun(t, `{"session_id":"sid-kid"}`, cmdHookSessionStart)
	for _, bad := range []string{"Monitor(", "arm ONE inbox Monitor", "fall back to a background Bash", "`flow watch <task-or-project-slug>`"} {
		if strings.Contains(ctx, bad) {
			t.Errorf("auto-run SessionStart still carries listener wording %q:\n%s", bad, ctx)
		}
	}
	for _, want := range []string{"do NOT arm an inbox listener", "`flow inbox pop` in the foreground"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("auto-run SessionStart missing %q:\n%s", want, ctx)
		}
	}

	// The interactive bound session keeps the listener discipline.
	t.Setenv("FLOW_AUTO_RUN", "")
	mkBusTask(t, db, "desk", "sid-desk")
	ctx = hookRun(t, `{"session_id":"sid-desk"}`, cmdHookSessionStart)
	if !strings.Contains(ctx, "arm ONE inbox Monitor") {
		t.Errorf("interactive SessionStart lost the listener discipline:\n%s", ctx)
	}
}

// The auto prompt itself carries the rule, so it holds even where hooks
// are not installed.
func TestAutoPromptForbidsInboxListener(t *testing.T) {
	setupFlowRoot(t)
	for name, p := range map[string]string{
		"orphan": buildAutoBootstrapPrompt("t1", "regular", "", ""),
		"child":  buildAutoBootstrapPrompt("t1", "regular", "", "parent"),
	} {
		for _, want := range []string{"Do NOT arm an inbox listener", "flow inbox pop --wait", "flow watch", "flow inbox pop in the foreground"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s auto prompt missing %q:\n%s", name, want, p)
			}
		}
	}
}

// Guard: `flow inbox pop --wait` refuses inside an auto run, with exit 3
// so the canonical listener loop breaks instead of spinning, and leaves
// the message unread.
func TestInboxPopWaitRefusedInAutoRun(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "kid", "sid-kid")
	mkBusTask(t, db, "boss", "sid-boss")
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-boss")
	captureStdout(t, func() {
		if rc := cmdMessage([]string{"user/kid", "ping"}); rc != 0 {
			t.Fatalf("message rc=%d", rc)
		}
	})

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-kid")
	t.Setenv("FLOW_AUTO_RUN", "kid")
	var rc int
	out := captureStdout(t, func() {
		rc = inboxPop([]string{"--wait", "--timeout", "1", "--json"})
	})
	if rc != popExitNoListener {
		t.Fatalf("pop --wait in auto run rc=%d, want %d; out=%s", rc, popExitNoListener, out)
	}
	if !strings.Contains(out, "auto run") || !strings.Contains(out, "one-shot `flow inbox pop` in the foreground") {
		t.Errorf("refusal must explain itself and give the foreground-pop rule, got: %s", out)
	}
	if strings.Contains(out, "flow inbox read <id>") {
		t.Errorf("refusal still points at flow inbox read: %s", out)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM bus_messages WHERE body='ping'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("refused pop changed the message status to %q", status)
	}

	// A one-shot (non-wait) pop stays allowed: the agent sees its output.
	out = captureStdout(t, func() { rc = inboxPop(nil) })
	if rc != 0 || !strings.Contains(out, "ping") {
		t.Fatalf("one-shot pop in auto run rc=%d out=%s", rc, out)
	}
}

// Item B: every SessionStart text flow injects is read by Claude Code or
// Codex, so none may assume Claude-only tools or call it a Claude session.
func TestHookSessionStartTextIsHarnessNeutral(t *testing.T) {
	setupFlowRoot(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	codexSkill := filepath.Join(codexHome, "skills", "flow", "SKILL.md")
	db := openFlowDB(t)
	mkBusTask(t, db, "desk", "sid-desk")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("FLOW_AUTO_RUN", "")

	outs := map[string]string{
		"bound":   hookRun(t, `{"session_id":"sid-desk"}`, cmdHookSessionStart),
		"unbound": hookRun(t, `{"session_id":"sid-nobody"}`, cmdHookSessionStart),
	}
	for name, ctx := range outs {
		if ctx == "" {
			t.Fatalf("%s: empty context", name)
		}
		if !strings.Contains(ctx, codexSkill) {
			t.Errorf("%s SessionStart lacks the Codex skill path %s:\n%s", name, codexSkill, ctx)
		}
		for _, bad := range []string{
			"invoke the `flow` skill via the Skill tool.",
			"Invoke the `flow` skill via the Skill tool now",
			"Claude session",
			"resumes Claude sessions",
		} {
			if strings.Contains(ctx, bad) {
				t.Errorf("%s SessionStart still says %q:\n%s", name, bad, ctx)
			}
		}
		if strings.Contains(ctx, "CLAUDE.md") && !strings.Contains(ctx, "AGENTS.md") {
			t.Errorf("%s SessionStart names CLAUDE.md without AGENTS.md:\n%s", name, ctx)
		}
	}
}

// In an auto run the PreToolUse notice tells the child to pop in the
// foreground now, not to rely on a Monitor it must not have.
func TestPreToolUseAutoRunSaysPopInForeground(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "kid", "sid-kid")
	mkBusTask(t, db, "boss", "sid-boss")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-boss")
	captureStdout(t, func() { _ = cmdMessage([]string{"user/kid", "ping"}) })
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("FLOW_AUTO_RUN", "kid")
	ctx := hookRun(t, `{"session_id":"sid-kid"}`, cmdHookPreToolUse)
	if !strings.Contains(ctx, "ping") || !strings.Contains(ctx, "in the foreground now") {
		t.Errorf("auto-run PreToolUse notice should say pop in the foreground:\n%s", ctx)
	}
	if strings.Contains(ctx, "Monitor") {
		t.Errorf("auto-run PreToolUse notice still points at a Monitor:\n%s", ctx)
	}
}
