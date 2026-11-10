package app

import (
	"strings"
	"testing"
)

// F3: `flow add task --help` created a task literally named "--help"
// (live test: help-13, help-14). Every command that takes a positional
// name/ref before its flags must treat -h/--help as a help request: print
// usage, exit 0, and touch nothing.
func TestHelpFlagPrintsUsageWithoutSideEffects(t *testing.T) {
	cmds := [][]string{
		{"add", "task"},
		{"add", "project"},
		{"add", "playbook"},
		{"add", "owner"},
		{"auto"},
		{"done"},
		{"edit"},
		{"archive"},
		{"unarchive"},
		{"update", "task"},
		{"update", "project"},
		{"run", "playbook"},
		{"owner", "show"},
		{"owner", "start"},
		{"owner", "pause"},
		{"owner", "retire"},
		{"owner", "next"},
		{"owner", "tick"},
		{"message"},
		{"broadcast"},
		{"workdir", "add"},
		{"workdir", "remove"},
		{"connector", "add"},
		{"connector", "remove"},
		{"connector", "test"},
		{"do"},
		{"show", "task"},
		{"show", "project"},
	}
	for _, base := range cmds {
		for _, tail := range [][]string{
			{"--help"},
			{"-h"},
			// a flag that would otherwise satisfy a required-field check
			{"--help", "--work-dir", "/tmp", "--every", "1h"},
		} {
			argv := append(append([]string{}, base...), tail...)
			t.Run(strings.Join(argv, " "), func(t *testing.T) {
				root := setupFlowRoot(t)
				before := sideEffectCounts(t)
				stderr := captureStderr(t)
				var rc int
				stdout := captureStdout(t, func() { rc = Run(argv) })
				errOut := stderr()
				if rc != 0 {
					t.Errorf("rc=%d, want 0\nstderr: %s", rc, errOut)
				}
				if !strings.Contains(strings.ToLower(stdout+errOut), "usage") {
					t.Errorf("no usage printed\nstdout: %s\nstderr: %s", stdout, errOut)
				}
				if after := sideEffectCounts(t); after != before {
					t.Errorf("side effects under %s: before=%q after=%q", root, before, after)
				}
			})
		}
	}
}

// A name that starts with a dash is a misplaced flag, never a name.
func TestAddTaskLeadingFlagIsNotAName(t *testing.T) {
	setupFlowRoot(t)
	before := sideEffectCounts(t)
	stderr := captureStderr(t)
	if rc := Run([]string{"add", "task", "--slug", "x", "real name"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
	if got := stderr(); !strings.Contains(got, "requires a name") {
		t.Errorf("stderr=%q", got)
	}
	if after := sideEffectCounts(t); after != before {
		t.Errorf("task created: before=%q after=%q", before, after)
	}
}

func sideEffectCounts(t *testing.T) string {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	var parts []string
	for _, tbl := range []string{"tasks", "projects", "playbooks", "owners", "workdirs", "bus_messages"} {
		var n string
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		parts = append(parts, tbl+"="+n)
	}
	return strings.Join(parts, ",")
}
