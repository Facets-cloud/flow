package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cmdList dispatches `flow list tasks|projects|playbooks|runs|tags`.
func cmdList(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: list requires 'tasks', 'projects', 'playbooks', 'runs', or 'tags'")
		return 2
	}
	switch args[0] {
	case "tasks":
		return listTasksCmd(args[1:])
	case "projects":
		return listProjectsCmd(args[1:])
	case "playbooks":
		return listPlaybooksCmd(args[1:])
	case "runs":
		return listRunsCmd(args[1:])
	case "tags":
		return listTagsCmd(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown list subcommand %q\n", args[0])
	return 2
}

// listTagsCmd prints all distinct tags currently in use across non-archived
// tasks, with a per-tag task count. Sorted by count descending so the
// most-used tags appear first. Read this before suggesting new tag
// names — keeps the user's tag vocabulary consistent.
func listTagsCmd(args []string) int {
	fs := flagSet("list tags")
	if err := fs.Parse(args); err != nil {
		return 2
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

	tags, err := flowdb.ListAllTags(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(tags) == 0 {
		fmt.Println("(no tags in use)")
		return 0
	}
	for _, tc := range tags {
		fmt.Printf("  #%-30s %d tasks\n", tc.Tag, tc.Count)
	}
	return 0
}

func listTasksCmd(args []string) int {
	fs := flagSet("list tasks")
	status := fs.String("status", "", "backlog|in-progress|done")
	project := fs.String("project", "", "project slug")
	priority := fs.String("priority", "", "high|medium|low")
	tag := fs.String("tag", "", "only tasks carrying this tag (case-insensitive)")
	since := fs.String("since", "", "today|monday|7d|YYYY-MM-DD")
	includeArchived := fs.Bool("include-archived", false, "include archived tasks")
	includeDone := fs.Bool("include-done", false, "include done tasks (hidden by default)")
	kind := fs.String("kind", "regular", "filter by task kind: regular | playbook_run | all")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	filter := flowdb.TaskFilter{
		Status:          *status,
		Project:         *project,
		Priority:        *priority,
		Tag:             flowdb.NormalizeTag(*tag),
		IncludeArchived: *includeArchived,
	}
	// Default kind is "regular"; "all" disables the kind filter.
	if *kind != "all" {
		filter.Kind = *kind
	}
	// Hide done tasks by default. Skipped if --status is given (user
	// explicitly chose a status, including possibly "done") or if
	// --include-done is set.
	if *status == "" && !*includeDone {
		filter.ExcludeDone = true
	}
	if *since != "" {
		s, err := parseSince(*since, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --since: %v\n", err)
			return 2
		}
		filter.Since = s.Format(time.RFC3339)
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

	tasks, err := flowdb.ListTasks(db, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(tasks) == 0 {
		fmt.Println("(no tasks)")
		return 0
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := time.Now()

	// Best-effort scan of running claude processes. ps failures are
	// silently ignored — the list still renders, just without [live]
	// markers. See sessions.go for the limitations.
	live, _ := liveClaudeSessions()

	// Batch-load tags for every task in the result set. Failures are
	// non-fatal; the list still renders without #tag tokens.
	slugs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		slugs = append(slugs, t.Slug)
	}
	tagsByTask, _ := flowdb.GetTaskTagsBatch(db, slugs)

	// Compute max slug+name width for alignment. We render
	// "<slug>  <name>" as the identity column; truncate later.
	type row struct {
		ident    string
		statusAb string
		pri      string
		project  string
		age      string
		due      string
		stale    string
		waiting  string
		assignee string
		liveTag  string
		tags     string
		archived bool
		done     bool
	}
	var rows []row
	maxIdent := 0
	for _, t := range tasks {
		ident := t.Slug
		if t.Name != "" && t.Name != t.Slug {
			ident = t.Slug
		}
		if n := len(ident); n > maxIdent {
			maxIdent = n
		}
		r := row{
			ident:    ident,
			statusAb: statusAbbrev(t.Status),
			pri:      priorityShort(t.Priority),
			archived: t.ArchivedAt.Valid,
			done:     t.Status == "done",
		}
		if t.ProjectSlug.Valid && t.ProjectSlug.String != "" {
			r.project = "(" + t.ProjectSlug.String + ")"
		}

		// Age: days in current status.
		if !t.ArchivedAt.Valid {
			if age := daysInStatus(t, now); age > 0 {
				r.age = fmt.Sprintf("%dd", age)
			}
		}

		// Due date indicator.
		if diff, ok := daysUntilDue(t, now); ok {
			switch {
			case diff < 0:
				r.due = fmt.Sprintf("⚠ overdue %dd", -diff)
			case diff == 0:
				r.due = "⚡ due today"
			case diff == 1:
				r.due = "due tomorrow"
			default:
				r.due = fmt.Sprintf("due %dd", diff)
			}
		}

		if t.Status == "in-progress" && !t.ArchivedAt.Valid {
			if days, ok := taskStaleness(t, root); ok {
				r.stale = fmt.Sprintf("⚠ stale (%dd)", days)
			}
		}
		if t.WaitingOn.Valid && t.WaitingOn.String != "" {
			r.waiting = "[waiting: " + t.WaitingOn.String + "]"
		}
		if t.Assignee.Valid && t.Assignee.String != "" {
			r.assignee = "[@" + t.Assignee.String + "]"
		}
		if t.SessionID.Valid && t.SessionID.String != "" {
			if live[strings.ToLower(t.SessionID.String)] {
				r.liveTag = "[live]"
			}
		}
		if tags, ok := tagsByTask[t.Slug]; ok && len(tags) > 0 {
			parts := make([]string, len(tags))
			for i, tg := range tags {
				parts[i] = "#" + tg
			}
			r.tags = strings.Join(parts, " ")
		}
		rows = append(rows, r)
	}

	// Render each row. We align the ident column across all rows.
	identW := maxIdent
	if identW > 40 {
		identW = 40
	}
	if identW < 10 {
		identW = 10
	}
	for _, r := range rows {
		var sb strings.Builder
		sb.WriteString("  ")
		sb.WriteString("[")
		sb.WriteString(r.statusAb)
		sb.WriteString("] ")
		sb.WriteString(fmt.Sprintf("%-6s ", r.pri))
		ident := r.ident
		if len(ident) > identW {
			ident = ident[:identW]
		}
		sb.WriteString(fmt.Sprintf("%-*s ", identW, ident))
		if r.project != "" {
			sb.WriteString(fmt.Sprintf(" %-18s", r.project))
		} else {
			sb.WriteString(fmt.Sprintf(" %-18s", ""))
		}
		if r.age != "" {
			sb.WriteString(fmt.Sprintf("  %4s", r.age))
		} else {
			sb.WriteString("      ")
		}
		if r.due != "" {
			sb.WriteString("  ")
			sb.WriteString(r.due)
		}
		if r.stale != "" {
			sb.WriteString("  ")
			sb.WriteString(r.stale)
		}
		if r.waiting != "" {
			sb.WriteString("  ")
			sb.WriteString(r.waiting)
		}
		if r.assignee != "" {
			sb.WriteString("  ")
			sb.WriteString(r.assignee)
		}
		if r.liveTag != "" {
			sb.WriteString("  ")
			sb.WriteString(r.liveTag)
		}
		if r.tags != "" {
			sb.WriteString("  ")
			sb.WriteString(r.tags)
		}
		if r.archived {
			sb.WriteString("  (archived)")
		}
		fmt.Println(strings.TrimRight(sb.String(), " "))
	}
	return 0
}

func listProjectsCmd(args []string) int {
	fs := flagSet("list projects")
	status := fs.String("status", "", "active|done")
	includeArchived := fs.Bool("include-archived", false, "include archived projects")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	filter := flowdb.ProjectFilter{
		Status:          *status,
		IncludeArchived: *includeArchived,
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

	projects, err := flowdb.ListProjects(db, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(projects) == 0 {
		fmt.Println("(no projects)")
		return 0
	}

	// Sort projects by priority (high, med, low) then slug. ListProjects
	// currently sorts by slug only, so reorder here.
	// A stable insertion sort is fine at the volumes we expect.
	sortedProjects := make([]*flowdb.Project, len(projects))
	copy(sortedProjects, projects)
	priorityOrder := func(p string) int {
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
	// Simple insertion sort for stability and small N.
	for i := 1; i < len(sortedProjects); i++ {
		for j := i; j > 0; j-- {
			a, b := sortedProjects[j-1], sortedProjects[j]
			if priorityOrder(b.Priority) < priorityOrder(a.Priority) {
				sortedProjects[j-1], sortedProjects[j] = b, a
			} else {
				break
			}
		}
	}

	maxSlug := 0
	for _, p := range sortedProjects {
		if n := len(p.Slug); n > maxSlug {
			maxSlug = n
		}
	}
	if maxSlug > 40 {
		maxSlug = 40
	}
	if maxSlug < 10 {
		maxSlug = 10
	}

	for _, p := range sortedProjects {
		counts, err := projectTaskCounts(db, p.Slug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		statusW := "active"
		if p.Status != "" {
			statusW = p.Status
		}
		slug := p.Slug
		if len(slug) > maxSlug {
			slug = slug[:maxSlug]
		}

		label := fmt.Sprintf("%d tasks", counts.total)
		if counts.total == 1 {
			label = "1 task "
		}
		var segs []string
		if counts.inProg > 0 {
			segs = append(segs, fmt.Sprintf("%d IP", counts.inProg))
		}
		if counts.backlog > 0 {
			segs = append(segs, fmt.Sprintf("%d BL", counts.backlog))
		}
		if counts.done > 0 {
			segs = append(segs, fmt.Sprintf("%d DN", counts.done))
		}
		breakdown := ""
		if len(segs) > 0 {
			breakdown = "(" + strings.Join(segs, ", ") + ")"
		}
		arch := ""
		if p.ArchivedAt.Valid {
			arch = "  (archived)"
		}
		fmt.Printf("  %-6s %-*s   %-7s %s %s%s\n",
			priorityShort(p.Priority), maxSlug, slug, statusW, label, breakdown, arch)
	}
	return 0
}

func listPlaybooksCmd(args []string) int {
	fs := flagSet("list playbooks")
	project := fs.String("project", "", "filter by project slug")
	includeArchived := fs.Bool("include-archived", false, "include archived")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	pbs, err := flowdb.ListPlaybooks(db, flowdb.PlaybookFilter{
		Project:         *project,
		IncludeArchived: *includeArchived,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(pbs) == 0 {
		fmt.Println("(no playbooks)")
		return 0
	}
	for _, pb := range pbs {
		proj := ""
		if pb.ProjectSlug.Valid {
			proj = "(" + pb.ProjectSlug.String + ")"
		}
		archived := ""
		if pb.ArchivedAt.Valid {
			archived = "  (archived)"
		}
		fmt.Printf("  %-40s %s%s\n", pb.Slug, proj, archived)
	}
	return 0
}

func listRunsCmd(args []string) int {
	fs := flagSet("list runs")
	status := fs.String("status", "", "backlog|in-progress|done")
	includeArchived := fs.Bool("include-archived", false, "include archived")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var playbookSlug string
	if fs.NArg() > 0 {
		playbookSlug = fs.Arg(0)
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{
		Kind:            "playbook_run",
		PlaybookSlug:    playbookSlug,
		Status:          *status,
		IncludeArchived: *includeArchived,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(tasks) == 0 {
		fmt.Println("(no runs)")
		return 0
	}
	for _, tk := range tasks {
		archived := ""
		if tk.ArchivedAt.Valid {
			archived = "  (archived)"
		}
		pbCol := ""
		if tk.PlaybookSlug.Valid {
			pbCol = "(" + tk.PlaybookSlug.String + ")"
		}
		fmt.Printf("  [%s] %-50s %s%s\n", statusAbbrev(tk.Status), tk.Slug, pbCol, archived)
	}
	return 0
}

// ---------- helpers ----------

type taskCounts struct {
	total, inProg, backlog, done int
}

func projectTaskCounts(db *sql.DB, projectSlug string) (taskCounts, error) {
	var c taskCounts
	rows, err := db.Query(
		`SELECT status, COUNT(*) FROM tasks
		 WHERE project_slug = ? AND archived_at IS NULL
		 GROUP BY status`, projectSlug)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return c, err
		}
		c.total += n
		switch s {
		case "in-progress":
			c.inProg += n
		case "backlog":
			c.backlog += n
		case "done":
			c.done += n
		}
	}
	return c, rows.Err()
}

func statusAbbrev(status string) string {
	switch status {
	case "backlog":
		return "BL"
	case "in-progress":
		return "IP"
	case "done":
		return "DN"
	}
	return "??"
}

func priorityShort(p string) string {
	switch p {
	case "high":
		return "high"
	case "medium":
		return "med"
	case "low":
		return "low"
	}
	return p
}

// parseSince converts "today" / "monday" / "7d" / "YYYY-MM-DD" / "Nd"
// into an absolute time lower bound, interpreted in local time. `now` is
// passed in for testability.
func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	case "monday":
		// Start of the current week (Monday 00:00).
		wd := int(now.Weekday()) // Sunday = 0
		// Convert so Monday = 0, Sunday = 6.
		offset := (wd + 6) % 7
		y, mo, d := now.Date()
		start := time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
		return start.AddDate(0, 0, -offset), nil
	}
	// Pattern "<N>d".
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		var n int
		if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	// YYYY-MM-DD.
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized --since value %q (want today|monday|Nd|YYYY-MM-DD)", s)
}

// ensureUpdatesDir is a small utility used in tests to pre-create an
// updates directory. Kept here so tests can share it without exposing
// internals elsewhere.
func ensureUpdatesDir(root, kind, slug string) (string, error) {
	dir := filepath.Join(root, kind, slug, "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
