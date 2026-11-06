package app

import (
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
	"testing"
)

// Only Codex opts into the deferred close-out sweep.
func TestDefersCloseoutInAutoRunCapability(t *testing.T) {
	if !harness.DefersCloseoutInAutoRun(codex.New()) {
		t.Error("codex should defer the close-out sweep in auto runs")
	}
	if harness.DefersCloseoutInAutoRun(claude.New()) {
		t.Error("claude must keep the inline sweep")
	}
}
