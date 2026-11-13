package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

// GH #59: a bound session whose transcript is not keyed under the task's
// work_dir must still be resumed with its history. Two ways a row gets
// there: the session was started in another directory (a `--here` bind
// made before the bind-time gate existed), or work_dir is a symlink and
// claude keyed the transcript on the resolved path.

const resumeCwdSID = "5e551011-2222-4333-8444-555555555555"

// realTempDir returns a temp dir with symlinks resolved, i.e. the path a
// harness process started there reports as its cwd (macOS temp dirs sit
// behind /var -> /private/var).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeClaudeTranscript materializes the jsonl claude would have written
// for a session started in startCwd.
func writeClaudeTranscript(t *testing.T, startCwd, sid string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", claude.EncodeCwd(startCwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := json.Marshal(map[string]string{"type": "user", "cwd": startCwd, "sessionId": sid})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session-owner"}` + "\n" + string(rec) + "\n"
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bindClaudeSession attaches sid to the task as a previously-started claude
// session and returns the refreshed row.
func bindClaudeSession(t *testing.T, slug, sid string) *flowdb.Task {
	t.Helper()
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, status='in-progress', harness='claude' WHERE slug=?`,
		sid, flowdb.NowISO(), slug,
	); err != nil {
		t.Fatal(err)
	}
	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// seedTaskAtSymlink creates a task whose work_dir is a symlink and returns
// (work_dir as stored, the directory it resolves to).
func seedTaskAtSymlink(t *testing.T, slug string) (link, real string) {
	t.Helper()
	base := realTempDir(t)
	real = filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if rc := cmdAdd([]string{"task", slug, "--work-dir", link}); rc != 0 {
		t.Fatalf("seed task rc=%d", rc)
	}
	return link, real
}

// The tab resume must start the harness in the directory the session
// lives in, and say so, rather than in a work_dir where the transcript
// is not.
func TestCmdDoResumeSpawnsWhereSessionLives(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "moved")
	task := bindClaudeSession(t, "moved", resumeCwdSID)
	elsewhere := realTempDir(t)
	writeClaudeTranscript(t, elsewhere, resumeCwdSID)
	stubPS(t, "PID COMMAND\n")
	_, getScript := stubITerm(t)
	readStderr := captureStderr(t)

	if rc := cmdDo([]string{"moved"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	script := getScript()
	if !strings.Contains(script, "--resume "+resumeCwdSID) {
		t.Errorf("should resume the bound session: %s", script)
	}
	if !strings.Contains(script, "cd '"+elsewhere+"'") {
		t.Errorf("should spawn in the session's directory %s: %s", elsewhere, script)
	}
	if strings.Contains(script, task.WorkDir) {
		t.Errorf("should not spawn in work_dir %s: %s", task.WorkDir, script)
	}
	if msg := readStderr(); !strings.Contains(msg, elsewhere) || !strings.Contains(msg, task.WorkDir) {
		t.Errorf("should explain the directory mismatch, naming both paths; stderr:\n%s", msg)
	}
}

// A symlinked work_dir is the same directory as the session's: no
// redirect and no mismatch note.
func TestCmdDoResumeSymlinkedWorkDirIsNotAMismatch(t *testing.T) {
	setupFlowRoot(t)
	link, real := seedTaskAtSymlink(t, "linked")
	bindClaudeSession(t, "linked", resumeCwdSID)
	writeClaudeTranscript(t, real, resumeCwdSID)
	stubPS(t, "PID COMMAND\n")
	_, getScript := stubITerm(t)
	readStderr := captureStderr(t)

	if rc := cmdDo([]string{"linked"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if script := getScript(); !strings.Contains(script, "cd '"+link+"'") {
		t.Errorf("should spawn in work_dir %s: %s", link, script)
	}
	if msg := readStderr(); msg != "" {
		t.Errorf("no note expected for a symlinked work_dir; stderr:\n%s", msg)
	}
}

// With no terminal to open, the handed-off command must be the resume in
// the session's directory, not a bootstrap that would start a blank
// conversation under the same id.
func TestCmdDoNoTerminalHandoffResumesWhereSessionLives(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "handoff")
	pinNoTerminalHost(t)
	bindClaudeSession(t, "handoff", resumeCwdSID)
	elsewhere := realTempDir(t)
	writeClaudeTranscript(t, elsewhere, resumeCwdSID)
	stubPS(t, "PID COMMAND\n")

	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"handoff"}) })
	if rc != 0 {
		t.Fatalf("rc=%d; output:\n%s", rc, out)
	}
	if !strings.Contains(out, "claude --resume "+resumeCwdSID) {
		t.Errorf("should hand off the resume command; got:\n%s", out)
	}
	if strings.Contains(out, "--session-id") {
		t.Errorf("must not hand off a bootstrap for a session that exists; got:\n%s", out)
	}
	if !strings.Contains(out, "cd '"+elsewhere+"'") {
		t.Errorf("should cd to the session's directory %s; got:\n%s", elsewhere, out)
	}
}

// An auto re-run must resume a session that exists, wherever its
// transcript is keyed. Starting it with --session-id instead either opens
// a blank conversation under the same id (different directory) or is
// refused outright (same directory behind a symlink).
func TestAutoExecResumesSessionNotKeyedUnderWorkDir(t *testing.T) {
	t.Run("started in another directory", func(t *testing.T) {
		setupFlowRoot(t)
		seedTask(t, "auto-moved")
		seedRunningAuto(t, "auto-moved", 4242)
		task, err := flowdb.GetTask(openFlowDB(t), "auto-moved")
		if err != nil {
			t.Fatal(err)
		}
		elsewhere := realTempDir(t)
		writeClaudeTranscript(t, elsewhere, task.SessionID.String)
		t.Chdir(task.WorkDir) // the supervisor is launched in work_dir

		var got harness.LaunchOpts
		var ranIn string
		stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
			got = opts
			ranIn, _ = os.Getwd()
			return nil
		})
		if rc := cmdAutoExec([]string{"auto-moved"}); rc != 0 {
			t.Fatalf("cmdAutoExec rc=%d", rc)
		}
		if !got.Resume {
			t.Error("opts.Resume=false: the run would start a blank conversation under the bound session id")
		}
		if resolved, _ := filepath.EvalSymlinks(ranIn); resolved != elsewhere {
			t.Errorf("harness ran in %s, want the session's directory %s", ranIn, elsewhere)
		}
	})

	t.Run("work_dir is a symlink", func(t *testing.T) {
		setupFlowRoot(t)
		link, real := seedTaskAtSymlink(t, "auto-linked")
		seedRunningAuto(t, "auto-linked", 4242)
		task, err := flowdb.GetTask(openFlowDB(t), "auto-linked")
		if err != nil {
			t.Fatal(err)
		}
		writeClaudeTranscript(t, real, task.SessionID.String)
		t.Chdir(link)

		var got harness.LaunchOpts
		stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
			got = opts
			return nil
		})
		if rc := cmdAutoExec([]string{"auto-linked"}); rc != 0 {
			t.Fatalf("cmdAutoExec rc=%d", rc)
		}
		if !got.Resume {
			t.Error("opts.Resume=false: claude refuses --session-id for a session that already exists")
		}
	})
}

// A background resume is seeded from the session's transcript, so it must
// run where that transcript is keyed.
func TestCmdDoBackgroundResumesWhereSessionLives(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bg-moved")
	stubBGMode(t)
	stubITerm(t)
	bindClaudeSession(t, "bg-moved", bgFullSID)
	elsewhere := realTempDir(t)
	writeClaudeTranscript(t, elsewhere, bgFullSID)

	const newSID = "99aabbcc-0000-4000-8000-000000000000"
	reg := bgAgentsJSON(newSID) // the old session is gone from the registry
	c := stubBGCommand(t, &reg)
	c.resumeBanner = "backgrounded · 99aabbcc · bg-moved\n"

	if rc := cmdDo([]string{"bg-moved"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.resume != 1 {
		t.Fatalf("resume calls = %d, want 1", c.resume)
	}
	if c.lastResumeDir != elsewhere {
		t.Errorf("background resume ran in %s, want the session's directory %s", c.lastResumeDir, elsewhere)
	}
}

// Binding the current session to a task whose work_dir is a symlink to
// the session's directory satisfies the bind-time gate.
func TestCmdDoHereAcceptsSymlinkedWorkDir(t *testing.T) {
	setupFlowRoot(t)
	_, real := seedTaskAtSymlink(t, "here-linked")
	writeClaudeTranscript(t, real, resumeCwdSID)
	t.Setenv("CLAUDE_CODE_SESSION_ID", resumeCwdSID)
	stubITerm(t)

	if rc := cmdDo([]string{"here-linked", "--here"}); rc != 0 {
		t.Fatalf("rc=%d, want 0: the transcript is in the directory work_dir points at", rc)
	}
	task, err := flowdb.GetTask(openFlowDB(t), "here-linked")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID.String != resumeCwdSID {
		t.Errorf("session_id = %q, want %s", task.SessionID.String, resumeCwdSID)
	}
}
