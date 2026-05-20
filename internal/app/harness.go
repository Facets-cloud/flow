package app

import (
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

// defaultHarness returns the harness flow uses for newly-spawned
// sessions when the caller has no specific binding. Today: always
// claude. Future: read $FLOW_HARNESS or look up tasks.harness.
//
// The app package owns this choice (rather than the harness package)
// so the harness package stays free of any concrete-impl imports,
// avoiding an import cycle once each impl (claude, codex, gemini,
// …) lives in its own sub-package.
func defaultHarness() harness.Harness {
	return claude.New()
}
