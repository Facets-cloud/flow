package server

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"flow/internal/monitor"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTaskAPIUsesFlowDataAndFiles(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/build-ui", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var task TaskView
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Slug != "build-ui" || task.Name != "Build dashboard UI" {
		t.Fatalf("unexpected task: %+v", task)
	}
	if task.ProjectSlug == nil || *task.ProjectSlug != "flow" {
		t.Fatalf("project slug = %#v", task.ProjectSlug)
	}
	if len(task.Tags) != 1 || task.Tags[0] != "ui" {
		t.Fatalf("tags = %#v", task.Tags)
	}
	if len(task.Updates) != 1 || task.Updates[0].Filename != "2026-05-12-progress.md" {
		t.Fatalf("updates = %#v", task.Updates)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/tasks/build-ui/brief", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("brief status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "Real task brief") {
		t.Fatalf("brief body = %q", rec.Body.String())
	}
}

func TestNewWiresGitHubListenerWhenDBAvailable(t *testing.T) {
	root, db := testRootDB(t)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	if srv.githubListener == nil {
		t.Fatal("github listener was not wired")
	}
}

func TestNewWiresInboxMonitorManager(t *testing.T) {
	root, db := testRootDB(t)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	if srv.inboxMonitors == nil {
		t.Fatal("inbox monitor manager was not wired")
	}
}

func TestTerminalPasteInputWrapsPromptForBracketedPaste(t *testing.T) {
	got := terminalPasteInput("flow monitor wake")
	want := "\x1b[200~flow monitor wake\x1b[201~\r"
	if got != want {
		t.Fatalf("terminalPasteInput() = %q, want %q", got, want)
	}
}

func TestFormatInboxWakePromptIncludesSourceAndURL(t *testing.T) {
	entries := []monitor.InboxEntry{{
		Event: monitor.InboundEvent{
			Kind:        "pr_review_comment",
			ChannelType: "github",
			Text:        "please update the migration",
			URL:         "https://github.com/acme/app/pull/7#discussion_r1",
		},
		Meta: monitor.InboxEventMeta{Source: "github", Actionable: true},
	}}

	prompt := formatInboxWakePrompt("review-task", entries)
	if !strings.Contains(prompt, "review-task") {
		t.Fatalf("prompt missing slug: %s", prompt)
	}
	if !strings.Contains(prompt, "github pr_review_comment") {
		t.Fatalf("prompt missing source/kind: %s", prompt)
	}
	if !strings.Contains(prompt, "https://github.com/acme/app/pull/7#discussion_r1") {
		t.Fatalf("prompt missing URL: %s", prompt)
	}
	if !strings.Contains(prompt, "Read the new task inbox entries") {
		t.Fatalf("prompt missing inbox instruction: %s", prompt)
	}
}

func TestInboxMonitorManagerStartIsIdempotent(t *testing.T) {
	t.Setenv("FLOW_ROOT", t.TempDir())
	manager := newInboxMonitorManager(inboxWakeTarget{})
	manager.start("review")
	manager.start("review")
	defer manager.stop("review")

	manager.mu.Lock()
	count := len(manager.cancel)
	manager.mu.Unlock()
	if count != 1 {
		t.Fatalf("monitor count = %d, want 1", count)
	}
}

func TestInboxMonitorManagerStopRemovesMonitor(t *testing.T) {
	t.Setenv("FLOW_ROOT", t.TempDir())
	manager := newInboxMonitorManager(inboxWakeTarget{})
	manager.start("review")
	manager.stop("review")

	manager.mu.Lock()
	count := len(manager.cancel)
	manager.mu.Unlock()
	if count != 0 {
		t.Fatalf("monitor count = %d, want 0", count)
	}
}

func TestSearchReadsUpdateBodies(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=current-data-marker", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res SearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Updates) != 1 {
		t.Fatalf("updates = %#v", res.Updates)
	}
	if res.Updates[0].Slug != "build-ui" {
		t.Fatalf("update result = %+v", res.Updates[0])
	}
}

func TestSearchReadsBriefBodies(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=real-task-brief", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res SearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 1 || res.Tasks[0].Slug != "build-ui" || res.Tasks[0].Scope != "brief" {
		t.Fatalf("task brief results = %#v", res.Tasks)
	}
}

func TestSearchTranscriptsRequireOptInScope(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	transcriptPath := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user","message":{"content":"server-transcript-marker"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET session_path = ? WHERE slug = 'build-ui'`, transcriptPath); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=server-transcript-marker", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res SearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Transcripts) != 0 {
		t.Fatalf("default search included transcripts: %#v", res.Transcripts)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/search?q=server-transcript-marker&in=transcripts", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("transcript status = %d, body = %s", rec.Code, rec.Body.String())
	}
	res = SearchResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Transcripts) != 1 || res.Transcripts[0].Slug != "build-ui" {
		t.Fatalf("transcript results = %#v", res.Transcripts)
	}
}

func TestSearchReadsMemoryBodies(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	if err := os.WriteFile(filepath.Join(root, "kb", "user.md"), []byte("server-flow-memory-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, ".codex", "memories", "raw_memories.md"), "server-codex-memory-marker\n")

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=server-flow-memory-marker", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Memories []SearchResult `json:"memories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Memories) != 1 || res.Memories[0].Slug != "flow-kb-user" || res.Memories[0].Scope != "memory" {
		t.Fatalf("memory results = %#v", res.Memories)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/search?q=server-codex-memory-marker&in=memories", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("codex status = %d, body = %s", rec.Code, rec.Body.String())
	}
	res = struct {
		Memories []SearchResult `json:"memories"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Memories) != 1 || res.Memories[0].Slug != "codex-memory-raw-memories" {
		t.Fatalf("codex memory results = %#v", res.Memories)
	}
}

func TestKBFileSaveRejectsStaleMTime(t *testing.T) {
	root, db := testRootDB(t)
	path := filepath.Join(root, "kb", "user.md")
	baseTime := time.Now().Add(-time.Hour).Round(0)
	newerTime := baseTime.Add(time.Second)
	if err := os.WriteFile(path, []byte("loaded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, baseTime, baseTime); err != nil {
		t.Fatal(err)
	}
	loadedMTime := baseTime.Format(time.RFC3339Nano)

	if err := os.WriteFile(path, []byte("newer on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, newerTime, newerTime); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/kb/user.md?mtime="+url.QueryEscape(loadedMTime),
		strings.NewReader("stale browser draft\n"),
	)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "newer on disk\n" {
		t.Fatalf("file was overwritten by stale save: %q", body)
	}
}

func TestMemoryWriteRejectsStaleMTime(t *testing.T) {
	root, db := testRootDB(t)
	if err := flowdb.UpsertWorkdir(db, root, "flow", "", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "AGENTS.md")
	baseTime := time.Now().Add(-time.Hour).Round(0)
	newerTime := baseTime.Add(time.Second)
	if err := os.WriteFile(path, []byte("loaded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, baseTime, baseTime); err != nil {
		t.Fatal(err)
	}
	loadedMTime := baseTime.Format(time.RFC3339Nano)

	if err := os.WriteFile(path, []byte("newer on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, newerTime, newerTime); err != nil {
		t.Fatal(err)
	}

	payload := fmt.Sprintf(`{"path":%q,"text":"stale browser draft\n","mtime":%q}`, path, loadedMTime)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/memory", strings.NewReader(payload))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "newer on disk\n" {
		t.Fatalf("file was overwritten by stale save: %q", body)
	}
}

func TestUIDataUsesFlowRecords(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ui-data.js", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"window.FLOW_BOOTSTRAP",
		"build-ui",
		"Build dashboard UI",
		"Flow project",
		"ui",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ui bootstrap missing %q in %s", want, body)
		}
	}
}

func TestUIDataJSONEndpointUsesFlowRecords(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ui-data", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q", got)
	}
	var data uiData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Backlog) != 1 {
		t.Fatalf("backlog = %#v", data.Backlog)
	}
	if data.Backlog[0].Slug != "build-ui" {
		t.Fatalf("backlog task = %+v", data.Backlog[0])
	}
}

func TestUIDataIncludesTaskDependencies(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	now := "2026-05-12T10:05:00+05:30"
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, kind, parent_slug, priority, work_dir, created_at, updated_at)
		 VALUES ('polish-ui', 'Polish dashboard UI', 'flow', 'backlog', 'regular', 'build-ui', 'medium', ?, ?, ?)`,
		root, now, now,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	data, err := srv.buildUIData()
	if err != nil {
		t.Fatal(err)
	}

	bySlug := map[string]uiBacklogTask{}
	for _, task := range data.Backlog {
		bySlug[task.Slug] = task
	}
	child := bySlug["polish-ui"]
	if child.Parent == nil || child.Parent.Slug != "build-ui" || child.Parent.Status != "backlog" {
		t.Fatalf("child dependency parent = %+v", child.Parent)
	}
	parent := bySlug["build-ui"]
	if len(parent.Children) != 1 || parent.Children[0].Slug != "polish-ui" || parent.Children[0].Status != "backlog" {
		t.Fatalf("parent dependency children = %+v", parent.Children)
	}
}

func TestUIDataTrashContainsDeletedRecords(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	now := "2026-05-15T10:00:00+05:30"
	if _, err := db.Exec(`UPDATE tasks SET deleted_at = ? WHERE slug = 'build-ui'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE projects SET deleted_at = ? WHERE slug = 'flow'`, now); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "old-playbook", Name: "Old Playbook", WorkDir: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE playbooks SET deleted_at = ? WHERE slug = 'old-playbook'`, now); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	data, err := srv.buildUIData()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Backlog) != 0 {
		t.Fatalf("deleted task leaked into backlog: %+v", data.Backlog)
	}
	if data.Trash.Total != 3 {
		t.Fatalf("trash total = %d, trash = %+v", data.Trash.Total, data.Trash)
	}
	if len(data.Trash.Tasks) != 1 || data.Trash.Tasks[0].Slug != "build-ui" || data.Trash.Tasks[0].Kind != "task" {
		t.Fatalf("trash tasks = %+v", data.Trash.Tasks)
	}
	if len(data.Trash.Projects) != 1 || data.Trash.Projects[0].Slug != "flow" || data.Trash.Projects[0].Kind != "project" {
		t.Fatalf("trash projects = %+v", data.Trash.Projects)
	}
	if len(data.Trash.Playbooks) != 1 || data.Trash.Playbooks[0].Slug != "old-playbook" || data.Trash.Playbooks[0].Kind != "playbook" {
		t.Fatalf("trash playbooks = %+v", data.Trash.Playbooks)
	}
}

func TestFSEntriesListsRealDirectories(t *testing.T) {
	root, db := testRootDB(t)
	parent := filepath.Join(root, "codebases")
	for _, dir := range []string{"agent-factory", "praxis-cli", "otaku"} {
		if err := os.MkdirAll(filepath.Join(parent, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(parent, "agent-factory", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/entries?path="+url.QueryEscape(parent), nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var view FSEntriesView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	seen := map[string]FSEntryView{}
	for _, entry := range view.Entries {
		seen[entry.Name] = entry
	}
	for _, name := range []string{"agent-factory", "praxis-cli", "otaku", "README.md"} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("missing %s from entries: %+v", name, view.Entries)
		}
	}
	if !seen["agent-factory"].IsDir || !seen["agent-factory"].IsGit {
		t.Fatalf("agent-factory entry = %+v", seen["agent-factory"])
	}
	if seen["README.md"].IsDir {
		t.Fatalf("README.md should be a file: %+v", seen["README.md"])
	}
}

func TestUIEventsStreamSendsInitialSnapshot(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := httptest.NewServer(New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler())
	defer srv.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content-type = %q", got)
	}
	reader := bufio.NewReader(resp.Body)
	eventLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	dataLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(eventLine) != "event: ui-data" {
		t.Fatalf("event line = %q", eventLine)
	}
	if !strings.Contains(dataLine, "build-ui") || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("data line = %q", dataLine)
	}
}

func TestToolCallActivitySeriesBucketsByMinute(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	ts := func(minutesAgo int) string {
		return now.Add(-time.Duration(minutesAgo) * time.Minute).Format(time.RFC3339)
	}
	transcript := []uiTranscript{
		{Type: "tool_use", Tool: "Bash", Time: ts(0)},
		{Type: "tool_use", Tool: "Read", Time: ts(0)},
		{Type: "tool_use", Tool: "Edit", Time: ts(5)},
		{Type: "assistant", Text: "counted", Time: ts(2)},
		{Type: "tool_use", Tool: "Old", Time: ts(120)},
		{Type: "tool_use", Tool: "Empty"},
	}

	series := toolCallActivitySeries(transcript, now)
	if len(series) != 60 {
		t.Fatalf("len = %d, want 60", len(series))
	}
	// Every timestamped entry within the last hour counts (any Type), so the
	// activity strip is meaningful for Claude and Codex alike.
	if series[59] != 2 {
		t.Fatalf("current minute bucket = %d, want 2 (two entries at minute 0)", series[59])
	}
	if series[57] != 1 {
		t.Fatalf("minute -2 bucket = %d, want 1 (assistant turn 2 min ago)", series[57])
	}
	if series[54] != 1 {
		t.Fatalf("minute -5 bucket = %d, want 1 (one entry 5 min ago)", series[54])
	}
	for i, v := range series {
		if i == 54 || i == 57 || i == 59 {
			continue
		}
		if v != 0 {
			t.Fatalf("bucket %d = %d, want 0 (untimestamped and >60min entries excluded)", i, v)
		}
	}
}

func TestToolCallActivitySeriesEmpty(t *testing.T) {
	series := toolCallActivitySeries(nil, time.Now())
	if len(series) != 60 {
		t.Fatalf("len = %d, want 60", len(series))
	}
	for _, v := range series {
		if v != 0 {
			t.Fatalf("empty transcript should yield all-zero series, got non-zero")
		}
	}
}

// TestToolCallActivitySeriesCodexShape exercises the function with the
// transcript shape produced by parseCodexTranscriptLine. Every timestamped
// entry counts toward per-minute activity (calls, results, and turns), so
// Codex sessions — which rarely surface discrete tool_use events — still
// render a meaningful activity strip.
func TestToolCallActivitySeriesCodexShape(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	ts := func(minutesAgo int) string {
		return now.Add(-time.Duration(minutesAgo) * time.Minute).Format(time.RFC3339)
	}
	// Codex flow: function_call → Tool: name, local_shell_call → Tool: local_shell.
	transcript := []uiTranscript{
		{Type: "tool_use", Tool: "apply_patch", Input: `{"path":"foo"}`, Time: ts(0)},
		{Type: "tool_use", Tool: "local_shell", Input: "ls -la", Time: ts(3)},
		{Type: "tool_result", Tool: "result", Summary: "ok", Time: ts(3)},
		{Type: "user", Text: "counted", Time: ts(1)},
	}
	series := toolCallActivitySeries(transcript, now)
	if series[59] != 1 {
		t.Fatalf("current minute bucket = %d, want 1 (codex function_call)", series[59])
	}
	if series[58] != 1 {
		t.Fatalf("minute -1 bucket = %d, want 1 (user turn)", series[58])
	}
	if series[56] != 2 {
		t.Fatalf("minute -3 bucket = %d, want 2 (local_shell call + its result)", series[56])
	}
}

func TestActivityHeatmapUsesTaskAndUpdateDates(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.Local)
	days := buildActivityHeatmap([]TaskView{{
		Slug:      "build-ui",
		Status:    "in-progress",
		Live:      true, // a live session counts as active "now" (2026-05-15)
		CreatedAt: "2026-05-12T10:00:00+05:30",
		UpdatedAt: "2026-05-13T11:00:00+05:30",
		Updates: []FileRef{{
			Filename: "2026-05-14-progress.md",
			MTime:    "2026-05-15T09:00:00+05:30",
		}},
	}}, now)
	counts := map[string]int{}
	for _, day := range days {
		counts[day.Date] = day.Count
	}
	for _, date := range []string{"2026-05-12", "2026-05-13", "2026-05-14", "2026-05-15"} {
		if counts[date] == 0 {
			t.Fatalf("expected activity on %s; counts=%#v", date, counts)
		}
	}
	if len(days) != 84 {
		t.Fatalf("len = %d", len(days))
	}
}

func TestAttachActionOpensBrowserTerminalBridge(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "11111111-1111-4111-8111-111111111111"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	oldPS := psRunner
	psRunner = func() ([]byte, error) {
		return []byte("123 claude --session-id " + sessionID + "\n"), nil
	}
	t.Cleanup(func() { psRunner = oldPS })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	resp, status := srv.runAction(actionRequest{Kind: "attach", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.OK || !resp.Bridge || resp.AlreadyLive {
		t.Fatalf("expected browser terminal bridge response, got %+v", resp)
	}
	if resp.Agent == nil || resp.Agent.Slug != "build-ui" || resp.Agent.Status != "running" {
		t.Fatalf("agent = %+v", resp.Agent)
	}
}

func TestPauseActionStopsBrowserTerminalButKeepsTaskIdle(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "22222222-2222-4222-8222-222222222222"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	oldPS := psRunner
	psRunner = func() ([]byte, error) { return []byte{}, nil }
	t.Cleanup(func() { psRunner = oldPS })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	browserSess := &terminalSession{
		slug:      "build-ui",
		sessionID: sessionID,
		done:      make(chan struct{}),
		clients:   map[*terminalClient]struct{}{},
	}
	srv.terminals.mu.Lock()
	srv.terminals.sessions["build-ui"] = browserSess
	srv.terminals.mu.Unlock()

	resp, status := srv.runAction(actionRequest{Kind: "pause", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.OK || resp.Agent == nil || resp.Agent.Status != "idle" {
		t.Fatalf("expected paused idle agent response, got %+v", resp)
	}
	srv.terminals.mu.Lock()
	_, stillRunning := srv.terminals.sessions["build-ui"]
	srv.terminals.mu.Unlock()
	if stillRunning {
		t.Fatal("pause should stop the browser terminal session")
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" {
		t.Fatalf("pause demoted task status = %q, want in-progress", task.Status)
	}
	if !task.SessionID.Valid || task.SessionID.String != sessionID {
		t.Fatalf("pause should preserve session_id, got %#v", task.SessionID)
	}
}

func TestClearWaitingActionClearsWaitingOnAndReturnsAgent(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "22222222-2222-4222-8222-222222222222"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ?, waiting_on = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30", "user review",
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	resp, status := srv.runAction(actionRequest{Kind: "clear-waiting", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.OK || resp.Agent == nil {
		t.Fatalf("expected agent response, got %+v", resp)
	}
	if resp.Agent.WaitingFor != nil {
		t.Fatalf("waiting_for still present: %+v", resp.Agent.WaitingFor)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.WaitingOn.Valid {
		t.Fatalf("waiting_on still set: %#v", task.WaitingOn)
	}
}

func TestPrepareTerminalLaunchAllocatesBrowserSession(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if !launch.Created || launch.Slug != "build-ui" || launch.SessionID == "" || launch.WorkDir != root {
		t.Fatalf("launch = %+v", launch)
	}
	if len(launch.Args) != 5 || launch.Args[0] != "--session-id" || launch.Args[1] != launch.SessionID {
		t.Fatalf("args = %#v", launch.Args)
	}
	if launch.Args[2] != "--permission-mode" || launch.Args[3] != "auto" {
		t.Fatalf("default permission args = %#v", launch.Args)
	}
	if !strings.Contains(launch.Args[4], "flow task build-ui") {
		t.Fatalf("bootstrap prompt = %q", launch.Args[4])
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" || !task.SessionID.Valid || task.SessionID.String != launch.SessionID || !task.SessionStarted.Valid {
		t.Fatalf("task after launch = %+v", task)
	}
}

func TestPrepareTerminalLaunchUsesTaskWorktree(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	repo := initGitRepoForServerTest(t)
	if _, err := db.Exec(`UPDATE tasks SET work_dir = ? WHERE slug = 'build-ui'`, repo); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	wantWT := filepath.Join(repo, ".claude", "worktrees", "build-ui")
	if launch.WorkDir != wantWT {
		t.Fatalf("launch workdir = %q, want %q", launch.WorkDir, wantWT)
	}
	if _, err := os.Stat(wantWT); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if !task.WorktreePath.Valid || task.WorktreePath.String != wantWT {
		t.Fatalf("worktree_path = %#v, want %q", task.WorktreePath, wantWT)
	}
}

func TestPrepareTerminalLaunchRefusesBlockedTask(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, kind, priority, work_dir, created_at, updated_at)
		 VALUES ('parent-task', 'Parent task', 'flow', 'backlog', 'regular', 'high', ?, ?, ?)`,
		root, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET parent_slug = ? WHERE slug = ?`, "parent-task", "build-ui"); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	if _, err := srv.prepareTerminalLaunch("build-ui"); err == nil || !strings.Contains(err.Error(), `depends on "parent-task"`) {
		t.Fatalf("prepareTerminalLaunch err = %v, want dependency blocker", err)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionStarted.Valid {
		t.Fatalf("blocked task should not be mutated: %+v", task)
	}
}

func TestSpawnActionRefusesBlockedTaskBeforeProviderChoice(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	if _, err := db.Exec(`UPDATE tasks SET waiting_on = ? WHERE slug = ?`, "external approval", "build-ui"); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	resp, status := srv.runAction(actionRequest{
		Kind:     "spawn",
		Slug:     "build-ui",
		Provider: "codex",
	})
	if status != http.StatusBadRequest || resp.OK || !strings.Contains(resp.Message, "waiting on external approval") {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider == "codex" || task.Status != "backlog" || task.SessionID.Valid {
		t.Fatalf("blocked spawn should not mutate provider/session: %+v", task)
	}
}

// stubProviderBins puts no-op executables named after each provider on PATH so
// detectCapabilities (which calls exec.LookPath fresh, uncached) reports them
// available. Host-independent: the test passes whether or not the real claude /
// codex CLIs are installed.
func stubProviderBins(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestUpdateProviderActionSwitchesBacklogThenLocks(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root) // build-ui: backlog, no session, provider NULL
	stubProviderBins(t, "claude", "codex")
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	// An unknown provider is rejected up front, before any mutation.
	if resp, status := srv.runAction(actionRequest{Kind: "update-provider", Slug: "build-ui", Provider: "gpt"}); status != http.StatusBadRequest || resp.OK {
		t.Fatalf("invalid provider: status=%d resp=%+v", status, resp)
	}

	// A not-yet-started backlog task can switch to codex; the choice is sticky.
	resp, status := srv.runAction(actionRequest{Kind: "update-provider", Slug: "build-ui", Provider: "codex"})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("switch to codex: status=%d resp=%+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != "codex" {
		t.Fatalf("session_provider = %q, want codex", task.SessionProvider)
	}

	// Once a session has started, the provider is locked.
	if _, err := db.Exec(`UPDATE tasks SET session_started = ? WHERE slug = ?`, flowdb.NowISO(), "build-ui"); err != nil {
		t.Fatal(err)
	}
	resp, status = srv.runAction(actionRequest{Kind: "update-provider", Slug: "build-ui", Provider: "claude"})
	if status != http.StatusBadRequest || resp.OK || !strings.Contains(resp.Message, "before a session starts") {
		t.Fatalf("locked switch: status=%d resp=%+v", status, resp)
	}
	if task, err = flowdb.GetTask(db, "build-ui"); err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != "codex" {
		t.Fatalf("provider after rejected switch = %q, want codex (unchanged)", task.SessionProvider)
	}
}

func TestWorkdirActionsAddRenameRemove(t *testing.T) {
	root, db := testRootDB(t)
	workDir := t.TempDir()
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	resp, status := srv.runAction(actionRequest{
		Kind:        "workdir-add",
		Path:        workDir,
		Name:        "Main repo",
		Description: "Primary development checkout",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("add status = %d, resp = %+v", status, resp)
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		t.Fatal(err)
	}
	wd, err := flowdb.GetWorkdir(db, abs)
	if err != nil {
		t.Fatal(err)
	}
	if wd.Name.String != "Main repo" || wd.Description.String != "Primary development checkout" {
		t.Fatalf("workdir after add = %+v", wd)
	}

	resp, status = srv.runAction(actionRequest{
		Kind:        "workdir-rename",
		Path:        workDir,
		Name:        "Renamed repo",
		Description: "Renamed description",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("rename status = %d, resp = %+v", status, resp)
	}
	wd, err = flowdb.GetWorkdir(db, abs)
	if err != nil {
		t.Fatal(err)
	}
	if wd.Name.String != "Renamed repo" || wd.Description.String != "Renamed description" {
		t.Fatalf("workdir after rename = %+v", wd)
	}

	resp, status = srv.runAction(actionRequest{Kind: "workdir-remove", Path: workDir})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("remove status = %d, resp = %+v", status, resp)
	}
	if _, err := flowdb.GetWorkdir(db, abs); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("workdir after remove err = %v, want sql.ErrNoRows", err)
	}
}

func TestDestroyOnlyDeletesTrashItems(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	resp, status := srv.runAction(actionRequest{Kind: "destroy", EntityKind: "task", Slug: "build-ui"})
	if status != http.StatusConflict || resp.OK || !strings.Contains(resp.Message, "must be in trash") {
		t.Fatalf("active destroy status = %d, resp = %+v", status, resp)
	}

	now := flowdb.NowISO()
	if _, err := db.Exec(`UPDATE tasks SET deleted_at = ? WHERE slug = 'build-ui'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO search_docs
			(doc_key, scope, entity_type, entity_slug, title, source_path, source_mtime, content, updated_at)
		 VALUES
			('task/build-ui/brief', 'brief', 'task', 'build-ui', 'Build dashboard UI', '/tmp/brief.md', ?, 'body', ?)`,
		now, now,
	); err != nil {
		t.Fatal(err)
	}
	// The on-disk task directory must be reclaimed on destroy, not orphaned.
	taskDir := filepath.Join(root, "tasks", "build-ui")
	if err := os.MkdirAll(filepath.Join(taskDir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "brief.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, status = srv.runAction(actionRequest{Kind: "destroy", EntityKind: "task", Slug: "build-ui"})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("trash destroy status = %d, resp = %+v", status, resp)
	}
	if _, err := flowdb.GetTask(db, "build-ui"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("task after destroy err = %v, want sql.ErrNoRows", err)
	}
	if _, err := os.Stat(taskDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("task dir after destroy: stat err = %v, want not-exist (dir should be removed)", err)
	}
	var docs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_docs WHERE entity_type = 'task' AND entity_slug = 'build-ui'`).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if docs != 0 {
		t.Fatalf("search docs after destroy = %d, want 0", docs)
	}
}

func TestDestroyProjectWithRefsIsBlocked(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	if _, err := db.Exec(`UPDATE projects SET deleted_at = ? WHERE slug = 'flow'`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	resp, status := srv.runAction(actionRequest{Kind: "destroy", EntityKind: "project", Slug: "flow"})
	if status != http.StatusConflict || resp.OK || !strings.Contains(resp.Message, "still has") {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
}

func TestPrepareTerminalLaunchAppliesPermissionMode(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	if _, err := db.Exec(`UPDATE tasks SET permission_mode = 'bypass' WHERE slug = 'build-ui'`); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(launch.Args, "--dangerously-skip-permissions") {
		t.Fatalf("bypass launch args = %#v", launch.Args)
	}
}

func TestPrepareTerminalLaunchCodexStartsPendingCapture(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	workDir := t.TempDir()
	if _, err := db.Exec(`UPDATE tasks SET session_provider = 'codex', work_dir = ? WHERE slug = 'build-ui'`, workDir); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if !launch.Created || launch.Provider != "codex" || launch.SessionID != "" || !launch.NeedsCapture {
		t.Fatalf("codex launch = %+v", launch)
	}
	wantPrefix := []string{"--no-alt-screen", "-C", workDir, "--add-dir", root, "--ask-for-approval", "never", "--sandbox", "workspace-write"}
	if len(launch.Args) < len(wantPrefix)+1 {
		t.Fatalf("codex args too short: %#v", launch.Args)
	}
	for i, want := range wantPrefix {
		if launch.Args[i] != want {
			t.Fatalf("codex args[%d] = %q, want %q; args=%#v", i, launch.Args[i], want, launch.Args)
		}
	}
	if !strings.Contains(launch.Args[len(launch.Args)-1], "flow task build-ui") {
		t.Fatalf("bootstrap prompt = %q", launch.Args[len(launch.Args)-1])
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" || task.SessionProvider != "codex" || task.SessionID.Valid || !task.SessionStarted.Valid {
		t.Fatalf("task after codex launch = %+v", task)
	}
}

func TestSlackTaskOpenerPreservesCodexProvider(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	if _, err := db.Exec(`UPDATE tasks SET session_provider = 'codex' WHERE slug = 'build-ui'`); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		path := filepath.Join(binDir, name)
		body := "#!/bin/sh\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	t.Cleanup(func() { srv.terminals.stop("build-ui") })
	opener := &slackTaskOpener{server: srv}
	if err := opener.OpenInUI("build-ui"); err != nil {
		t.Fatal(err)
	}

	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != "codex" || task.SessionID.Valid {
		t.Fatalf("slack opener should preserve pending codex launch, got %+v", task)
	}
}

func TestPrepareTerminalLaunchResumesCodexSession(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	workDir := t.TempDir()
	sessionID := "55555555-5555-4555-8555-555555555555"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_provider = 'codex', session_id = ?, session_started = ?, work_dir = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30", workDir,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if launch.Created || launch.Provider != "codex" || launch.SessionID != sessionID || launch.NeedsCapture {
		t.Fatalf("codex resume launch = %+v", launch)
	}
	want := []string{"resume", "--include-non-interactive", "--no-alt-screen", "-C", workDir, "--add-dir", root, "--ask-for-approval", "never", "--sandbox", "workspace-write", "-c", "sandbox_workspace_write.network_access=true", sessionID}
	if strings.Join(launch.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("codex resume args = %#v, want %#v", launch.Args, want)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionLastResumed.Valid {
		t.Fatalf("session_last_resumed not recorded: %+v", task)
	}
}

func TestPrepareTerminalLaunchCodexAutoUsesSandboxedNoApproval(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	if _, err := db.Exec(`UPDATE tasks SET session_provider = 'codex', permission_mode = 'auto' WHERE slug = 'build-ui'`); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--ask-for-approval", "never", "--sandbox", "workspace-write"} {
		if !containsString(launch.Args, want) {
			t.Fatalf("codex auto args missing %q: %#v", want, launch.Args)
		}
	}
	if containsString(launch.Args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("codex auto should remain sandboxed, got %#v", launch.Args)
	}
}

func TestTerminalAddClientReplaysSanitizedScrollback(t *testing.T) {
	sess := &terminalSession{
		provider:   "codex",
		sessionID:  "55555555-5555-4555-8555-555555555555",
		clients:    map[*terminalClient]struct{}{},
		scrollback: []byte("\x1b[?1049hmid-frame-output\x1b[?1049l"),
	}
	client := &terminalClient{send: make(chan terminalWSMessage, 4), done: make(chan struct{})}
	sess.addClient(client, true)

	first := <-client.send
	if first.Type != "status" || !strings.Contains(first.Message, "connected to codex session") {
		t.Fatalf("first message = %+v", first)
	}
	second := <-client.send
	if second.Type != "output" || second.Data != "mid-frame-output" {
		t.Fatalf("replay message = %+v", second)
	}
}

func TestTerminalEnvForcesBrowserFriendlyClaudeRenderer(t *testing.T) {
	t.Setenv("CLAUDE_CODE_NO_FLICKER", "1")
	t.Setenv("CLAUDE_CODE_DISABLE_MOUSE", "0")

	env := terminalEnv("/tmp/flow-root", "/tmp/flow-bin/flow")
	if got := envValue(env, "CLAUDE_CODE_NO_FLICKER"); got != "0" {
		t.Fatalf("CLAUDE_CODE_NO_FLICKER = %q, want 0", got)
	}
	if got := envValue(env, "CLAUDE_CODE_DISABLE_MOUSE"); got != "1" {
		t.Fatalf("CLAUDE_CODE_DISABLE_MOUSE = %q, want 1", got)
	}
	if got := envValue(env, "FLOW_ROOT"); got != "/tmp/flow-root" {
		t.Fatalf("FLOW_ROOT = %q, want /tmp/flow-root", got)
	}
	if got := envValue(env, "PATH"); !strings.HasPrefix(got, "/tmp/flow-bin"+string(os.PathListSeparator)) {
		t.Fatalf("PATH should prefer UI flow binary dir, got %q", got)
	}
}

func TestCreateFlowPersistsPermissionMode(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:           "create-flow",
		Slug:           "ui-permission",
		Name:           "UI Permission",
		WorkDir:        root,
		Priority:       "medium",
		Prompt:         "Test permission mode.",
		PermissionMode: "auto",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "ui-permission")
	if err != nil {
		t.Fatal(err)
	}
	if task.PermissionMode != "auto" {
		t.Fatalf("permission mode = %q, want auto", task.PermissionMode)
	}
}

func TestCreateFlowDefaultsPermissionModeAuto(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:     "create-flow",
		Slug:     "ui-auto-default",
		Name:     "UI Auto Default",
		WorkDir:  root,
		Priority: "medium",
		Prompt:   "Use implicit permission mode.",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "ui-auto-default")
	if err != nil {
		t.Fatal(err)
	}
	if task.PermissionMode != "auto" {
		t.Fatalf("permission mode = %q, want auto", task.PermissionMode)
	}
}

func TestCreateFlowPreservesExplicitDefaultPermissionMode(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:           "create-flow",
		Slug:           "ui-explicit-default",
		Name:           "UI Explicit Default",
		WorkDir:        root,
		Priority:       "medium",
		Prompt:         "Use explicit default permission mode.",
		PermissionMode: "default",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "ui-explicit-default")
	if err != nil {
		t.Fatal(err)
	}
	if task.PermissionMode != "default" {
		t.Fatalf("permission mode = %q, want default", task.PermissionMode)
	}
}

func TestCreateFlowPersistsCodexProvider(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:     "create-flow",
		Slug:     "ui-codex",
		Name:     "UI Codex",
		WorkDir:  root,
		Priority: "medium",
		Provider: "codex",
		Prompt:   "Use Codex.",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "ui-codex")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != "codex" {
		t.Fatalf("session provider = %q, want codex", task.SessionProvider)
	}
}

func TestCreateFlowMultipartImagesStoresAttachmentsInBrief(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"kind":            "create-flow",
		"slug":            "image-task",
		"name":            "Image Task",
		"work_dir":        root,
		"priority":        "medium",
		"permission_mode": "auto",
		"prompt":          "Use the attached screenshot.",
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("images", "login callback.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/actions", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp actionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Agent == nil || resp.Agent.Slug != "image-task" {
		t.Fatalf("response = %+v", resp)
	}

	attachmentsDir := filepath.Join(root, "tasks", "image-task", "attachments")
	entries, err := os.ReadDir(attachmentsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Name(), "login-callback.png") {
		t.Fatalf("attachments = %#v, want sanitized image attachment", entries)
	}
	attachmentPath := filepath.Join(attachmentsDir, entries[0].Name())
	saved, err := os.ReadFile(attachmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "png bytes" {
		t.Fatalf("saved body = %q", saved)
	}
	brief, err := os.ReadFile(filepath.Join(root, "tasks", "image-task", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "Use the attached screenshot.") ||
		!strings.Contains(string(brief), "Attached images:") ||
		!strings.Contains(string(brief), attachmentPath) {
		t.Fatalf("brief = %q", brief)
	}
}

func TestCreateFlowReactivatesDeletedArchivedTask(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	insertProjectTask(t, db, root)
	if _, err := db.Exec(
		`UPDATE tasks SET
			status = 'done',
			session_provider = 'claude',
			session_id = '11111111-1111-4111-8111-111111111111',
			session_started = '2026-05-12T10:01:00+05:30',
			archived_at = '2026-05-13T10:01:00+05:30',
			deleted_at = '2026-05-14T10:01:00+05:30'
		 WHERE slug = 'build-ui'`,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:           "create-flow",
		Slug:           "build-ui",
		Name:           "Build UI again",
		Project:        "flow",
		WorkDir:        root,
		Priority:       "high",
		Provider:       "codex",
		PermissionMode: "auto",
		Prompt:         "Re-run this task with Codex.",
	})
	if status != http.StatusOK || !resp.OK || !resp.Bridge {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !strings.Contains(resp.Message, "reactivated build-ui") {
		t.Fatalf("message = %q", resp.Message)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.ArchivedAt.Valid || task.DeletedAt.Valid {
		t.Fatalf("task still hidden: %+v", task)
	}
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionStarted.Valid || task.SessionLastResumed.Valid {
		t.Fatalf("task session/status not reset: %+v", task)
	}
	if task.SessionProvider != "codex" || task.PermissionMode != "auto" {
		t.Fatalf("provider/permission not updated: %+v", task)
	}
	brief, err := os.ReadFile(filepath.Join(root, "tasks", "build-ui", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "Re-run this task with Codex.") {
		t.Fatalf("brief = %q", brief)
	}
}

func TestCreateFlowExistingActiveTaskOpensExisting(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})

	resp, status := srv.runAction(actionRequest{
		Kind:     "create-flow",
		Slug:     "build-ui",
		Name:     "Build UI duplicate",
		WorkDir:  root,
		Priority: "high",
		Provider: "codex",
		Prompt:   "Do not overwrite this active task.",
	})
	if status != http.StatusOK || !resp.OK || !resp.Bridge {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Name != "Build dashboard UI" || task.SessionProvider != "claude" {
		t.Fatalf("active duplicate should not mutate existing task: %+v", task)
	}
}

func TestCreateProjectCreatesProject(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	workDir := filepath.Join(root, "newproj-dir")
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})

	resp, status := srv.runAction(actionRequest{
		Kind:        "create-project",
		Slug:        "newproj",
		Name:        "New Project",
		WorkDir:     workDir,
		Priority:    "high",
		Mkdir:       true,
		Description: "A project created via the UI.\n\nSecond paragraph.",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	project, err := flowdb.GetProject(db, "newproj")
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "New Project" || project.Priority != "high" || project.WorkDir != workDir {
		t.Fatalf("project not persisted as expected: %+v", project)
	}
	if _, err := os.Stat(workDir); err != nil {
		t.Fatalf("--mkdir did not create work_dir: %v", err)
	}
	brief, err := os.ReadFile(filepath.Join(root, "projects", "newproj", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "A project created via the UI.") {
		t.Fatalf("brief did not capture description: %q", brief)
	}
	if !strings.HasPrefix(string(brief), "# New Project") {
		t.Fatalf("brief should start with project name heading: %q", brief)
	}
}

func TestCreateProjectRejectsDuplicateSlug(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})

	resp, status := srv.runAction(actionRequest{
		Kind:     "create-project",
		Slug:     "flow",
		Name:     "Duplicate",
		WorkDir:  root,
		Priority: "medium",
	})
	if status != http.StatusConflict || resp.OK {
		t.Fatalf("expected 409 conflict, got status=%d resp=%+v", status, resp)
	}
}

func TestCreatePlaybookCreatesPlaybook(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})

	resp, status := srv.runAction(actionRequest{
		Kind:        "create-playbook",
		Slug:        "daily-triage",
		Name:        "Daily Triage",
		Project:     "flow",
		WorkDir:     root,
		Description: "## Each run does\n- Review waiting sessions\n- Route new inbox messages",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	pb, err := flowdb.GetPlaybook(db, "daily-triage")
	if err != nil {
		t.Fatal(err)
	}
	if pb.Name != "Daily Triage" || pb.WorkDir != root || !pb.ProjectSlug.Valid || pb.ProjectSlug.String != "flow" {
		t.Fatalf("playbook not persisted as expected: %+v", pb)
	}
	brief, err := os.ReadFile(filepath.Join(root, "playbooks", "daily-triage", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(brief), "# Daily Triage") || !strings.Contains(string(brief), "Review waiting sessions") {
		t.Fatalf("brief did not capture definition: %q", brief)
	}
	if _, err := os.Stat(filepath.Join(root, "playbooks", "daily-triage", "updates")); err != nil {
		t.Fatalf("updates dir missing: %v", err)
	}
}

func TestCreateKBActionCreatesDocument(t *testing.T) {
	root, db := testRootDB(t)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})

	resp, status := srv.runAction(actionRequest{
		Kind:        "create-kb",
		Slug:        "runbooks.md",
		Name:        "Runbooks",
		Description: "Ops notes and escalation commands.",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	body, err := os.ReadFile(filepath.Join(root, "kb", "runbooks.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "# Runbooks") || !strings.Contains(string(body), "Ops notes") {
		t.Fatalf("kb body did not capture content: %q", body)
	}

	handler := srv.Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/kb", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("kb list status = %d", rec.Code)
	}
	var files []KBFileView
	if err := json.Unmarshal(rec.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range files {
		if file.Filename == "runbooks.md" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created kb doc missing from list: %+v", files)
	}
}

func TestUpdatePermissionModeStoresMode(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})

	resp, status := srv.runAction(actionRequest{
		Kind:           "update-permission-mode",
		Slug:           "build-ui",
		PermissionMode: "auto",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.PermissionMode != "auto" {
		t.Fatalf("permission mode = %q, want auto", task.PermissionMode)
	}
}

func TestUpdatePermissionModeRestartsLiveBrowserTerminal(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "11111111-2222-4333-8444-555555555555"
	if _, err := db.Exec(
		`UPDATE tasks SET
			status = 'in-progress',
			session_provider = 'codex',
			session_id = ?,
			session_started = ?,
			permission_mode = 'default'
		 WHERE slug = 'build-ui'`,
		sessionID,
		"2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	sess := &terminalSession{
		hub:     srv.terminals,
		slug:    "build-ui",
		done:    make(chan struct{}),
		clients: map[*terminalClient]struct{}{},
	}
	srv.terminals.mu.Lock()
	srv.terminals.sessions["build-ui"] = sess
	srv.terminals.mu.Unlock()

	resp, status := srv.runAction(actionRequest{
		Kind:           "update-permission-mode",
		Slug:           "build-ui",
		PermissionMode: "bypass",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.Bridge || resp.Agent == nil {
		t.Fatalf("live permission change should reopen the browser bridge, got %+v", resp)
	}
	if resp.Agent.PermissionMode != "bypass" {
		t.Fatalf("agent permission mode = %q, want bypass", resp.Agent.PermissionMode)
	}
	if srv.terminals.running("build-ui") {
		t.Fatal("old browser terminal session still running after permission mode change")
	}
	select {
	case <-sess.done:
	default:
		t.Fatal("old browser terminal session was not terminated")
	}

	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.PermissionMode != "bypass" {
		t.Fatalf("permission mode = %q, want bypass", task.PermissionMode)
	}
}

func TestUpdatePermissionModeRejectsInvalidMode(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:           "update-permission-mode",
		Slug:           "build-ui",
		PermissionMode: "not-a-mode",
	})
	if status != http.StatusBadRequest || resp.OK {
		t.Fatalf("expected 400 bad request, got status=%d resp=%+v", status, resp)
	}
}

func TestUpdatePermissionModeUnknownSlug(t *testing.T) {
	root, db := testRootDB(t)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:           "update-permission-mode",
		Slug:           "no-such-task",
		PermissionMode: "auto",
	})
	if status != http.StatusNotFound || resp.OK {
		t.Fatalf("expected 404, got status=%d resp=%+v", status, resp)
	}
}

func TestUpdatePriorityStoresPriority(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})

	resp, status := srv.runAction(actionRequest{
		Kind:     "update-priority",
		Slug:     "build-ui",
		Priority: "high",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Priority != "high" {
		t.Fatalf("priority = %q, want high", task.Priority)
	}
}

func TestUpdatePriorityRejectsInvalidPriority(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:     "update-priority",
		Slug:     "build-ui",
		Priority: "urgent",
	})
	if status != http.StatusBadRequest || resp.OK {
		t.Fatalf("expected 400 bad request, got status=%d resp=%+v", status, resp)
	}
}

func TestUpdatePriorityUnknownSlug(t *testing.T) {
	root, db := testRootDB(t)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:     "update-priority",
		Slug:     "no-such-task",
		Priority: "high",
	})
	if status != http.StatusNotFound || resp.OK {
		t.Fatalf("expected 404, got status=%d resp=%+v", status, resp)
	}
}

func TestUpdateTaskNameStoresName(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	resp, status := srv.runAction(actionRequest{
		Kind: "update-task-name",
		Slug: "build-ui",
		Name: "Build mission control UI",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Name != "Build mission control UI" {
		t.Fatalf("task name = %q, want updated name", task.Name)
	}
}

func TestUpdateTaskNameRejectsBlankName(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	resp, status := srv.runAction(actionRequest{
		Kind: "update-task-name",
		Slug: "build-ui",
		Name: "   ",
	})
	if status != http.StatusBadRequest || resp.OK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Name != "Build dashboard UI" {
		t.Fatalf("task name changed on blank update: %q", task.Name)
	}
}

func TestCreateProjectRequiresWorkDir(t *testing.T) {
	root, db := testRootDB(t)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: testFlowBinary(t)})
	resp, status := srv.runAction(actionRequest{
		Kind:     "create-project",
		Slug:     "no-wd",
		Name:     "Missing workdir",
		Priority: "medium",
	})
	if status != http.StatusBadRequest || resp.OK {
		t.Fatalf("expected 400 bad request, got status=%d resp=%+v", status, resp)
	}
}

func TestSpawnBacklogActionAppliesProviderChoiceBeforeSessionCreation(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})

	resp, status := srv.runAction(actionRequest{
		Kind:     "spawn",
		Slug:     "build-ui",
		Provider: "codex",
	})
	if status != http.StatusOK || !resp.OK || !resp.Bridge || resp.Agent == nil {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if resp.Agent.Provider != "codex" {
		t.Fatalf("agent provider = %q, want codex", resp.Agent.Provider)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != "codex" {
		t.Fatalf("task provider = %q, want codex", task.SessionProvider)
	}
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionStarted.Valid {
		t.Fatalf("spawn action should not create a session before terminal launch: %+v", task)
	}
}

func TestSpawnRunActionCreatesBrowserBridgeRun(t *testing.T) {
	root, db := testRootDB(t)
	playbookDir := filepath.Join(root, "playbooks", "tri")
	if err := os.MkdirAll(filepath.Join(playbookDir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	playbookBrief := []byte("# Triage\n\n## Each run does\n- Inspect the queue\n")
	if err := os.WriteFile(filepath.Join(playbookDir, "brief.md"), playbookBrief, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "tri", Name: "Triage", WorkDir: root}); err != nil {
		t.Fatal(err)
	}
	oldRunner := iterm.Runner
	spawns := 0
	iterm.Runner = func(args []string) error {
		spawns++
		return nil
	}
	t.Cleanup(func() { iterm.Runner = oldRunner })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/definitely-not-flow"})
	resp, status := srv.runAction(actionRequest{Kind: "spawn-run", Target: "tri"})
	if status != http.StatusOK || !resp.OK || !resp.Bridge || resp.Agent == nil {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if spawns != 0 {
		t.Fatalf("native terminal spawns = %d, want 0", spawns)
	}
	if !strings.HasPrefix(resp.Agent.Slug, "tri--") {
		t.Fatalf("run slug = %q", resp.Agent.Slug)
	}
	task, err := flowdb.GetTask(db, resp.Agent.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "playbook_run" || !task.PlaybookSlug.Valid || task.PlaybookSlug.String != "tri" {
		t.Fatalf("not a tri playbook run: %+v", task)
	}
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionProvider != "claude" || task.PermissionMode != "auto" {
		t.Fatalf("run should be ready for browser bridge start, got %+v", task)
	}
	runBrief, err := os.ReadFile(filepath.Join(root, "tasks", resp.Agent.Slug, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(runBrief) != string(playbookBrief) {
		t.Fatalf("run brief not snapshotted:\n%s", runBrief)
	}
}

func TestPlaybookBriefCanBeUpdatedFromUI(t *testing.T) {
	root, db := testRootDB(t)
	playbookDir := filepath.Join(root, "playbooks", "tri")
	if err := os.MkdirAll(filepath.Join(playbookDir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(playbookDir, "brief.md"), []byte("# Old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "tri", Name: "Triage", WorkDir: root}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: db, FlowRoot: root}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/playbooks/tri/brief", strings.NewReader("# Updated\n\n- Check queues\n"))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(playbookDir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, "# Updated") || strings.Contains(got, "# Old") {
		t.Fatalf("brief not updated: %q", got)
	}
}

func TestProjectBriefCanBeUpdatedFromUI(t *testing.T) {
	root, db := testRootDB(t)
	projectDir := filepath.Join(root, "projects", "ops")
	if err := os.MkdirAll(filepath.Join(projectDir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "brief.md"), []byte("# Old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at)
		 VALUES ('ops', 'Ops', 'active', 'high', ?, ?, ?)`,
		root, now, now,
	); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: db, FlowRoot: root}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/projects/ops/brief", strings.NewReader("# Updated\n\n- Read updates\n"))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(projectDir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, "# Updated") || strings.Contains(got, "# Old") {
		t.Fatalf("brief not updated: %q", got)
	}
}

func TestOverviewChatPreparesFloatingLaunchWithoutTask(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareOverviewFloatingLaunch(actionRequest{
		Prompt:   "What should I do today?",
		Provider: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Slug == overviewTaskSlug || !strings.HasPrefix(launch.Slug, "overview-") {
		t.Fatalf("floating launch slug = %q", launch.Slug)
	}
	if launch.WorkDir != root || launch.Provider != "codex" || launch.PermissionMode != flowdb.DefaultPermissionMode {
		t.Fatalf("floating launch = %+v", launch)
	}
	if !launch.Created || launch.NeedsCapture {
		t.Fatalf("floating launch lifecycle fields = %+v", launch)
	}
	if !strings.Contains(strings.Join(launch.Args, "\n"), "What should I do today?") {
		t.Fatalf("overview prompt missing from args = %#v", launch.Args)
	}
	if _, err := flowdb.GetTask(db, overviewTaskSlug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("overview task should not exist, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", overviewTaskSlug)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overview task directory should not exist, err = %v", err)
	}
}

func TestOverviewChatActionReturnsFloatingTerminal(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	resp, status := srv.runAction(actionRequest{
		Kind:     "overview-chat",
		Prompt:   "Inspect stalled sessions",
		Provider: "codex",
	})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("overview action status=%d resp=%+v", status, resp)
	}
	if resp.Agent != nil || resp.Bridge {
		t.Fatalf("overview action should not return task agent bridge: %+v", resp)
	}
	if resp.FloatingTerminal == nil {
		t.Fatalf("overview action missing floating terminal: %+v", resp)
	}
	if !strings.HasPrefix(resp.FloatingTerminal.ID, "overview-") || resp.FloatingTerminal.Provider != "codex" {
		t.Fatalf("floating terminal = %+v", resp.FloatingTerminal)
	}
	srv.terminals.mu.Lock()
	launch, ok := srv.terminals.floatingLaunches[resp.FloatingTerminal.ID]
	srv.terminals.mu.Unlock()
	if !ok || !launch.FreeAgent {
		t.Fatalf("floating launch not registered as free agent: ok=%v launch=%+v", ok, launch)
	}
	if _, err := flowdb.GetTask(db, overviewTaskSlug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("overview task should not exist, err = %v", err)
	}
}

func TestFloatingTerminalEnvOmitsFlowTask(t *testing.T) {
	env := terminalEnvMap("", "", "", "overview-abc", "codex", flowdb.DefaultPermissionMode, true)
	if _, ok := env["FLOW_TASK"]; ok {
		t.Fatalf("free agent env should not include FLOW_TASK: %+v", env)
	}
	if env["FLOW_FREE_AGENT"] != "1" {
		t.Fatalf("free agent marker missing: %+v", env)
	}
	taskEnv := terminalEnvMap("", "", "", "build-ui", "claude", flowdb.DefaultPermissionMode, false)
	if taskEnv["FLOW_TASK"] != "build-ui" || taskEnv["FLOW_FREE_AGENT"] != "" {
		t.Fatalf("task env = %+v", taskEnv)
	}
}

func TestPrepareTerminalLaunchResumesExistingSession(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "22222222-2222-4222-8222-222222222222"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if launch.Created || launch.SessionID != sessionID {
		t.Fatalf("launch = %+v", launch)
	}
	if len(launch.Args) != 4 || launch.Args[0] != "--resume" || launch.Args[1] != sessionID {
		t.Fatalf("args = %#v", launch.Args)
	}
	if launch.Args[2] != "--permission-mode" || launch.Args[3] != "auto" {
		t.Fatalf("default permission args on resume = %#v", launch.Args)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionLastResumed.Valid {
		t.Fatalf("session_last_resumed not recorded: %+v", task)
	}
}

func TestRestartBrowserTerminalPreservesExistingSession(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "33333333-3333-4333-8333-333333333333"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	resp, status := srv.runAction(actionRequest{Kind: "restart", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.OK || !resp.Bridge {
		t.Fatalf("expected browser bridge restart response, got %+v", resp)
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" || !task.SessionID.Valid || task.SessionID.String != sessionID {
		t.Fatalf("restart cleared or changed session: %+v", task)
	}
	if !task.SessionLastResumed.Valid {
		t.Fatalf("restart did not record resume timestamp: %+v", task)
	}
}

func TestRestartFreshBrowserTerminalClearsExistingSession(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "44444444-4444-4444-8444-444444444444"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ?, session_last_resumed = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30", "2026-05-12T10:02:00+05:30",
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	browserSess := &terminalSession{
		slug:      "build-ui",
		sessionID: sessionID,
		done:      make(chan struct{}),
		clients:   map[*terminalClient]struct{}{},
	}
	srv.terminals.mu.Lock()
	srv.terminals.sessions["build-ui"] = browserSess
	srv.terminals.mu.Unlock()

	resp, status := srv.runAction(actionRequest{Kind: "restart-fresh", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.OK || !resp.Bridge {
		t.Fatalf("expected fresh browser bridge response, got %+v", resp)
	}
	srv.terminals.mu.Lock()
	_, stillRunning := srv.terminals.sessions["build-ui"]
	srv.terminals.mu.Unlock()
	if stillRunning {
		t.Fatal("fresh restart should stop the stale browser terminal session")
	}

	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "backlog" {
		t.Fatalf("status = %q, want backlog before websocket launch", task.Status)
	}
	if task.SessionID.Valid || task.SessionStarted.Valid || task.SessionLastResumed.Valid {
		t.Fatalf("fresh restart should clear session fields: %+v", task)
	}

	launch, err := srv.prepareTerminalLaunch("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if !launch.Created || launch.SessionID == "" || launch.SessionID == sessionID {
		t.Fatalf("launch = %+v, want fresh new session", launch)
	}
	if len(launch.Args) < 2 || launch.Args[0] != "--session-id" || launch.Args[1] != launch.SessionID {
		t.Fatalf("args = %#v, want fresh claude launch args", launch.Args)
	}
	task, err = flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" || !task.SessionID.Valid || task.SessionID.String != launch.SessionID {
		t.Fatalf("task after websocket launch = %+v", task)
	}
}

func TestITermActionOpensNativeTerminalNotBrowserBridge(t *testing.T) {
	root, db := testRootDB(t)
	commands := enableSharedTerminalForTest(t)
	insertProjectTask(t, db, root)
	sessionID := "44444444-4444-4444-8444-444444444444"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	oldRunner := iterm.Runner
	var script string
	iterm.Runner = func(args []string) error {
		script = strings.Join(args, "\n")
		return nil
	}
	t.Cleanup(func() { iterm.Runner = oldRunner })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	resp, status := srv.runAction(actionRequest{Kind: "iterm", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.OK || resp.Bridge {
		t.Fatalf("expected native terminal response without browser bridge, got %+v", resp)
	}
	body := readITermLaunchScriptBody(t, script)
	if !strings.Contains(script, "iTerm2") || !strings.Contains(script, "newline yes") || !strings.Contains(body, "tmux attach-session -t flow-build-ui") {
		t.Fatalf("unexpected iTerm script: %s", script)
	}
	if !strings.Contains(fmt.Sprint(*commands), "claude --resume "+sessionID) {
		t.Fatalf("shared tmux session did not resume claude session %s: %#v", sessionID, *commands)
	}
	if resp.Agent == nil || resp.Agent.Terminal.Mode != "shared" {
		t.Fatalf("native open should return shared terminal agent, got %+v", resp.Agent)
	}
}

func TestITermActionResumesCodexSession(t *testing.T) {
	root, db := testRootDB(t)
	commands := enableSharedTerminalForTest(t)
	insertProjectTask(t, db, root)
	sessionID := "11111111-2222-4333-8444-555555555555"
	workDir := t.TempDir()
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_provider = 'codex', session_id = ?, session_started = ?, work_dir = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-15T21:29:55+05:30", workDir,
	); err != nil {
		t.Fatal(err)
	}
	oldRunner := iterm.Runner
	var script string
	iterm.Runner = func(args []string) error {
		script = strings.Join(args, "\n")
		return nil
	}
	t.Cleanup(func() { iterm.Runner = oldRunner })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: filepath.Join(root, "bin", "flow")})
	resp, status := srv.runAction(actionRequest{Kind: "iterm", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	body := readITermLaunchScriptBody(t, script)
	if !strings.Contains(script, "newline yes") {
		t.Fatalf("iTerm script should submit launcher:\n%s", script)
	}
	if !strings.Contains(body, "tmux attach-session -t flow-build-ui") {
		t.Fatalf("iTerm should attach to the shared tmux terminal:\n%s", body)
	}
	tmuxCommands := fmt.Sprint(*commands)
	for _, want := range []string{
		"codex resume",
		"--include-non-interactive",
		"--add-dir " + root,
		sessionID,
	} {
		if !strings.Contains(tmuxCommands, want) {
			t.Fatalf("shared tmux codex launcher missing %q:\n%#v", want, *commands)
		}
	}
}

func readITermLaunchScriptBody(t *testing.T, script string) string {
	t.Helper()
	match := regexp.MustCompile(`/bin/sh '([^']+)'`).FindStringSubmatch(script)
	if len(match) != 2 {
		t.Fatalf("launcher path not found in iTerm script:\n%s", script)
	}
	t.Cleanup(func() { _ = os.Remove(match[1]) })
	data, err := os.ReadFile(match[1])
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	return string(data)
}

func TestNativeTerminalActionStillLaunchesWhenSessionAlreadyLive(t *testing.T) {
	root, db := testRootDB(t)
	enableSharedTerminalForTest(t)
	insertProjectTask(t, db, root)
	sessionID := "55555555-5555-4555-8555-555555555555"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	oldPS := psRunner
	psRunner = func() ([]byte, error) {
		return []byte("123 claude --resume " + sessionID + "\n"), nil
	}
	t.Cleanup(func() { psRunner = oldPS })
	oldRunner := iterm.Runner
	spawns := 0
	iterm.Runner = func(args []string) error {
		spawns++
		return nil
	}
	t.Cleanup(func() { iterm.Runner = oldRunner })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	resp, status := srv.runAction(actionRequest{Kind: "iterm", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if !resp.OK || resp.AlreadyLive || resp.Bridge {
		t.Fatalf("expected native launch response, got %+v", resp)
	}
	if spawns != 1 {
		t.Fatalf("iTerm spawns = %d, want 1", spawns)
	}
	if resp.Agent == nil || resp.Agent.Terminal.Mode != "shared" {
		t.Fatalf("native live open should return shared terminal agent, got %+v", resp.Agent)
	}
}

func TestNativeTerminalActionKeepsSharedBrowserTerminalAfterNativeOpen(t *testing.T) {
	root, db := testRootDB(t)
	enableSharedTerminalForTest(t)
	insertProjectTask(t, db, root)
	sessionID := "66666666-6666-4666-8666-666666666666"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	oldPS := psRunner
	psRunner = func() ([]byte, error) {
		return []byte("123 claude --resume " + sessionID + "\n"), nil
	}
	t.Cleanup(func() { psRunner = oldPS })
	oldRunner := iterm.Runner
	var script string
	iterm.Runner = func(args []string) error {
		script = strings.Join(args, "\n")
		return nil
	}
	t.Cleanup(func() { iterm.Runner = oldRunner })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	browserSess := &terminalSession{
		slug:       "build-ui",
		sessionID:  sessionID,
		sharedName: "flow-build-ui",
		done:       make(chan struct{}),
		clients:    map[*terminalClient]struct{}{},
	}
	srv.terminals.mu.Lock()
	srv.terminals.sessions["build-ui"] = browserSess
	srv.terminals.mu.Unlock()

	resp, status := srv.runAction(actionRequest{Kind: "iterm", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}

	srv.terminals.mu.Lock()
	got := srv.terminals.sessions["build-ui"]
	srv.terminals.mu.Unlock()
	if got != browserSess || !browserSess.running() {
		t.Fatalf("shared browser terminal should stay attached after native open; got=%p running=%v", got, browserSess.running())
	}
	if !strings.Contains(readITermLaunchScriptBody(t, script), "tmux attach-session -t flow-build-ui") {
		t.Fatalf("native handoff should attach to the shared tmux session:\n%s", script)
	}
	if resp.Agent == nil || resp.Agent.Terminal.Mode != "shared" {
		t.Fatalf("native handoff response should mark terminal mode shared, got %+v", resp.Agent)
	}
}

func TestAgentSnapshotMarksNativeTerminalMode(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "77777777-7777-4777-8777-777777777777"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	oldPS := psRunner
	psRunner = func() ([]byte, error) {
		return []byte("123 claude --resume " + sessionID + "\n"), nil
	}
	t.Cleanup(func() { psRunner = oldPS })

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	agent, err := srv.agentForTask("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != "running" || agent.Terminal.Mode != "native" {
		t.Fatalf("agent status/mode = %q/%q, want running/native", agent.Status, agent.Terminal.Mode)
	}
}

func TestAgentSnapshotUsesTranscriptWhenBrowserTerminalIsStale(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessionID := "88888888-8888-4888-8888-888888888888"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00Z",
	); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, ".claude", "projects", encodeCwdForClaude(root))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"assistant","timestamp":"2026-05-12T10:05:00Z","message":{"role":"assistant","content":[{"type":"text","text":"later native terminal reply"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, sessionID+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	lastOutput, err := time.Parse(time.RFC3339, "2026-05-12T10:01:30Z")
	if err != nil {
		t.Fatal(err)
	}
	srv.terminals.mu.Lock()
	srv.terminals.sessions["build-ui"] = &terminalSession{
		slug:         "build-ui",
		sessionID:    sessionID,
		done:         make(chan struct{}),
		clients:      map[*terminalClient]struct{}{},
		scrollback:   []byte("old browser terminal output"),
		lastOutputAt: lastOutput,
	}
	srv.terminals.mu.Unlock()

	agent, err := srv.agentForTask("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Terminal.Mode != "native" {
		t.Fatalf("terminal mode = %q, want native for transcript ahead of browser PTY", agent.Terminal.Mode)
	}
	if len(agent.Transcript) == 0 || !strings.Contains(agent.Transcript[len(agent.Transcript)-1].Text, "later native terminal reply") {
		t.Fatalf("agent transcript was not loaded from session jsonl: %+v", agent.Transcript)
	}
}

func TestSwitchBranchActionUpdatesGitBranchAndAgent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	runGitTest(t, root, "init", "-b", "main")
	runGitTest(t, root, "config", "user.email", "flow@example.invalid")
	runGitTest(t, root, "config", "user.name", "Flow Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("flow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "README.md")
	runGitTest(t, root, "commit", "-m", "init")
	runGitTest(t, root, "switch", "-c", "feature/ui")
	runGitTest(t, root, "switch", "main")

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	resp, status := srv.runAction(actionRequest{Kind: "switch-branch", Target: "build-ui", Branch: "feature/ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	if resp.Agent == nil || resp.Agent.Branch != "feature/ui" {
		t.Fatalf("agent branch = %+v", resp.Agent)
	}
	if got := gitBranch(root); got != "feature/ui" {
		t.Fatalf("git branch = %q", got)
	}
	if len(resp.Agent.Branches) == 0 || !containsString(resp.Agent.Branches, "main") {
		t.Fatalf("branches = %#v", resp.Agent.Branches)
	}
}

func TestTaskBridgeEndpointReturnsAgentSnapshot(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/build-ui/bridge", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var agent uiAgent
	if err := json.Unmarshal(rec.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Slug != "build-ui" || agent.WorkDir != root {
		t.Fatalf("agent = %+v", agent)
	}
	if len(agent.Transcript) == 0 || agent.Terminal.Banner == nil {
		t.Fatalf("expected transcript and terminal snapshot, got %+v", agent)
	}
}

// TestTaskBridgeAgentUsesWorktreeForGitDiff pins that when a task has a
// worktree_path on a feature branch, the bridge agent snapshot reports
// the worktree's branch and a diff containing only the worktree's local
// edits — NOT the parent repo's checked-out branch (e.g. main).
//
// This is a regression test for the bug where the GIT DIFF panel was
// running git in the main checkout instead of the per-task worktree,
// so the panel showed unrelated main-branch changes instead of the
// agent's actual in-flight work.
func TestTaskBridgeAgentUsesWorktreeForGitDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	// Initialize the parent repo with a committed baseline, then stage a
	// main-branch-only change so the parent's `git diff` is non-empty —
	// this is what the buggy code path would have surfaced.
	runGitTest(t, root, "init", "-b", "main")
	runGitTest(t, root, "config", "user.email", "flow@example.invalid")
	runGitTest(t, root, "config", "user.name", "Flow Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("flow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "README.md")
	runGitTest(t, root, "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(root, "main-only.txt"), []byte("main edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Spin up a worktree on a feature branch with a different unique file.
	worktreeDir := filepath.Join(root, ".claude", "worktrees", "build-ui")
	runGitTest(t, root, "worktree", "add", "-b", "flow/build-ui", worktreeDir)
	if err := os.WriteFile(filepath.Join(worktreeDir, "worktree-only.txt"), []byte("worktree edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wire the worktree onto the task. Use absolute path to match what
	// `flow do` writes via worktree.Ensure().
	absWorktree, err := filepath.Abs(worktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE tasks SET worktree_path = ? WHERE slug = 'build-ui'`,
		absWorktree,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/build-ui/bridge", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var agent uiAgent
	if err := json.Unmarshal(rec.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Branch != "flow/build-ui" {
		t.Errorf("branch = %q, want flow/build-ui (worktree branch, not parent main)", agent.Branch)
	}
	names := map[string]bool{}
	for _, f := range agent.DiffFiles {
		names[f.Name] = true
	}
	if !names["worktree-only.txt"] {
		t.Errorf("diff should contain worktree-only.txt; got files=%v", names)
	}
	if names["main-only.txt"] {
		t.Errorf("diff must NOT contain main-only.txt (that's the parent repo's change); got files=%v", names)
	}
}

func TestTaskAttachmentUploadStoresFileAndReturnsInsertText(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "screen shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/build-ui/attachments", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp terminalAttachmentUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %#v, want one", resp.Files)
	}
	file := resp.Files[0]
	if !strings.Contains(file.Filename, "screen-shot.png") {
		t.Fatalf("filename = %q, want sanitized original name", file.Filename)
	}
	if !strings.Contains(file.Path, filepath.Join(root, "tasks", "build-ui", "attachments")) {
		t.Fatalf("path = %q, want task attachment dir", file.Path)
	}
	if !strings.Contains(resp.InsertText, shellQuoteArg(file.Path)) {
		t.Fatalf("insert_text = %q, want quoted path %q", resp.InsertText, file.Path)
	}
	if !strings.HasPrefix(resp.InsertText, "@") {
		t.Fatalf("insert_text = %q, want '@'-prefixed Claude file reference", resp.InsertText)
	}
	saved, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "png bytes" {
		t.Fatalf("saved body = %q", saved)
	}
}

func TestTaskAttachmentUploadCodexUsesBarePaths(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	if _, err := db.Exec(`UPDATE tasks SET session_provider = 'codex' WHERE slug = 'build-ui'`); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "diagram.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/build-ui/attachments", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp terminalAttachmentUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(resp.InsertText, "@") {
		t.Fatalf("insert_text = %q, Codex sessions must receive bare paths (no '@' prefix)", resp.InsertText)
	}
	if len(resp.Files) != 1 || !strings.Contains(resp.InsertText, shellQuoteArg(resp.Files[0].Path)) {
		t.Fatalf("insert_text = %q, want it to contain the bare absolute path", resp.InsertText)
	}
}

func TestTaskBridgeEndpointUsesCodexProviderForTranscript(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sessionID := "11111111-2222-4333-8444-555555555555"
	if _, err := db.Exec(
		`UPDATE tasks
		    SET status = 'done',
		        session_provider = 'codex',
		        session_id = ?,
		        session_started = ?,
		        session_last_resumed = ?
		  WHERE slug = 'build-ui'`,
		sessionID,
		"2026-05-12T10:01:00+05:30",
		"2026-05-12T10:02:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "05", "12")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"session_meta","payload":{"id":"` + sessionID + `","timestamp":"2026-05-12T10:01:00+05:30","cwd":"` + root + `"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"codex full transcript marker"}]}}`,
	}
	for i := 0; i < 25; i++ {
		lines = append(lines, `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex filler `+string(rune('a'+i))+`"}]}}`)
	}
	lines = append(lines, `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex assistant closeout marker"}]}}`)
	body := strings.Join(lines, "\n") + "\n"
	sessionPath := filepath.Join(sessionDir, "rollout-2026-05-12T10-01-00-"+sessionID+".jsonl")
	if err := os.WriteFile(sessionPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/build-ui/bridge", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var agent uiAgent
	if err := json.Unmarshal(rec.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	transcriptText := ""
	for _, entry := range agent.Transcript {
		transcriptText += entry.Text + "\n"
	}
	if !strings.Contains(transcriptText, "codex full transcript marker") ||
		!strings.Contains(transcriptText, "codex assistant closeout marker") {
		t.Fatalf("codex transcript was not rendered: %#v", agent.Transcript)
	}
	if len(agent.Transcript) <= 24 {
		t.Fatalf("bridge transcript should not use the capped bootstrap transcript, got %d entries", len(agent.Transcript))
	}
}

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func initGitRepoForServerTest(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "README.md")
	runGitTest(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init")
	if canon, err := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel").Output(); err == nil {
		return strings.TrimSpace(string(canon))
	}
	return repo
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func envValue(items []string, key string) string {
	prefix := key + "="
	for _, item := range items {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func testFlowBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "flow-test")
	body := "#!/bin/sh\ncd " + shellQuote(repoRoot) + " && exec go run . \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestStripTerminalGeneratedInput(t *testing.T) {
	got := stripTerminalGeneratedInput("typed\x1b[>0;276;0c text\x1b[?1;2c")
	if got != "typed text" {
		t.Fatalf("stripTerminalGeneratedInput = %q, want %q", got, "typed text")
	}
}

func enableSharedTerminalForTest(t *testing.T) *[][]string {
	t.Helper()
	commands := [][]string{}
	sessions := map[string]bool{}
	sharedTerminalLookPath = func(name string) (string, error) {
		if name == "tmux" {
			return "/usr/bin/tmux", nil
		}
		return "", exec.ErrNotFound
	}
	// sharedTerminalAvailable() memoizes its first call via sync.Once for
	// production CPU savings; tests that swap sharedTerminalLookPath must
	// reset that memo so the new mock takes effect for this test (and
	// again on cleanup so the next test starts from a clean slate).
	resetSharedTerminalAvailable()
	t.Cleanup(resetSharedTerminalAvailable)
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "has-session":
			name := args[len(args)-1]
			if sessions[name] {
				return nil, nil
			}
			return nil, fmt.Errorf("missing session %s", name)
		case "new-session":
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "-s" {
					sessions[args[i+1]] = true
					break
				}
			}
			return nil, nil
		case "kill-session":
			delete(sessions, args[len(args)-1])
			return nil, nil
		default:
			return nil, nil
		}
	}
	return &commands
}

func testRootDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	oldSharedLookPath := sharedTerminalLookPath
	oldSharedCommand := sharedTerminalCommand
	sharedTerminalLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	}
	// Force sharedTerminalAvailable() to re-resolve under the new mock —
	// otherwise an earlier test's cached "true" would leak into this one.
	resetSharedTerminalAvailable()
	t.Cleanup(func() {
		sharedTerminalLookPath = oldSharedLookPath
		sharedTerminalCommand = oldSharedCommand
		resetSharedTerminalAvailable()
	})
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "tasks", "build-ui", "updates"),
		filepath.Join(root, "projects", "flow", "updates"),
		filepath.Join(root, "kb"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := flowdb.OpenDB(filepath.Join(root, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return root, db
}

func insertProjectTask(t *testing.T, db *sql.DB, root string) {
	t.Helper()
	now := "2026-05-12T10:00:00+05:30"
	if _, err := db.Exec(
		`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at)
		 VALUES ('flow', 'Flow project', 'active', 'high', ?, ?, ?)`,
		root, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, kind, priority, work_dir, created_at, updated_at)
		 VALUES ('build-ui', 'Build dashboard UI', 'flow', 'backlog', 'regular', 'high', ?, ?, ?)`,
		root, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.AddTaskTag(db, "build-ui", "ui"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "build-ui", "brief.md"), []byte("# Real task brief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "build-ui", "updates", "2026-05-12-progress.md"), []byte("- current-data-marker came from disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
