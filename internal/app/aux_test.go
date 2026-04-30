package app

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestEnumerateAuxFiles(t *testing.T) {
	dir := t.TempDir()

	// Files we expect to be excluded.
	mustWriteAux(t, filepath.Join(dir, "brief.md"), "brief")
	if err := os.MkdirAll(filepath.Join(dir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteAux(t, filepath.Join(dir, "updates", "2026-04-30-foo.md"), "u1")

	// Files we expect to be included.
	mustWriteAux(t, filepath.Join(dir, "research.md"), "r")
	mustWriteAux(t, filepath.Join(dir, "design.md"), "d")

	// Non-markdown files: excluded.
	mustWriteAux(t, filepath.Join(dir, "notes.txt"), "ignored")
	mustWriteAux(t, filepath.Join(dir, "image.png"), "ignored")

	got, err := enumerateAuxFiles(dir)
	if err != nil {
		t.Fatalf("enumerateAuxFiles: %v", err)
	}
	sort.Strings(got)

	want := []string{
		filepath.Join(dir, "design.md"),
		filepath.Join(dir, "research.md"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d files (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestEnumerateAuxFilesEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := enumerateAuxFiles(dir)
	if err != nil {
		t.Fatalf("enumerateAuxFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestEnumerateAuxFilesMissingDir(t *testing.T) {
	got, err := enumerateAuxFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func mustWriteAux(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
