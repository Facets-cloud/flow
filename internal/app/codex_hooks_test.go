package app

import (
	"encoding/json"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

const codexHookSID = "019ff4b1-6162-7263-974f-f5f1866ad0fe"

// Codex's stop.command.output schema is closed (additionalProperties:
// false) and has no hookSpecificOutput; a Stop hook that wants the agent
// to see text must return {"decision":"block","reason":...}, which Codex
// feeds back as a continuation prompt — the same effect Claude gives the
// nudge. stop_hook_active still guards the loop.
func TestHookStopCodexUsesBlockDecision(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", codexHookSID)
	db := openFlowDB(t)
	mkBusTask(t, db, "task-c", codexHookSID)
	if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug='task-c'`); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.AddWatch(db, "user", "task-c"); err != nil {
		t.Fatal(err)
	}
	out := stopHookOnce(t, false)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if parsed["decision"] != "block" {
		t.Errorf("decision=%v, want block: %s", parsed["decision"], out)
	}
	if r, _ := parsed["reason"].(string); !strings.Contains(r, "flow broadcast") {
		t.Errorf("reason missing nudge: %s", out)
	}
	if _, ok := parsed["hookSpecificOutput"]; ok {
		t.Errorf("Codex Stop output must not carry hookSpecificOutput: %s", out)
	}
	if out := stopHookOnce(t, true); strings.TrimSpace(out) != "" {
		t.Errorf("nudge fired during hook continuation: %s", out)
	}
}
