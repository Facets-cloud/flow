package match

import (
	"math"
	"testing"
)

// TestExportApplyEntryRoundTrip checks that exporting a task's entry and
// re-applying it to a fresh model reproduces the same BM25/cosine/coverage
// scores — the fidelity the per-task cache relies on.
func TestExportApplyEntryRoundTrip(t *testing.T) {
	tasks := []TaskData{
		{Slug: "a-one", Name: "kafka partitions", Brief: "Goal: rebalance kafka partitions across brokers.",
			Updates: []Update{{Date: "2026-01-01", Text: "moved 3 partitions, lag cleared"}},
			Prompts: []string{"why is kafka lag growing", "how to rebalance partitions"}, CreatedAt: "2026-01-01T00:00:00Z"},
		{Slug: "b-two", Name: "postgres replication", Brief: "Goal: set up postgres streaming replication.",
			CreatedAt: "2026-01-02T00:00:00Z"},
	}
	full := BuildRelatedIndex(tasks)

	ed, ok := full.ExportEntry("a-one")
	if !ok {
		t.Fatal("export failed")
	}

	// A model built from b-two only, then a-one applied from the snapshot.
	rebuilt := BuildRelatedIndex(tasks[1:])
	rebuilt.ApplyEntry(ed)

	// DF and TotalLen must match the full build exactly.
	if len(rebuilt.Tasks) != 2 {
		t.Fatalf("rebuilt has %d tasks, want 2", len(rebuilt.Tasks))
	}
	q := full.NewQuery("kafka partition rebalance")
	for _, slug := range []string{"a-one", "b-two"} {
		b1, b2 := full.BM25(q, slug), rebuilt.BM25(q, slug)
		if math.Abs(b1-b2) > 1e-9 {
			t.Errorf("BM25(%s): full=%.9f rebuilt=%.9f", slug, b1, b2)
		}
		c1, c2 := full.Coverage(q, slug), rebuilt.Coverage(q, slug)
		if math.Abs(c1-c2) > 1e-9 {
			t.Errorf("Coverage(%s): full=%.9f rebuilt=%.9f", slug, c1, c2)
		}
		s1, s2 := full.Sim(q, slug), rebuilt.Sim(q, slug)
		if math.Abs(s1-s2) > 1e-9 {
			t.Errorf("Sim(%s): full=%.9f rebuilt=%.9f", slug, s1, s2)
		}
	}
}

// TestApplyEntryReplaces checks ApplyEntry replaces an existing entry and
// keeps DF exact (no double counting).
func TestApplyEntryReplaces(t *testing.T) {
	tasks := []TaskData{
		{Slug: "a-one", Name: "kafka", Brief: "kafka partitions", CreatedAt: "2026-01-01T00:00:00Z"},
		{Slug: "b-two", Name: "postgres", Brief: "postgres replication", CreatedAt: "2026-01-02T00:00:00Z"},
	}
	m := BuildRelatedIndex(tasks)
	ed, _ := m.ExportEntry("a-one")
	before := len(m.Tasks)
	dfBefore := map[int32]int{}
	for k, v := range m.DF {
		dfBefore[k] = v
	}
	m.ApplyEntry(ed) // re-apply the same entry: must be idempotent
	if len(m.Tasks) != before {
		t.Fatalf("task count changed: %d -> %d", before, len(m.Tasks))
	}
	for k, v := range m.DF {
		if dfBefore[k] != v {
			t.Fatalf("DF for term id %d changed: %d -> %d", k, dfBefore[k], v)
		}
	}
}
