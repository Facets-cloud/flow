package server

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"flow/internal/flowdb"
	"flow/internal/iterm"
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

func TestActivitySeriesReflectsRecency(t *testing.T) {
	running := activitySeries("build-ui", 30, "running")
	if len(running) != 60 {
		t.Fatalf("len = %d", len(running))
	}
	if running[59] == 0 {
		t.Fatalf("recent running activity should light the latest bucket: %#v", running[55:])
	}
	old := activitySeries("build-ui", 2*24*3600, "stale")
	for _, v := range old {
		if v != 0 {
			t.Fatalf("old stale activity should not be synthetic: %#v", old)
		}
	}
}

func TestActivityHeatmapUsesTaskAndUpdateDates(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.Local)
	days := buildActivityHeatmap([]TaskView{{
		Slug:      "build-ui",
		Status:    "in-progress",
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
	if len(launch.Args) != 3 || launch.Args[0] != "--session-id" || launch.Args[1] != launch.SessionID {
		t.Fatalf("args = %#v", launch.Args)
	}
	if !strings.Contains(launch.Args[2], "flow task build-ui") {
		t.Fatalf("bootstrap prompt = %q", launch.Args[2])
	}
	task, err := flowdb.GetTask(db, "build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" || !task.SessionID.Valid || task.SessionID.String != launch.SessionID || !task.SessionStarted.Valid {
		t.Fatalf("task after launch = %+v", task)
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
	wantPrefix := []string{"--no-alt-screen", "-C", workDir, "--add-dir", root, "--ask-for-approval", "on-request", "--sandbox", "workspace-write"}
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
	want := []string{"resume", "--include-non-interactive", "--no-alt-screen", "-C", workDir, "--add-dir", root, "--ask-for-approval", "on-request", "--sandbox", "workspace-write", sessionID}
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
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionProvider != "claude" || task.PermissionMode != "default" {
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

func TestStaticActionPayloadForwardsProvider(t *testing.T) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "provider: target.provider") {
		t.Fatal("serverAction payload must forward provider so UI-created Codex tasks do not default to Claude")
	}
	if !strings.Contains(body, "/api/tasks/${encodeURIComponent(sessionSlug)}/bridge") {
		t.Fatal("completed session routes must fetch the full bridge snapshot instead of using capped ui-data transcripts")
	}
	if !strings.Contains(body, "setBridgeAgents(prev => ({ ...prev, [data.agent.slug]: data.agent }))") {
		t.Fatal("spawn/attach actions must retain the action-returned agent locally until the terminal websocket updates the DB")
	}
	if !strings.Contains(body, "const nativeSessionSnapshot = rawSessionAgent?.terminal?.mode === 'native' || bridgeAgent?.terminal?.mode === 'native'") {
		t.Fatal("session routes must detect native terminal transcript snapshots")
	}
	if !strings.Contains(body, "rawSessionAgent && bridgeAgent") {
		t.Fatal("session routes must prefer refreshed bridge snapshots for active session status")
	}
	if !strings.Contains(body, "mode: nativeSessionSnapshot ? 'native' : (bridgeAgent.terminal?.mode || rawSessionAgent.terminal?.mode)") {
		t.Fatal("session routes must merge retained bridge snapshots so spawn does not fall back to stale ui-data")
	}
	if !strings.Contains(body, "existing.status === agent.status") {
		t.Fatal("session routes must refresh retained bridge snapshots when status changes without transcript growth")
	}
	if !strings.Contains(body, "const completedAgent = doneAgent ? (bridgeTranscriptCount > doneTranscriptCount ? bridgeAgent : doneAgent) : null") {
		t.Fatal("completed session routes must not let a stale retained bridge agent override the done task snapshot")
	}
	if !strings.Contains(body, "if (kind === 'spawn-run')") || !strings.Contains(body, "goto(`session/${data.agent.slug}`)") {
		t.Fatal("playbook spawn-run must navigate to the created browser terminal run")
	}
	tiles, err := staticFS.ReadFile("static/assets/dfbb0627-5c41-4bf8-85df-037b2d384519.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(tiles), `<Icon name="external-link" size={11}/>Open</button>`) < 3 {
		t.Fatal("agent session tiles should label attach/navigation actions as Open with a navigation icon")
	}
	if !strings.Contains(string(tiles), "permissionWaiting ? 'Awaiting your approval' : 'Awaiting your input'") {
		t.Fatal("question waits should be labeled as input instead of approval")
	}
	if !strings.Contains(string(tiles), "permissionWaiting ? (") || !strings.Contains(string(tiles), "onAction('pause', agent)") {
		t.Fatal("non-permission waiting tiles should expose pause/open actions instead of approve/deny")
	}
	screens, err := staticFS.ReadFile("static/assets/c906f42d-c4d3-4f33-b4a9-aca5e8a18052.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(screens), "Attach to ") || strings.Contains(string(screens), ">Attach</button>") {
		t.Fatal("session navigation copy should say Open instead of Attach")
	}
}

func TestStaticPlaybookDetailUsesRealDataAndEditableBrief(t *testing.T) {
	data, err := staticFS.ReadFile("static/assets/c906f42d-c4d3-4f33-b4a9-aca5e8a18052.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"fetch(`/api/playbooks/${encodeURIComponent(slug)}`)",
		"fetch(`/api/playbooks/${encodeURIComponent(slug)}/brief`",
		"method: 'PUT'",
		"const recentRuns = detail ? (detail.recent_runs || []) : []",
		"const relatedFiles = [",
		"/${file.route}/${encodeURIComponent(file.filename)}",
		"goto(`session/${r.slug}`)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("playbook detail missing %q", want)
		}
	}
	if strings.Contains(body, "trigger: 'cron'") || strings.Contains(body, "dur: '3m 24s'") {
		t.Fatal("playbook detail should not render fake history rows")
	}
}

func TestOverviewTaskUsesFlowRootAndFreshPrompt(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	wrongDir := t.TempDir()
	sessionID := "99999999-9999-4999-8999-999999999999"
	now := "2026-05-12T10:00:00+05:30"
	if _, err := db.Exec(
		`INSERT INTO tasks (
			slug, name, project_slug, status, kind, priority, work_dir,
			session_id, session_started, status_changed_at, created_at, updated_at
		) VALUES (?, ?, 'flow', 'in-progress', 'regular', 'high', ?, ?, ?, ?, ?, ?)`,
		overviewTaskSlug, "Old overview", wrongDir, sessionID, now, now, now, now,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	if err := srv.prepareOverviewTask("What should I do today?"); err != nil {
		t.Fatal(err)
	}
	task, err := flowdb.GetTask(db, overviewTaskSlug)
	if err != nil {
		t.Fatal(err)
	}
	if task.WorkDir != root {
		t.Fatalf("overview work_dir = %q, want %q", task.WorkDir, root)
	}
	if task.ProjectSlug.Valid {
		t.Fatalf("overview project should be adhoc, got %q", task.ProjectSlug.String)
	}
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionStarted.Valid || task.SessionLastResumed.Valid {
		t.Fatalf("overview task should be reset before launch: %+v", task)
	}
	brief, err := os.ReadFile(filepath.Join(root, "tasks", overviewTaskSlug, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "What should I do today?") {
		t.Fatalf("brief = %q", brief)
	}
	launch, err := srv.prepareTerminalLaunch(overviewTaskSlug)
	if err != nil {
		t.Fatal(err)
	}
	if !launch.Created || launch.WorkDir != root {
		t.Fatalf("overview launch = %+v", launch)
	}
	if len(launch.Args) != 3 || strings.TrimSpace(launch.Args[2]) != "What should I do today?" {
		t.Fatalf("overview prompt args = %#v", launch.Args)
	}
}

func TestPrepareTerminalLaunchResetsStaleOverviewSession(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	wrongDir := t.TempDir()
	sessionID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	now := "2026-05-12T10:00:00+05:30"
	if _, err := db.Exec(
		`INSERT INTO tasks (
			slug, name, project_slug, status, kind, priority, work_dir,
			session_id, session_started, status_changed_at, created_at, updated_at
		) VALUES (?, ?, 'flow', 'in-progress', 'regular', 'high', ?, ?, ?, ?, ?, ?)`,
		overviewTaskSlug, "Old overview", wrongDir, sessionID, now, now, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tasks", overviewTaskSlug), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "tasks", overviewTaskSlug, "brief.md"),
		[]byte("# Flow overview command center\n\nLatest user request:\nCheck my inbox"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	launch, err := srv.prepareTerminalLaunch(overviewTaskSlug)
	if err != nil {
		t.Fatal(err)
	}
	if !launch.Created || launch.WorkDir != root || launch.SessionID == sessionID {
		t.Fatalf("overview launch = %+v", launch)
	}
	if len(launch.Args) != 3 || strings.TrimSpace(launch.Args[2]) != "Check my inbox" {
		t.Fatalf("overview prompt args = %#v", launch.Args)
	}
	task, err := flowdb.GetTask(db, overviewTaskSlug)
	if err != nil {
		t.Fatal(err)
	}
	if task.WorkDir != root || !task.SessionID.Valid || task.SessionID.String != launch.SessionID {
		t.Fatalf("overview task after launch = %+v", task)
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
	if len(launch.Args) != 2 || launch.Args[0] != "--resume" || launch.Args[1] != sessionID {
		t.Fatalf("args = %#v", launch.Args)
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

func TestAgentSnapshotIncludesOptionalPRLinks(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "33333333-3333-4333-8333-333333333333"
	if _, err := db.Exec(
		`UPDATE tasks SET status = 'in-progress', session_id = ?, session_started = ? WHERE slug = 'build-ui'`,
		sessionID, "2026-05-12T10:01:00+05:30",
	); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertTaskPRLink(db, "build-ui", "acme/flow", 48, "https://github.com/acme/flow/pull/48"); err != nil {
		t.Fatal(err)
	}

	srv := New(Config{DB: db, FlowRoot: root, CommandPath: "/bin/false"})
	agent, err := srv.agentForTask("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.PRLinks) != 1 {
		t.Fatalf("pr links = %#v", agent.PRLinks)
	}
	if agent.PRLinks[0].URL != "https://github.com/acme/flow/pull/48" || agent.PRLinks[0].State != "open" {
		t.Fatalf("pr link = %+v", agent.PRLinks[0])
	}
}

func TestITermActionOpensNativeTerminalNotBrowserBridge(t *testing.T) {
	root, db := testRootDB(t)
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
	if !strings.Contains(script, "iTerm2") || !strings.Contains(script, "newline yes") ||
		!strings.Contains(body, "claude") || !strings.Contains(body, sessionID) {
		t.Fatalf("unexpected iTerm script: %s", script)
	}
}

func TestITermActionResumesCodexSession(t *testing.T) {
	root, db := testRootDB(t)
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
	for _, want := range []string{
		"codex resume",
		"--include-non-interactive",
		"--add-dir " + root,
		sessionID,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("iTerm codex resume launcher missing %q:\n%s", want, body)
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
}

func TestNativeTerminalActionStopsBrowserTerminalAfterHandoff(t *testing.T) {
	root, db := testRootDB(t)
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
		slug:      "build-ui",
		sessionID: sessionID,
		done:      make(chan struct{}),
		clients:   map[*terminalClient]struct{}{},
	}
	srv.terminals.mu.Lock()
	srv.terminals.sessions["build-ui"] = browserSess
	srv.terminals.mu.Unlock()

	resp, status := srv.runAction(actionRequest{Kind: "iterm", Target: "build-ui"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, resp = %+v", status, resp)
	}
	_ = readITermLaunchScriptBody(t, script)

	srv.terminals.mu.Lock()
	got := srv.terminals.sessions["build-ui"]
	srv.terminals.mu.Unlock()
	if got != nil || browserSess.running() {
		t.Fatalf("browser terminal session should stop after native handoff; got=%p running=%v", got, browserSess.running())
	}
	if resp.Agent == nil || resp.Agent.Terminal.Mode != "native" {
		t.Fatalf("native handoff response should mark terminal mode native, got %+v", resp.Agent)
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

func TestParseGitHubPRURLAcceptsWebAndAPI(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/acme/flow/pull/48",
		"https://api.github.com/repos/acme/flow/pulls/48",
	} {
		repo, number, ok := parseGitHubPRURL(raw)
		if !ok || repo != "acme/flow" || number != 48 {
			t.Fatalf("parseGitHubPRURL(%q) = %q, %d, %v", raw, repo, number, ok)
		}
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

func testRootDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
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
