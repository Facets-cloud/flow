package stats

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

// WeeklyPoint is one bucket of the lookups/tokens sparkline.
type WeeklyPoint struct {
	WeekStart time.Time
	Lookups   int
	Tokens    int64
}

// Stats is the fully-aggregated analytics result for one window/project.
type Stats struct {
	Window        string
	Project       string
	LookupsByKind map[LookupKind]int
	LookupsTotal  int
	Tokens        Usage
	TasksDone     int
	AutoRuns      int
	OwnerTicks    int
	PlaybookRuns  int
	KBFacts       int
	Savings       Savings
	Weekly        []WeeklyPoint
}

// BuildOpts are the injectable inputs to BuildStats (paths injected so
// tests can point at temp dirs).
type BuildOpts struct {
	Root           string
	ClaudeProjects string
	DB             *sql.DB
	Cache          *Cache
	Constants      Constants
	Since          time.Time // zero = all-time
	Project        string    // "" = all
}

// BuildStats derives a Stats from flow.db + transcripts + on-disk dirs.
func BuildStats(o BuildOpts) (Stats, error) {
	s := Stats{
		Window:        windowLabel(o.Since),
		Project:       o.Project,
		LookupsByKind: map[LookupKind]int{},
	}

	tasks, err := flowdb.ListTasks(o.DB, flowdb.TaskFilter{IncludeArchived: true, Project: o.Project})
	if err != nil {
		return s, fmt.Errorf("list tasks: %w", err)
	}

	weekly := map[time.Time]*WeeklyPoint{}
	seen := map[string]bool{}
	for _, t := range tasks {
		if !t.SessionID.Valid || t.SessionID.String == "" {
			continue
		}
		path := filepath.Join(o.ClaudeProjects, claude.EncodeCwd(t.WorkDir), t.SessionID.String+".jsonl")
		if _, statErr := os.Stat(path); statErr != nil {
			continue
		}
		roll, scanErr := o.Cache.ScanFile(path, t.Slug)
		if scanErr != nil {
			continue
		}
		seen[path] = true

		for _, l := range roll.Lookups {
			if !o.Since.IsZero() && (l.TS.IsZero() || l.TS.Before(o.Since)) {
				continue
			}
			s.LookupsByKind[l.Kind]++
			s.LookupsTotal++
			wk := weekStart(l.TS)
			wp := weekly[wk]
			if wp == nil {
				wp = &WeeklyPoint{WeekStart: wk}
				weekly[wk] = wp
			}
			wp.Lookups++
		}

		// Tokens: include the whole file's usage when its last activity is
		// in-window (token usage is not per-record-timestamped here).
		if o.Since.IsZero() || roll.Last.IsZero() || !roll.Last.Before(o.Since) {
			s.Tokens.Input += roll.Usage.Input
			s.Tokens.Output += roll.Usage.Output
			s.Tokens.CacheCreation += roll.Usage.CacheCreation
			s.Tokens.CacheRead += roll.Usage.CacheRead
			wk := weekStart(roll.Last)
			wp := weekly[wk]
			if wp == nil {
				wp = &WeeklyPoint{WeekStart: wk}
				weekly[wk] = wp
			}
			wp.Tokens += roll.Usage.Total()
		}
	}
	o.Cache.Prune(seen)

	done, err := flowdb.ListTasks(o.DB, flowdb.TaskFilter{Status: "done", IncludeArchived: true, Project: o.Project})
	if err != nil {
		return s, fmt.Errorf("list done: %w", err)
	}
	s.TasksDone = len(done)

	runs, err := flowdb.ListTasks(o.DB, flowdb.TaskFilter{Kind: "playbook_run", IncludeArchived: true, Project: o.Project})
	if err != nil {
		return s, fmt.Errorf("list runs: %w", err)
	}
	s.PlaybookRuns = len(runs)

	s.AutoRuns = countFiles(filepath.Join(o.Root, "tasks"), "auto-runs", ".log")
	s.OwnerTicks = countFiles(filepath.Join(o.Root, "owners"), "updates", ".md")
	s.KBFacts = countKBFacts(filepath.Join(o.Root, "kb"))

	s.Savings = ComputeSavings(o.Constants, Counts{
		AutoRuns:      s.AutoRuns,
		OwnerTicks:    s.OwnerTicks,
		ResumeLookups: s.LookupsByKind[LookupResume],
		RefLookups:    s.LookupsByKind[LookupReference],
		KBLookups:     s.LookupsByKind[LookupKB],
		CrossLookups:  s.LookupsByKind[LookupCrossTask],
	})

	s.Weekly = sortedWeekly(weekly)
	return s, nil
}

// ParseSince converts a --since value to a lower-bound time. "all"/"" → zero
// (no bound); "<N>d" → now minus N days; otherwise RFC3339.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if s == "" || s == "all" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("invalid --since %q", s)
		}
		return now.AddDate(0, 0, -n), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q (use all, <N>d, or RFC3339)", s)
	}
	return t, nil
}

func windowLabel(since time.Time) string {
	if since.IsZero() {
		return "all-time"
	}
	return "since " + since.Format("2006-01-02")
}

// weekStart returns Monday 00:00 UTC of t's ISO week (zero in → zero out).
func weekStart(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	t = t.UTC()
	wd := (int(t.Weekday()) + 6) % 7 // Mon=0 … Sun=6
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, -wd)
}

func sortedWeekly(m map[time.Time]*WeeklyPoint) []WeeklyPoint {
	out := make([]WeeklyPoint, 0, len(m))
	for _, wp := range m {
		out = append(out, *wp)
	}
	for i := 1; i < len(out); i++ { // insertion sort by WeekStart asc
		for j := i; j > 0 && out[j].WeekStart.Before(out[j-1].WeekStart); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// countFiles counts files ending in ext inside <base>/*/<sub>/.
func countFiles(base, sub, ext string) int {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner := filepath.Join(base, e.Name(), sub)
		files, err := os.ReadDir(inner)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ext) {
				n++
			}
		}
	}
	return n
}

// countKBFacts counts entry lines (starting with "- ") across kb/*.md.
func countKBFacts(kbDir string) int {
	entries, err := os.ReadDir(kbDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(kbDir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "- ") {
				n++
			}
		}
	}
	return n
}
