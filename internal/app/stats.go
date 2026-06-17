package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/stats"
)

// cmdStats implements `flow stats` — usage & ROI analytics derived from
// flow.db, session transcripts, and on-disk auto-runs/owner/kb dirs.
func cmdStats(args []string) int {
	fs := flagSet("stats")
	since := fs.String("since", "all", "window: all | <N>d | RFC3339")
	project := fs.String("project", "", "limit to one project slug")
	card := fs.Bool("card", false, "render a shareable HTML card instead of the terminal report")
	out := fs.String("out", "", "output path for --card; ignored without --card (default <flow-root>/stats-card.html)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	sinceTime, err := stats.ParseSince(*since, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: no home dir: %v\n", err)
		return 1
	}
	cachePath := filepath.Join(root, "stats-cache.json")
	cache := stats.LoadCache(cachePath)
	consts := stats.LoadConstants(filepath.Join(root, "stats.json"))

	s, err := stats.BuildStats(stats.BuildOpts{
		Root:           root,
		ClaudeProjects: filepath.Join(home, ".claude", "projects"),
		DB:             db,
		Cache:          cache,
		Constants:      consts,
		Since:          sinceTime,
		Project:        *project,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: build stats: %v\n", err)
		return 1
	}
	if saveErr := cache.Save(cachePath); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write stats cache: %v\n", saveErr)
	}

	if *card {
		outPath := *out
		if outPath == "" {
			outPath = filepath.Join(root, "stats-card.html")
		}
		if err := writeCard(outPath, s); err != nil {
			fmt.Fprintf(os.Stderr, "error: write card: %v\n", err)
			return 1
		}
		fmt.Printf("card written: %s\n", outPath)
		return 0
	}

	if err := renderReport(os.Stdout, s); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// renderReport prints the terminal analytics report.
func renderReport(w io.Writer, s stats.Stats) error {
	fmt.Fprintf(w, "flow stats — %s", s.Window)
	if s.Project != "" {
		fmt.Fprintf(w, " · project %s", s.Project)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "  flow served you stored context %d times\n", s.LookupsTotal)
	fmt.Fprintf(w, "    resume %d · reference %d · cross-task %d · kb %d\n",
		s.LookupsByKind[stats.LookupResume], s.LookupsByKind[stats.LookupReference],
		s.LookupsByKind[stats.LookupCrossTask], s.LookupsByKind[stats.LookupKB])
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Ground truth")
	fmt.Fprintf(w, "    Tokens processed : %d\n", s.Tokens.Total())
	fmt.Fprintf(w, "    Tasks done       : %d\n", s.TasksDone)
	fmt.Fprintf(w, "    Auto runs        : %d\n", s.AutoRuns)
	fmt.Fprintf(w, "    Owner ticks      : %d\n", s.OwnerTicks)
	fmt.Fprintf(w, "    Playbook runs    : %d\n", s.PlaybookRuns)
	fmt.Fprintf(w, "    KB facts         : %d\n", s.KBFacts)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Estimated savings (est. — assumptions in stats.json)")
	fmt.Fprintf(w, "    Automation       : ~%.1f hrs (est.)\n", s.Savings.AutomationHours)
	fmt.Fprintf(w, "    Context recovery : ~%.1f hrs (est.)\n", s.Savings.ContextSwitchHours)
	fmt.Fprintf(w, "    Context re-established : ~%s tokens (est.) — context you never re-explained\n", humanInt(s.Savings.ContextTokens))
	fmt.Fprintf(w, "    Addressed by slug: %d (never hunted a UUID)\n", s.Savings.AddressableCount)
	fmt.Fprintf(w, "    Saved            : ~%.1f hrs · ~$%s (at $%.0f/hr, est.)\n", s.Savings.TotalHours, humanInt(int64(s.Savings.TotalDollars)), s.DollarPerHour)

	if len(s.Weekly) > 0 {
		vals := make([]int, len(s.Weekly))
		for i, wp := range s.Weekly {
			vals[i] = wp.Lookups
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Weekly lookups   : %s\n", sparkline(vals))
	}
	return nil
}

// humanInt formats n with thousands separators (e.g. 1041234 → "1,041,234").
// Pure Go, no third-party deps.
func humanInt(n int64) string {
	if n < 0 {
		return "-" + humanInt(-n)
	}
	s := fmt.Sprintf("%d", n)
	// Insert commas every 3 digits from the right.
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders ints as a unicode bar string (one rune per value).
func sparkline(values []int) string {
	if len(values) == 0 {
		return ""
	}
	max := 0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	out := make([]rune, len(values))
	for i, v := range values {
		idx := 0
		if max > 0 {
			idx = v * (len(sparkRunes) - 1) / max
		}
		out[i] = sparkRunes[idx]
	}
	return string(out)
}

