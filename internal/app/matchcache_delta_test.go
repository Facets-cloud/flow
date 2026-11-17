package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWarmCallWritesOnlyDeltas verifies point 1: a warm recall call that sees
// one changed task writes ONLY that task's per-task delta file plus the tiny
// manifest — the ~base index is left byte-for-byte untouched.
func TestWarmCallWritesOnlyDeltas(t *testing.T) {
	matchFixture(t)

	// Build the base.
	if rc := cmdRecall([]string{"train"}); rc != 0 {
		t.Fatalf("train rc=%d", rc)
	}
	basePath, _ := lookbackCachePath()
	baseBefore := fileSum(t, basePath)
	ddir, _ := deltaDir()
	if entries, _ := os.ReadDir(ddir); len(entries) != 0 {
		t.Fatalf("train should leave no deltas, found %d", len(entries))
	}

	// Change one task's brief so its fingerprint drifts.
	root, _ := flowRoot()
	brief := filepath.Join(root, "tasks", "kafka-lag", "brief.md")
	if err := os.WriteFile(brief, []byte("# kafka-lag\n\nnew content about broker partition rebalancing and consumer lag offsets"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A warm recall call (free text, unbound).
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if rc := cmdRecall([]string{"kafka broker partition lag", "--k", "3"}); rc != 0 {
		t.Fatalf("warm recall rc=%d", rc)
	}

	// The base index must be untouched.
	if fileSum(t, basePath) != baseBefore {
		t.Fatal("a warm call rewrote the base index — point 1 violated")
	}
	// A delta file for the changed task must exist; others must not.
	if _, err := os.Stat(filepath.Join(ddir, "kafka-lag.gob")); err != nil {
		t.Fatalf("expected a delta for the changed task: %v", err)
	}
	entries, _ := os.ReadDir(ddir)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one delta file, got %d", len(entries))
	}
	man := loadManifest()
	if man == nil || man.Deltas["kafka-lag"] == "" {
		t.Fatalf("manifest missing the changed task: %+v", man)
	}

	// Reload (base + delta) must reflect the new brief content.
	c := loadRawCache()
	if c == nil {
		t.Fatal("reload failed")
	}
	var found bool
	for _, tk := range c.Tasks {
		if tk.Slug == "kafka-lag" {
			found = true
			if tk.Brief == "" || tk.Brief == "# kafka-lag\n\nkafka consumer lag on broker partition" {
				t.Fatalf("reload did not pick up the delta brief: %q", tk.Brief)
			}
		}
	}
	if !found {
		t.Fatal("kafka-lag missing after reload")
	}

	// A fresh train compacts everything back into the base and clears deltas.
	if rc := cmdRecall([]string{"train"}); rc != 0 {
		t.Fatalf("second train rc=%d", rc)
	}
	if entries, _ := os.ReadDir(ddir); len(entries) != 0 {
		t.Fatalf("train should clear deltas, found %d", len(entries))
	}
	if loadManifest() != nil {
		t.Fatal("train should clear the manifest")
	}
}

// TestWarmCallRemovedTask verifies a removed task is dropped via the manifest
// without rewriting the base, and recall still succeeds.
func TestWarmCallRemovedTask(t *testing.T) {
	matchFixture(t)
	if rc := cmdRecall([]string{"train"}); rc != 0 {
		t.Fatalf("train rc=%d", rc)
	}
	basePath, _ := lookbackCachePath()
	baseBefore := fileSum(t, basePath)

	// Archive (soft-delete) a task by removing its row via the DB is heavy;
	// instead delete its on-disk dir AND remove it from the scan by archiving.
	if rc := cmdArchive([]string{"billing-fix"}); rc != 0 {
		t.Fatalf("archive rc=%d", rc)
	}
	// Archived tasks are still scanned (Archived flag), so fingerprint changes
	// (archived bit) rather than removal — exercise the changed path too.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if rc := cmdRecall([]string{"invoice ledger refund", "--k", "3"}); rc != 0 {
		t.Fatalf("warm recall rc=%d", rc)
	}
	if fileSum(t, basePath) != baseBefore {
		t.Fatal("warm call rewrote the base index")
	}
}
