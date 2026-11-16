package app

import (
	"strings"
	"testing"
)

// F5(a) + F2 hint: an interactive Codex session has no question tool and
// runs sandboxed. Its SessionStart context must say: ask in plain chat
// and wait for the reply before closing; always close with `flow done`
// (never `flow update task --status done`); and run flow's
// harness-launching commands escalated.
func TestHookSessionStartInteractiveCodexRules(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "cx", "sid-cx")
	pinCodex(t, db, "cx")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	ctx := hookRun(t, `{"session_id":"sid-cx"}`, cmdHookSessionStart)
	for _, want := range []string{
		"no AskUserQuestion", "plain chat", "wait for the user's reply",
		"flow done", "never `flow update task --status done`",
		"escalated", "flow auto",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("interactive Codex SessionStart missing %q:\n%s", want, ctx)
		}
	}
	// Claude has AskUserQuestion and no seatbelt — no Codex rules.
	mkBusTask(t, db, "cl", "sid-cl")
	ctx = hookRun(t, `{"session_id":"sid-cl"}`, cmdHookSessionStart)
	if strings.Contains(ctx, "no AskUserQuestion") {
		t.Errorf("Claude SessionStart got the Codex rules:\n%s", ctx)
	}
}
