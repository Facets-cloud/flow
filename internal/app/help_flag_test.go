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
		{"connector", "list"},
		{"connector", "escalate"},
		{"connector", "priority"},
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

// Review note 3 on #132: `flow <group> --help` answered "unknown
// subcommand" with rc 2. A group prints its own usage and exits 0.
func TestGroupHelpPrintsUsage(t *testing.T) {
	for _, group := range []string{"add", "run", "show", "list", "update", "owner", "workdir", "skill", "hook", "connector"} {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(group+" "+flag, func(t *testing.T) {
				setupFlowRoot(t)
				before := sideEffectCounts(t)
				stderr := captureStderr(t)
				var rc int
				stdout := captureStdout(t, func() { rc = Run([]string{group, flag}) })
				errOut := stderr()
				if rc != 0 {
					t.Errorf("rc=%d, want 0\nstderr: %s", rc, errOut)
				}
				out := stdout + errOut
				if !strings.Contains(strings.ToLower(out), "usage") || !strings.Contains(out, "flow "+group+" ") {
					t.Errorf("no %s usage printed: %q", group, out)
				}
				if strings.Contains(out, "unknown") {
					t.Errorf("help treated as an unknown subcommand: %q", out)
				}
				if after := sideEffectCounts(t); after != before {
					t.Errorf("side effects: before=%q after=%q", before, after)
				}
			})
		}
	}
}

// `--` ends flag parsing, so a name that starts with a dash can be
// created on purpose (a bare leading dash still reads as a misplaced flag).
func TestAddTaskDoubleDashAllowsDashName(t *testing.T) {
	setupFlowRoot(t)
	stderr := captureStderr(t)
	var rc int
	out := captureStdout(t, func() { rc = Run([]string{"add", "task", "--", "-5 degrees fix"}) })
	if rc != 0 {
		t.Fatalf("rc=%d out=%s stderr=%s", rc, out, stderr())
	}
	db := openFlowDB(t)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE name = ?`, "-5 degrees fix").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("task named %q not created (count=%d); out=%s", "-5 degrees fix", n, out)
	}
}

func TestSplitPositionalDoubleDash(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		pos     string
		rest    []string
		missing bool
	}{
		{[]string{"--", "-5 degrees fix"}, "-5 degrees fix", []string{}, false},
		{[]string{"--", "--help", "--slug", "x"}, "--help", []string{"--slug", "x"}, false},
		{[]string{"--"}, "", []string{"--"}, true},
		{[]string{"--", ""}, "", []string{"--", ""}, true},
		{[]string{"-5 degrees fix"}, "", []string{"-5 degrees fix"}, true},
		{[]string{"name", "--slug", "x"}, "name", []string{"--slug", "x"}, false},
	} {
		pos, rest, missing := splitPositional(tc.args)
		if pos != tc.pos || missing != tc.missing || strings.Join(rest, "\x00") != strings.Join(tc.rest, "\x00") {
			t.Errorf("splitPositional(%q) = %q, %q, %v; want %q, %q, %v", tc.args, pos, rest, missing, tc.pos, tc.rest, tc.missing)
		}
	}
}

// A backquoted word in a flag's usage string is taken by package flag as
// the argument NAME, so `--no-sweep` rendered as "-no-sweep flow done".
func TestHelpDoesNotRenderBackquotedCommandAsArgName(t *testing.T) {
	for _, argv := range [][]string{
		{"update", "task", "--help"},
		{"do", "--help"},
		{"run", "playbook", "--help"},
		{"add", "owner", "--help"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			setupFlowRoot(t)
			stderr := captureStderr(t)
			var rc int
			stdout := captureStdout(t, func() { rc = Run(argv) })
			out := stdout + stderr()
			if rc != 0 {
				t.Fatalf("rc=%d out=%s", rc, out)
			}
			for _, bad := range []string{"-no-sweep flow done", "-auto flow done", "-every flow owner next"} {
				if strings.Contains(out, bad) {
					t.Errorf("help renders %q:\n%s", bad, out)
				}
			}
		})
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
