package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flow/internal/flowdb"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	writeJSON(w, HealthView{OK: true, Version: s.cfg.Version, FlowRoot: s.cfg.FlowRoot})
}

func (s *Server) handleFSEntries(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	dir, err := expandUIPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	out := FSEntriesView{
		Path:        dir,
		DisplayPath: displayUIPath(dir),
		IsGit:       isGitWorkdir(dir),
		Breadcrumbs: fsBreadcrumbs(dir),
		Entries:     []FSEntryView{},
	}
	if parent := filepath.Dir(dir); parent != dir {
		out.Parent = &parent
	}
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		isDir := entry.IsDir()
		if !isDir && entry.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(child); err == nil {
				isDir = info.IsDir()
			}
		}
		out.Entries = append(out.Entries, FSEntryView{
			Name:        entry.Name(),
			Path:        child,
			DisplayPath: displayUIPath(child),
			IsDir:       isDir,
			IsGit:       isDir && isGitWorkdir(child),
			Hidden:      strings.HasPrefix(entry.Name(), "."),
		})
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	writeJSON(w, out)
}

func expandUIPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch {
	case raw == "", raw == "~":
		return home, nil
	case strings.HasPrefix(raw, "~/"):
		return filepath.Clean(filepath.Join(home, strings.TrimPrefix(raw, "~/"))), nil
	case filepath.IsAbs(raw):
		return filepath.Clean(raw), nil
	default:
		return filepath.Abs(raw)
	}
}

func displayUIPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err == nil && rel == "." {
		return "~"
	}
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

func fsBreadcrumbs(path string) []FSBreadcrumb {
	home, err := os.UserHomeDir()
	if err == nil {
		rel, relErr := filepath.Rel(home, path)
		if relErr == nil && rel == "." {
			return []FSBreadcrumb{{Name: "~", Path: home}}
		}
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			crumbs := []FSBreadcrumb{{Name: "~", Path: home}}
			cursor := home
			for _, part := range strings.Split(rel, string(os.PathSeparator)) {
				if part == "" {
					continue
				}
				cursor = filepath.Join(cursor, part)
				crumbs = append(crumbs, FSBreadcrumb{Name: part, Path: cursor})
			}
			return crumbs
		}
	}

	volume := filepath.VolumeName(path)
	root := string(os.PathSeparator)
	if volume != "" {
		root = volume + string(os.PathSeparator)
	}
	crumbs := []FSBreadcrumb{{Name: root, Path: root}}
	rel := strings.TrimPrefix(path, root)
	cursor := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		cursor = filepath.Join(cursor, part)
		crumbs = append(crumbs, FSBreadcrumb{Name: part, Path: cursor})
	}
	return crumbs
}

func isGitWorkdir(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	live, _ := liveAgentSessions()
	tasks, err := flowdb.ListTasks(s.cfg.DB, flowdb.TaskFilter{IncludeArchived: false, Kind: ""})
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	taskViews := make([]TaskView, 0, len(tasks))
	for _, task := range tasks {
		view, err := BuildTaskView(s.cfg.DB, s.cfg.FlowRoot, task, live)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		taskViews = append(taskViews, view)
	}
	out := OverviewView{
		LiveSessions:        []TaskView{},
		InFlight:            []TaskView{},
		HighPriorityBacklog: []TaskView{},
		Waiting:             []TaskView{},
		Stale:               []TaskView{},
		ActivePlaybooks:     []PlaybookView{},
	}
	for _, task := range taskViews {
		if task.Live {
			out.LiveSessions = append(out.LiveSessions, task)
		}
		if task.Status == "in-progress" && task.Kind == "regular" {
			out.InFlight = append(out.InFlight, task)
		}
		if task.Status == "backlog" && task.Priority == "high" {
			out.HighPriorityBacklog = append(out.HighPriorityBacklog, task)
		}
		if task.WaitingOn != nil {
			out.Waiting = append(out.Waiting, task)
		}
		if task.StaleDays != nil {
			out.Stale = append(out.Stale, task)
		}
	}
	pbs, err := flowdb.ListPlaybooks(s.cfg.DB, flowdb.PlaybookFilter{})
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	pbViews, err := BuildPlaybookViews(s.cfg.DB, s.cfg.FlowRoot, pbs)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	for _, pb := range pbViews {
		if pb.RunCount7d > 0 {
			out.ActivePlaybooks = append(out.ActivePlaybooks, pb)
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	if r.URL.Path != "/api/tasks" {
		http.NotFound(w, r)
		return
	}
	filter, err := taskFilterFromQuery(r.URL.Query())
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	tasks, err := flowdb.ListTasks(s.cfg.DB, filter)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	views, err := BuildTaskViews(s.cfg.DB, s.cfg.FlowRoot, tasks)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, views)
}

func (s *Server) handleTaskRoute(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	parts, ok := routeParts(w, r, "/api/tasks/")
	if !ok {
		return
	}
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	slug := parts[0]
	task, err := flowdb.GetTask(s.cfg.DB, slug)
	if err != nil {
		writeNotFoundOrError(w, err)
		return
	}
	switch {
	case len(parts) == 1:
		live, _ := liveAgentSessions()
		view, err := BuildTaskView(s.cfg.DB, s.cfg.FlowRoot, task, live)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, view)
	case len(parts) == 2 && parts[1] == "brief":
		serveMarkdown(w, filepath.Join(s.cfg.FlowRoot, "tasks", slug, "brief.md"))
	case len(parts) == 2 && parts[1] == "updates":
		writeJSON(w, markdownFiles(filepath.Join(s.cfg.FlowRoot, "tasks", slug, "updates"), true))
	case len(parts) == 3 && parts[1] == "updates":
		path, err := fileForEntity(s.cfg.FlowRoot, "tasks", slug, "updates", parts[2])
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		serveMarkdown(w, path)
	case len(parts) == 3 && parts[1] == "aux":
		path, err := fileForEntity(s.cfg.FlowRoot, "tasks", slug, "aux", parts[2])
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		serveMarkdown(w, path)
	case len(parts) == 2 && parts[1] == "transcript":
		s.handleTaskTranscript(w, task)
	case len(parts) == 2 && parts[1] == "bridge":
		agent, err := s.agentForTask(slug)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, agent)
	case len(parts) == 2 && parts[1] == "workspace":
		writeJSON(w, workspaceTree(task.WorkDir))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleTaskTranscript(w http.ResponseWriter, task *flowdb.Task) {
	path, err := sessionJSONLPath(task)
	if err != nil {
		writeJSON(w, TranscriptResponse{Available: false, Message: err.Error(), Entries: []TranscriptEntry{}})
		return
	}
	entries, err := parseTranscriptFile(path)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, TranscriptResponse{Available: true, Entries: entries})
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	if r.URL.Path != "/api/projects" {
		http.NotFound(w, r)
		return
	}
	filter := flowdb.ProjectFilter{
		Status:          r.URL.Query().Get("status"),
		IncludeArchived: boolQuery(r.URL.Query(), "include_archived"),
		IncludeDeleted:  boolQuery(r.URL.Query(), "include_deleted"),
		DeletedOnly:     boolQuery(r.URL.Query(), "deleted"),
	}
	if filter.DeletedOnly {
		filter.IncludeArchived = true
	}
	projects, err := flowdb.ListProjects(s.cfg.DB, filter)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	views, err := BuildProjectViews(s.cfg.DB, s.cfg.FlowRoot, projects)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, views)
}

func (s *Server) handleProjectRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts, ok := routeParts(w, r, "/api/projects/")
	if !ok {
		return
	}
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	slug := parts[0]
	project, err := flowdb.GetProject(s.cfg.DB, slug)
	if err != nil {
		writeNotFoundOrError(w, err)
		return
	}
	switch {
	case len(parts) == 1:
		if !getOnly(w, r) {
			return
		}
		view, err := BuildProjectView(s.cfg.DB, s.cfg.FlowRoot, project)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, view)
	case len(parts) == 2 && parts[1] == "brief":
		if r.Method == http.MethodPut {
			s.saveProjectBrief(w, r, project)
			return
		}
		if !getOnly(w, r) {
			return
		}
		serveMarkdown(w, filepath.Join(s.cfg.FlowRoot, "projects", slug, "brief.md"))
	case len(parts) == 2 && parts[1] == "updates":
		if !getOnly(w, r) {
			return
		}
		writeJSON(w, markdownFiles(filepath.Join(s.cfg.FlowRoot, "projects", slug, "updates"), true))
	case len(parts) == 3 && parts[1] == "updates":
		if !getOnly(w, r) {
			return
		}
		path, err := fileForEntity(s.cfg.FlowRoot, "projects", slug, "updates", parts[2])
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		serveMarkdown(w, path)
	case len(parts) == 2 && parts[1] == "tasks":
		if !getOnly(w, r) {
			return
		}
		filter := flowdb.TaskFilter{
			Project:        slug,
			IncludeDeleted: boolQuery(r.URL.Query(), "include_deleted"),
			DeletedOnly:    boolQuery(r.URL.Query(), "deleted"),
			ExcludeDone:    !boolQuery(r.URL.Query(), "include_done") && !boolQuery(r.URL.Query(), "deleted"),
		}
		if filter.DeletedOnly {
			filter.IncludeArchived = true
		}
		tasks, err := flowdb.ListTasks(s.cfg.DB, filter)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		views, err := BuildTaskViews(s.cfg.DB, s.cfg.FlowRoot, tasks)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, views)
	case len(parts) == 2 && parts[1] == "playbooks":
		if !getOnly(w, r) {
			return
		}
		pbs, err := flowdb.ListPlaybooks(s.cfg.DB, flowdb.PlaybookFilter{Project: slug})
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		views, err := BuildPlaybookViews(s.cfg.DB, s.cfg.FlowRoot, pbs)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, views)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) saveProjectBrief(w http.ResponseWriter, r *http.Request, project *flowdb.Project) {
	path := filepath.Join(s.cfg.FlowRoot, "projects", project.Slug, "brief.md")
	cleanBase := filepath.Join(filepath.Clean(s.cfg.FlowRoot), "projects") + string(os.PathSeparator)
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, cleanBase) {
		writeError(w, errors.New("invalid project brief path"), http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	now := flowdb.NowISO()
	if _, err := s.cfg.DB.Exec(`UPDATE projects SET updated_at = ? WHERE slug = ?`, now, project.Slug); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "slug": project.Slug, "updated_at": now})
}

func (s *Server) handlePlaybooks(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	if r.URL.Path != "/api/playbooks" {
		http.NotFound(w, r)
		return
	}
	filter := flowdb.PlaybookFilter{
		Project:         r.URL.Query().Get("project"),
		IncludeArchived: boolQuery(r.URL.Query(), "include_archived"),
		IncludeDeleted:  boolQuery(r.URL.Query(), "include_deleted"),
		DeletedOnly:     boolQuery(r.URL.Query(), "deleted"),
	}
	if filter.DeletedOnly {
		filter.IncludeArchived = true
	}
	pbs, err := flowdb.ListPlaybooks(s.cfg.DB, filter)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	views, err := BuildPlaybookViews(s.cfg.DB, s.cfg.FlowRoot, pbs)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, views)
}

func (s *Server) handlePlaybookRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts, ok := routeParts(w, r, "/api/playbooks/")
	if !ok {
		return
	}
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	slug := parts[0]
	pb, err := flowdb.GetPlaybook(s.cfg.DB, slug)
	if err != nil {
		writeNotFoundOrError(w, err)
		return
	}
	switch {
	case len(parts) == 1:
		if !getOnly(w, r) {
			return
		}
		view, err := BuildPlaybookView(s.cfg.DB, s.cfg.FlowRoot, pb)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, view)
	case len(parts) == 2 && parts[1] == "brief":
		if r.Method == http.MethodPut {
			s.savePlaybookBrief(w, r, pb)
			return
		}
		if !getOnly(w, r) {
			return
		}
		serveMarkdown(w, filepath.Join(s.cfg.FlowRoot, "playbooks", pb.Slug, "brief.md"))
	case len(parts) == 2 && parts[1] == "updates":
		if !getOnly(w, r) {
			return
		}
		writeJSON(w, markdownFiles(filepath.Join(s.cfg.FlowRoot, "playbooks", slug, "updates"), true))
	case len(parts) == 3 && parts[1] == "updates":
		if !getOnly(w, r) {
			return
		}
		path, err := fileForEntity(s.cfg.FlowRoot, "playbooks", slug, "updates", parts[2])
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		serveMarkdown(w, path)
	case len(parts) == 3 && parts[1] == "aux":
		if !getOnly(w, r) {
			return
		}
		path, err := fileForEntity(s.cfg.FlowRoot, "playbooks", slug, "aux", parts[2])
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		serveMarkdown(w, path)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) savePlaybookBrief(w http.ResponseWriter, r *http.Request, pb *flowdb.Playbook) {
	path := filepath.Join(s.cfg.FlowRoot, "playbooks", pb.Slug, "brief.md")
	cleanBase := filepath.Join(filepath.Clean(s.cfg.FlowRoot), "playbooks") + string(os.PathSeparator)
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, cleanBase) {
		writeError(w, errors.New("invalid playbook brief path"), http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	now := flowdb.NowISO()
	if _, err := s.cfg.DB.Exec(`UPDATE playbooks SET updated_at = ? WHERE slug = ?`, now, pb.Slug); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "slug": pb.Slug, "updated_at": now})
}

func (s *Server) handleWorkdirs(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	workdirs, err := flowdb.ListWorkdirs(s.cfg.DB)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, BuildWorkdirViews(s.cfg.DB, workdirs))
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	tags, err := flowdb.ListAllTags(s.cfg.DB)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if tags == nil {
		tags = []flowdb.TagCount{}
	}
	writeJSON(w, tags)
}

func (s *Server) handleKB(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	if r.URL.Path != "/api/kb" {
		http.NotFound(w, r)
		return
	}
	views := []KBFileView{}
	for _, path := range kbFiles(s.cfg.FlowRoot) {
		views = append(views, BuildKBFileView(path))
	}
	writeJSON(w, views)
}

func (s *Server) handleKBFile(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	parts, ok := routeParts(w, r, "/api/kb/")
	if !ok {
		return
	}
	if len(parts) != 1 || !validFilename(parts[0]) {
		writeError(w, errors.New("invalid KB filename"), http.StatusBadRequest)
		return
	}
	allowed := false
	for _, path := range kbFiles(s.cfg.FlowRoot) {
		if filepath.Base(path) == parts[0] {
			allowed = true
			break
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	serveMarkdown(w, filepath.Join(s.cfg.FlowRoot, "kb", parts[0]))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	resp := SearchResponse{Query: q}
	if q == "" {
		writeJSON(w, resp)
		return
	}
	resp.Tasks = s.searchTasks(q)
	resp.Projects = s.searchProjects(q)
	resp.Playbooks = s.searchPlaybooks(q)
	resp.Updates = s.searchUpdates(q)
	writeJSON(w, resp)
}

func (s *Server) handleWebSocketPlaceholder(w http.ResponseWriter, r *http.Request) {
	writeError(w, errors.New("websocket live updates are not implemented in this build; the UI uses live fetches and refresh"), http.StatusNotImplemented)
}

func (s *Server) searchTasks(q string) []SearchResult {
	rows, err := s.cfg.DB.Query(
		`SELECT `+flowdb.TaskCols+` FROM tasks
		 WHERE archived_at IS NULL AND deleted_at IS NULL AND (slug LIKE ? OR name LIKE ?)
		 ORDER BY updated_at DESC LIMIT 8`, like(q), like(q))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		t, err := flowdb.ScanTask(rows)
		if err != nil {
			continue
		}
		out = append(out, SearchResult{Type: "task", Slug: t.Slug, Name: t.Name, URL: "/tasks/" + url.PathEscape(t.Slug), Snippet: t.Status + " / " + t.Priority})
	}
	return out
}

func (s *Server) searchProjects(q string) []SearchResult {
	rows, err := s.cfg.DB.Query(
		`SELECT `+flowdb.ProjectCols+` FROM projects
		 WHERE archived_at IS NULL AND deleted_at IS NULL AND (slug LIKE ? OR name LIKE ?)
		 ORDER BY updated_at DESC LIMIT 8`, like(q), like(q))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		p, err := flowdb.ScanProject(rows)
		if err != nil {
			continue
		}
		out = append(out, SearchResult{Type: "project", Slug: p.Slug, Name: p.Name, URL: "/projects/" + url.PathEscape(p.Slug), Snippet: p.Status + " / " + p.Priority})
	}
	return out
}

func (s *Server) searchPlaybooks(q string) []SearchResult {
	rows, err := s.cfg.DB.Query(
		`SELECT `+flowdb.PlaybookCols+` FROM playbooks
		 WHERE archived_at IS NULL AND deleted_at IS NULL AND (slug LIKE ? OR name LIKE ?)
		 ORDER BY updated_at DESC LIMIT 8`, like(q), like(q))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		pb, err := flowdb.ScanPlaybook(rows)
		if err != nil {
			continue
		}
		out = append(out, SearchResult{Type: "playbook", Slug: pb.Slug, Name: pb.Name, URL: "/playbooks/" + url.PathEscape(pb.Slug), Snippet: "playbook"})
	}
	return out
}

func (s *Server) searchUpdates(q string) []SearchResult {
	var out []SearchResult
	roots := []struct {
		kind string
		url  string
		dir  string
	}{
		{"task", "/tasks/", filepath.Join(s.cfg.FlowRoot, "tasks")},
		{"project", "/projects/", filepath.Join(s.cfg.FlowRoot, "projects")},
		{"playbook", "/playbooks/", filepath.Join(s.cfg.FlowRoot, "playbooks")},
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root.dir)
		if err != nil {
			continue
		}
		for _, entity := range entries {
			if len(out) >= 12 || !entity.IsDir() {
				continue
			}
			updates := markdownFiles(filepath.Join(root.dir, entity.Name(), "updates"), true)
			for _, update := range updates {
				if len(out) >= 12 {
					break
				}
				body, err := os.ReadFile(update.Path)
				if err != nil {
					continue
				}
				idx := strings.Index(strings.ToLower(string(body)), strings.ToLower(q))
				if idx < 0 && !strings.Contains(strings.ToLower(update.Filename), strings.ToLower(q)) {
					continue
				}
				out = append(out, SearchResult{
					Type:    root.kind + "_update",
					Slug:    entity.Name(),
					Name:    update.Filename,
					URL:     root.url + url.PathEscape(entity.Name()),
					Snippet: snippet(string(body), q),
				})
			}
		}
	}
	return out
}

func taskFilterFromQuery(q url.Values) (flowdb.TaskFilter, error) {
	filter := flowdb.TaskFilter{
		Status:          q.Get("status"),
		Project:         q.Get("project"),
		Priority:        q.Get("priority"),
		Tag:             flowdb.NormalizeTag(q.Get("tag")),
		IncludeArchived: boolQuery(q, "include_archived"),
		IncludeDeleted:  boolQuery(q, "include_deleted"),
		DeletedOnly:     boolQuery(q, "deleted"),
	}
	if filter.DeletedOnly {
		filter.IncludeArchived = true
	}
	kind := q.Get("kind")
	switch kind {
	case "", "all":
		filter.Kind = ""
	default:
		filter.Kind = kind
	}
	if q.Get("playbook") != "" {
		filter.PlaybookSlug = q.Get("playbook")
	}
	if filter.Status == "" && !boolQuery(q, "include_done") && !filter.DeletedOnly {
		filter.ExcludeDone = true
	}
	if since := q.Get("since"); since != "" && since != "all" {
		t, err := parseSince(since, time.Now())
		if err != nil {
			return filter, err
		}
		filter.Since = t.Format(time.RFC3339)
	}
	return filter, nil
}

func serveMarkdown(w http.ResponseWriter, path string) {
	body, err := readMarkdown(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, err, http.StatusNotFound)
			return
		}
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func routeParts(w http.ResponseWriter, r *http.Request, prefix string) ([]string, bool) {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == r.URL.Path {
		http.NotFound(w, r)
		return nil, false
	}
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return nil, true
	}
	raw := strings.Split(rest, "/")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return nil, false
		}
		parts = append(parts, decoded)
	}
	return parts, true
}

func getOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeNotFoundOrError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, err, http.StatusNotFound)
		return
	}
	writeError(w, err, http.StatusInternalServerError)
}

func boolQuery(q url.Values, key string) bool {
	v := strings.ToLower(strings.TrimSpace(q.Get(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func like(q string) string {
	return "%" + q + "%"
}

func snippet(body, q string) string {
	clean := strings.Join(strings.Fields(body), " ")
	lower := strings.ToLower(clean)
	idx := strings.Index(lower, strings.ToLower(q))
	if idx < 0 {
		if len(clean) > 160 {
			return clean[:160] + "..."
		}
		return clean
	}
	start := idx - 60
	if start < 0 {
		start = 0
	}
	end := idx + len(q) + 100
	if end > len(clean) {
		end = len(clean)
	}
	s := clean[start:end]
	if start > 0 {
		s = "..." + s
	}
	if end < len(clean) {
		s += "..."
	}
	return s
}

func sortTasksByUpdatedDesc(tasks []TaskView) {
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].UpdatedAt > tasks[j].UpdatedAt })
}
