package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if !uuidRe.MatchString(id) {
			t.Errorf("newUUID returned %q, does not match UUID v4 format", id)
		}
	}
}

func TestNewUUIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID after %d: %s", i, id)
		}
		seen[id] = true
	}
}

func TestEncodeCwdForClaude(t *testing.T) {
	got := EncodeCwdForClaude("/Users/rohit/code/flow")
	want := "-Users-rohit-code-flow"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindNewestSessionFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "old-uuid.jsonl")
	b := filepath.Join(dir, "new-uuid.jsonl")
	if err := os.WriteFile(a, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(a, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := FindNewestSessionFile(dir); got != "new-uuid" {
		t.Errorf("got %q, want new-uuid", got)
	}
	if got := FindNewestSessionFile(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("missing dir: got %q, want empty", got)
	}
}
