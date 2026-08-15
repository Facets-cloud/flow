package harness_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"flow/internal/harness"
)

func tree() fstest.MapFS {
	return fstest.MapFS{
		"SKILL.md":              &fstest.MapFile{Data: []byte("current skill\n")},
		"references/setup.md":   &fstest.MapFile{Data: []byte("setup\n")},
		"references/binding.md": &fstest.MapFile{Data: []byte("binding\n")},
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

// TestSyncTreePrunesRenamedFiles is the regression guard for orphaned
// references: an installer that only writes leaves a file from every
// past corpus version on disk forever, and an agent browsing
// references/ cannot tell which are current.
func TestSyncTreePrunesRenamedFiles(t *testing.T) {
	dir := t.TempDir()
	// An older corpus, with names that no longer exist.
	for _, stale := range []string{"references/bootstrap.md", "references/kb-closeout.md", "references/task-intake.md"} {
		path := filepath.Join(dir, filepath.FromSlash(stale))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// And the sidecar, which is not part of the tree but must survive.
	if err := os.WriteFile(filepath.Join(dir, harness.SkillVersionFile), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := harness.SyncTree(tree(), dir, harness.SkillVersionFile); err != nil {
		t.Fatalf("SyncTree: %v", err)
	}

	want := []string{"SKILL.md", "VERSION", "references/binding.md", "references/setup.md"}
	got := listFiles(t, dir)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tree not synced\n got:  %v\n want: %v", got, want)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, harness.SkillVersionFile)); string(data) != "v1\n" {
		t.Error("VERSION sidecar was not preserved")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(data) != "current skill\n" {
		t.Error("SKILL.md was not overwritten with current content")
	}
}

// TestSyncTreeRemovesEmptiedDirectories: pruning every file out of a
// subdirectory should take the directory with it, not leave a husk.
func TestSyncTreeRemovesEmptiedDirectories(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "guides", "deep")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "gone.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := harness.SyncTree(tree(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "guides")); !os.IsNotExist(err) {
		t.Errorf("emptied directory left behind: %v", err)
	}
}

// TestSyncTreeIsIdempotent: running it twice changes nothing.
func TestSyncTreeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := harness.SyncTree(tree(), dir); err != nil {
		t.Fatal(err)
	}
	first := listFiles(t, dir)
	if err := harness.SyncTree(tree(), dir); err != nil {
		t.Fatal(err)
	}
	if strings.Join(listFiles(t, dir), ",") != strings.Join(first, ",") {
		t.Error("second sync changed the tree")
	}
}
