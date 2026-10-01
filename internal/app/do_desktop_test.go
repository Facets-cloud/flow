package app

import (
	"net/url"
	"strings"
	"testing"

	"flow/internal/desktop"
	"flow/internal/flowdb"
	"flow/internal/spawner"
)

const desktopSID = "abcdef12-3456-4789-8abc-def012345678"

// desktopApp is the argv prefix of Claude Desktop's bundled claude.
const desktopApp = "/Users/x/Library/Application Support/Claude/claude-code/2.1.284/claude.app/Contents/MacOS/claude"

// stubDesktop pins the Desktop backend, clears the ambient harness so
// cmdDo picks Claude, and records every claude:// link instead of
// opening the app. Returns the captured links.
func stubDesktop(t *testing.T) *[]string {
	t.Helper()
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	oldOverride := spawner.Override
	spawner.Override = spawner.BackendDesktop
	t.Cleanup(func() { spawner.Override = oldOverride })
	var links []string
	oldOpen := desktop.OpenURL
	desktop.OpenURL = func(link string) error {
		links = append(links, link)
		return nil
	}
	t.Cleanup(func() { desktop.OpenURL = oldOpen })
	return &links
}

func bindSession(t *testing.T, slug, sid string) {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, harness='claude' WHERE slug=?`,
		sid, flowdb.NowISO(), slug,
	); err != nil {
		t.Fatal(err)
	}
}

func getTask(t *testing.T, slug string) *flowdb.Task {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// TestCmdDoDesktopNewTaskOpensPrefilledSession: a task with no session
// opens claude://code/new in its work_dir with a prompt that binds via
// `flow do --here` and then bootstraps. The DB is untouched — binding
// (and the status flip) happen when the user sends the prompt.
func TestCmdDoDesktopNewTaskOpensPrefilledSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "dt-new")
	links := stubDesktop(t)

	if rc := cmdDo([]string{"dt-new", "--with", "start with the README"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(*links) != 1 {
		t.Fatalf("links = %v, want one", *links)
	}
	u, err := url.Parse((*links)[0])
	if err != nil || u.Host != "code" || u.Path != "/new" {
		t.Fatalf("link = %q, want claude://code/new", (*links)[0])
	}
	task := getTask(t, "dt-new")
	if got := u.Query().Get("folder"); got != task.WorkDir {
		t.Errorf("folder = %q, want work_dir %q", got, task.WorkDir)
	}
	q := u.Query().Get("q")
	for _, want := range []string{
		"flow do --here dt-new",
		"You are the execution session for flow task dt-new",
		"Claude Desktop reference",
		"start with the README",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("prompt missing %q:\n%s", want, q)
		}
	}
	if task.SessionID.Valid || task.Status != "backlog" {
		t.Errorf("task changed before binding: session=%v status=%q", task.SessionID, task.Status)
	}
}

// TestCmdDoDesktopFreshRebinds: --fresh on a bound task binds the new
// Desktop session with --force (replacing the old session).
func TestCmdDoDesktopFreshRebinds(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "dt-fresh")
	bindSession(t, "dt-fresh", desktopSID)
	links := stubDesktop(t)

	if rc := cmdDo([]string{"dt-fresh", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	u, _ := url.Parse((*links)[0])
	if q := u.Query().Get("q"); !strings.Contains(q, "flow do --here --force dt-fresh") {
		t.Errorf("fresh prompt should rebind with --force:\n%s", q)
	}
}

// TestCmdDoDesktopResumes: a bound task that is not running anywhere is
// re-opened with claude://resume and flipped to in-progress.
func TestCmdDoDesktopResumes(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "dt-resume")
	bindSession(t, "dt-resume", desktopSID)
	links := stubDesktop(t)
	stubPS(t, "  PID COMMAND\n")

	if rc := cmdDo([]string{"dt-resume"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if want := "claude://resume?session=" + desktopSID; len(*links) != 1 || (*links)[0] != want {
		t.Fatalf("links = %v, want [%s]", *links, want)
	}
	task := getTask(t, "dt-resume")
	if task.Status != "in-progress" || !task.SessionLastResumed.Valid {
		t.Errorf("after resume: status=%q last_resumed=%v", task.Status, task.SessionLastResumed)
	}
}

// TestCmdDoDesktopLiveHolders: already open in Desktop → switch to it;
// open in a terminal → refuse (two writers on one transcript) unless
// --force.
func TestCmdDoDesktopLiveHolders(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "dt-live")
	bindSession(t, "dt-live", desktopSID)
	links := stubDesktop(t)

	stubPS(t, "  PID COMMAND\n"+
		"45011 /Applications/Claude.app/Contents/Helpers/disclaimer --pgroup -- "+desktopApp+" --resume="+desktopSID+"\n"+
		"45012 "+desktopApp+" --output-format stream-json --resume="+desktopSID+"\n")
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"dt-live"}); rc != 0 {
			t.Fatalf("desktop-held rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Already open") || len(*links) != 1 {
		t.Errorf("desktop-held: out=%q links=%v", out, *links)
	}

	stubPS(t, "  PID COMMAND\n64949 claude --resume "+desktopSID+"\n")
	if rc := cmdDo([]string{"dt-live"}); rc != 1 {
		t.Errorf("terminal-held rc=%d, want 1", rc)
	}
	if len(*links) != 1 {
		t.Errorf("terminal-held session was opened in Desktop: %v", *links)
	}
	if rc := cmdDo([]string{"dt-live", "--force"}); rc != 0 || len(*links) != 2 {
		t.Errorf("--force: rc=%d links=%v", rc, *links)
	}
}

// TestCmdDoDesktopRefusals: --with can't ride a resume link, and a task
// pinned to another harness can't open in Desktop.
func TestCmdDoDesktopRefusals(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "dt-with")
	bindSession(t, "dt-with", desktopSID)
	seedTask(t, "dt-codex")
	links := stubDesktop(t)
	stubPS(t, "  PID COMMAND\n")

	if rc := cmdDo([]string{"dt-with", "--with", "do X"}); rc != 2 {
		t.Errorf("--with on resume rc=%d, want 2", rc)
	}
	if rc := cmdDo([]string{"dt-codex", "--harness", "codex"}); rc != 1 {
		t.Errorf("codex task rc=%d, want 1", rc)
	}
	if len(*links) != 0 {
		t.Errorf("refused calls opened links: %v", *links)
	}
}

// TestDesktopSessionHint: the SessionStart suffix appears only inside
// Claude Desktop and points at the skill's Desktop reference.
func TestDesktopSessionHint(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	if got := desktopSessionHint(); got != "" {
		t.Errorf("hint outside Desktop = %q, want empty", got)
	}
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "claude-desktop")
	if got := desktopSessionHint(); !strings.Contains(got, "references/desktop.md") {
		t.Errorf("Desktop hint = %q, want pointer to references/desktop.md", got)
	}
}
