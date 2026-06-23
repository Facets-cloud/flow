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
	png := fs.Bool("png", false, "render a shareable PNG card (no browser needed)")
	out := fs.String("out", "", "output path for --card/--png; default <flow-root>/stats-card.{html,png}")
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

	if *png {
		outPath := *out
		if outPath == "" {
			outPath = filepath.Join(root, "stats-card.png")
		}
		if err := writeCardPNG(outPath, s); err != nil {
			fmt.Fprintf(os.Stderr, "error: write png: %v\n", err)
			return 1
		}
		fmt.Printf("png written: %s\n", outPath)
		return 0
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

	fmt.Fprintln(w, "  Your AI remembered, so you didn't.")
	fmt.Fprintf(w, "  flow recalled your context %d times — you never re-explained it.\n", s.LookupsTotal)
	fmt.Fprintf(w, "    resume %d · reference %d · cross-task %d · kb %d\n",
		s.LookupsByKind[stats.LookupResume], s.LookupsByKind[stats.LookupReference],
		s.LookupsByKind[stats.LookupCrossTask], s.LookupsByKind[stats.LookupKB])
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Memory")
	fmt.Fprintf(w, "    Context re-established : ~%s tokens you never re-typed (est.)\n", humanInt(s.Savings.ContextTokens))
	fmt.Fprintf(w, "    Instant resumes        : %d× — flow dropped you straight back into work, in context not from scratch\n", s.LookupsByKind[stats.LookupResume])
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  Shipped")
	fmt.Fprintf(w, "    Tasks done       : %d\n", s.TasksDone)
	fmt.Fprintf(w, "    Tokens processed : %s\n", humanInt(s.Tokens.Total()))
	fmt.Fprintf(w, "    KB facts         : %d\n", s.KBFacts)

	if s.AutoRuns+s.OwnerTicks+s.PlaybookRuns > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Automation (power-user)")
		fmt.Fprintf(w, "    Auto runs        : %d\n", s.AutoRuns)
		fmt.Fprintf(w, "    Owner ticks      : %d\n", s.OwnerTicks)
		fmt.Fprintf(w, "    Playbook runs    : %d\n", s.PlaybookRuns)
		fmt.Fprintf(w, "    Unattended work  : ~%.1f hrs · ≈$%s (est.)\n", s.Savings.AutomationHours, humanInt(int64(s.Savings.AutomationHours*s.DollarPerHour)))
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "  Addressed by name, not a UUID : %d\n", s.Savings.AddressableCount)

	if len(s.Weekly) > 0 {
		vals := make([]int, len(s.Weekly))
		for i, wp := range s.Weekly {
			vals[i] = wp.Lookups
		}
		fmt.Fprintf(w, "  Weekly recalls   : %s\n", sparkline(vals))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  counts exact · time/tokens are est. · tune ~/.flow/stats.json\n")
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

// humanCompact formats large numbers with B/M/K suffix for compact display.
// ≥1B → "X.XXB"; ≥1M → "X.XXM"; ≥1K → "XK" or "X.XK"; else plain integer.
func humanCompact(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		if n%1000 == 0 {
			return fmt.Sprintf("%dK", n/1000)
		}
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
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

