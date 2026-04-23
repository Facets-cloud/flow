package app

import (
	"archive/tar"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// ---------- tar inspection helpers (shared with import_test.go) ----------

func listTarEntries(t *testing.T, tarPath string) []string {
	t.Helper()
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open tar: %v", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

func readTarEntry(t *testing.T, tarPath, entry string) []byte {
	t.Helper()
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open tar: %v", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if hdr.Name == entry {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read entry %s: %v", entry, err)
			}
			return data
		}
	}
	t.Fatalf("entry %q not found in %s", entry, tarPath)
	return nil
}

func tarContains(t *testing.T, tarPath, entry string) bool {
	t.Helper()
	for _, e := range listTarEntries(t, tarPath) {
		if e == entry {
			return true
		}
	}
	return false
}

// ---------- export task tests ----------

func TestExportTaskCreatesBundle(t *testing.T) {
	root, db := showListEditDB(t)
	home := root // use root as fake home so maskHome is predictable
	t.Setenv("HOME", home)

	insertTask(t, db, "my-task", "My Task", "backlog", "high", filepath.Join(home, "code", "repo"), nil)

	// Write brief and an update file.
	taskDir := filepath.Join(root, "tasks", "my-task")
	os.MkdirAll(filepath.Join(taskDir, "updates"), 0o755)
	os.WriteFile(filepath.Join(taskDir, "brief.md"), []byte("# My Task\n\n## What\nDo the thing."), 0o644)
	os.WriteFile(filepath.Join(taskDir, "updates", "2026-04-23-progress.md"), []byte("Progress note."), 0o644)

	outDir := t.TempDir()
	out := captureStdout(t, func() {
		if rc := cmdExport([]string{"task", "my-task", "--output", outDir}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})

	tarPath := strings.TrimSpace(out)
	if _, err := os.Stat(tarPath); err != nil {
		t.Fatalf("bundle not found at %q: %v", tarPath, err)
	}
	if !strings.HasSuffix(tarPath, ".tar") {
		t.Errorf("expected .tar extension, got %q", tarPath)
	}

	// Verify manifest.
	mfData := readTarEntry(t, tarPath, "manifest.json")
	var mf bundleManifest
	if err := json.Unmarshal(mfData, &mf); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if mf.Type != "task" || mf.Slug != "my-task" || mf.Version != "1" {
		t.Errorf("manifest wrong: %+v", mf)
	}

	// Verify task.json has correct fields and no session data.
	taskData := readTarEntry(t, tarPath, "task.json")
	var bt bundledTask
	if err := json.Unmarshal(taskData, &bt); err != nil {
		t.Fatalf("parse task.json: %v", err)
	}
	if bt.Slug != "my-task" || bt.Name != "My Task" {
		t.Errorf("task fields wrong: %+v", bt)
	}
	if strings.Contains(string(taskData), "session_id") {
		t.Errorf("task.json must not contain session_id")
	}

	// Verify work_dir uses <HOME> placeholder.
	if !strings.HasPrefix(bt.WorkDir, "<HOME>") {
		t.Errorf("work_dir not masked: %q", bt.WorkDir)
	}

	// Verify brief.md and update file present.
	if !tarContains(t, tarPath, "brief.md") {
		t.Errorf("brief.md missing from bundle")
	}
	if !tarContains(t, tarPath, "updates/2026-04-23-progress.md") {
		t.Errorf("update file missing from bundle")
	}
}

func TestExportTaskNoBriefOk(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "no-brief", "No Brief", "backlog", "medium", filepath.Join(root, "x"), nil)

	outDir := t.TempDir()
	out := captureStdout(t, func() {
		if rc := cmdExport([]string{"task", "no-brief", "--output", outDir}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	tarPath := strings.TrimSpace(out)
	if _, err := os.Stat(tarPath); err != nil {
		t.Errorf("bundle missing: %v", err)
	}
	if !tarContains(t, tarPath, "manifest.json") || !tarContains(t, tarPath, "task.json") {
		t.Errorf("core entries missing")
	}
}

func TestExportTaskUnknownSlugErrors(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdExport([]string{"task", "nope"}); rc != 1 {
			t.Errorf("rc should be 1 for unknown slug")
		}
	})
	if !strings.Contains(out, "nope") {
		t.Errorf("expected slug in error; out=%q", out)
	}
}

func TestExportTaskMissingSlugErrors(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdExport([]string{"task"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestExportBadSubcommand(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdExport(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
	if rc := cmdExport([]string{"nope"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestMaskHome(t *testing.T) {
	home := "/Users/alice"
	cases := []struct{ in, want string }{
		{"/Users/alice/code/repo", "<HOME>/code/repo"},
		{"/Users/alice", "<HOME>"},
		{"/opt/something", "/opt/something"},
		{"", ""},
	}
	for _, c := range cases {
		got := maskHome(c.in, home)
		if got != c.want {
			t.Errorf("maskHome(%q, %q) = %q, want %q", c.in, home, got, c.want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	home := "/Users/bob"
	cases := []struct{ in, want string }{
		{"<HOME>/code/repo", "/Users/bob/code/repo"},
		{"<HOME>", "/Users/bob"},
		{"/opt/something", "/opt/something"},
		{"", ""},
	}
	for _, c := range cases {
		got := expandHome(c.in, home)
		if got != c.want {
			t.Errorf("expandHome(%q, %q) = %q, want %q", c.in, home, got, c.want)
		}
	}
}

// ---------- export project tests ----------

func TestExportProjectCreatesBundle(t *testing.T) {
	root, db := showListEditDB(t)
	home := root
	t.Setenv("HOME", home)

	insertProject(t, db, "my-proj", "My Project", filepath.Join(home, "code", "proj"), "high")
	insertTask(t, db, "task-a", "Task A", "in-progress", "high", filepath.Join(home, "code", "proj"), "my-proj")
	insertTask(t, db, "task-b", "Task B", "backlog", "medium", filepath.Join(home, "code", "proj"), "my-proj")

	projDir := filepath.Join(root, "projects", "my-proj")
	os.MkdirAll(filepath.Join(projDir, "updates"), 0o755)
	os.WriteFile(filepath.Join(projDir, "brief.md"), []byte("# My Project"), 0o644)

	for _, slug := range []string{"task-a", "task-b"} {
		td := filepath.Join(root, "tasks", slug)
		os.MkdirAll(filepath.Join(td, "updates"), 0o755)
		os.WriteFile(filepath.Join(td, "brief.md"), []byte("# "+slug), 0o644)
	}
	os.WriteFile(filepath.Join(root, "tasks", "task-a", "updates", "2026-04-23-note.md"), []byte("note"), 0o644)

	outDir := t.TempDir()
	out := captureStdout(t, func() {
		if rc := cmdExport([]string{"project", "my-proj", "--output", outDir}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})

	tarPath := strings.TrimSpace(out)
	if _, err := os.Stat(tarPath); err != nil {
		t.Fatalf("bundle missing: %v", err)
	}

	mfData := readTarEntry(t, tarPath, "manifest.json")
	var mf bundleManifest
	json.Unmarshal(mfData, &mf)
	if mf.Type != "project" || mf.Slug != "my-proj" {
		t.Errorf("manifest wrong: %+v", mf)
	}

	pData := readTarEntry(t, tarPath, "project.json")
	var bp bundledProject
	json.Unmarshal(pData, &bp)
	if bp.Slug != "my-proj" {
		t.Errorf("project.json slug wrong: %q", bp.Slug)
	}
	if !strings.HasPrefix(bp.WorkDir, "<HOME>") {
		t.Errorf("project work_dir not masked: %q", bp.WorkDir)
	}

	if !tarContains(t, tarPath, "tasks/task-a/task.json") {
		t.Errorf("tasks/task-a/task.json missing")
	}
	if !tarContains(t, tarPath, "tasks/task-b/task.json") {
		t.Errorf("tasks/task-b/task.json missing")
	}
	if !tarContains(t, tarPath, "tasks/task-a/brief.md") {
		t.Errorf("tasks/task-a/brief.md missing")
	}
	if !tarContains(t, tarPath, "tasks/task-a/updates/2026-04-23-note.md") {
		t.Errorf("task-a update file missing")
	}
	if !tarContains(t, tarPath, "brief.md") {
		t.Errorf("project brief.md missing")
	}
}

func TestExportProjectMissingSlugErrors(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdExport([]string{"project"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestExportProjectUnknownSlugErrors(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdExport([]string{"project", "ghost"}); rc != 1 {
			t.Errorf("expected rc=1")
		}
	})
	if !strings.Contains(out, "ghost") {
		t.Errorf("expected slug in error; out=%q", out)
	}
}

func TestExportProjectNoTasks(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "empty-proj", "Empty", filepath.Join(root, "x"), "medium")

	outDir := t.TempDir()
	out := captureStdout(t, func() {
		if rc := cmdExport([]string{"project", "empty-proj", "--output", outDir}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	tarPath := strings.TrimSpace(out)
	if _, err := os.Stat(tarPath); err != nil {
		t.Errorf("bundle missing: %v", err)
	}
	if !tarContains(t, tarPath, "project.json") {
		t.Errorf("project.json missing")
	}
}

func TestExportProjectIncludesArchivedTasks(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "arch-proj", "Arch Project", filepath.Join(root, "x"), "medium")
	insertTask(t, db, "active-t", "Active", "backlog", "medium", filepath.Join(root, "x"), "arch-proj")
	insertTask(t, db, "archived-t", "Archived", "done", "low", filepath.Join(root, "x"), "arch-proj")
	// Archive the second task.
	if _, err := db.Exec(`UPDATE tasks SET archived_at = ? WHERE slug = ?`, flowdb.NowISO(), "archived-t"); err != nil {
		t.Fatalf("archive task: %v", err)
	}

	outDir := t.TempDir()
	out := captureStdout(t, func() {
		if rc := cmdExport([]string{"project", "arch-proj", "--output", outDir}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	tarPath := strings.TrimSpace(out)
	if !tarContains(t, tarPath, "tasks/active-t/task.json") {
		t.Errorf("active task missing from bundle")
	}
	if !tarContains(t, tarPath, "tasks/archived-t/task.json") {
		t.Errorf("archived task missing from bundle — IncludeArchived must be true")
	}
}

// Dummy reference to flowdb to avoid unused import if needed.
var _ = flowdb.TaskFilter{}
