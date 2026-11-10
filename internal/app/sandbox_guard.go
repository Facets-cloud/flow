package app

import (
	"fmt"
	"os"
)

// codexSandboxEnvVar is set by Codex on every shell command it runs under
// its OS sandbox (macOS seatbelt: CODEX_SANDBOX=seatbelt, alongside
// CODEX_SANDBOX_NETWORK_DISABLED=1). Commands the user approved to run
// escalated, and bypass-mode sessions, run without it.
const codexSandboxEnvVar = "CODEX_SANDBOX"

// inCodexSandbox reports whether this flow process runs inside Codex's
// sandbox, where a harness flow launches can't reach the network, the
// keychain, or ~/.claude — so it dies (Codex can't mint a thread, Claude
// is "Not logged in", the close-out sweep fails).
func inCodexSandbox() bool { return os.Getenv(codexSandboxEnvVar) != "" }

// refuseInCodexSandbox is the guard every harness-launching command runs
// before any side effect. It returns 0 when launching is fine; otherwise
// it prints the escalation instruction and returns 1. what names the
// command for the message (e.g. "flow auto").
func refuseInCodexSandbox(what string) int {
	if !inCodexSandbox() {
		return 0
	}
	fmt.Fprintf(os.Stderr,
		"error: %s launches a harness run, which cannot work inside the Codex sandbox (%s=%s: no network, "+
			"no keychain or ~/.claude access) — nothing was changed.\n"+
			"  re-run this exact command with escalated permissions (outside the sandbox): request "+
			"escalation/approval for it, the same way you would for `git push`.\n",
		what, codexSandboxEnvVar, os.Getenv(codexSandboxEnvVar))
	return 1
}
