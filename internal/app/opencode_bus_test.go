package app

import (
	"strings"
	"testing"
)

const ocBusSessionID = "ses_0123456789ABCDEFGHIJKLMNOP"

// TestHookSessionStartOpencodeListenerDiscipline pins the flow-side hook
// parity change: an opencode-bound task's SessionStart context tells the
// agent the plugin delivers `[flow inbox]` prompts into the conversation,
// and must NOT tell it to arm a Monitor listener (opencode has none).
func TestHookSessionStartOpencodeListenerDiscipline(t *testing.T) {
	setupFlowRoot(t)
	t.Cleanup(func() { hookSessionID, hookPrompt, hookPromptKnown = "", "", false })
	t.Setenv("OPENCODE_SESSION_ID", ocBusSessionID)
	db := openFlowDB(t)
	mkBusTask(t, db, "oc-bound", ocBusSessionID)
	if _, err := db.Exec(`UPDATE tasks SET harness='opencode' WHERE slug='oc-bound'`); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	ctx := busHookContext(t, out)
	if !strings.Contains(ctx, "[flow inbox]") {
		t.Errorf("opencode SessionStart context missing [flow inbox] text:\n%s", ctx)
	}
	if strings.Contains(ctx, "Monitor(") {
		t.Errorf("opencode SessionStart context must not tell the agent to arm a Monitor:\n%s", ctx)
	}
	if !strings.Contains(ctx, "flow watch") {
		t.Errorf("opencode SessionStart context should keep the flow watch subscription hint:\n%s", ctx)
	}
}

// TestHookSessionStartClaudeListenerDisciplineKept pins that a
// claude-bound task still gets the existing Monitor listener text
// (claude behaviour unchanged).
func TestHookSessionStartClaudeListenerDisciplineKept(t *testing.T) {
	setupFlowRoot(t)
	t.Cleanup(func() { hookSessionID, hookPrompt, hookPromptKnown = "", "", false })
	const sid = "deadbeef-1234-4567-8abc-def012345678"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	db := openFlowDB(t)
	mkBusTask(t, db, "claude-bound", sid)
	if _, err := db.Exec(`UPDATE tasks SET harness='claude' WHERE slug='claude-bound'`); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	ctx := busHookContext(t, out)
	if !strings.Contains(ctx, "Monitor(") {
		t.Errorf("claude SessionStart context lost the Monitor listener discipline:\n%s", ctx)
	}
	if strings.Contains(ctx, "[flow inbox]") {
		t.Errorf("claude SessionStart context must not carry the opencode listener text:\n%s", ctx)
	}
}

// TestIsHumanPromptFlowInjectedPrefixes pins that prompts injected by
// flow's opencode plugin are not treated as human-typed.
func TestIsHumanPromptFlowInjectedPrefixes(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, p := range []string{
		`[flow inbox] {"id":"ab12","from":"user","body":"hi"}`,
		"[flow] a one-liner broadcast nudge",
	} {
		if isHumanPrompt(true, p, getenv) {
			t.Errorf("isHumanPrompt(%q) = true, want false (flow-injected)", p)
		}
	}
	if !isHumanPrompt(true, "please fix the failing test", getenv) {
		t.Error("isHumanPrompt(human text) = false, want true")
	}
}
