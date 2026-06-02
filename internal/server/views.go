package server

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func BuildTaskView(db *sql.DB, root string, t *flowdb.Task, live map[string]bool) (TaskView, error) {
	now := time.Now()
	view := TaskView{
		Slug:           t.Slug,
		Name:           t.Name,
		Status:         t.Status,
		Kind:           t.Kind,
		Priority:       t.Priority,
		WorkDir:        t.WorkDir,
		PermissionMode: t.PermissionMode,
		Live:           false,
		DaysInStatus:   daysInStatus(t, now),
		CreatedAt:      t.CreatedAt,
		UpdatedAt:      t.UpdatedAt,
		BriefPath:      filepath.Join(root, "tasks", t.Slug, "brief.md"),
	}
	view.ProjectSlug = nullStringPtr(t.ProjectSlug)
	view.PlaybookSlug = nullStringPtr(t.PlaybookSlug)
	view.WorktreePath = nullStringPtr(t.WorktreePath)
	view.ParentSlug = nullStringPtr(t.ParentSlug)
	view.WaitingOn = nullStringPtr(t.WaitingOn)
	view.DueDate = nullStringPtr(t.DueDate)
	view.Assignee = nullStringPtr(t.Assignee)
	view.SessionID = nullStringPtr(t.SessionID)
	view.SessionStarted = nullStringPtr(t.SessionStarted)
	view.SessionLastResumed = nullStringPtr(t.SessionLastResumed)
	view.SessionPath = nullStringPtr(t.SessionPath)
	view.InboxSeenAt = nullStringPtr(t.InboxSeenAt)
	view.ArchivedAt = nullStringPtr(t.ArchivedAt)
	view.DeletedAt = nullStringPtr(t.DeletedAt)
	provider := t.SessionProvider
	if provider == "" {
		provider = "claude"
	}
	view.SessionProvider = &provider
	if view.DueDate != nil {
		if s := formatDueDateInfo(*view.DueDate, now); s != "" {
			view.DueInfo = &s
		}
	}
	view.TemporalSummary = temporalSummary(t, now)
	if t.SessionID.Valid && t.SessionID.String != "" {
		if live[strings.ToLower(t.SessionID.String)] {
			view.Live = true
		}
		if _, err := sessionJSONLPath(db, t); err == nil {
			view.TranscriptAvailable = true
		}
	}
	if t.Status == "in-progress" && !t.ArchivedAt.Valid {
		if days, stale := taskStaleness(t, root); stale {
			view.StaleDays = &days
		}
	}
	if wd, err := flowdb.GetWorkdir(db, t.WorkDir); err == nil {
		view.WorkdirKnown = workdirKnown(wd)
	}
	tags, err := flowdb.GetTaskTags(db, t.Slug)
	if err != nil {
		return view, err
	}
	if tags == nil {
		tags = []string{}
	}
	view.Tags = tags
	view.Updates = markdownFiles(filepath.Join(root, "tasks", t.Slug, "updates"), true)
	if view.Updates == nil {
		view.Updates = []FileRef{}
	}
	view.AuxFiles = auxFiles(filepath.Join(root, "tasks", t.Slug))
	if view.AuxFiles == nil {
		view.AuxFiles = []FileRef{}
	}

	// Inbox: stat the file, count unread relative to inbox_seen_at.
	// Cheap: one stat + one file scan only when inbox exists.
	inboxFile := filepath.Join(root, "tasks", t.Slug, "inbox.md")
	if _, err := os.Stat(inboxFile); err == nil {
		view.InboxPath = inboxFile
		if entries, _ := readInboxEntries(inboxFile); entries != nil {
			view.InboxUnreadCount = unreadInboxCount(t, entries)
		}
	}

	// Dependencies: task_dependencies is the source of truth. Parents is
	// the full list; Parent is the first parent (backwards-compat for any
	// caller / test that still reads the singular field).
	view.Parents = loadParents(db, t.Slug)
	if len(view.Parents) > 0 {
		first := view.Parents[0]
		view.Parent = &first
	} else if t.ParentSlug.Valid && t.ParentSlug.String != "" {
		// Defensive fallback: row exists with parent_slug set but no
		// task_dependencies row (e.g. external insert that bypassed
		// AddTaskParent). Surface that single parent.
		view.Parent = loadParent(db, t.ParentSlug.String)
		if view.Parent != nil {
			view.Parents = []TaskSummary{*view.Parent}
		}
	}
	// Children: tasks whose parent_slug points at this task (kept on the
	// legacy column as the dep mirror) plus any task_dependencies row
	// pointing here. The dep table is authoritative; the column read is a
	// defensive fallback for the same reasons as above.
	view.Children = loadChildren(db, t.Slug)

	// Runtime status: latest agent_runtime_states row for this session.
	// Surfaces in UI as the chip next to the task status.
	if t.SessionID.Valid && t.SessionID.String != "" {
		if state, err := flowdb.AgentRuntimeStateBySessionID(db, provider, t.SessionID.String); err == nil {
			rs := state.Status
			view.RuntimeStatus = &rs
		}
	}
	return view, nil
}

func loadParent(db *sql.DB, parentSlug string) *TaskSummary {
	row := db.QueryRow(
		`SELECT slug, name, status, priority, project_slug, updated_at
		 FROM tasks
		 WHERE slug = ? AND deleted_at IS NULL
		 LIMIT 1`,
		parentSlug,
	)
	s, err := scanTaskSummary(row)
	if err != nil {
		return nil
	}
	return &s
}

// loadParents returns every parent task (via task_dependencies) for the
// given child, in dependency-creation order. Excludes soft-deleted rows.
func loadParents(db *sql.DB, childSlug string) []TaskSummary {
	rows, err := db.Query(
		`SELECT t.slug, t.name, t.status, t.priority, t.project_slug, t.updated_at
		 FROM task_dependencies d
		 JOIN tasks t ON t.slug = d.parent_slug
		 WHERE d.child_slug = ?
		   AND t.deleted_at IS NULL
		 ORDER BY d.created_at ASC, d.parent_slug ASC`,
		childSlug,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TaskSummary
	for rows.Next() {
		s, err := scanTaskSummary(rows)
		if err != nil {
			return out
		}
		out = append(out, s)
	}
	return out
}

func loadChildren(db *sql.DB, parentSlug string) []TaskSummary {
	rows, err := db.Query(
		`SELECT slug, name, status, priority, project_slug, updated_at
		 FROM tasks
		 WHERE parent_slug = ? AND deleted_at IS NULL
		 ORDER BY created_at ASC`,
		parentSlug,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TaskSummary
	for rows.Next() {
		s, err := scanTaskSummary(rows)
		if err != nil {
			return out
		}
		out = append(out, s)
	}
	return out
}

func scanTaskSummary(row interface{ Scan(dest ...any) error }) (TaskSummary, error) {
	var s TaskSummary
	var proj sql.NullString
	if err := row.Scan(&s.Slug, &s.Name, &s.Status, &s.Priority, &proj, &s.UpdatedAt); err != nil {
		return s, err
	}
	if proj.Valid {
		v := proj.String
		s.ProjectSlug = &v
	}
	return s, nil
}

func BuildTaskViews(db *sql.DB, root string, tasks []*flowdb.Task) ([]TaskView, error) {
	live, _ := liveAgentSessions()
	return buildTaskViewsWithLive(db, root, tasks, live)
}

// buildTaskViewsWithLive lets callers that already hold a recent live snapshot
// (e.g. buildUIData via cachedLiveAgentSessions) avoid re-forking `ps`.
func buildTaskViewsWithLive(db *sql.DB, root string, tasks []*flowdb.Task, live map[string]bool) ([]TaskView, error) {
	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		v, err := BuildTaskView(db, root, t, live)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func BuildProjectView(db *sql.DB, root string, p *flowdb.Project) (ProjectView, error) {
	view := ProjectView{
		Slug:       p.Slug,
		Name:       p.Name,
		Status:     p.Status,
		Priority:   p.Priority,
		WorkDir:    p.WorkDir,
		CreatedAt:  p.CreatedAt,
		UpdatedAt:  p.UpdatedAt,
		ArchivedAt: nullStringPtr(p.ArchivedAt),
		DeletedAt:  nullStringPtr(p.DeletedAt),
		BriefPath:  filepath.Join(root, "projects", p.Slug, "brief.md"),
		Updates:    markdownFiles(filepath.Join(root, "projects", p.Slug, "updates"), true),
		AuxFiles:   auxFiles(filepath.Join(root, "projects", p.Slug)),
	}
	if view.Updates == nil {
		view.Updates = []FileRef{}
	}
	if view.AuxFiles == nil {
		view.AuxFiles = []FileRef{}
	}
	if wd, err := flowdb.GetWorkdir(db, p.WorkDir); err == nil {
		view.WorkdirKnown = workdirKnown(wd)
	}
	counts, err := projectTaskCounts(db, p.Slug)
	if err != nil {
		return view, err
	}
	view.TaskCounts = counts
	view.RecentTasks, _ = recentProjectTasks(db, p.Slug, 3)
	if view.RecentTasks == nil {
		view.RecentTasks = []TaskSummary{}
	}
	return view, nil
}

func BuildProjectViews(db *sql.DB, root string, projects []*flowdb.Project) ([]ProjectView, error) {
	out := make([]ProjectView, 0, len(projects))
	for _, p := range projects {
		v, err := BuildProjectView(db, root, p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := priorityRank(out[i].Priority), priorityRank(out[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

func BuildPlaybookView(db *sql.DB, root string, pb *flowdb.Playbook) (PlaybookView, error) {
	view := PlaybookView{
		Slug:        pb.Slug,
		Name:        pb.Name,
		ProjectSlug: nullStringPtr(pb.ProjectSlug),
		WorkDir:     pb.WorkDir,
		CreatedAt:   pb.CreatedAt,
		UpdatedAt:   pb.UpdatedAt,
		ArchivedAt:  nullStringPtr(pb.ArchivedAt),
		DeletedAt:   nullStringPtr(pb.DeletedAt),
		BriefPath:   filepath.Join(root, "playbooks", pb.Slug, "brief.md"),
		Updates:     markdownFiles(filepath.Join(root, "playbooks", pb.Slug, "updates"), true),
		AuxFiles:    auxFiles(filepath.Join(root, "playbooks", pb.Slug)),
		RunDays30:   make([]int, 30),
	}
	if view.Updates == nil {
		view.Updates = []FileRef{}
	}
	if view.AuxFiles == nil {
		view.AuxFiles = []FileRef{}
	}
	runs, err := flowdb.ListTasks(db, flowdb.TaskFilter{
		Kind:            "playbook_run",
		PlaybookSlug:    pb.Slug,
		IncludeArchived: true,
	})
	if err != nil {
		return view, err
	}
	now := time.Now()
	weekAgo := now.AddDate(0, 0, -7)
	for _, r := range runs {
		rs := RunSummary{
			Slug:       r.Slug,
			Name:       r.Name,
			Status:     r.Status,
			Priority:   r.Priority,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
			StartedAt:  nullStringPtr(r.SessionStarted),
			ArchivedAt: nullStringPtr(r.ArchivedAt),
			DeletedAt:  nullStringPtr(r.DeletedAt),
		}
		view.RecentRuns = append(view.RecentRuns, rs)
		if created, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
			if created.After(weekAgo) {
				view.RunCount7d++
			}
			for i := 0; i < 30; i++ {
				y1, m1, d1 := now.AddDate(0, 0, -29+i).Date()
				y2, m2, d2 := created.Date()
				if y1 == y2 && m1 == m2 && d1 == d2 {
					view.RunDays30[i]++
				}
			}
		}
	}
	sort.SliceStable(view.RecentRuns, func(i, j int) bool {
		return view.RecentRuns[i].CreatedAt > view.RecentRuns[j].CreatedAt
	})
	if view.RecentRuns == nil {
		view.RecentRuns = []RunSummary{}
	}
	return view, nil
}

func BuildPlaybookViews(db *sql.DB, root string, pbs []*flowdb.Playbook) ([]PlaybookView, error) {
	out := make([]PlaybookView, 0, len(pbs))
	for _, pb := range pbs {
		v, err := BuildPlaybookView(db, root, pb)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func BuildWorkdirView(db *sql.DB, w *flowdb.Workdir) WorkdirView {
	view := WorkdirView{
		Path:           w.Path,
		Name:           nullStringPtr(w.Name),
		Description:    nullStringPtr(w.Description),
		GitRemote:      nullStringPtr(w.GitRemote),
		LastUsedAt:     nullStringPtr(w.LastUsedAt),
		CreatedAt:      w.CreatedAt,
		TasksUsingThis: tasksUsingWorkdir(db, w.Path),
	}
	if w.LastUsedAt.Valid && w.LastUsedAt.String != "" {
		if ts, err := time.Parse(time.RFC3339, w.LastUsedAt.String); err == nil {
			view.Untouched30d = time.Since(ts) > 30*24*time.Hour
		}
	}
	return view
}

func BuildWorkdirViews(db *sql.DB, workdirs []*flowdb.Workdir) []WorkdirView {
	out := make([]WorkdirView, 0, len(workdirs))
	for _, w := range workdirs {
		out = append(out, BuildWorkdirView(db, w))
	}
	return out
}

func BuildKBFileView(path string) KBFileView {
	info, _ := os.Stat(path)
	view := KBFileView{
		Filename: filepath.Base(path),
		Path:     path,
	}
	if info != nil {
		view.MTime = info.ModTime().Format(time.RFC3339Nano)
		view.Size = info.Size()
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return view
	}
	view.Content = string(body)
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			view.Entries++
			view.Preview = strings.TrimPrefix(trimmed, "- ")
		}
	}
	return view
}

func kbFiles(root string) []string {
	names := []string{"user.md", "org.md", "products.md", "processes.md", "business.md"}
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		path := filepath.Join(root, "kb", name)
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
			seen[name] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "kb"))
	if err != nil {
		return out
	}
	var extra []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || seen[name] || filepath.Ext(name) != ".md" || !validFilename(name) {
			continue
		}
		extra = append(extra, name)
	}
	sort.Strings(extra)
	for _, name := range extra {
		out = append(out, filepath.Join(root, "kb", name))
	}
	return out
}

func markdownFiles(dir string, newestFirst bool) []FileRef {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []FileRef
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		out = append(out, FileRef{
			Filename: entry.Name(),
			Path:     path,
			MTime:    info.ModTime().Format(time.RFC3339),
			Size:     info.Size(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if newestFirst {
			return out[i].Filename > out[j].Filename
		}
		return out[i].Filename < out[j].Filename
	})
	return out
}

func auxFiles(dir string) []FileRef {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []FileRef
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "brief.md" || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		out = append(out, FileRef{
			Filename: entry.Name(),
			Path:     path,
			MTime:    info.ModTime().Format(time.RFC3339),
			Size:     info.Size(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Filename < out[j].Filename })
	return out
}

func workdirKnown(w *flowdb.Workdir) *WorkdirKnown {
	known := &WorkdirKnown{}
	if w.Name.Valid && w.Name.String != "" {
		known.Name = &w.Name.String
	}
	if w.GitRemote.Valid && w.GitRemote.String != "" {
		known.GitRemote = &w.GitRemote.String
	}
	return known
}

func nullStringPtr(ns sql.NullString) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return &ns.String
}

func nullStringFromPtr(s *string) sql.NullString {
	if s == nil || *s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func projectTaskCounts(db *sql.DB, projectSlug string) (TaskCounts, error) {
	var counts TaskCounts
	rows, err := db.Query(
		`SELECT status, COUNT(*) FROM tasks
		 WHERE project_slug = ? AND archived_at IS NULL AND deleted_at IS NULL
		 GROUP BY status`, projectSlug)
	if err != nil {
		return counts, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return counts, err
		}
		counts.Total += n
		switch status {
		case "in-progress":
			counts.InProgress += n
		case "backlog":
			counts.Backlog += n
		case "done":
			counts.Done += n
		}
	}
	return counts, rows.Err()
}

func recentProjectTasks(db *sql.DB, projectSlug string, limit int) ([]TaskSummary, error) {
	rows, err := db.Query(
		`SELECT `+flowdb.TaskCols+` FROM tasks
		 WHERE project_slug = ? AND archived_at IS NULL AND deleted_at IS NULL
		 ORDER BY updated_at DESC
		 LIMIT ?`, projectSlug, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskSummary
	for rows.Next() {
		t, err := flowdb.ScanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, taskSummary(t))
	}
	return out, rows.Err()
}

func taskSummary(t *flowdb.Task) TaskSummary {
	return TaskSummary{
		Slug:        t.Slug,
		Name:        t.Name,
		Status:      t.Status,
		Priority:    t.Priority,
		ProjectSlug: nullStringPtr(t.ProjectSlug),
		UpdatedAt:   t.UpdatedAt,
	}
}

func tasksUsingWorkdir(db *sql.DB, path string) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE work_dir = ? AND archived_at IS NULL AND deleted_at IS NULL`, path).Scan(&n)
	return n
}

func priorityRank(p string) int {
	switch p {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}

func staleDaysThreshold() int {
	if s := os.Getenv("FLOW_STALE_DAYS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return 3
}

func taskStaleness(t *flowdb.Task, root string) (int, bool) {
	last, err := time.Parse(time.RFC3339, t.UpdatedAt)
	if err != nil {
		return 0, false
	}
	updatesDir := filepath.Join(root, "tasks", t.Slug, "updates")
	if entries, err := os.ReadDir(updatesDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			info, err := entry.Info()
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

func daysUntilDue(t *flowdb.Task, now time.Time) (int, bool) {
	if !t.DueDate.Valid || t.DueDate.String == "" {
		return 0, false
	}
	due, err := time.ParseInLocation("2006-01-02", t.DueDate.String, now.Location())
	if err != nil {
		return 0, false
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return int(due.Sub(today) / (24 * time.Hour)), true
}

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
			return "overdue by 1 day"
		}
		return fmt.Sprintf("overdue by %d days", abs)
	case diff == 0:
		return "today"
	case diff == 1:
		return "tomorrow"
	default:
		return fmt.Sprintf("in %d days", diff)
	}
}

func temporalSummary(t *flowdb.Task, now time.Time) string {
	var parts []string
	if age := daysInStatus(t, now); age > 0 {
		word := "days"
		if age == 1 {
			word = "day"
		}
		parts = append(parts, fmt.Sprintf("%s for %d %s", t.Status, age, word))
	}
	if diff, ok := daysUntilDue(t, now); ok {
		switch {
		case diff < 0:
			abs := -diff
			word := "days"
			if abs == 1 {
				word = "day"
			}
			parts = append(parts, fmt.Sprintf("overdue by %d %s", abs, word))
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

func parseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "":
		return time.Time{}, nil
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	case "monday", "this-week", "week":
		wd := int(now.Weekday())
		offset := (wd + 6) % 7
		y, m, d := now.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
		return start.AddDate(0, 0, -offset), nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &n); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized since value %q", s)
}

func workspaceTree(root string) WorkspaceView {
	view := WorkspaceView{Root: root}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return view
	}
	view.Exists = true
	entries, truncated := readWorkspaceDir(root, root, 0, 3, 400)
	view.Nodes = entries
	view.Truncated = truncated
	return view
}

func readWorkspaceDir(base, dir string, depth, maxDepth, remaining int) ([]WorkspaceNode, bool) {
	if remaining <= 0 || depth > maxDepth {
		return nil, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	var nodes []WorkspaceNode
	truncated := false
	for _, entry := range entries {
		if entry.Name() == ".git" || entry.Name() == "node_modules" {
			continue
		}
		if len(nodes) >= remaining {
			truncated = true
			break
		}
		full := filepath.Join(dir, entry.Name())
		rel, _ := filepath.Rel(base, full)
		node := WorkspaceNode{Name: entry.Name(), Path: rel}
		if entry.IsDir() {
			node.Type = "dir"
			if depth < maxDepth {
				children, childTruncated := readWorkspaceDir(base, full, depth+1, maxDepth, remaining-len(nodes)-1)
				node.Children = children
				truncated = truncated || childTruncated
			}
		} else {
			node.Type = "file"
			if info, err := entry.Info(); err == nil {
				node.Size = info.Size()
			}
		}
		nodes = append(nodes, node)
	}
	return nodes, truncated
}

func fileForEntity(root, kind, slug, section, filename string) (string, error) {
	base := filepath.Join(root, kind, slug)
	switch section {
	case "brief":
		return filepath.Join(base, "brief.md"), nil
	case "updates":
		if !validFilename(filename) {
			return "", errors.New("invalid filename")
		}
		return filepath.Join(base, "updates", filename), nil
	case "aux":
		if !validFilename(filename) {
			return "", errors.New("invalid filename")
		}
		return filepath.Join(base, filename), nil
	default:
		return "", errors.New("invalid section")
	}
}

func validFilename(name string) bool {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func readMarkdown(path string) ([]byte, error) {
	if filepath.Ext(path) != ".md" {
		return nil, errors.New("not markdown")
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []byte{}, nil
	}
	return body, err
}
