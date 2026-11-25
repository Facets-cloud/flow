package codex

import (
	"os"
	"path/filepath"
	"testing"
)

// One unreadable directory must not hide a rollout elsewhere in the tree.
func TestResolveTranscriptPathSkipsUnreadableDir(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "2026", "11", "24")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(good, "rollout-2026-11-24T10-00-00-abc123.jsonl")
	if err := os.WriteFile(want, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "2026", "10")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })
	got, err := resolveTranscriptPath(root, "abc123")
	if err != nil || got != want {
		t.Fatalf("resolveTranscriptPath = %q, %v; want %q", got, err, want)
	}
}
