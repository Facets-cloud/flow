package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHookSessionStartNoFlowTaskEmitsAmbientHint pins the contract for
// ad-hoc sessions (e.g. bare `claude` with no FLOW_TASK): the hook must
// emit a value-prop framing that names flow, instructs Skill-tool
// invocation, and explicitly disclaims any "substantive" gate. The
// skill — not the hook — owns the decision of whether to offer a task,
// save a KB entry, or stay quiet.
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
	for _, want := range []string{
		"already tracks",
		"`flow` skill",
		"Skill tool",
		"knowledge base",
		"AskUserQuestion",
		"existing flow task",
		"create a new one",
		"~/.flow/kb/",
		"don't recognize",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("ambient hint missing %q; got:\n%s", want, ctx)
		}
	}
	// The hint must NOT mention "substantive" — naming the past gate
	// just primes Claude to think about gating again. Affirmative
	// framing only: load the skill, confirm task binding, proceed.
	if strings.Contains(ctx, "substantive") {
		t.Errorf("ambient hint must not mention 'substantive'; got:\n%s", ctx)
	}
	// Must NOT include task-specific instructions (no register-session,
	// no slug-bound reload).
	if strings.Contains(ctx, "flow register-session") {
		t.Errorf("ambient hint should not instruct register-session (no FLOW_TASK bound):\n%s", ctx)
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

// TestHookUserPromptSubmitIsNoOp pins the v0.1.0-alpha.7 contract:
// the UserPromptSubmit hook is a permanent no-op — exits 0 with no
// stdout regardless of FLOW_TASK state. Kept around only for forward
// compatibility with stale settings.json entries on older installs.
// `flow skill install` actively removes the entry on upgrade.
func TestHookUserPromptSubmitIsNoOp(t *testing.T) {
	for _, flowTask := range []string{"", "some-slug"} {
		t.Setenv("FLOW_TASK", flowTask)
		out := captureStdout(t, func() {
			if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
				t.Fatalf("FLOW_TASK=%q: rc=%d", flowTask, rc)
			}
		})
		if strings.TrimSpace(out) != "" {
			t.Errorf("FLOW_TASK=%q: expected empty stdout, got:\n%s", flowTask, out)
		}
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
