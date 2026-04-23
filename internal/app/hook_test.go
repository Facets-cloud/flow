package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHookSessionStartNoFlowTaskEmitsAmbientHint pins the contract for
// ad-hoc sessions (e.g. bare `flowde` with no FLOW_TASK): the hook must
// emit additionalContext naming the flow skill and instructing the
// session to invoke it via the Skill tool when the user's request
// touches flow concerns. Without this hint, Claude Code may not
// auto-invoke the skill on the user's first turn.
func TestHookSessionStartNoFlowTaskEmitsAmbientHint(t *testing.T) {
	t.Setenv("FLOW_TASK", "")
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
	if !strings.Contains(ctx, "`flow` skill") {
		t.Errorf("ambient hint must name the `flow` skill, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "Skill tool") {
		t.Errorf("ambient hint must instruct Skill tool invocation, got:\n%s", ctx)
	}
	// Must NOT include task-specific instructions (register-session,
	// reading the brief) since there is no task bound to this session.
	if strings.Contains(ctx, "flow register-session") {
		t.Errorf("ambient hint should not instruct register-session (no FLOW_TASK bound):\n%s", ctx)
	}
	// Must nudge the session to offer "create new task or switch to an
	// existing one" when the user starts substantive work — otherwise
	// the session's transcript is homeless. Both levers must be named.
	if !strings.Contains(ctx, "create a new flow task") {
		t.Errorf("ambient hint must offer to create a new task, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "switch to an existing task") {
		t.Errorf("ambient hint must offer to switch to an existing task, got:\n%s", ctx)
	}
}

// TestHookSessionStartRequiresSkillInvocation pins the invariant that
// the injected additionalContext explicitly instructs the session to
// invoke the flow skill via the Skill tool as its first action, and
// mentions the task slug so the agent has something anchor-visible.
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
	// Self-registration is gone — the UUID is pre-allocated by `flow do`.
	// Make sure we don't regress by re-introducing it here.
	if strings.Contains(ctx, "register-session") {
		t.Errorf("additionalContext should not mention register-session (pre-allocated by flow do):\n%s", ctx)
	}
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
	if strings.Contains(prompt, "register-session") {
		t.Errorf("bootstrap prompt should not mention register-session (pre-allocated by flow do):\n%s", prompt)
	}
	if !strings.Contains(prompt, "task-x") {
		t.Errorf("bootstrap prompt must mention the task slug")
	}
}
