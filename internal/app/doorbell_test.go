package app

import (
	"io/fs"
	"strings"
	"testing"
)

// A `flow message` to a task whose session runs on Claude prints the
// session NAME a Claude sender can ring with SendMessage (the content-free
// doorbell). The name must be the exact --name flow gives the session.
func TestMessagePrintsDoorbellForClaudeTask(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_NO_WAKE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-sender")
	db := openFlowDB(t)
	mkBusTask(t, db, "sender", "sid-sender")
	if rc := cmdAdd([]string{"project", "proj", "--slug", "proj", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	mkBusTask(t, db, "rx-claude", "sid-rx")
	mkBusTask(t, db, "rx-codex", "sid-codex")
	mkBusTask(t, db, "rx-floating", "sid-float")
	mkBusTask(t, db, "rx-nosession", "")
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE tasks SET project_slug='proj', harness='claude' WHERE slug='rx-claude'`)
	mustExec(`UPDATE tasks SET project_slug='proj', harness='codex' WHERE slug='rx-codex'`)
	mustExec(`UPDATE tasks SET harness='claude' WHERE slug='rx-floating'`)
	mustExec(`UPDATE tasks SET project_slug='proj' WHERE slug='rx-nosession'`)

	send := func(addr string) string {
		t.Helper()
		return captureStdout(t, func() {
			if rc := cmdMessage([]string{addr, "hello"}); rc != 0 {
				t.Fatalf("message %s rc=%d", addr, rc)
			}
		})
	}

	if out := send("user/rx-claude"); !strings.Contains(out, "doorbell: proj/rx-claude") {
		t.Errorf("claude task should print its doorbell name, got:\n%s", out)
	}
	if out := send("user/rx-floating"); !strings.Contains(out, "doorbell: rx-floating ") {
		t.Errorf("floating claude task doorbell is the bare slug, got:\n%s", out)
	}
	for _, addr := range []string{"user/rx-codex", "user/rx-nosession", "user"} {
		if out := send(addr); strings.Contains(out, "doorbell") {
			t.Errorf("%s must not print a doorbell, got:\n%s", addr, out)
		}
	}
}

// The doorbell name and the bg --name come from one helper, so they can
// never drift (including the 30-rune truncation).
func TestSessionNameMatchesTabTitle(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if rc := cmdAdd([]string{"project", "p", "--slug", "a-rather-long-project-slug", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatal("add project")
	}
	mkBusTask(t, db, "a-long-task-slug-too", "")
	if _, err := db.Exec(`UPDATE tasks SET project_slug='a-rather-long-project-slug' WHERE slug='a-long-task-slug-too'`); err != nil {
		t.Fatal(err)
	}
	task, rc := findTask(db, "a-long-task-slug-too")
	if rc != 0 {
		t.Fatal("findTask")
	}
	got, err := claudeSessionName(db, task)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a-rather-long-project-slug/a-…"; got != want {
		t.Errorf("claudeSessionName = %q, want %q", got, want)
	}
}

// The skill teaches the doorbell: a one-line trigger in the resident core
// (§4.18) and the full recipe + limits in references/messaging.md.
func TestSkillTeachesDoorbell(t *testing.T) {
	core, err := fs.ReadFile(skillFiles(), "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(core), "doorbell:") {
		t.Errorf("SKILL.md §4.18 must point at the doorbell line")
	}
	ref, err := fs.ReadFile(skillFiles(), "references/messaging.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Claude doorbell",
		"ListAgents",
		"SendMessage",
		"flow mail waiting: run `flow inbox pop`",
		"Never for broadcasts",
		"never secrets",
		"stopped",
		"Codex",
	} {
		if !strings.Contains(string(ref), want) {
			t.Errorf("references/messaging.md missing %q", want)
		}
	}
}

// Tab spawn and tab resume both pass --name <project>/<slug> — the same
// name background sessions get — so tab sessions are doorbell-addressable.
func TestCmdDoTabPassesSessionName(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"project", "proj", "--slug", "proj", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatal("add project")
	}
	if rc := cmdAdd([]string{"task", "tabby", "--slug", "tabby", "--project", "proj"}); rc != 0 {
		t.Fatal("add task")
	}
	_, getScript := stubITerm(t)
	stubNewUUID(t, "11111111-2222-4333-8444-555555555555")

	if rc := cmdDo([]string{"tabby"}); rc != 0 {
		t.Fatalf("fresh rc=%d", rc)
	}
	if s := getScript(); !strings.Contains(s, "--name 'proj/tabby'") {
		t.Errorf("fresh tab spawn should pass --name proj/tabby: %s", s)
	}

	stubClaudeStatOK(t)
	if rc := cmdDo([]string{"tabby", "--force"}); rc != 0 {
		t.Fatalf("resume rc=%d", rc)
	}
	if s := getScript(); !strings.Contains(s, "--resume") || !strings.Contains(s, "--name 'proj/tabby'") {
		t.Errorf("tab resume should pass --name proj/tabby: %s", s)
	}
}
