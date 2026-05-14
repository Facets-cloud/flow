package app

import (
	"database/sql"
	"flag"
	"flow/internal/flowdb"
	"flow/internal/listfmt"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// waitingMaxRunes caps the [waiting: ...] field width in table output so a
// long freeform blocking note doesn't blow the row past terminal width.
// JSON/TSV emit the full value. The --no-truncate flag suppresses this cap.
const waitingMaxRunes = 60

// listOpts are the format/color/truncation flags every list subcommand shares.
type listOpts struct {
	format     *string
	noColor    *bool
	noTruncate *bool
}

// addListFlags registers the common --format / --no-color / --no-truncate
// flags on fs. Slugs are never truncated — only the freeform waiting field
// has a default cap, which --no-truncate disables.
func addListFlags(fs *flag.FlagSet) listOpts {
	return listOpts{
		format:     fs.String("format", "table", "output format: table|json|tsv"),
		noColor:    fs.Bool("no-color", false, "disable ANSI color even when stdout is a TTY"),
		noTruncate: fs.Bool("no-truncate", false, "do not truncate the [waiting: ...] field in table output"),
	}
}

// waitMax returns waitingMaxRunes when truncation is enabled, 0 otherwise.
func (o listOpts) waitMax() int {
	if *o.noTruncate {
		return 0
	}
	return waitingMaxRunes
}

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

// Color palette. Red is reserved for anomaly signals (overdue / stale).
// "high" priority is the dominant active state for daily users, so coloring
// it red turns every row into a wall of red and defeats the signal —
// keep it bold-uncolored instead.

func statusColor(status string) string {
	switch status {
	case "in-progress":
		return listfmt.Green
	case "backlog":
		return listfmt.Yellow
	case "done":
		return listfmt.Dim
	}
	return ""
}

func priorityColor(pri string) string {
	switch pri {
	case "high":
		return listfmt.Bold
	case "low":
		return listfmt.Dim
	}
	return ""
}

// emptyResult prints the conventional "(no X)" line and returns 0. Honors
// the requested format: JSON emits "[]", TSV emits a header-only stream,
// table emits the human-friendly placeholder.
func emptyResult(format listfmt.Format, label string, tsvHeaders []string) int {
	switch format {
	case listfmt.FormatJSON:
		return runJSON(os.Stdout, []any{})
	case listfmt.FormatTSV:
		_ = listfmt.RenderTSV(os.Stdout, tsvHeaders, nil)
	default:
		fmt.Printf("(no %s)\n", label)
	}
	return 0
}

func listTagsCmd(args []string) int {
	fs := flagSet("list tags")
	opts := addListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmtKind, err := listfmt.ParseFormat(*opts.format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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

	headers := []string{"TAG", "COUNT"}
	tsvHeaders := []string{"tag", "count"}
	if len(tags) == 0 {
		return emptyResult(fmtKind, "tags in use", tsvHeaders)
	}

	switch fmtKind {
	case listfmt.FormatJSON:
		type tagRow struct {
			Tag   string `json:"tag"`
			Count int    `json:"count"`
		}
		rows := make([]tagRow, len(tags))
		for i, tc := range tags {
			rows[i] = tagRow{Tag: tc.Tag, Count: tc.Count}
		}
		return runJSON(os.Stdout, rows)
	case listfmt.FormatTSV:
		rows := make([][]string, len(tags))
		for i, tc := range tags {
			rows[i] = []string{tc.Tag, fmt.Sprintf("%d", tc.Count)}
		}
		_ = listfmt.RenderTSV(os.Stdout, tsvHeaders, rows)
		return 0
	}

	painter := listfmt.Painter{Enabled: listfmt.ColorEnabled(os.Stdout, *opts.noColor)}
	tabRows := make([][]string, len(tags))
	for i, tc := range tags {
		tabRows[i] = []string{
			painter.Wrap("#"+tc.Tag, listfmt.Cyan),
			fmt.Sprintf("%d tasks", tc.Count),
		}
	}
	tab := &listfmt.Table{
		Headers: dimHeaders(painter, headers),
		Rows:    tabRows,
	}
	_ = tab.Render(os.Stdout)
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
	opts := addListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	fmtKind, err := listfmt.ParseFormat(*opts.format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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

	headers := []string{"STATUS", "PRIORITY", "SLUG", "PROJECT", "AGE", "DUE", "NOTES"}
	tsvHeaders := []string{
		"slug", "status", "priority", "project",
		"age_days", "due_in_days", "due_label",
		"stale", "stale_days", "waiting_on", "assignee", "live",
		"archived", "tags",
	}
	if len(tasks) == 0 {
		return emptyResult(fmtKind, "tasks", tsvHeaders)
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := time.Now()

	// Best-effort scan of running claude processes. ps failures are
	// silently ignored — the rows still render, just without [live]
	// markers. See sessions.go for the limitations.
	live, _ := liveClaudeSessions()

	// Batch-load tags for every task in the result set. Failures are
	// non-fatal; the rows still render without #tag tokens.
	slugs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		slugs = append(slugs, t.Slug)
	}
	tagsByTask, _ := flowdb.GetTaskTagsBatch(db, slugs)

	rows := make([]taskListRow, 0, len(tasks))
	for _, t := range tasks {
		r := taskListRow{
			Slug:     t.Slug,
			Status:   t.Status,
			Priority: t.Priority,
			Archived: t.ArchivedAt.Valid,
		}
		if t.ProjectSlug.Valid {
			r.Project = t.ProjectSlug.String
		}
		if !t.ArchivedAt.Valid {
			if age := daysInStatus(t, now); age > 0 {
				r.AgeDays = age
			}
		}
		if diff, ok := daysUntilDue(t, now); ok {
			d := diff
			r.DueInDays = &d
			switch {
			case diff < 0:
				r.DueLabel = fmt.Sprintf("⚠ overdue %dd", -diff)
			case diff == 0:
				r.DueLabel = "⚡ due today"
			case diff == 1:
				r.DueLabel = "due tomorrow"
			default:
				r.DueLabel = fmt.Sprintf("due %dd", diff)
			}
		}
		if t.Status == "in-progress" && !t.ArchivedAt.Valid {
			if days, ok := taskStaleness(t, root); ok {
				r.Stale = true
				r.StaleDays = days
			}
		}
		if t.WaitingOn.Valid {
			r.WaitingOn = t.WaitingOn.String
		}
		if t.Assignee.Valid {
			r.Assignee = t.Assignee.String
		}
		if t.SessionID.Valid && live[strings.ToLower(t.SessionID.String)] {
			r.Live = true
		}
		if tags, ok := tagsByTask[t.Slug]; ok {
			r.Tags = tags
		}
		rows = append(rows, r)
	}

	switch fmtKind {
	case listfmt.FormatJSON:
		return runJSON(os.Stdout, rows)
	case listfmt.FormatTSV:
		tsvRows := make([][]string, len(rows))
		for i, r := range rows {
			tsvRows[i] = []string{
				r.Slug,
				r.Status,
				r.Priority,
				r.Project,
				intOrEmpty(r.AgeDays),
				intPtrOrEmpty(r.DueInDays),
				r.DueLabel,
				boolStr(r.Stale),
				intOrEmpty(r.StaleDays),
				r.WaitingOn,
				r.Assignee,
				boolStr(r.Live),
				boolStr(r.Archived),
				strings.Join(r.Tags, ","),
			}
		}
		_ = listfmt.RenderTSV(os.Stdout, tsvHeaders, tsvRows)
		return 0
	}

	// Table mode: assemble color-aware cells.
	painter := listfmt.Painter{Enabled: listfmt.ColorEnabled(os.Stdout, *opts.noColor)}
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		tableRows[i] = []string{
			painter.Wrap("["+statusAbbrev(r.Status)+"]", statusColor(r.Status)),
			painter.Wrap(priorityShort(r.Priority), priorityColor(r.Priority)),
			r.Slug,
			projectCell(r.Project),
			ageString(r.AgeDays),
			painter.Wrap(r.DueLabel, dueColor(r)),
			notesCell(painter, r, opts.waitMax()),
		}
	}
	tab := &listfmt.Table{
		Headers: dimHeaders(painter, headers),
		Rows:    tableRows,
	}
	_ = tab.Render(os.Stdout)
	return 0
}

func ageString(days int) string {
	if days <= 0 {
		return ""
	}
	return fmt.Sprintf("%dd", days)
}

func projectCell(slug string) string {
	if slug == "" {
		return ""
	}
	return "(" + slug + ")"
}

// intOrEmpty stringifies n unless it's zero, in which case it returns "" —
// useful for TSV cells where 0 means "no data" rather than literal zero.
func intOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

func intPtrOrEmpty(p *int) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%d", *p)
}

// boolStr renders booleans as "true"/"" so empty TSV cells stay visually
// quiet and don't clutter the grep'able stream.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return ""
}

// taskListRow is the row shape that feeds table, JSON, and TSV rendering
// for `flow list tasks`. Field order matters for JSON output stability.
type taskListRow struct {
	Slug      string   `json:"slug"`
	Status    string   `json:"status"`
	Priority  string   `json:"priority"`
	Project   string   `json:"project,omitempty"`
	AgeDays   int      `json:"age_days,omitempty"`
	DueInDays *int     `json:"due_in_days,omitempty"`
	DueLabel  string   `json:"due_label,omitempty"`
	Stale     bool     `json:"stale,omitempty"`
	StaleDays int      `json:"stale_days,omitempty"`
	WaitingOn string   `json:"waiting_on,omitempty"`
	Assignee  string   `json:"assignee,omitempty"`
	Live      bool     `json:"live,omitempty"`
	Archived  bool     `json:"archived,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

func dueColor(r taskListRow) string {
	if r.DueInDays == nil {
		return ""
	}
	if *r.DueInDays < 0 {
		return listfmt.Red
	}
	if *r.DueInDays == 0 {
		return listfmt.Yellow
	}
	return ""
}

// notesCell builds the trailing NOTES column in table output. Each fragment
// is colored independently so the row reads well at a glance. waitMax > 0
// truncates the waiting field; 0 disables truncation (the --no-truncate
// path).
func notesCell(p listfmt.Painter, r taskListRow, waitMax int) string {
	var parts []string
	if r.Stale {
		parts = append(parts, p.Wrap(fmt.Sprintf("⚠ stale (%dd)", r.StaleDays), listfmt.Red))
	}
	if r.WaitingOn != "" {
		parts = append(parts, p.Wrap("[waiting: "+listfmt.Truncate(r.WaitingOn, waitMax)+"]", listfmt.Yellow))
	}
	if r.Assignee != "" {
		parts = append(parts, p.Wrap("[@"+r.Assignee+"]", listfmt.Blue))
	}
	if r.Live {
		parts = append(parts, p.Wrap("[live]", listfmt.Cyan))
	}
	for _, t := range r.Tags {
		parts = append(parts, p.Wrap("#"+t, listfmt.Gray))
	}
	if r.Archived {
		parts = append(parts, p.Wrap("(archived)", listfmt.Dim))
	}
	return strings.Join(parts, " ")
}

func listProjectsCmd(args []string) int {
	fs := flagSet("list projects")
	status := fs.String("status", "", "active|done")
	includeArchived := fs.Bool("include-archived", false, "include archived projects")
	opts := addListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmtKind, err := listfmt.ParseFormat(*opts.format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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

	headers := []string{"PRIORITY", "SLUG", "STATUS", "TASKS", "BREAKDOWN", "NOTES"}
	tsvHeaders := []string{"slug", "priority", "status", "total", "in_progress", "backlog", "done", "archived"}
	if len(projects) == 0 {
		return emptyResult(fmtKind, "projects", tsvHeaders)
	}

	// Sort projects by priority (high, med, low) then slug. ListProjects
	// currently sorts by slug only, so reorder here.
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

	type projectRow struct {
		Slug       string `json:"slug"`
		Priority   string `json:"priority"`
		Status     string `json:"status"`
		Total      int    `json:"total"`
		InProgress int    `json:"in_progress"`
		Backlog    int    `json:"backlog"`
		Done       int    `json:"done"`
		Archived   bool   `json:"archived,omitempty"`
	}

	rows := make([]projectRow, 0, len(sortedProjects))
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
		rows = append(rows, projectRow{
			Slug:       p.Slug,
			Priority:   p.Priority,
			Status:     statusW,
			Total:      counts.total,
			InProgress: counts.inProg,
			Backlog:    counts.backlog,
			Done:       counts.done,
			Archived:   p.ArchivedAt.Valid,
		})
	}

	switch fmtKind {
	case listfmt.FormatJSON:
		return runJSON(os.Stdout, rows)
	case listfmt.FormatTSV:
		tsvRows := make([][]string, len(rows))
		for i, r := range rows {
			tsvRows[i] = []string{
				r.Slug, r.Priority, r.Status,
				fmt.Sprintf("%d", r.Total),
				fmt.Sprintf("%d", r.InProgress),
				fmt.Sprintf("%d", r.Backlog),
				fmt.Sprintf("%d", r.Done),
				boolStr(r.Archived),
			}
		}
		_ = listfmt.RenderTSV(os.Stdout, tsvHeaders, tsvRows)
		return 0
	}

	painter := listfmt.Painter{Enabled: listfmt.ColorEnabled(os.Stdout, *opts.noColor)}
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		taskLabel := fmt.Sprintf("%d tasks", r.Total)
		if r.Total == 1 {
			taskLabel = "1 task"
		}
		var segs []string
		if r.InProgress > 0 {
			segs = append(segs, fmt.Sprintf("%d IP", r.InProgress))
		}
		if r.Backlog > 0 {
			segs = append(segs, fmt.Sprintf("%d BL", r.Backlog))
		}
		if r.Done > 0 {
			segs = append(segs, fmt.Sprintf("%d DN", r.Done))
		}
		breakdown := ""
		if len(segs) > 0 {
			breakdown = "(" + strings.Join(segs, ", ") + ")"
		}
		notes := ""
		if r.Archived {
			notes = painter.Wrap("(archived)", listfmt.Dim)
		}
		statusCol := r.Status
		switch r.Status {
		case "active":
			statusCol = painter.Wrap(r.Status, listfmt.Green)
		case "done":
			statusCol = painter.Wrap(r.Status, listfmt.Dim)
		}
		tableRows[i] = []string{
			painter.Wrap(priorityShort(r.Priority), priorityColor(r.Priority)),
			r.Slug,
			statusCol,
			taskLabel,
			breakdown,
			notes,
		}
	}
	tab := &listfmt.Table{
		Headers: dimHeaders(painter, headers),
		Rows:    tableRows,
	}
	_ = tab.Render(os.Stdout)
	return 0
}

func listPlaybooksCmd(args []string) int {
	fs := flagSet("list playbooks")
	project := fs.String("project", "", "filter by project slug")
	includeArchived := fs.Bool("include-archived", false, "include archived")
	opts := addListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmtKind, err := listfmt.ParseFormat(*opts.format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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

	headers := []string{"SLUG", "PROJECT", "NOTES"}
	tsvHeaders := []string{"slug", "project", "archived"}
	if len(pbs) == 0 {
		return emptyResult(fmtKind, "playbooks", tsvHeaders)
	}

	type playbookRow struct {
		Slug     string `json:"slug"`
		Project  string `json:"project,omitempty"`
		Archived bool   `json:"archived,omitempty"`
	}
	rows := make([]playbookRow, len(pbs))
	for i, pb := range pbs {
		r := playbookRow{Slug: pb.Slug, Archived: pb.ArchivedAt.Valid}
		if pb.ProjectSlug.Valid {
			r.Project = pb.ProjectSlug.String
		}
		rows[i] = r
	}

	switch fmtKind {
	case listfmt.FormatJSON:
		return runJSON(os.Stdout, rows)
	case listfmt.FormatTSV:
		tsvRows := make([][]string, len(rows))
		for i, r := range rows {
			tsvRows[i] = []string{r.Slug, r.Project, boolStr(r.Archived)}
		}
		_ = listfmt.RenderTSV(os.Stdout, tsvHeaders, tsvRows)
		return 0
	}

	painter := listfmt.Painter{Enabled: listfmt.ColorEnabled(os.Stdout, *opts.noColor)}
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		notes := ""
		if r.Archived {
			notes = painter.Wrap("(archived)", listfmt.Dim)
		}
		tableRows[i] = []string{
			r.Slug,
			projectCell(r.Project),
			notes,
		}
	}
	tab := &listfmt.Table{
		Headers: dimHeaders(painter, headers),
		Rows:    tableRows,
	}
	_ = tab.Render(os.Stdout)
	return 0
}

func listRunsCmd(args []string) int {
	fs := flagSet("list runs")
	status := fs.String("status", "", "backlog|in-progress|done")
	includeArchived := fs.Bool("include-archived", false, "include archived")
	opts := addListFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmtKind, err := listfmt.ParseFormat(*opts.format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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

	headers := []string{"STATUS", "SLUG", "PLAYBOOK", "NOTES"}
	tsvHeaders := []string{"slug", "status", "playbook", "archived"}
	if len(tasks) == 0 {
		return emptyResult(fmtKind, "runs", tsvHeaders)
	}

	type runRow struct {
		Slug     string `json:"slug"`
		Status   string `json:"status"`
		Playbook string `json:"playbook,omitempty"`
		Archived bool   `json:"archived,omitempty"`
	}
	rows := make([]runRow, len(tasks))
	for i, tk := range tasks {
		r := runRow{Slug: tk.Slug, Status: tk.Status, Archived: tk.ArchivedAt.Valid}
		if tk.PlaybookSlug.Valid {
			r.Playbook = tk.PlaybookSlug.String
		}
		rows[i] = r
	}

	switch fmtKind {
	case listfmt.FormatJSON:
		return runJSON(os.Stdout, rows)
	case listfmt.FormatTSV:
		tsvRows := make([][]string, len(rows))
		for i, r := range rows {
			tsvRows[i] = []string{r.Slug, r.Status, r.Playbook, boolStr(r.Archived)}
		}
		_ = listfmt.RenderTSV(os.Stdout, tsvHeaders, tsvRows)
		return 0
	}

	painter := listfmt.Painter{Enabled: listfmt.ColorEnabled(os.Stdout, *opts.noColor)}
	tableRows := make([][]string, len(rows))
	for i, r := range rows {
		notes := ""
		if r.Archived {
			notes = painter.Wrap("(archived)", listfmt.Dim)
		}
		tableRows[i] = []string{
			painter.Wrap("["+statusAbbrev(r.Status)+"]", statusColor(r.Status)),
			r.Slug,
			projectCell(r.Playbook),
			notes,
		}
	}
	tab := &listfmt.Table{
		Headers: dimHeaders(painter, headers),
		Rows:    tableRows,
	}
	_ = tab.Render(os.Stdout)
	return 0
}

// runJSON is a thin wrapper that reports errors as exit code 1.
func runJSON(w io.Writer, v any) int {
	if err := listfmt.RenderJSON(w, v); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// dimHeaders wraps each header label in dim ANSI when color is enabled so
// the header line reads as supporting text rather than a row of data.
func dimHeaders(p listfmt.Painter, hs []string) []string {
	if !p.Enabled {
		return hs
	}
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = p.Wrap(h, listfmt.Dim)
	}
	return out
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
