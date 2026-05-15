// Package dashboard assembles a read-only snapshot of flow state for the
// `flow dashboard` TUI: bucketed task lists (working / awaiting / stale),
// summary counts, and a git-log-style activity stream derived from the DB
// and from update files on disk.
//
// The package is data-only: no rendering, no terminal, no bubbletea. The
// caller (internal/app/dashboard.go and internal/dashboard/view.go) is
// responsible for presentation.
package dashboard

import (
	"database/sql"
	"flow/internal/flowdb"
	"fmt"
	"sort"
	"time"
)

// StaleAfter is the threshold after which an in-progress task with no
// updates is flagged as stale. Matches the §4.1 stale marker rule.
const StaleAfter = 7 * 24 * time.Hour

// DoneWindow is the lookback for counting recently-completed tasks in
// the header pill.
const DoneWindow = 7 * 24 * time.Hour

// Snapshot is everything the dashboard view needs to render one frame.
type Snapshot struct {
	AsOf      time.Time
	Counts    Counts
	Working   []TaskRow     // status=in-progress, not waiting, not stale
	Awaiting  []TaskRow     // status=in-progress with waiting_on set
	Stale     []TaskRow     // status=in-progress, updated_at older than StaleAfter
	Backlog   []TaskRow     // status=backlog, sorted high-priority first
	Playbooks []PlaybookRow // playbook definitions (kind=regular tasks filtered out)
	Activity  []Event       // most recent first, capped by Loader.ActivityN
}

// PlaybookRow is one renderable row in the playbooks pane plus the
// data its detail/runs sub-panes need.
type PlaybookRow struct {
	Playbook    *flowdb.Playbook
	ProjectSlug string    // "" for floating
	RunCount    int       // total non-archived runs across all statuses
	LastRunAgo  string    // relative time of the most recent run; "" if no runs
	Headline    string    // first non-heading line of the playbook's latest update file
	Runs        []TaskRow // runs of this playbook (kind=playbook_run tasks), newest first
}

// Counts populates the header pill (`● 3 working · ◐ 2 awaiting · …`).
type Counts struct {
	Working  int
	Awaiting int
	Done7d   int
	Backlog  int
}

// TaskRow is one renderable row in the working/awaiting/stale/backlog
// sections. The list-side rendering uses only Slug + RelTime; the
// right-hand detail pane consumes Headline + Tags plus the underlying
// Task fields (priority, waiting_on, status, due_date, …).
type TaskRow struct {
	Task        *flowdb.Task
	ProjectSlug string   // "" for floating
	Tags        []string // attached tags, alphabetical; empty for untagged
	Headline    string   // first non-heading line of the newest update file; "" when none
	RelTime     string   // "2h ago", "1d ago"
	CreatedAgo  string   // relative time string for Task.CreatedAt; "" when unparseable
}

// Event is one row in the activity stream.
type Event struct {
	Hash       string    // 4-char cosmetic ID for visual stability
	When       time.Time // event timestamp (file mtime, status_changed_at, etc.)
	RelTime    string    // "2h", "1d"
	Kind       string    // "note added" | "started" | "done" | "project +"
	TargetSlug string    // task or project slug the event is about
	TargetProj string    // owning project slug, "" if floating or N/A
}

// Loader is the entry point. DB and FlowRoot must be set; Now defaults
// to time.Now if nil (overridable for tests).
type Loader struct {
	DB        *sql.DB
	FlowRoot  string
	ActivityN int              // cap for the activity stream; 0 → 15
	Now       func() time.Time // defaults to time.Now
}

// Snapshot assembles one full frame.
func (l *Loader) Snapshot() (*Snapshot, error) {
	if l.DB == nil {
		return nil, fmt.Errorf("dashboard.Loader: DB is nil")
	}
	if l.FlowRoot == "" {
		return nil, fmt.Errorf("dashboard.Loader: FlowRoot is empty")
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	asOf := now()

	tasks, err := flowdb.ListTasks(l.DB, flowdb.TaskFilter{Kind: "regular"})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	headlines, err := loadHeadlines(l.FlowRoot, tasks)
	if err != nil {
		return nil, fmt.Errorf("load headlines: %w", err)
	}

	slugs := make([]string, len(tasks))
	for i, t := range tasks {
		slugs[i] = t.Slug
	}
	tagsBySlug, err := flowdb.GetTaskTagsBatch(l.DB, slugs)
	if err != nil {
		return nil, fmt.Errorf("load tags: %w", err)
	}

	s := &Snapshot{AsOf: asOf}
	for _, t := range tasks {
		row := TaskRow{
			Task:        t,
			ProjectSlug: nullStr(t.ProjectSlug),
			Tags:        tagsBySlug[t.Slug],
		}
		row.RelTime = relTime(asOf, t.UpdatedAt)
		row.CreatedAgo = relTime(asOf, t.CreatedAt)
		row.Headline = headlines[t.Slug]

		switch t.Status {
		case "in-progress":
			if isStale(asOf, t) {
				s.Stale = append(s.Stale, row)
				continue
			}
			if t.WaitingOn.Valid && t.WaitingOn.String != "" {
				s.Awaiting = append(s.Awaiting, row)
				continue
			}
			s.Working = append(s.Working, row)
		case "backlog":
			s.Backlog = append(s.Backlog, row)
		}
	}

	s.Counts.Working = len(s.Working)
	s.Counts.Awaiting = len(s.Awaiting)
	s.Counts.Backlog = len(s.Backlog)

	doneSince := asOf.Add(-DoneWindow).Format(time.RFC3339)
	doneTasks, err := flowdb.ListTasks(l.DB, flowdb.TaskFilter{
		Kind:   "regular",
		Status: "done",
		Since:  doneSince,
	})
	if err != nil {
		return nil, fmt.Errorf("list done tasks: %w", err)
	}
	s.Counts.Done7d = len(doneTasks)

	// Stable in-section ordering: project_slug then slug for in-progress
	// buckets; high-priority-first for backlog so urgent work surfaces.
	for _, sec := range [][]TaskRow{s.Working, s.Awaiting, s.Stale} {
		sort.SliceStable(sec, func(i, j int) bool {
			if sec[i].ProjectSlug != sec[j].ProjectSlug {
				return sec[i].ProjectSlug < sec[j].ProjectSlug
			}
			return sec[i].Task.Slug < sec[j].Task.Slug
		})
	}
	sort.SliceStable(s.Backlog, func(i, j int) bool {
		pi, pj := priorityRank(s.Backlog[i].Task.Priority), priorityRank(s.Backlog[j].Task.Priority)
		if pi != pj {
			return pi < pj
		}
		if s.Backlog[i].ProjectSlug != s.Backlog[j].ProjectSlug {
			return s.Backlog[i].ProjectSlug < s.Backlog[j].ProjectSlug
		}
		return s.Backlog[i].Task.Slug < s.Backlog[j].Task.Slug
	})

	cap := l.ActivityN
	if cap <= 0 {
		cap = 15
	}
	events, err := buildActivity(l.DB, l.FlowRoot, asOf, cap)
	if err != nil {
		return nil, fmt.Errorf("build activity: %w", err)
	}
	s.Activity = events

	pbRows, err := loadPlaybookRows(l.DB, l.FlowRoot, asOf)
	if err != nil {
		return nil, fmt.Errorf("load playbooks: %w", err)
	}
	s.Playbooks = pbRows

	return s, nil
}

// loadPlaybookRows queries playbook definitions and, for each, the
// runs that belong to it. Returns playbook rows sorted by most-recent
// run (or updated_at as fallback), so the most active playbooks
// surface first in the pane.
func loadPlaybookRows(db *sql.DB, root string, asOf time.Time) ([]PlaybookRow, error) {
	pbs, err := flowdb.ListPlaybooks(db, flowdb.PlaybookFilter{})
	if err != nil {
		return nil, fmt.Errorf("list playbooks: %w", err)
	}
	if len(pbs) == 0 {
		return nil, nil
	}

	// Headlines for playbooks live under <root>/playbooks/<slug>/updates/.
	headlines, err := loadPlaybookHeadlines(root, pbs)
	if err != nil {
		return nil, fmt.Errorf("load playbook headlines: %w", err)
	}

	out := make([]PlaybookRow, 0, len(pbs))
	for _, pb := range pbs {
		runs, err := flowdb.ListTasks(db, flowdb.TaskFilter{
			Kind:         "playbook_run",
			PlaybookSlug: pb.Slug,
		})
		if err != nil {
			return nil, fmt.Errorf("list runs for %s: %w", pb.Slug, err)
		}
		runRows := make([]TaskRow, 0, len(runs))
		var lastRun time.Time
		for _, t := range runs {
			row := TaskRow{
				Task:        t,
				ProjectSlug: nullStr(t.ProjectSlug),
				RelTime:     relTime(asOf, t.UpdatedAt),
				CreatedAgo:  relTime(asOf, t.CreatedAt),
			}
			runRows = append(runRows, row)
			if parsed, perr := time.Parse(time.RFC3339, t.CreatedAt); perr == nil {
				if parsed.After(lastRun) {
					lastRun = parsed
				}
			}
		}
		// Newest run first.
		sort.SliceStable(runRows, func(i, j int) bool {
			return runRows[i].Task.CreatedAt > runRows[j].Task.CreatedAt
		})
		var lastRunAgo string
		if !lastRun.IsZero() {
			lastRunAgo = relTimeDur(asOf.Sub(lastRun)) + " ago"
		}
		out = append(out, PlaybookRow{
			Playbook:    pb,
			ProjectSlug: nullStr(pb.ProjectSlug),
			RunCount:    len(runs),
			LastRunAgo:  lastRunAgo,
			Headline:    headlines[pb.Slug],
			Runs:        runRows,
		})
	}

	// Sort playbooks: most-recently-run first, then alphabetical.
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].LastRunAgo, out[j].LastRunAgo
		if (ai == "") != (aj == "") {
			return aj == "" // playbooks with runs come before those without
		}
		if out[i].RunCount != out[j].RunCount {
			return out[i].RunCount > out[j].RunCount
		}
		return out[i].Playbook.Slug < out[j].Playbook.Slug
	})
	return out, nil
}

// isStale matches the §4.1 rule: in-progress AND updated_at older than
// StaleAfter (default 7d).
func isStale(asOf time.Time, t *flowdb.Task) bool {
	if t.Status != "in-progress" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, t.UpdatedAt)
	if err != nil {
		return false
	}
	return asOf.Sub(parsed) > StaleAfter
}

func nullStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

// priorityRank maps the three priority enums to a sortable integer
// where high comes first.
func priorityRank(p string) int {
	switch p {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	}
	return 3
}
