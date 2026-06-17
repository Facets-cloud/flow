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
		Savings:       stats.Savings{AutomationHours: 1.0, TotalHours: 1.5, TotalDollars: 150, ContextTokens: 3000, AddressableCount: 4},
		DollarPerHour: 100,
		Weekly:        []stats.WeeklyPoint{{Lookups: 1}, {Lookups: 4}, {Lookups: 6}},
	}
	var buf bytes.Buffer
	if err := renderReport(&buf, s); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"flow served you stored context", "6", "all-time", "Tokens", "est.", "Saved", "Context re-established", "context you never re-explained", "$100/hr"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q.\n---\n%s", want, out)
		}
	}
}

func TestHumanInt(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{1041234, "1,041,234"},
		{-1234567, "-1,234,567"},
	}
	for _, c := range cases {
		got := humanInt(c.in)
		if got != c.want {
			t.Errorf("humanInt(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSparkline(t *testing.T) {
	got := sparkline([]int{0, 5, 10})
	if len([]rune(got)) != 3 {
		t.Errorf("sparkline len = %d runes, want 3 (%q)", len([]rune(got)), got)
	}
}

func TestSparklineEmpty(t *testing.T) {
	got := sparkline([]int{})
	if len([]rune(got)) != 0 {
		t.Errorf("sparkline([]) = %q, want empty string", got)
	}
}

func TestSparklineAllZero(t *testing.T) {
	// All-zero input exercises the `max > 0` division-by-zero guard:
	// every value maps to the lowest bar.
	got := sparkline([]int{0, 0, 0})
	if got != "▁▁▁" {
		t.Errorf("sparkline([0,0,0]) = %q, want %q", got, "▁▁▁")
	}
}
