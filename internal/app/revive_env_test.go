package app

import (
	"strings"
	"testing"
)

// Gap C: a revive is launched from the CHILD's process, which runs inside
// a Claude session. Its messaging socket, token, pid and nesting markers
// belong to that child; the revive's supervisor and resumed session must
// not inherit them.
func TestAutoChildEnvStripsHarnessSessionEnv(t *testing.T) {
	leaky := []string{
		"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN",
		"CLAUDE_PID", "CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID",
	}
	for _, k := range leaky {
		t.Setenv(k, "child-value")
	}
	t.Setenv("CLAUDE_CODE_KEEP_ME", "1") // unrelated config survives
	db, envs := reviveSetup(t)
	mail(t, db, "g1", "c", "d", false)
	if len(*envs) != 1 {
		t.Fatalf("launches = %d, want 1", len(*envs))
	}
	for _, env := range [][]string{autoChildEnv(), (*envs)[0]} {
		for _, kv := range env {
			for _, k := range leaky {
				if strings.HasPrefix(kv, k+"=") {
					t.Errorf("env leaks %s", kv)
				}
			}
		}
		if !hasEnv(env, "CLAUDE_CODE_KEEP_ME=1") {
			t.Error("unrelated CLAUDE_CODE_ config was stripped")
		}
	}
}
