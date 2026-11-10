package app

import (
	"os"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// F2: from inside Codex's sandbox (CODEX_SANDBOX=seatbelt, no network, no
// keychain), anything flow spawns dies — `flow auto` children could not
// mint a session / were "Not logged in", and `flow done`'s sweep failed
// after the status had already flipped, losing the sweep. Every command
// that launches a harness run must refuse BEFORE any side effect and tell
// the agent to re-run escalated.
func TestSandboxedLaunchCommandsRefuseBeforeSideEffects(t *testing.T) {
	cases := []struct {
		name string
		argv func(t *testing.T) []string
	}{
		{"auto", func(t *testing.T) []string {
			brief := t.TempDir() + "/b.md"
			os.WriteFile(brief, []byte("# do it\n"), 0o644)
			return []string{"auto", "child work", "--slug", "kid", "--brief", brief}
		}},
		{"do", func(t *testing.T) []string { return []string{"do", "victim"} }},
		{"do --auto", func(t *testing.T) []string { return []string{"do", "--auto", "victim"} }},
		{"done", func(t *testing.T) []string { return []string{"done", "started"} }},
		{"update --status done", func(t *testing.T) []string {
			return []string{"update", "task", "started", "--status", "done"}
		}},
		{"update --status done + other fields", func(t *testing.T) []string {
			return []string{"update", "task", "started", "--priority", "low", "--status", "done"}
		}},
		{"run playbook", func(t *testing.T) []string { return []string{"run", "playbook", "pb"} }},
		{"owner tick", func(t *testing.T) []string { return []string{"owner", "tick", "own"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setupFlowRoot(t)
			calls := stubClaudeRunner(t, nil)
			seedTask(t, "victim")
			seedStartedTask(t, "started")
			wd := t.TempDir()
			if rc := cmdAdd([]string{"playbook", "pb", "--work-dir", wd}); rc != 0 {
				t.Fatalf("add playbook rc=%d", rc)
			}
			if rc := cmdAdd([]string{"owner", "own", "--work-dir", wd, "--every", "1h"}); rc != 0 {
				t.Fatalf("add owner rc=%d", rc)
			}
			spawns, _ := stubITerm(t)
			autoCalls, _, _, _, _ := stubAutoLauncher(t, 4242)
			argv := c.argv(t)
			before := sideEffectCounts(t) + "|" + taskStates(t)

			t.Setenv("CODEX_SANDBOX", "seatbelt")
			var rc int
			out := captureStdout(t, func() { rc = Run(argv) })
			t.Setenv("CODEX_SANDBOX", "")

			if rc == 0 {
				t.Errorf("rc=0 inside the Codex sandbox, want a refusal\n%s", out)
			}
			if !strings.Contains(out, "escalated") || !strings.Contains(out, "sandbox") {
				t.Errorf("refusal does not tell the agent to re-run escalated:\n%s", out)
			}
			if after := sideEffectCounts(t) + "|" + taskStates(t); after != before {
				t.Errorf("side effects before refusal:\nbefore=%s\nafter =%s", before, after)
			}
			if *spawns != 0 || len(*calls) != 0 || *autoCalls != 0 {
				t.Errorf("spawned %d tabs / %d sweeps / %d auto runs inside the sandbox", *spawns, len(*calls), *autoCalls)
			}
		})
	}
}

// Outside the sandbox (or escalated), nothing changes; --no-sweep and
// `flow do --here` launch nothing and stay allowed inside it.
func TestSandboxGuardAllowsNonLaunching(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedStartedTask(t, "started")
	t.Setenv("CODEX_SANDBOX", "seatbelt")
	var rc int
	out := captureStdout(t, func() { rc = Run([]string{"update", "task", "started", "--status", "done", "--no-sweep"}) })
	if rc != 0 {
		t.Errorf("--no-sweep refused in sandbox: rc=%d\n%s", rc, out)
	}
}

func taskStates(t *testing.T) string {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{Kind: "", IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, tk := range tasks {
		b.WriteString(tk.Slug + ":" + tk.Status + ":" + tk.Priority + ":" + tk.SessionID.String + ";")
	}
	return b.String()
}
