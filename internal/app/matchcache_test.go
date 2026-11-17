package app

import (
	"flow/internal/match"
	"reflect"
	"testing"
)

func TestFlattenUnflattenV(t *testing.T) {
	sp := &match.LSASpace{V: [][]float32{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}}
	flat, cols, ok := flattenV(sp)
	if !ok || cols != 3 || len(flat) != 9 {
		t.Fatalf("flatten: ok=%v cols=%d len=%d", ok, cols, len(flat))
	}
	got := unflattenV(flat, cols)
	want := [][]float32{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unflatten = %v", got)
	}
	if _, _, ok := flattenV(&match.LSASpace{V: [][]float32{{1, 2}, {3}}}); ok {
		t.Fatal("ragged V should not flatten")
	}
	if _, _, ok := flattenV(&match.LSASpace{}); ok {
		t.Fatal("empty V should not flatten")
	}
}

func mkRow(slug, fp string) matchRow {
	return matchRow{data: match.TaskData{Slug: slug, Fingerprint: fp}}
}

// TestLookbackCacheRoundTrip checks a full build persists and reloads
// with the slim tasks, model and space intact.
func TestLookbackCacheRoundTrip(t *testing.T) {
	t.Setenv("FLOW_ROOT", t.TempDir())
	tasks := []match.TaskData{
		{Slug: "a-one", Name: "task a", Brief: "Goal: kafka partitions.", CreatedAt: "2026-01-01T00:00:00Z", Project: "p", Fingerprint: "f1"},
		{Slug: "b-two", Name: "task b", Brief: "Goal: postgres replication.", CreatedAt: "2026-01-02T00:00:00Z", Project: "p", Fingerprint: "f2"},
	}
	if err := writeCache(&lookbackCache{Tasks: slimAll(tasks), Space: match.FitRelatedSpace(tasks),
		Model: match.BuildRelatedIndex(tasks), BasisN: 2}); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := loadRawCache()
	if c == nil || c.Model == nil || c.Space == nil || len(c.Tasks) != 2 {
		t.Fatalf("round-trip incomplete: %+v", c)
	}
	if c.Tasks[0].Name != "task a" || c.Tasks[0].Brief == "" || c.Tasks[0].Fingerprint != "f1" {
		t.Fatalf("slim task lost fields: %+v", c.Tasks[0])
	}
	// Restored space must score (byTask was rebuilt by Reindex).
	if c.Space.NumPoints("a-one") == 0 {
		t.Fatal("space lost points after round-trip/reindex")
	}
}

// TestMaxChurnRefitThreshold documents the fold-in vs refit boundary.
func TestMaxChurnRefitThreshold(t *testing.T) {
	if maxChurn(10) != lsaRefitFloor {
		t.Fatalf("small corpus churn floor = %d", maxChurn(10))
	}
	if maxChurn(1000) != 200 {
		t.Fatalf("large corpus churn = %d, want 200", maxChurn(1000))
	}
}
