package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func TestParseVerifyCommands(t *testing.T) {
	brief := strings.Join([]string{
		"# Goal",
		"```sh",
		"echo not-in-section",
		"```",
		"## Done when",
		"tests green",
		"## Verify",
		"Run these (prose is never executed):",
		"```sh",
		"# a comment",
		"go test ./...",
		"",
		"  go vet ./...  ",
		"```",
		"~~~",
		"make lint",
		"~~~",
		"## Gates",
		"```",
		"rm -rf / # never: outside the section",
		"```",
	}, "\n")
	got := parseVerifyCommands(brief)
	want := []string{"go test ./...", "go vet ./...", "make lint"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseVerifyCommands = %q, want %q", got, want)
	}
	if got := parseVerifyCommands("## Goal\nno verify here\n"); len(got) != 0 {
		t.Errorf("no section: got %q, want none", got)
	}
}

// seedVerifyTask creates a started task whose brief has a Verify section
// and whose work_dir is a fresh temp dir.
func seedVerifyTask(t *testing.T, slug string, cmds ...string) {
	t.Helper()
	seedStartedTask(t, slug, "")
	wd := t.TempDir()
	if _, err := openFlowDB(t).Exec(`UPDATE tasks SET work_dir=? WHERE slug=?`, wd, slug); err != nil {
		t.Fatal(err)
	}
	root, err := flowRoot()
	if err != nil {
		t.Fatal(err)
	}
	brief := "# " + slug + "\n\n## Verify\n```sh\n" + strings.Join(cmds, "\n") + "\n```\n"
	if err := os.WriteFile(filepath.Join(root, "tasks", slug, "brief.md"), []byte(brief), 0o644); err != nil {
		t.Fatal(err)
	}
}

func verifyReceipts(t *testing.T, slug string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range taskEvents(t, slug, flowdb.EventVerify) {
		out = append(out, eventDetail(t, e))
	}
	return out
}

// TestDoneInAutoRunRecordsVerifyReceipts: the self-close inside an auto
// run runs the brief's checks and records a receipt per command, pass or
// fail; without --require-verify the close still lands.
func TestDoneInAutoRunRecordsVerifyReceipts(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	seedVerifyTask(t, "v1", "true", "echo boom; exit 3")
	t.Setenv(autoRunEnvVar, "v1")

	if rc := cmdDone([]string{"v1"}); rc != 0 {
		t.Fatalf("done rc=%d, want 0 (advisory)", rc)
	}
	rs := verifyReceipts(t, "v1")
	if len(rs) != 2 {
		t.Fatalf("receipts = %d, want 2", len(rs))
	}
	if rs[0]["passed"] != true || rs[0]["exit_code"] != float64(0) {
		t.Errorf("first receipt = %v, want passed", rs[0])
	}
	if rs[1]["passed"] != false || rs[1]["exit_code"] != float64(3) ||
		!strings.Contains(rs[1]["output_tail"].(string), "boom") {
		t.Errorf("second receipt = %v, want exit 3 with output", rs[1])
	}
	if task, _ := flowdb.GetTask(openFlowDB(t), "v1"); task.Status != "done" {
		t.Errorf("status = %q, want done", task.Status)
	}
	if evs := taskEvents(t, "v1", flowdb.EventVerify); evs[0].Actor != flowdb.ActorAutoRun {
		t.Errorf("verify actor = %q, want auto-run", evs[0].Actor)
	}
}

// TestDoneRequireVerifyRefusesOnFailure: with --require-verify a failing
// check keeps the task open (rc 1) and still records the receipt.
func TestDoneRequireVerifyRefusesOnFailure(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	seedVerifyTask(t, "v2", "exit 1")

	if rc := cmdDone([]string{"v2", "--require-verify"}); rc != 1 {
		t.Fatalf("done rc=%d, want 1 (refused)", rc)
	}
	if task, _ := flowdb.GetTask(openFlowDB(t), "v2"); task.Status == "done" {
		t.Error("task closed despite a failing required check")
	}
	if len(verifyReceipts(t, "v2")) != 1 {
		t.Error("the failing receipt must still be recorded")
	}
	if n := len(taskEvents(t, "v2", flowdb.EventDone)); n != 0 {
		t.Errorf("done events = %d, want 0", n)
	}
}

// TestDoneVerifyDefaults: a human's plain done does not run checks;
// --verify opts in; --no-verify skips even inside an auto run.
func TestDoneVerifyDefaults(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")

	seedVerifyTask(t, "plain", "true")
	cmdDone([]string{"plain"})
	if n := len(verifyReceipts(t, "plain")); n != 0 {
		t.Errorf("plain done ran %d checks, want 0", n)
	}

	seedVerifyTask(t, "optin", "true")
	cmdDone([]string{"optin", "--verify"})
	if n := len(verifyReceipts(t, "optin")); n != 1 {
		t.Errorf("--verify ran %d checks, want 1", n)
	}

	seedVerifyTask(t, "skip", "true")
	t.Setenv(autoRunEnvVar, "skip")
	cmdDone([]string{"skip", "--no-verify"})
	if n := len(verifyReceipts(t, "skip")); n != 0 {
		t.Errorf("--no-verify ran %d checks, want 0", n)
	}

	if rc := cmdDone([]string{"skip", "--no-verify", "--verify"}); rc != 2 {
		t.Errorf("--no-verify --verify rc=%d, want 2", rc)
	}
}

func TestTailKeepsEnd(t *testing.T) {
	s := strings.Repeat("x", 3000) + "\nlast line"
	got := tail(s, 100)
	if !strings.HasSuffix(got, "last line") || len(got) > 110 {
		t.Errorf("tail = %q", got)
	}
	if tail("short\n", 100) != "short" {
		t.Error("short input should be returned trimmed")
	}
}
