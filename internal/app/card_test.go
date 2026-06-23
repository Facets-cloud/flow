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
		Savings:       stats.Savings{TotalHours: 3.5, TotalDollars: 350, ContextSwitchHours: 2.0},
		DollarPerHour: 100,
		LookupsByKind: map[stats.LookupKind]int{stats.LookupResume: 5},
	}
	var buf bytes.Buffer
	if err := renderCardHTML(&buf, s); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{
		"<!doctype html",
		"flow",
		"42×",
		"context recalls — you never re-explained",
		"est.",
		"5 instant resumes",
		"in context not from scratch",
		"your AI remembered, so you didn't",
	} {
		if !strings.Contains(strings.ToLower(html), strings.ToLower(want)) {
			t.Errorf("card html missing %q", want)
		}
	}
	// No automation runs → no $ in output (manual-only user).
	if strings.Contains(html, "$") {
		t.Errorf("card html should not contain $ when ShowAutomation=false\n---\n%s", html)
	}
	// Footer must not contain dollar or /hr.
	if strings.Contains(html, "/hr") {
		t.Errorf("card footer should not contain /hr\n---\n%s", html)
	}
}

func TestRenderCardHTMLAutomationBand(t *testing.T) {
	base := stats.Stats{
		Window:        "all-time",
		LookupsByKind: map[stats.LookupKind]int{},
		Savings:       stats.Savings{},
	}

	t.Run("absent when zero", func(t *testing.T) {
		s := base
		s.AutoRuns = 0
		s.OwnerTicks = 0
		s.PlaybookRuns = 0
		var buf bytes.Buffer
		if err := renderCardHTML(&buf, s); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if strings.Contains(html, "runs flow did unattended") {
			t.Errorf("automation band should be absent when ShowAutomation=false\n---\n%s", html)
		}
		if strings.Contains(html, "$") {
			t.Errorf("no $ expected when ShowAutomation=false\n---\n%s", html)
		}
	})

	t.Run("present when nonzero", func(t *testing.T) {
		s := base
		s.AutoRuns = 3
		s.OwnerTicks = 2
		s.PlaybookRuns = 1
		s.Savings.AutomationHours = 2.5
		s.DollarPerHour = 100
		var buf bytes.Buffer
		if err := renderCardHTML(&buf, s); err != nil {
			t.Fatal(err)
		}
		html := buf.String()
		if !strings.Contains(html, "runs flow did unattended") {
			t.Errorf("automation band should be present when ShowAutomation=true\n---\n%s", html)
		}
		if !strings.Contains(html, "6 runs") {
			t.Errorf("automation band should show total run count (6)\n---\n%s", html)
		}
		if !strings.Contains(html, "$") {
			t.Errorf("automation band should contain $ when ShowAutomation=true\n---\n%s", html)
		}
	})
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
