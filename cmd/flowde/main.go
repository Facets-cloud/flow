// Package main implements `flowde` — a thin wrapper around the `claude`
// CLI. Its job is twofold:
//
//  1. Keep the flow skill (and its SessionStart hook) fresh in
//     ~/.claude/ by calling `flow skill install --force` on every
//     invocation. That command is idempotent: it rewrites
//     ~/.claude/skills/flow/SKILL.md from the embedded copy and ensures
//     the flow SessionStart hook is wired in ~/.claude/settings.json.
//  2. Exec `claude` with the original argv, so the user's interactive
//     session is indistinguishable from running `claude` directly —
//     stdin/stdout/stderr/signals all flow through because we replace
//     the current process via syscall.Exec.
//
// `flow do` also spawns `flowde` (not `claude`) so the skill-freshness
// guarantee applies uniformly across both ad-hoc invocations and
// per-task sessions.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// installSkill runs `flow skill install --force`. Overridable for tests.
var installSkill = defaultInstallSkill

// execClaude replaces the current process with `claude <args...>`.
// Overridable for tests (so we don't actually exec during go test).
var execClaude = defaultExecClaude

func defaultInstallSkill() error {
	cmd := exec.Command("flow", "skill", "install", "--force")
	// Silence success output — on every flowde invocation this prints
	// "installed flow skill to ..." which is noise for the user.
	// Errors still surface via stderr.
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func defaultExecClaude(args []string) error {
	path, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found on PATH: %w", err)
	}
	return syscall.Exec(path, append([]string{"claude"}, args...), os.Environ())
}

// run does the wrapper's work. Returned in a function so tests can
// exercise it without forking a subprocess. Only reached by the real
// main() when execClaude returns without replacing the process (which
// only happens in tests or on error).
func run(args []string) int {
	if err := installSkill(); err != nil {
		// Non-fatal: surface the error but still hand off to claude.
		// A stale or missing flow binary shouldn't block the user's
		// session from opening.
		fmt.Fprintf(os.Stderr, "flowde: warning: `flow skill install --force` failed: %v\n", err)
	}
	if err := execClaude(args); err != nil {
		fmt.Fprintf(os.Stderr, "flowde: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:]))
}
