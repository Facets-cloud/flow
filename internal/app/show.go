package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cmdShow dispatches `flow show task|project|playbook`. Per spec §5.4.
func cmdShow(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: show requires 'task', 'project', or 'playbook'")
		return 2
	}
	switch args[0] {
	case "task":
		return showTaskCmd(args[1:])
	case "project":
		return showProjectCmd(args[1:])
	case "playbook":
		return showPlaybookCmd(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown show subcommand %q\n", args[0])
	return 2
}

// showTaskCmd implements `flow show task [<ref>]`.
func showTaskCmd(args []string) int {
	fs := flagSet("show task")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ref := ""
	if fs.NArg() > 0 {
		ref = fs.Arg(0)
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

	var t *flowdb.Task
	if ref == "" {
		// No explicit ref: reverse-lookup via the current Claude session.
		bound, lookupErr := currentSessionTask(db)
		if lookupErr != nil {
			if isNoBindingErr(lookupErr) {
				if currentSessionID() == "" {
					fmt.Fprintln(os.Stderr, "error: no task ref given and not running inside a Claude session ($CLAUDE_CODE_SESSION_ID unset)")
				} else {
					fmt.Fprintln(os.Stderr, "error: no task ref given and this Claude session is not bound to a task — pass a slug or run `flow do --here <slug>` first")
				}
				return 1
			}
			fmt.Fprintf(os.Stderr, "error: lookup task by session: %v\n", lookupErr)
			return 1
		}
		t = bound
	} else {
		t, err = resolveTaskRef(db, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	printTaskMetadata(db, t, root)
	return 0
}

// showProjectCmd implements `flow show project [<ref>]`. With no
// argument, falls back to the project of the task bound to the
// current Claude session.
func showProjectCmd(args []string) int {
	fs := flagSet("show project")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ref := ""
	if fs.NArg() > 0 {
		ref = fs.Arg(0)
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

	if ref == "" {
		// Reverse-lookup: find the task bound to this session, then its project.
		bound, lookupErr := currentSessionTask(db)
		if lookupErr != nil {
			if isNoBindingErr(lookupErr) {
				if currentSessionID() == "" {
					fmt.Fprintln(os.Stderr, "error: no project ref given and not running inside a Claude session ($CLAUDE_CODE_SESSION_ID unset)")
				} else {
					fmt.Fprintln(os.Stderr, "error: no project ref given and this Claude session is not bound to a task")
				}
				return 1
			}
			fmt.Fprintf(os.Stderr, "error: lookup task by session: %v\n", lookupErr)
			return 1
		}
		if !bound.ProjectSlug.Valid || bound.ProjectSlug.String == "" {
			fmt.Fprintf(os.Stderr, "error: bound task %q is floating (no project)\n", bound.Slug)
			return 1
		}
		ref = bound.ProjectSlug.String
	}

	p, err := resolveProjectRef(db, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	printProjectMetadata(db, p, root)
	return 0
}

// showPlaybookCmd implements `flow show playbook [<ref>]`.
func showPlaybookCmd(args []string) int {
	fs := flagSet("show playbook")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ref := ""
	if fs.NArg() > 0 {
		ref = fs.Arg(0)
	}
	if ref == "" {
		fmt.Fprintln(os.Stderr, "error: no playbook ref given")
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

	pb, err := ResolvePlaybook(db, ref, true) // include archived for show
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	printPlaybookMetadata(db, pb, root)
	return 0
}

// ---------- resolution helpers ----------

// resolveTaskRef resolves a ref to a task. Includes archived rows so
// `show task` can display them.
func resolveTaskRef(db *sql.DB, query string) (*flowdb.Task, error) {
	return ResolveTask(db, query, true)
}

// resolveProjectRef resolves a ref to a project. Includes archived rows.
func resolveProjectRef(db *sql.DB, query string) (*flowdb.Project, error) {
	return ResolveProject(db, query, true)
}

// ---------- pretty printers ----------

// printTaskMetadata writes the human-readable view of a task row.
func printTaskMetadata(db *sql.DB, t *flowdb.Task, root string) {
	archivedMarker := ""
	if t.ArchivedAt.Valid {
		archivedMarker = "  (archived)"
	}
	fmt.Printf("slug:          %s%s\n", t.Slug, archivedMarker)
	fmt.Printf("name:          %s\n", t.Name)
	projName := "(floating)"
	if t.ProjectSlug.Valid && t.ProjectSlug.String != "" {
		projName = t.ProjectSlug.String
	}
	fmt.Printf("project:       %s\n", projName)
	fmt.Printf("status:        %s\n", t.Status)

	// Staleness marker for in-progress tasks.
	if t.Status == "in-progress" && !t.ArchivedAt.Valid {
		if days, stale := taskStaleness(t, root); stale {
			fmt.Printf("               ⚠ stale (%d days)\n", days)
		}
	}

	fmt.Printf("priority:      %s\n", t.Priority)

	// Due date.
	if t.DueDate.Valid && t.DueDate.String != "" {
		dueLabel := t.DueDate.String
		if dueInfo := formatDueDateInfo(t.DueDate.String, time.Now()); dueInfo != "" {
			dueLabel += "  " + dueInfo
		}
		fmt.Printf("due:           %s\n", dueLabel)
	}

	// Temporal summary: days in current status + due-date proximity.
	if !t.ArchivedAt.Valid {
		if summary := temporalSummary(t, time.Now()); summary != "" {
			fmt.Printf("temporal:      %s\n", summary)
		}
	}

	// Work dir + optional workdir registry annotation.
	wdLine := t.WorkDir
	if wd, err := flowdb.GetWorkdir(db, t.WorkDir); err == nil {
		var parts []string
		if wd.Name.Valid && wd.Name.String != "" {
			parts = append(parts, "known: "+wd.Name.String)
		} else {
			parts = append(parts, "known")
		}
		if wd.GitRemote.Valid && wd.GitRemote.String != "" {
			parts = append(parts, "origin: "+wd.GitRemote.String)
		}
		wdLine = fmt.Sprintf("%s  [%s]", t.WorkDir, strings.Join(parts, ", "))
	}
	fmt.Printf("work_dir:      %s\n", wdLine)

	if t.WaitingOn.Valid && t.WaitingOn.String != "" {
		fmt.Printf("waiting_on:    %s\n", t.WaitingOn.String)
	}
	if t.Assignee.Valid && t.Assignee.String != "" {
		fmt.Printf("assignee:      %s\n", t.Assignee.String)
	}

	if tags, err := flowdb.GetTaskTags(db, t.Slug); err == nil && len(tags) > 0 {
		parts := make([]string, len(tags))
		for i, tg := range tags {
			parts[i] = "#" + tg
		}
		fmt.Printf("tags:          %s\n", strings.Join(parts, " "))
	}

	sid := "(not bootstrapped)"
	if t.SessionID.Valid && t.SessionID.String != "" {
		sid = t.SessionID.String
		if live, err := liveClaudeSessions(); err == nil {
			if live[strings.ToLower(t.SessionID.String)] {
				sid += "  [live]"
			}
		}
	}
	fmt.Printf("session_id:            %s\n", sid)
	sstart := "(not bootstrapped)"
	if t.SessionStarted.Valid && t.SessionStarted.String != "" {
		sstart = t.SessionStarted.String
	}
	fmt.Printf("session_started:       %s\n", sstart)
	slast := "(never)"
	if t.SessionLastResumed.Valid && t.SessionLastResumed.String != "" {
		slast = t.SessionLastResumed.String
	}
	fmt.Printf("session_last_resumed:  %s\n", slast)

	fmt.Printf("created:       %s\n", t.CreatedAt)
	fmt.Printf("updated:       %s\n", t.UpdatedAt)
	if t.ArchivedAt.Valid {
		fmt.Printf("archived:      %s\n", t.ArchivedAt.String)
	}
	briefPath := filepath.Join(root, "tasks", t.Slug, "brief.md")
	fmt.Printf("brief:         %s\n", briefPath)

	updates := listUpdateFiles(filepath.Join(root, "tasks", t.Slug, "updates"))
	if len(updates) == 0 {
		fmt.Println("updates:       (none)")
	} else {
		fmt.Println("updates:")
		for _, u := range updates {
			fmt.Printf("  - %s\n", u)
		}
	}

	// Auxiliary .md files (sidecar references — not eagerly loaded).
	auxFiles, err := enumerateAuxFiles(filepath.Join(root, "tasks", t.Slug))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: enumerate aux files: %v\n", err)
	}
	if len(auxFiles) == 0 {
		fmt.Println("other:         (none)")
	} else {
		fmt.Println("other:")
		for _, p := range auxFiles {
			fmt.Printf("  - %s\n", p)
		}
	}

	// Transcript CTA — only shown when the task has a session.
	if t.SessionID.Valid && t.SessionID.String != "" {
		fmt.Printf("transcript:    run `flow transcript %s` to view conversation history\n", t.Slug)
	}

	// Knowledge-base files — durable facts about the user and their org.
	// Execution sessions are instructed (via the skill and SessionStart
	// hook) to Read each file listed here as part of their context load.
	kb := kbFiles(root)
	if len(kb) == 0 {
		fmt.Println("kb:            (none)")
	} else {
		fmt.Println("kb:")
		for _, k := range kb {
			fmt.Printf("  - %s\n", k)
		}
	}
}

// printProjectMetadata writes the human-readable view of a project row.
func printProjectMetadata(db *sql.DB, p *flowdb.Project, root string) {
	archivedMarker := ""
	if p.ArchivedAt.Valid {
		archivedMarker = "  (archived)"
	}
	fmt.Printf("slug:        %s%s\n", p.Slug, archivedMarker)
	fmt.Printf("name:        %s\n", p.Name)
	fmt.Printf("status:      %s\n", p.Status)
	fmt.Printf("priority:    %s\n", p.Priority)

	wdLine := p.WorkDir
	if wd, err := flowdb.GetWorkdir(db, p.WorkDir); err == nil {
		var parts []string
		if wd.Name.Valid && wd.Name.String != "" {
			parts = append(parts, "known: "+wd.Name.String)
		} else {
			parts = append(parts, "known")
		}
		if wd.GitRemote.Valid && wd.GitRemote.String != "" {
			parts = append(parts, "origin: "+wd.GitRemote.String)
		}
		wdLine = fmt.Sprintf("%s  [%s]", p.WorkDir, strings.Join(parts, ", "))
	}
	fmt.Printf("work_dir:    %s\n", wdLine)

	fmt.Printf("created:     %s\n", p.CreatedAt)
	fmt.Printf("updated:     %s\n", p.UpdatedAt)
	if p.ArchivedAt.Valid {
		fmt.Printf("archived:    %s\n", p.ArchivedAt.String)
	}
	briefPath := filepath.Join(root, "projects", p.Slug, "brief.md")
	fmt.Printf("brief:       %s\n", briefPath)

	updates := listUpdateFiles(filepath.Join(root, "projects", p.Slug, "updates"))
	if len(updates) == 0 {
		fmt.Println("updates:     (none)")
	} else {
		fmt.Println("updates:")
		for _, u := range updates {
			fmt.Printf("  - %s\n", u)
		}
	}

	// Auxiliary .md files (sidecar references — not eagerly loaded).
	auxFiles, auxErr := enumerateAuxFiles(filepath.Join(root, "projects", p.Slug))
	if auxErr != nil {
		fmt.Fprintf(os.Stderr, "warning: enumerate aux files: %v\n", auxErr)
	}
	if len(auxFiles) == 0 {
		fmt.Println("other:       (none)")
	} else {
		fmt.Println("other:")
		for _, fp := range auxFiles {
			fmt.Printf("  - %s\n", fp)
		}
	}

	// Knowledge-base files, same as on `flow show task`.
	kb := kbFiles(root)
	if len(kb) == 0 {
		fmt.Println("kb:          (none)")
	} else {
		fmt.Println("kb:")
		for _, k := range kb {
			fmt.Printf("  - %s\n", k)
		}
	}

	// Task breakdown.
	slug := p.Slug
	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{Project: slug, IncludeArchived: false})
	if err != nil {
		fmt.Printf("tasks:       (error: %v)\n", err)
		return
	}
	var inProg, backlog, done int
	for _, t := range tasks {
		switch t.Status {
		case "in-progress":
			inProg++
		case "backlog":
			backlog++
		case "done":
			done++
		}
	}
	fmt.Printf("tasks:       %d total  (%d in-progress, %d backlog, %d done)\n",
		len(tasks), inProg, backlog, done)
}

// printPlaybookMetadata writes the human-readable view of a playbook row.
func printPlaybookMetadata(db *sql.DB, pb *flowdb.Playbook, root string) {
	archivedMarker := ""
	if pb.ArchivedAt.Valid {
		archivedMarker = "  (archived)"
	}
	fmt.Printf("slug:        %s%s\n", pb.Slug, archivedMarker)
	fmt.Printf("name:        %s\n", pb.Name)
	if pb.ProjectSlug.Valid {
		fmt.Printf("project:     %s\n", pb.ProjectSlug.String)
	} else {
		fmt.Printf("project:     (floating)\n")
	}

	wdLine := pb.WorkDir
	if wd, err := flowdb.GetWorkdir(db, pb.WorkDir); err == nil {
		var parts []string
		if wd.Name.Valid && wd.Name.String != "" {
			parts = append(parts, "known: "+wd.Name.String)
		} else {
			parts = append(parts, "known")
		}
		if wd.GitRemote.Valid && wd.GitRemote.String != "" {
			parts = append(parts, "origin: "+wd.GitRemote.String)
		}
		wdLine = fmt.Sprintf("%s  [%s]", pb.WorkDir, strings.Join(parts, ", "))
	}
	fmt.Printf("work_dir:    %s\n", wdLine)

	fmt.Printf("created:     %s\n", pb.CreatedAt)
	fmt.Printf("updated:     %s\n", pb.UpdatedAt)
	if pb.ArchivedAt.Valid {
		fmt.Printf("archived:    %s\n", pb.ArchivedAt.String)
	}

	pbDir := filepath.Join(root, "playbooks", pb.Slug)
	briefPath := filepath.Join(pbDir, "brief.md")
	fmt.Printf("brief:       %s\n", briefPath)

	updates := listUpdateFiles(filepath.Join(pbDir, "updates"))
	if len(updates) == 0 {
		fmt.Println("updates:     (none)")
	} else {
		fmt.Println("updates:")
		for _, u := range updates {
			fmt.Printf("  - %s\n", u)
		}
	}

	// Recent runs (last 5).
	runs, err := flowdb.ListTasks(db, flowdb.TaskFilter{
		Kind:         "playbook_run",
		PlaybookSlug: pb.Slug,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: list runs: %v\n", err)
	}
	if len(runs) == 0 {
		fmt.Println("runs (last 5): (none)")
	} else {
		// Sort by created_at descending so the most recent appears first.
		sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt > runs[j].CreatedAt })
		max := 5
		if len(runs) < max {
			max = len(runs)
		}
		fmt.Println("runs (last 5):")
		for _, r := range runs[:max] {
			fmt.Printf("  %-50s [%s]\n", r.Slug, statusAbbrev(r.Status))
		}
	}

	// Aux files (sidecar references — on-demand load).
	auxFiles, auxErr := enumerateAuxFiles(pbDir)
	if auxErr != nil {
		fmt.Fprintf(os.Stderr, "warning: enumerate aux files: %v\n", auxErr)
	}
	if len(auxFiles) == 0 {
		fmt.Println("other:       (none)")
	} else {
		fmt.Println("other:")
		for _, fp := range auxFiles {
			fmt.Printf("  - %s\n", fp)
		}
	}

	// KB refs.
	kb := kbFiles(root)
	if len(kb) == 0 {
		fmt.Println("kb:          (none)")
	} else {
		fmt.Println("kb:")
		for _, k := range kb {
			fmt.Printf("  - %s\n", k)
		}
	}
}

// listUpdateFiles returns absolute paths to all *.md files under dir,
// sorted ascending. Missing dir yields an empty slice, not an error.
func listUpdateFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".md" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)
	return paths
}

// staleDaysThreshold returns the staleness threshold in days. Reads
// FLOW_STALE_DAYS env var; defaults to 3.
func staleDaysThreshold() int {
	if s := os.Getenv("FLOW_STALE_DAYS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return 3
}

// taskStaleness returns (daysSinceLastTouch, stale) for an in-progress
// task. "Last touch" is max(updated_at, newest update file mtime).
// Staleness threshold is configurable via FLOW_STALE_DAYS (default 3).
func taskStaleness(t *flowdb.Task, root string) (int, bool) {
	last, err := time.Parse(time.RFC3339, t.UpdatedAt)
	if err != nil {
		return 0, false
	}
	updatesDir := filepath.Join(root, "tasks", t.Slug, "updates")
	if entries, err := os.ReadDir(updatesDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(last) {
				last = info.ModTime()
			}
		}
	}
	age := time.Since(last)
	days := int(age / (24 * time.Hour))
	threshold := staleDaysThreshold()
	return days, age > time.Duration(threshold)*24*time.Hour
}

// daysInStatus returns the number of days the task has been in its
// current status. Uses status_changed_at if set, else falls back to
// created_at.
func daysInStatus(t *flowdb.Task, now time.Time) int {
	ref := t.CreatedAt
	if t.StatusChangedAt.Valid && t.StatusChangedAt.String != "" {
		ref = t.StatusChangedAt.String
	}
	parsed, err := time.Parse(time.RFC3339, ref)
	if err != nil {
		return 0
	}
	return int(now.Sub(parsed) / (24 * time.Hour))
}

// daysUntilDue returns the number of days until the due date. Negative
// means overdue. Returns (0, false) if no due date is set.
func daysUntilDue(t *flowdb.Task, now time.Time) (int, bool) {
	if !t.DueDate.Valid || t.DueDate.String == "" {
		return 0, false
	}
	due, err := time.ParseInLocation("2006-01-02", t.DueDate.String, now.Location())
	if err != nil {
		return 0, false
	}
	// Compare dates only (strip time component from now).
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	diff := int(due.Sub(today) / (24 * time.Hour))
	return diff, true
}

// formatDueDateInfo returns a parenthetical like "(in 3 days)",
// "(today)", or "(overdue by 2 days)" for display next to the date.
func formatDueDateInfo(dateStr string, now time.Time) string {
	due, err := time.ParseInLocation("2006-01-02", dateStr, now.Location())
	if err != nil {
		return ""
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	diff := int(due.Sub(today) / (24 * time.Hour))
	switch {
	case diff < 0:
		abs := -diff
		if abs == 1 {
			return "(overdue by 1 day)"
		}
		return fmt.Sprintf("(overdue by %d days)", abs)
	case diff == 0:
		return "(today)"
	case diff == 1:
		return "(tomorrow)"
	default:
		return fmt.Sprintf("(in %d days)", diff)
	}
}

// temporalSummary builds the "in-progress for 5 days, due in 2 days"
// line for `flow show task`.
func temporalSummary(t *flowdb.Task, now time.Time) string {
	var parts []string

	age := daysInStatus(t, now)
	if age > 0 {
		dayWord := "days"
		if age == 1 {
			dayWord = "day"
		}
		parts = append(parts, fmt.Sprintf("%s for %d %s", t.Status, age, dayWord))
	}

	if diff, ok := daysUntilDue(t, now); ok {
		switch {
		case diff < 0:
			abs := -diff
			dayWord := "days"
			if abs == 1 {
				dayWord = "day"
			}
			parts = append(parts, fmt.Sprintf("overdue by %d %s", abs, dayWord))
		case diff == 0:
			parts = append(parts, "due today")
		case diff == 1:
			parts = append(parts, "due tomorrow")
		default:
			parts = append(parts, fmt.Sprintf("due in %d days", diff))
		}
	}

	return strings.Join(parts, ", ")
}
