package app

import (
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"testing"
)

// writeSessionFile simulates claude creating its jsonl for a session.
// The register-session command finds the newest such file in the
// encoded work_dir and writes its UUID back to the DB.
func writeSessionFile(t *testing.T, home, workDir, sid string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", EncodeCwdForClaude(workDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterSessionFromEnv(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "register-me")
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "register-me")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "captured-sid-1")
	t.Setenv("FLOW_TASK", "register-me")

	if rc := cmdRegisterSession(nil); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "register-me")
	if !task.SessionID.Valid || task.SessionID.String != "captured-sid-1" {
		t.Errorf("session_id = %+v, want captured-sid-1", task.SessionID)
	}
}

func TestRegisterSessionExplicitSlug(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "explicit")
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "explicit")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "sid-explicit")

	if rc := cmdRegisterSession([]string{"explicit"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "explicit")
	if task.SessionID.String != "sid-explicit" {
		t.Errorf("session_id = %q, want sid-explicit", task.SessionID.String)
	}
}

func TestRegisterSessionRefusesOverwrite(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "already-set")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='already-here' WHERE slug='already-set'`); err != nil {
		t.Fatal(err)
	}
	task, _ := flowdb.GetTask(db, "already-set")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "would-overwrite")

	// No --force → should preserve existing value, rc=0 (non-fatal).
	if rc := cmdRegisterSession([]string{"already-set"}); rc != 0 {
		t.Errorf("rc=%d (non-fatal refuse)", rc)
	}
	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "already-set")
	if task.SessionID.String != "already-here" {
		t.Errorf("existing session_id was overwritten: got %q, want already-here", task.SessionID.String)
	}
}

func TestRegisterSessionForceOverwrites(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "force-me")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='old' WHERE slug='force-me'`); err != nil {
		t.Fatal(err)
	}
	task, _ := flowdb.GetTask(db, "force-me")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "brand-new")

	if rc := cmdRegisterSession([]string{"--force", "force-me"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "force-me")
	if task.SessionID.String != "brand-new" {
		t.Errorf("--force didn't overwrite: got %q, want brand-new", task.SessionID.String)
	}
}

func TestRegisterSessionNoSessionFile(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "no-session")
	// Do NOT write any jsonl file.

	if rc := cmdRegisterSession([]string{"no-session"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when no jsonl present", rc)
	}
}

func TestRegisterSessionNoEnvAndNoArg(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_TASK", "")

	if rc := cmdRegisterSession(nil); rc != 2 {
		t.Errorf("rc=%d, want 2 when no slug and no env", rc)
	}
}

// TestRegisterSessionDotfileWorkDir pins the 2026-04-15 regression: a
// task whose work_dir contains a dotfile segment (e.g.
// `/Users/rohit/.flow/tasks/foo/workspace`) must still register because
// EncodeCwdForClaude now maps `.` to `-` like Claude Code itself does.
func TestRegisterSessionDotfileWorkDir(t *testing.T) {
	setupFlowRoot(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Seed a task and then rewrite its work_dir to something containing
	// a dotfile segment — we want to exercise the encoding, not the
	// filesystem, so the path doesn't need to exist on disk.
	seedTask(t, "dotfile-task")
	db := openFlowDB(t)
	dotWD := "/Users/someone/.flow/tasks/dotfile-task/workspace"
	if _, err := db.Exec(`UPDATE tasks SET work_dir=? WHERE slug='dotfile-task'`, dotWD); err != nil {
		t.Fatal(err)
	}
	db.Close()

	writeSessionFile(t, tmp, dotWD, "sid-dotfile")
	t.Setenv("FLOW_TASK", "dotfile-task")

	if rc := cmdRegisterSession(nil); rc != 0 {
		t.Fatalf("rc=%d (expected the dotfile path to register cleanly)", rc)
	}
	db = openFlowDB(t)
	task, _ := flowdb.GetTask(db, "dotfile-task")
	if task.SessionID.String != "sid-dotfile" {
		t.Errorf("session_id = %q, want sid-dotfile", task.SessionID.String)
	}
}

// TestRegisterSessionCwdFallback exercises the safety-net path:
// register-session should succeed even if a jsonl ended up in a dir
// whose name does not match our encoding rule, as long as its recorded
// `cwd` matches the task's work_dir. Simulates a future Claude Code
// encoding drift.
func TestRegisterSessionCwdFallback(t *testing.T) {
	setupFlowRoot(t)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	seedTask(t, "fallback-task")
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "fallback-task")
	db.Close()

	// Write the jsonl under an arbitrarily-named dir (not matching
	// EncodeCwdForClaude), but with a first-record `cwd` that DOES match
	// task.WorkDir. Only the fallback can find this.
	projects := filepath.Join(tmp, ".claude", "projects", "totally-different-name")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	sidPath := filepath.Join(projects, "sid-fallback.jsonl")
	if err := os.WriteFile(sidPath,
		[]byte(`{"cwd":"`+task.WorkDir+`","type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := cmdRegisterSession([]string{"fallback-task"}); rc != 0 {
		t.Fatalf("rc=%d (fallback cwd scan should have matched)", rc)
	}
	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "fallback-task")
	if task.SessionID.String != "sid-fallback" {
		t.Errorf("session_id = %q, want sid-fallback", task.SessionID.String)
	}
}
