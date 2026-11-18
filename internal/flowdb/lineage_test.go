package flowdb

import "testing"

// A bg resume forks a new session id from a source id; the lineage maps
// new -> source so flow can find the transcript-bearing ancestor of a
// session that never took a turn.
func TestSessionLineageRecordAndLookup(t *testing.T) {
	db := wakeTestDB(t)
	if src, ok, _ := SessionSource(db, "child"); ok || src != "" {
		t.Fatalf("unknown id: src=%q ok=%v, want none", src, ok)
	}
	if err := RecordSessionSource(db, "child", "parent", "t1"); err != nil {
		t.Fatal(err)
	}
	if err := RecordSessionSource(db, "grandchild", "child", "t1"); err != nil {
		t.Fatal(err)
	}
	if src, ok, _ := SessionSource(db, "grandchild"); !ok || src != "child" {
		t.Fatalf("grandchild source = %q ok=%v, want child", src, ok)
	}
	// Re-recording the same child is an upsert, not an error.
	if err := RecordSessionSource(db, "child", "other", "t1"); err != nil {
		t.Fatal(err)
	}
	if src, _, _ := SessionSource(db, "child"); src != "other" {
		t.Fatalf("child source = %q, want other after re-record", src)
	}
	// Self-loops are refused (they would make an ancestor walk spin).
	if err := RecordSessionSource(db, "x", "x", "t1"); err == nil {
		t.Fatal("self-source accepted")
	}
}

// A query failure must not read as "no lineage".
func TestSessionSourceErrorIsNotMissing(t *testing.T) {
	db := wakeTestDB(t)
	if _, ok, err := SessionSource(db, "x"); ok || err != nil {
		t.Fatalf("no row: ok=%v err=%v, want false,nil", ok, err)
	}
	db.Close()
	if _, ok, err := SessionSource(db, "x"); ok || err == nil {
		t.Fatalf("closed db: ok=%v err=%v, want error", ok, err)
	}
}
