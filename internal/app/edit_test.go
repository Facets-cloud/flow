package app

import (
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeEditor writes an executable script into dir that appends the
// text "edited by test" to any file path passed as its first argument.
// Returns the script path. Skips the test on Windows.
func writeFakeEditor(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake editor relies on a POSIX shell script")
	}
	path := filepath.Join(dir, "fake-editor.sh")
	content := `#!/bin/sh
printf '\nedited by test\n' >> "$1"
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake editor: %v", err)
	}
	return path
}

func TestCmdEditTaskBumpsUpdatedAt(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "authfix", "Auth", "backlog", "high", filepath.Join(root, "x"), nil)
	// Back-date so we can verify the bump is forward.
	if _, err := db.Exec(`UPDATE tasks SET updated_at = '2020-01-01T00:00:00Z' WHERE slug = ?`, "authfix"); err != nil {
		t.Fatal(err)
	}

	// Pre-create brief so the fake editor has something to append to.
	briefPath := filepath.Join(root, "tasks", "authfix", "brief.md")
	if err := os.MkdirAll(filepath.Dir(briefPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(briefPath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := writeFakeEditor(t, t.TempDir())
	oldEditor := os.Getenv("EDITOR")
	os.Setenv("EDITOR", script)
	t.Cleanup(func() { os.Setenv("EDITOR", oldEditor) })

	out := captureStdout(t, func() {
		if rc := cmdEdit([]string{"authfix"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Edited task authfix") {
		t.Errorf("missing confirmation; out=%q", out)
	}

	// Verify the fake editor ran (file mutated).
	data, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	if !strings.Contains(string(data), "edited by test") {
		t.Errorf("brief not edited: %q", string(data))
	}

	// Verify updated_at moved.
	task, err := flowdb.GetTask(db, "authfix")
	if err != nil {
		t.Fatal(err)
	}
	if task.UpdatedAt == "2020-01-01T00:00:00Z" {
		t.Errorf("updated_at not bumped: %q", task.UpdatedAt)
	}
}

func TestCmdEditProjectBumpsUpdatedAt(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "bigp", "Big P", filepath.Join(root, "x"), "medium")
	if _, err := db.Exec(`UPDATE projects SET updated_at = '2020-01-01T00:00:00Z' WHERE slug = ?`, "bigp"); err != nil {
		t.Fatal(err)
	}
	briefPath := filepath.Join(root, "projects", "bigp", "brief.md")
	if err := os.MkdirAll(filepath.Dir(briefPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(briefPath, []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeFakeEditor(t, t.TempDir())
	os.Setenv("EDITOR", script)
	t.Cleanup(func() { os.Unsetenv("EDITOR") })

	if rc := cmdEdit([]string{"bigp"}); rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	got, err := flowdb.GetProject(db, "bigp")
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt == "2020-01-01T00:00:00Z" {
		t.Errorf("project updated_at not bumped")
	}
}

func TestCmdEditUnknownRef(t *testing.T) {
	_, _ = showListEditDB(t)
	os.Setenv("EDITOR", "true") // no-op; should never run
	t.Cleanup(func() { os.Unsetenv("EDITOR") })
	out := captureStdout(t, func() {
		if rc := cmdEdit([]string{"ghost"}); rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "no task, project, or playbook matching") {
		t.Errorf("missing error; out=%q", out)
	}
}

func TestCmdEditMissingRef(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdEdit(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestCmdEditAmbiguousSameSlug(t *testing.T) {
	// Same slug in tasks and projects → must surface as ambiguity.
	root, db := showListEditDB(t)
	insertProject(t, db, "twin", "Twin", filepath.Join(root, "x"), "medium")
	insertTask(t, db, "twin", "Twin", "backlog", "medium", filepath.Join(root, "x"), nil)

	os.Setenv("EDITOR", "true")
	t.Cleanup(func() { os.Unsetenv("EDITOR") })
	out := captureStdout(t, func() {
		if rc := cmdEdit([]string{"twin"}); rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "ambiguous") {
		t.Errorf("missing ambiguity error; out=%q", out)
	}
}

func TestCmdEditNamespacePrefix(t *testing.T) {
	// task/<slug> and project/<slug> prefixes disambiguate.
	root, db := showListEditDB(t)
	insertProject(t, db, "twin", "Twin", filepath.Join(root, "x"), "medium")
	insertTask(t, db, "twin", "Twin", "backlog", "medium", filepath.Join(root, "x"), nil)

	// Pre-create both briefs.
	for _, kind := range []string{"tasks", "projects"} {
		p := filepath.Join(root, kind, "twin", "brief.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("."), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := writeFakeEditor(t, t.TempDir())
	os.Setenv("EDITOR", script)
	t.Cleanup(func() { os.Unsetenv("EDITOR") })

	out := captureStdout(t, func() {
		if rc := cmdEdit([]string{"task/twin"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Edited task twin") {
		t.Errorf("task prefix failed; out=%q", out)
	}
	out = captureStdout(t, func() {
		if rc := cmdEdit([]string{"project/twin"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Edited project twin") {
		t.Errorf("project prefix failed; out=%q", out)
	}
}

func TestCmdEditTaskOnlyByDefault(t *testing.T) {
	// When only a task exists, fuzzy ref resolves without prefix.
	root, db := showListEditDB(t)
	insertTask(t, db, "soloTask", "S", "backlog", "medium", filepath.Join(root, "x"), nil)
	briefPath := filepath.Join(root, "tasks", "soloTask", "brief.md")
	if err := os.MkdirAll(filepath.Dir(briefPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(briefPath, []byte("."), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeFakeEditor(t, t.TempDir())
	os.Setenv("EDITOR", script)
	t.Cleanup(func() { os.Unsetenv("EDITOR") })

	out := captureStdout(t, func() {
		if rc := cmdEdit([]string{"soloTask"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Edited task soloTask") {
		t.Errorf("out=%q", out)
	}
}

func TestCmdEditProjectOnlyByDefault(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "soloProj", "S", filepath.Join(root, "x"), "medium")
	briefPath := filepath.Join(root, "projects", "soloProj", "brief.md")
	if err := os.MkdirAll(filepath.Dir(briefPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(briefPath, []byte("."), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeFakeEditor(t, t.TempDir())
	os.Setenv("EDITOR", script)
	t.Cleanup(func() { os.Unsetenv("EDITOR") })

	out := captureStdout(t, func() {
		if rc := cmdEdit([]string{"soloProj"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Edited project soloProj") {
		t.Errorf("out=%q", out)
	}
}

func TestCmdEditPlaybook(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	t.Setenv("EDITOR", "/usr/bin/true")
	if rc := cmdEdit([]string{"p"}); rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	briefPath := filepath.Join(root, "playbooks", "p", "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Errorf("brief.md missing: %v", err)
	}

	// updated_at should be bumped — verify the row's updated_at is recent.
	db := openFlowDB(t)
	pb, err := flowdb.GetPlaybook(db, "p")
	if err != nil {
		t.Fatal(err)
	}
	if pb.UpdatedAt == "" {
		t.Errorf("UpdatedAt empty")
	}
}
