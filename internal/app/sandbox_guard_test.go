package app

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

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

// CodeRabbit #132: the scheduler pass `owner tick-due` launches headless
// owner ticks too, so it must refuse inside the sandbox before dispatching
// or advancing next_wake_at.
func TestSandboxRefusesOwnerTickDue(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	past := sql.NullString{String: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Valid: true}
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "due", Name: "D", WorkDir: "/x", Every: "30m", NextWakeAt: past}); err != nil {
		t.Fatal(err)
	}
	launched := 0
	old := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) { launched++; return 7, nil }
	t.Cleanup(func() { ownerTickLauncher = old })

	t.Setenv("CODEX_SANDBOX", "seatbelt")
	var rc int
	out := captureStdout(t, func() { rc = Run([]string{"owner", "tick-due"}) })
	if rc == 0 || !strings.Contains(out, "escalated") {
		t.Errorf("tick-due inside the sandbox: rc=%d, want a refusal\n%s", rc, out)
	}
	if launched != 0 {
		t.Errorf("launched %d owner ticks inside the sandbox", launched)
	}
	o, err := flowdb.GetOwner(db, "due")
	if err != nil {
		t.Fatal(err)
	}
	if o.NextWakeAt.String != past.String {
		t.Errorf("next_wake_at advanced to %q inside the sandbox", o.NextWakeAt.String)
	}
}

// CodeRabbit #132: a refused `update --status done --work-dir X --mkdir`
// said "nothing was changed" but had already created X.
func TestSandboxRefusedUpdateCreatesNoWorkDir(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedStartedTask(t, "started")
	newDir := t.TempDir() + "/new-workspace"
	t.Setenv("CODEX_SANDBOX", "seatbelt")
	var rc int
	out := captureStdout(t, func() {
		rc = Run([]string{"update", "task", "started", "--status", "done", "--work-dir", newDir, "--mkdir"})
	})
	if rc == 0 {
		t.Fatalf("rc=0, want a refusal\n%s", out)
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Errorf("refused update created %s (stat err=%v)", newDir, err)
	}
}
