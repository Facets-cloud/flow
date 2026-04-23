package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// makeTaskBundle creates a task tar bundle and returns its path.
// It uses a separate temp FLOW_ROOT so it doesn't interfere with the import root.
func makeTaskBundle(t *testing.T, slug, name, status, priority string) string {
	t.Helper()
	// Temporarily point FLOW_ROOT at a source dir just for building the bundle.
	srcRoot := t.TempDir()
	srcHome := srcRoot
	srcDB, err := flowdb.OpenDB(filepath.Join(srcRoot, "flow.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer srcDB.Close()
	for _, sub := range []string{"projects", "tasks"} {
		os.MkdirAll(filepath.Join(srcRoot, sub), 0o755)
	}
	now := flowdb.NowISO()
	srcDB.Exec(`INSERT INTO tasks (slug, name, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		slug, name, status, priority, filepath.Join(srcHome, "code", "repo"), now, now)

	taskDir := filepath.Join(srcRoot, "tasks", slug)
	os.MkdirAll(filepath.Join(taskDir, "updates"), 0o755)
	os.WriteFile(filepath.Join(taskDir, "brief.md"), []byte("# "+name), 0o644)
	os.WriteFile(filepath.Join(taskDir, "updates", "2026-04-23-note.md"), []byte("Progress."), 0o644)

	task, err := flowdb.GetTask(srcDB, slug)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	outDir := t.TempDir()
	outPath, err := writeTaskBundle(task, srcRoot, outDir, srcHome)
	if err != nil {
		t.Fatalf("writeTaskBundle: %v", err)
	}
	return outPath
}

// makeProjectBundle creates a project tar bundle and returns its path.
func makeProjectBundle(t *testing.T, projSlug, projName string, taskSlugs []string) string {
	t.Helper()
	srcRoot := t.TempDir()
	srcHome := srcRoot
	srcDB, err := flowdb.OpenDB(filepath.Join(srcRoot, "flow.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer srcDB.Close()
	for _, sub := range []string{"projects", "tasks"} {
		os.MkdirAll(filepath.Join(srcRoot, sub), 0o755)
	}
	now := flowdb.NowISO()
	srcDB.Exec(`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, 'active', 'medium', ?, ?, ?)`,
		projSlug, projName, filepath.Join(srcHome, "code"), now, now)

	projDir := filepath.Join(srcRoot, "projects", projSlug)
	os.MkdirAll(filepath.Join(projDir, "updates"), 0o755)
	os.WriteFile(filepath.Join(projDir, "brief.md"), []byte("# "+projName), 0o644)

	for _, ts := range taskSlugs {
		srcDB.Exec(`INSERT INTO tasks (slug, name, project_slug, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, ?, 'backlog', 'medium', ?, ?, ?)`,
			ts, "Task "+ts, projSlug, filepath.Join(srcHome, "code"), now, now)
		td := filepath.Join(srcRoot, "tasks", ts)
		os.MkdirAll(filepath.Join(td, "updates"), 0o755)
		os.WriteFile(filepath.Join(td, "brief.md"), []byte("# "+ts), 0o644)
	}

	project, _ := flowdb.GetProject(srcDB, projSlug)
	tasks, _ := flowdb.ListTasks(srcDB, flowdb.TaskFilter{Project: projSlug})
	outDir := t.TempDir()
	outPath, err := writeProjectBundle(project, tasks, srcRoot, outDir, srcHome)
	if err != nil {
		t.Fatalf("writeProjectBundle: %v", err)
	}
	return outPath
}

// ---------- task import ----------

func TestImportTaskRoundTrip(t *testing.T) {
	bundle := makeTaskBundle(t, "rt-task", "RoundTrip Task", "backlog", "high")

	importRoot, importDB := showListEditDB(t)

	if err := importBundle(importDB, importRoot, bundle, importRoot, false); err != nil {
		t.Fatalf("importBundle: %v", err)
	}

	task, err := flowdb.GetTask(importDB, "rt-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Name != "RoundTrip Task" {
		t.Errorf("name wrong: %q", task.Name)
	}
	if task.SessionID.Valid {
		t.Errorf("session_id should be NULL after import")
	}
	// work_dir should be expanded: <HOME> → importRoot.
	if !strings.HasPrefix(task.WorkDir, importRoot) {
		t.Errorf("work_dir not expanded: %q", task.WorkDir)
	}

	briefPath := filepath.Join(importRoot, "tasks", "rt-task", "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Errorf("brief.md missing: %v", err)
	}
	updatePath := filepath.Join(importRoot, "tasks", "rt-task", "updates", "2026-04-23-note.md")
	if _, err := os.Stat(updatePath); err != nil {
		t.Errorf("update file missing: %v", err)
	}
}

func TestImportTaskConflictErrors(t *testing.T) {
	bundle := makeTaskBundle(t, "conflict-task", "Conflict", "backlog", "medium")
	importRoot, importDB := showListEditDB(t)
	insertTask(t, importDB, "conflict-task", "Existing", "done", "low", filepath.Join(importRoot, "x"), nil)

	err := importBundle(importDB, importRoot, bundle, importRoot, false)
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists'; got: %v", err)
	}
}

func TestImportTaskForce(t *testing.T) {
	bundle := makeTaskBundle(t, "force-task", "Force Task", "backlog", "high")
	importRoot, importDB := showListEditDB(t)
	insertTask(t, importDB, "force-task", "Old Name", "done", "low", filepath.Join(importRoot, "x"), nil)

	if err := importBundle(importDB, importRoot, bundle, importRoot, true); err != nil {
		t.Fatalf("importBundle --force: %v", err)
	}
	task, _ := flowdb.GetTask(importDB, "force-task")
	if task.Name != "Force Task" {
		t.Errorf("task not overwritten: %q", task.Name)
	}
}

// ---------- project import ----------

func TestImportProjectRoundTrip(t *testing.T) {
	bundle := makeProjectBundle(t, "my-proj", "My Project", []string{"t-alpha", "t-beta"})
	importRoot, importDB := showListEditDB(t)

	if err := importBundle(importDB, importRoot, bundle, importRoot, false); err != nil {
		t.Fatalf("importBundle project: %v", err)
	}

	proj, err := flowdb.GetProject(importDB, "my-proj")
	if err != nil {
		t.Fatalf("project not found: %v", err)
	}
	if proj.Name != "My Project" {
		t.Errorf("project name wrong: %q", proj.Name)
	}

	for _, ts := range []string{"t-alpha", "t-beta"} {
		task, err := flowdb.GetTask(importDB, ts)
		if err != nil {
			t.Fatalf("task %s not found: %v", ts, err)
		}
		if task.ProjectSlug.String != "my-proj" {
			t.Errorf("task %s project_slug wrong: %q", ts, task.ProjectSlug.String)
		}
		if task.SessionID.Valid {
			t.Errorf("task %s session_id should be NULL", ts)
		}
	}

	if _, err := os.Stat(filepath.Join(importRoot, "projects", "my-proj", "brief.md")); err != nil {
		t.Errorf("project brief.md missing: %v", err)
	}
}

func TestImportProjectConflictErrors(t *testing.T) {
	bundle := makeProjectBundle(t, "dup-proj", "Dup Project", nil)
	importRoot, importDB := showListEditDB(t)
	insertProject(t, importDB, "dup-proj", "Existing", filepath.Join(importRoot, "x"), "low")

	err := importBundle(importDB, importRoot, bundle, importRoot, false)
	if err == nil {
		t.Fatal("expected conflict error for project")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists'; got: %v", err)
	}
}

func TestImportProjectForce(t *testing.T) {
	bundle := makeProjectBundle(t, "fp", "Force Project", []string{"fp-task"})
	importRoot, importDB := showListEditDB(t)
	insertProject(t, importDB, "fp", "Old Project", filepath.Join(importRoot, "x"), "low")

	if err := importBundle(importDB, importRoot, bundle, importRoot, true); err != nil {
		t.Fatalf("importBundle --force project: %v", err)
	}
	proj, _ := flowdb.GetProject(importDB, "fp")
	if proj.Name != "Force Project" {
		t.Errorf("project not overwritten: %q", proj.Name)
	}
}

// ---------- all import ----------

func TestImportAllRoundTrip(t *testing.T) {
	// Build an "all" bundle directly.
	srcRoot := t.TempDir()
	srcHome := srcRoot
	srcDB, _ := flowdb.OpenDB(filepath.Join(srcRoot, "flow.db"))
	defer srcDB.Close()
	for _, sub := range []string{"projects", "tasks", "kb"} {
		os.MkdirAll(filepath.Join(srcRoot, sub), 0o755)
	}
	now := flowdb.NowISO()
	srcDB.Exec(`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at) VALUES ('all-proj','All Project','active','high',?,?,?)`,
		filepath.Join(srcHome, "code"), now, now)
	srcDB.Exec(`INSERT INTO tasks (slug, name, project_slug, status, priority, work_dir, created_at, updated_at) VALUES ('all-task','All Task','all-proj','backlog','high',?,?,?)`,
		filepath.Join(srcHome, "code"), now, now)
	srcDB.Exec(`INSERT INTO tasks (slug, name, status, priority, work_dir, created_at, updated_at) VALUES ('float-task','Float Task','in-progress','medium',?,?,?)`,
		filepath.Join(srcHome, "other"), now, now)
	os.WriteFile(filepath.Join(srcRoot, "kb", "user.md"), []byte("# User"), 0o644)

	allProjects, _ := flowdb.ListProjects(srcDB, flowdb.ProjectFilter{IncludeArchived: true})
	allTasks, _ := flowdb.ListTasks(srcDB, flowdb.TaskFilter{IncludeArchived: true})
	outDir := t.TempDir()
	bundlePath, err := writeAllBundle(allProjects, allTasks, srcRoot, outDir, srcHome)
	if err != nil {
		t.Fatalf("writeAllBundle: %v", err)
	}

	importRoot, importDB := showListEditDB(t)
	os.MkdirAll(filepath.Join(importRoot, "kb"), 0o755)
	if err := importBundle(importDB, importRoot, bundlePath, importRoot, false); err != nil {
		t.Fatalf("importBundle all: %v", err)
	}

	if _, err := flowdb.GetProject(importDB, "all-proj"); err != nil {
		t.Errorf("project not imported: %v", err)
	}
	if _, err := flowdb.GetTask(importDB, "all-task"); err != nil {
		t.Errorf("project task not imported: %v", err)
	}
	if _, err := flowdb.GetTask(importDB, "float-task"); err != nil {
		t.Errorf("floating task not imported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(importRoot, "kb", "user.md")); err != nil {
		t.Errorf("kb/user.md not restored: %v", err)
	}
}

// ---------- cmdImport integration ----------

func TestCmdImportTaskDispatch(t *testing.T) {
	bundle := makeTaskBundle(t, "cli-task", "CLI Task", "backlog", "high")
	_, _ = showListEditDB(t)

	out := captureStdout(t, func() {
		if rc := cmdImport([]string{bundle}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "imported") {
		t.Errorf("expected import confirmation; out=%q", out)
	}
}

func TestCmdImportMissingFile(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdImport(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestCmdImportFileNotFound(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdImport([]string{"/tmp/nonexistent-bundle.tar"}); rc != 1 {
			t.Errorf("expected rc=1")
		}
	})
	if !strings.Contains(out, "open bundle") {
		t.Errorf("expected open error; out=%q", out)
	}
}

func TestCmdImportWritesToFlowRoot(t *testing.T) {
	// Build a task bundle from a source root.
	bundle := makeTaskBundle(t, "e2e-import-task", "E2E Import Task", "backlog", "high")

	// Set up a fresh FLOW_ROOT as the import target.
	importRoot, _ := showListEditDB(t)

	// Run cmdImport — it uses flowRoot() internally, which reads $FLOW_ROOT.
	out := captureStdout(t, func() {
		if rc := cmdImport([]string{bundle}); rc != 0 {
			t.Errorf("cmdImport rc=%d", rc)
		}
	})
	if !strings.Contains(out, "imported task") {
		t.Errorf("expected confirmation; out=%q", out)
	}

	// Verify via cmdShow that the task is queryable through the normal CLI path.
	showOut := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "e2e-import-task"}); rc != 0 {
			t.Errorf("cmdShow rc=%d", rc)
		}
	})
	if !strings.Contains(showOut, "E2E Import Task") {
		t.Errorf("task not visible via flow show: %s", showOut)
	}

	// Verify files on disk under the correct FLOW_ROOT.
	briefPath := filepath.Join(importRoot, "tasks", "e2e-import-task", "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Errorf("brief.md not written to FLOW_ROOT: %v", err)
	}
}

// Suppress unused import warning — flowdb used in test helpers above.
var _ = json.Marshal
