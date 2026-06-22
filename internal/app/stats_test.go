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
	for _, want := range []string{
		"Your AI remembered",
		"Memory",
		"Context re-established",
		"Instant resumes",
		"in context not from scratch",
		"Addressed by name",
		"all-time",
		"6",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q.\n---\n%s", want, out)
		}
	}
	// Dollar figure must appear in the automation block (Unattended work line), not globally.
	if !strings.Contains(out, "Unattended work") {
		t.Errorf("report missing Unattended work line\n---\n%s", out)
	}
	if !strings.Contains(out, "$") {
		t.Errorf("report missing $ in automation block\n---\n%s", out)
	}
	// "Where was I" must be gone.
	if strings.Contains(out, "Where was I") {
		t.Errorf("report should not contain 'Where was I'\n---\n%s", out)
	}
}

func TestRenderReportHidesAutomationWhenZero(t *testing.T) {
	base := stats.Stats{
		Window:        "all-time",
		LookupsByKind: map[stats.LookupKind]int{},
		Savings:       stats.Savings{},
	}

	t.Run("hidden when zero", func(t *testing.T) {
		s := base
		s.AutoRuns = 0
		s.OwnerTicks = 0
		s.PlaybookRuns = 0
		var buf bytes.Buffer
		if err := renderReport(&buf, s); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if strings.Contains(out, "Automation (power-user)") {
			t.Errorf("expected Automation section to be absent when AutoRuns+OwnerTicks+PlaybookRuns==0\n---\n%s", out)
		}
		if strings.Contains(out, "$") {
			t.Errorf("expected no $ for manual-only user\n---\n%s", out)
		}
	})

	t.Run("shown when nonzero", func(t *testing.T) {
		s := base
		s.AutoRuns = 1
		s.OwnerTicks = 0
		s.PlaybookRuns = 0
		s.Savings.AutomationHours = 2.0
		s.DollarPerHour = 100
		var buf bytes.Buffer
		if err := renderReport(&buf, s); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "Automation (power-user)") {
			t.Errorf("expected Automation section to be present when AutoRuns>0\n---\n%s", out)
		}
		if !strings.Contains(out, "$") {
			t.Errorf("expected $ in automation block when AutoRuns>0\n---\n%s", out)
		}
	})
}

func TestHumanCompact(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{766, "766"},
		{999, "999"},
		{1000, "1K"},
		{1500, "1.5K"},
		{320000, "320K"},
		{999999, "1000.0K"},
		{1000000, "1.00M"},
		{1149208, "1.15M"},
		{1000000000, "1.00B"},
		{5188827405, "5.19B"},
	}
	for _, c := range cases {
		got := humanCompact(c.in)
		if got != c.want {
			t.Errorf("humanCompact(%d) = %q, want %q", c.in, got, c.want)
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
