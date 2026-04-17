package main

import (
	"errors"
	"reflect"
	"testing"
)

// restore swaps installSkill/execClaude back to their production defaults
// at test end so tests don't leak into each other.
func restore(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		installSkill = defaultInstallSkill
		execClaude = defaultExecClaude
	})
}

// TestRunInstallsSkillThenExecsClaude pins the core contract: flowde
// first refreshes the skill, then hands off to claude with the exact
// argv it received.
func TestRunInstallsSkillThenExecsClaude(t *testing.T) {
	restore(t)

	var order []string
	var captured []string
	installSkill = func() error {
		order = append(order, "install")
		return nil
	}
	execClaude = func(args []string) error {
		order = append(order, "exec")
		captured = args
		return nil
	}

	argv := []string{"--resume", "abc-123", "-p", "hello"}
	if rc := run(argv); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if !reflect.DeepEqual(order, []string{"install", "exec"}) {
		t.Errorf("order=%v, want [install exec]", order)
	}
	if !reflect.DeepEqual(captured, argv) {
		t.Errorf("forwarded args=%v, want %v", captured, argv)
	}
}

// TestRunInstallFailureIsNonFatal verifies a `flow skill install`
// failure surfaces as a warning but still lets claude launch. The user
// can recover a stale skill later; they shouldn't be blocked from
// opening a session.
func TestRunInstallFailureIsNonFatal(t *testing.T) {
	restore(t)

	installSkill = func() error { return errors.New("flow binary missing") }
	execed := false
	execClaude = func(args []string) error {
		execed = true
		return nil
	}
	if rc := run(nil); rc != 0 {
		t.Errorf("rc=%d, want 0", rc)
	}
	if !execed {
		t.Error("claude should have been exec'd even after install failure")
	}
}

// TestRunExecFailureReturnsNonZero verifies flowde exits non-zero if
// the exec itself fails (e.g., claude not on PATH) so users see a
// diagnostic instead of a silent disappear.
func TestRunExecFailureReturnsNonZero(t *testing.T) {
	restore(t)

	installSkill = func() error { return nil }
	execClaude = func(args []string) error { return errors.New("claude not found") }
	if rc := run(nil); rc != 1 {
		t.Errorf("rc=%d, want 1", rc)
	}
}

// TestRunForwardsEmptyArgs covers bare `flowde` with no user args —
// the wrapper should still run install and exec with an empty slice.
func TestRunForwardsEmptyArgs(t *testing.T) {
	restore(t)

	installed := false
	var captured []string
	installSkill = func() error { installed = true; return nil }
	execClaude = func(args []string) error { captured = args; return nil }

	if rc := run([]string{}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !installed {
		t.Error("installSkill should have been called")
	}
	if len(captured) != 0 {
		t.Errorf("args=%v, want empty slice", captured)
	}
}
