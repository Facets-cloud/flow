package stats

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheHitMissAndPersist(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "s.jsonl")
	line := `{"type":"assistant","timestamp":"2026-06-01T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show task"}}]}}`
	if err := os.WriteFile(jsonl, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	c := LoadCache(filepath.Join(dir, "cache.json")) // missing → empty
	r1, err := c.ScanFile(jsonl, "mine")
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Lookups) != 1 {
		t.Fatalf("first scan lookups = %d, want 1", len(r1.Lookups))
	}
	if len(c.Entries) != 1 {
		t.Fatalf("cache should have 1 entry, got %d", len(c.Entries))
	}

	// Second scan, unchanged file → served from cache (entry count stable).
	if _, err := c.ScanFile(jsonl, "mine"); err != nil {
		t.Fatal(err)
	}
	if len(c.Entries) != 1 {
		t.Fatalf("cache entry count changed unexpectedly: %d", len(c.Entries))
	}

	// Persist + reload round-trips.
	cpath := filepath.Join(dir, "cache.json")
	if err := c.Save(cpath); err != nil {
		t.Fatal(err)
	}
	c2 := LoadCache(cpath)
	if len(c2.Entries) != 1 {
		t.Fatalf("reloaded cache entries = %d, want 1", len(c2.Entries))
	}

	// Prune drops unseen entries.
	c2.Prune(map[string]bool{}) // nothing seen
	if len(c2.Entries) != 0 {
		t.Fatalf("prune left %d entries, want 0", len(c2.Entries))
	}
}
