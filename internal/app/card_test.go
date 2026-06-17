package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/stats"
)

func TestRenderCardHTML(t *testing.T) {
	s := stats.Stats{
		Window:        "all-time",
		LookupsTotal:  42,
		Tokens:        stats.Usage{Input: 1000, Output: 500},
		TasksDone:     7,
		Savings:       stats.Savings{TotalHours: 3.5, TotalDollars: 350},
		LookupsByKind: map[stats.LookupKind]int{},
	}
	var buf bytes.Buffer
	if err := renderCardHTML(&buf, s); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{"<!doctype html", "flow", "42", "times flow remembered so you didn't", "est.", "at $"} {
		if !strings.Contains(strings.ToLower(html), strings.ToLower(want)) {
			t.Errorf("card html missing %q", want)
		}
	}
}

func TestWriteCard(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "card.html")
	if err := writeCard(p, stats.Stats{LookupsTotal: 1, LookupsByKind: map[stats.LookupKind]int{}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(data), "<!doctype html") {
		t.Fatalf("card file not written as html: %v", err)
	}
}
