package app

import (
	"encoding/json"
	"flow/internal/flowdb"
	"os"
	"strings"
	"testing"
)

// TestHookSessionStartBoundViaPraxisSessionID verifies that the hook
// discovers a task bound via PRAXIS_SESSION_ID (not just
// CLAUDE_CODE_SESSION_ID). The hook's lookupBoundTask should probe ALL
// registered harness env vars, so a praxis session is correctly bound.
func TestHookSessionStartBoundViaPraxisSessionID(t *testing.T) {
	setupFlowRoot(t)

	seedTask(t, "praxis-bound-task")
	const sid = "deadbeef-1234-4567-8abc-def012345678"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, status='in-progress', session_started=? WHERE slug='praxis-bound-task'`,
		sid, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}

	// Clear CLAUDE_CODE_SESSION_ID, set PRAXIS_SESSION_ID.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PRAXIS_SESSION_ID", sid)

	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})

	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse hook output: %v\nraw: %s", err, out)
	}
	if parsed.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", parsed.HookSpecificOutput.HookEventName)
	}
	ctx := parsed.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "praxis-bound-task") {
		t.Errorf("additionalContext should mention the bound task slug; got:\n%s", ctx)
	}
}

// TestHookUserPromptSubmitBoundViaPraxisSessionID mirrors the above for
// the UserPromptSubmit hook.
func TestHookUserPromptSubmitBoundViaPraxisSessionID(t *testing.T) {
	setupFlowRoot(t)

	if rc := cmdAdd([]string{"task", "Praxis Integration Work"}); rc != 0 {
		t.Fatalf("seed task rc=%d", rc)
	}
	const sid = "deadbeef-1234-4567-8abc-def012345678"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, status='in-progress', session_started=? WHERE slug='praxis-integration-work'`,
		sid, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PRAXIS_SESSION_ID", sid)

	out := captureStdout(t, func() {
		if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse hook output: %v\nraw: %s", err, out)
	}
	if parsed.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Errorf("hookEventName = %q, want UserPromptSubmit", parsed.HookSpecificOutput.HookEventName)
	}
	ctx := parsed.HookSpecificOutput.AdditionalContext
	for _, want := range []string{"Praxis Integration Work", "praxis-integration-work"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("anchor missing %q; got:\n%s", want, ctx)
		}
	}
}

// TestCurrentSessionIDProbesAllHarnesses verifies that the env-var
// lookup probes all registered harnesses, not just claude.
func TestCurrentSessionIDProbesAllHarnesses(t *testing.T) {
	setupFlowRoot(t)

	// Nothing set → empty.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PRAXIS_SESSION_ID", "")
	if got := currentSessionID(); got != "" {
		t.Errorf("currentSessionID() = %q, want empty", got)
	}

	// PRAXIS_SESSION_ID set → found.
	t.Setenv("PRAXIS_SESSION_ID", "test-praxis-sid")
	if got := currentSessionID(); got != "test-praxis-sid" {
		t.Errorf("currentSessionID() = %q, want test-praxis-sid", got)
	}

	// CLAUDE wins (registered first in allHarnesses).
	t.Setenv("CLAUDE_CODE_SESSION_ID", "test-claude-sid")
	t.Setenv("PRAXIS_SESSION_ID", "test-praxis-sid")
	if got := currentSessionID(); got != "test-claude-sid" {
		t.Errorf("currentSessionID() = %q, want test-claude-sid (claude is registered first)", got)
	}
}

// Every ambient-detection caller must resolve through the SAME rule:
// before unification the hook picked the first match while
// currentSessionID refused to guess, so a nested session got task
// context injected by the hook while `flow show` reported no session.
func TestAmbientDetectionAgreesAcrossCallers(t *testing.T) {
	setupFlowRoot(t)

	t.Setenv("CLAUDE_CODE_SESSION_ID", "test-claude-sid")
	t.Setenv("PRAXIS_SESSION_ID", "test-praxis-sid")

	h := ambientHarness()
	if h == nil {
		t.Fatal("ambientHarness() = nil with two session env vars set; callers would disagree")
	}
	if got, want := currentSessionID(), os.Getenv(h.SessionIDEnvVar()); got != want {
		t.Errorf("currentSessionID() = %q but ambientHarness() is %s (%q)", got, h.Name(), want)
	}
	spawn, err := harnessForSpawn(nil)
	if err != nil {
		t.Fatalf("harnessForSpawn: %v", err)
	}
	if spawn.Name() != h.Name() {
		t.Errorf("harnessForSpawn = %s, ambientHarness = %s", spawn.Name(), h.Name())
	}
	if defaultHarness().Name() != h.Name() {
		t.Errorf("defaultHarness = %s, ambientHarness = %s", defaultHarness().Name(), h.Name())
	}
}
