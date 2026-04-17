package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHookSessionStartNoFlowTaskEmitsNothing pins the contract for
// non-flow sessions: the hook produces no output at all, so Claude Code
// treats it as a deliberate no-op.
func TestHookSessionStartNoFlowTaskEmitsNothing(t *testing.T) {
	t.Setenv("FLOW_TASK", "")
	out := captureStdout(t, func() {
		if rc := cmdHookSessionStart(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("non-flow hook emitted output: %q", out)
	}
}

// TestHookSessionStartRequiresSkillInvocation pins the requirement from
// brief fix-register-session-path-encoding-always: the injected
// additionalContext must explicitly instruct the session to invoke the
// flow skill via the Skill tool, and must position it BEFORE
// register-session (so skill load does not depend on registration).
func TestHookSessionStartRequiresSkillInvocation(t *testing.T) {
	t.Setenv("FLOW_TASK", "some-slug")
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
	if !strings.Contains(ctx, "Skill tool") {
		t.Errorf("additionalContext must instruct Skill tool invocation, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "`flow` skill") {
		t.Errorf("additionalContext must name the `flow` skill, got:\n%s", ctx)
	}
	// Skill invocation must come before register-session so skill load
	// is not gated on registration success.
	skillIdx := strings.Index(ctx, "Skill tool")
	regIdx := strings.Index(ctx, "flow register-session")
	if skillIdx < 0 || regIdx < 0 {
		t.Fatalf("skill or register-session phrase missing from context:\n%s", ctx)
	}
	if skillIdx > regIdx {
		t.Errorf("skill invocation must precede register-session; skill@%d reg@%d", skillIdx, regIdx)
	}
	// Must mention the task slug verbatim for agent-visible context.
	if !strings.Contains(ctx, "some-slug") {
		t.Errorf("additionalContext should mention the task slug, got:\n%s", ctx)
	}
}

// TestBuildBootstrapPromptInvokesSkill pins the same invariant for the
// fresh-spawn prompt used by `flow do` (the hook only covers resume).
func TestBuildBootstrapPromptInvokesSkill(t *testing.T) {
	prompt := buildBootstrapPrompt("task-x")
	if !strings.Contains(prompt, "flow skill") && !strings.Contains(prompt, "`flow` skill") {
		t.Errorf("bootstrap prompt must name the flow skill:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Skill tool") {
		t.Errorf("bootstrap prompt must instruct Skill tool invocation:\n%s", prompt)
	}
	skillIdx := strings.Index(prompt, "Skill tool")
	regIdx := strings.Index(prompt, "flow register-session")
	if skillIdx < 0 || regIdx < 0 {
		t.Fatalf("skill or register-session phrase missing:\n%s", prompt)
	}
	if skillIdx > regIdx {
		t.Errorf("skill invocation must precede register-session; skill@%d reg@%d", skillIdx, regIdx)
	}
	if !strings.Contains(prompt, "task-x") {
		t.Errorf("bootstrap prompt must mention the task slug")
	}
}
