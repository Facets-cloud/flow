package app

import (
	"bytes"
	"strings"
	"testing"

	"flow/internal/stats"
)

func TestRenderReportContainsSections(t *testing.T) {
	s := stats.Stats{
		Window:        "all-time",
		LookupsByKind: map[stats.LookupKind]int{stats.LookupResume: 4, stats.LookupKB: 2},
		LookupsTotal:  6,
		Tokens:        stats.Usage{Input: 100, Output: 50},
		TasksDone:     3,
		AutoRuns:      2,
		OwnerTicks:    1,
		PlaybookRuns:  1,
		KBFacts:       5,
		Savings:       stats.Savings{AutomationHours: 1.0, TotalHours: 1.5, TotalDollars: 150, KBTokens: 3000, AddressableCount: 4},
		Weekly:        []stats.WeeklyPoint{{Lookups: 1}, {Lookups: 4}, {Lookups: 6}},
	}
	var buf bytes.Buffer
	if err := renderReport(&buf, s); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"flow served you stored context", "6", "all-time", "Tokens", "est.", "Saved"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q.\n---\n%s", want, out)
		}
	}
}

func TestSparkline(t *testing.T) {
	got := sparkline([]int{0, 5, 10})
	if len([]rune(got)) != 3 {
		t.Errorf("sparkline len = %d runes, want 3 (%q)", len([]rune(got)), got)
	}
}
