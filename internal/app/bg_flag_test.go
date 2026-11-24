package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// spawnPrompt returns the prompt positional of the last `claude --bg` spawn
// (argv: --bg --name <title> <prompt> ...).
func spawnPrompt(t *testing.T, c *bgCalls) string {
	t.Helper()
	if len(c.lastSpawn) < 4 {
		t.Fatalf("spawn argv too short: %v", c.lastSpawn)
	}
	return c.lastSpawn[3]
}

// `flow do <slug> --bg` spawns a background session without $FLOW_TERM=bg
// (the env stays unset, so children never inherit it).
func TestCmdDoBgFlagSpawnsBackground(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgflag")
	t.Setenv("FLOW_TERM", "")
	itermCount, _ := stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bgflag", "--bg"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *itermCount != 0 || c.spawn != 1 {
		t.Fatalf("tabs=%d bg spawns=%d, want 0/1", *itermCount, c.spawn)
	}
	if os.Getenv("FLOW_TERM") != "" {
		t.Errorf("--bg leaked FLOW_TERM=%q", os.Getenv("FLOW_TERM"))
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "bgflag")
	if task.SessionID.String != bgFullSID || task.Harness.String != "claude" {
		t.Errorf("task = %q/%q", task.SessionID.String, task.Harness.String)
	}
}

func TestCmdDoBgFlagConflicts(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgc")
	for _, args := range [][]string{
		{"bgc", "--bg", "--auto"},
		{"bgc", "--bg", "--here"},
	} {
		stderr := captureStderr(t)
		if rc := cmdDo(args); rc != 2 {
			t.Errorf("%v: rc=%d, want 2", args, rc)
		}
		if got := stderr(); !strings.Contains(got, "--bg") {
			t.Errorf("%v: stderr %q should name --bg", args, got)
		}
	}
}

// A harness without background support refuses --bg with a message that
// names the flag (not only $FLOW_TERM).
func TestCmdDoBgFlagRefusedForCodex(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgcodex")
	stubITerm(t)
	stderr := captureStderr(t)
	if rc := cmdDo([]string{"bgcodex", "--bg", "--harness", "codex"}); rc == 0 {
		t.Fatal("want refusal")
	}
	if got := stderr(); !strings.Contains(got, "--bg") || !strings.Contains(got, "codex") {
		t.Errorf("stderr = %q", got)
	}
}

// A fresh background spawn carries the persistence rules: no human at a
// prompt, a blocker is mailed and the turn ended (never exit), mail wakes
// the session, flow done closes it. Parentless → reports go to the human.
func TestBgSpawnPromptIsPersistent(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgp")
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgp", "--bg"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	p := spawnPrompt(t, c)
	for _, want := range []string{
		"flow show task bgp", // normal bootstrap kept
		"BACKGROUND session", // persistence section
		"END YOUR TURN",      // blocker → end turn
		"Never exit",         // ... not die
		"wakes",              // mail wakes it
		"flow message user ", // parentless: the human
		"flow done bgp",      // closes itself
		"flow inbox pop",     // foreground drain
	} {
		if !strings.Contains(p, want) {
			t.Errorf("bg prompt missing %q:\n%s", want, p)
		}
	}
	if strings.ContainsAny(p, "\"'`$") {
		t.Errorf("bg prompt must stay shell-safe:\n%s", p)
	}
}

// With a parent task the persistence rules report to user/<parent>.
func TestBgSpawnPromptReportsToParent(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgparent")
	seedTask(t, "bgkid")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='bgparent' WHERE slug='bgkid'`); err != nil {
		t.Fatal(err)
	}
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgkid", "--bg"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if p := spawnPrompt(t, c); !strings.Contains(p, "flow message user/bgparent") {
		t.Errorf("bg prompt should report to the parent:\n%s", p)
	}
}

// The persistence rules apply to every background spawn, $FLOW_TERM=bg too.
func TestBgSpawnPromptPersistentUnderFlowTermBg(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgenv")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgenv"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if p := spawnPrompt(t, c); !strings.Contains(p, "END YOUR TURN") {
		t.Errorf("FLOW_TERM=bg spawn lacks persistence rules:\n%s", p)
	}
}

// The interactive tab bootstrap is unchanged (no persistence section).
func TestTabBootstrapHasNoBgSection(t *testing.T) {
	if p := buildTaskBootstrapPrompt("x"); strings.Contains(p, "BACKGROUND session") {
		t.Errorf("tab prompt gained the bg section:\n%s", p)
	}
}

// `flow auto "<name>" --brief b --bg` adds the task, writes the brief and
// spawns a background session (skip-permissions implied), not an auto run.
func TestCmdAutoBg(t *testing.T) {
	root := setupFlowRoot(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	brief := filepath.Join(t.TempDir(), "b.md")
	if err := os.WriteFile(brief, []byte("## Goal\nship it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var rc int
	out := captureStdout(t, func() {
		rc = cmdAuto([]string{"BG child", "--slug", "bgchild", "--brief", brief, "--bg"})
	})
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	if c.spawn != 1 || !containsArg(c.lastSpawn, "--dangerously-skip-permissions") {
		t.Errorf("bg spawn = %d argv %v, want one skip-permissions spawn", c.spawn, c.lastSpawn)
	}
	if !strings.Contains(out, "spawning bg child: bgchild") {
		t.Errorf("out = %s", out)
	}
	got, _ := os.ReadFile(filepath.Join(root, "tasks", "bgchild", "brief.md"))
	if !strings.Contains(string(got), "ship it") {
		t.Errorf("brief not written: %q", got)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "bgchild")
	if task.SessionID.String != bgFullSID || task.AutoRunStatus.Valid {
		t.Errorf("task session=%q auto_run=%v", task.SessionID.String, task.AutoRunStatus)
	}
}

// `flow auto --bg` on a harness without background support refuses before
// creating the task, so nothing is left behind.
func TestCmdAutoBgRefusesUnsupportedHarnessUpFront(t *testing.T) {
	setupFlowRoot(t)
	brief := filepath.Join(t.TempDir(), "b.md")
	os.WriteFile(brief, []byte("goal\n"), 0o644)
	stderr := captureStderr(t)
	if rc := cmdAuto([]string{"Codex bg", "--slug", "cbg", "--brief", brief, "--bg", "--harness", "codex"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
	if got := stderr(); !strings.Contains(got, "background") {
		t.Errorf("stderr = %q", got)
	}
	if _, err := flowdb.GetTask(openFlowDB(t), "cbg"); err == nil {
		t.Error("task was created despite the refusal")
	}
}
