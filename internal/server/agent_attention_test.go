package server

import (
	"encoding/json"
	"flow/internal/flowdb"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestContextWindowForProvider(t *testing.T) {
	if got := contextWindowForProvider("claude"); got != 1000000 {
		t.Fatalf("claude context window = %d, want 1000000", got)
	}
	if got := contextWindowForProvider("codex"); got != 200000 {
		t.Fatalf("codex context window = %d, want 200000", got)
	}
}

func TestSessionTranscriptUsageStats(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude.jsonl")
	if err := os.WriteFile(claudePath, []byte(`{"type":"assistant","timestamp":"2026-05-16T12:00:00Z","message":{"role":"assistant","usage":{"input_tokens":10,"cache_read_input_tokens":20,"output_tokens":5},"content":[{"type":"text","text":"Done"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claude := sessionTranscriptUsageStats(claudePath)
	if claude.TokensUsed != 35 || claude.LastTimestamp != "2026-05-16T12:00:00Z" {
		t.Fatalf("claude stats = %+v, want 35 tokens and timestamp", claude)
	}

	codexPath := filepath.Join(dir, "codex.jsonl")
	codexLine := `{"type":"event_msg","timestamp":"2026-05-16T12:01:00Z","payload":{"type":"token_count","info":{"model_context_window":258400,"last_token_usage":{"input_tokens":100,"cached_input_tokens":50,"output_tokens":25,"reasoning_output_tokens":5,"total_tokens":180},"total_token_usage":{"total_tokens":999}}}}`
	if err := os.WriteFile(codexPath, []byte(codexLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codex := sessionTranscriptUsageStats(codexPath)
	if codex.TokensUsed != 180 || codex.TokensMax != 258400 || codex.LastTimestamp != "2026-05-16T12:01:00Z" {
		t.Fatalf("codex stats = %+v, want reported usage/window/timestamp", codex)
	}
}

func TestAgentAttentionNotifications(t *testing.T) {
	notifs := agentAttentionNotifications([]uiAgent{
		{
			Slug:       "switcher",
			Name:       "Switcher",
			Provider:   "codex",
			Status:     "waiting",
			SessionID:  "019e2f71-82f0-76b0-9353-fbc4a662d442",
			LastAction: "permission requested",
			WaitingFor: &uiWaitingFor{Kind: "permission", Why: "Would you like to run the following command?"},
		},
	})
	if len(notifs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifs))
	}
	if notifs[0].Level != "approval" || notifs[0].Status != "unread" || notifs[0].Source != "agent" {
		t.Fatalf("notification = %+v, want unread agent approval", notifs[0])
	}
}

func TestAgentHookPermissionCreatesWaitingNotification(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "019e2f71-82f0-76b0-9353-fbc4a662d442"
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', session_provider='claude', session_id=?, session_started=? WHERE slug='build-ui'`,
		sessionID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	payload := map[string]any{
		"hook_event_name": "PermissionRequest",
		"session_id":      sessionID,
		"tool_name":       "Bash",
		"tool_input": map[string]any{
			"command": "git status",
		},
	}
	resp, err := srv.ingestAgentHook(agentHookTestRequest("claude"), payload, agentHookTestRaw(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Task != "build-ui" || resp.Kind != "permission_request" || resp.NotificationID == "" {
		t.Fatalf("response = %+v", resp)
	}

	agent, err := srv.agentForTask("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != "waiting" || agent.WaitingFor == nil || agent.WaitingFor.Kind != "permission" {
		t.Fatalf("agent = %+v, want waiting for permission", agent)
	}
	monitor := srv.uiMonitor([]uiAgent{*agent})
	if monitor.Unread != 1 || monitor.Approvals != 1 {
		t.Fatalf("monitor = %+v, want one unread approval", monitor)
	}
}

func TestAgentHookPostToolUseClearsWaiting(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "019e2f71-82f0-76b0-9353-fbc4a662d442"
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', session_provider='codex', session_id=?, session_started=? WHERE slug='build-ui'`,
		sessionID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	permission := map[string]any{
		"hook_event_name": "PermissionRequest",
		"session_id":      sessionID,
		"tool_name":       "Bash",
	}
	if _, err := srv.ingestAgentHook(agentHookTestRequest("codex"), permission, agentHookTestRaw(t, permission)); err != nil {
		t.Fatal(err)
	}
	done := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      sessionID,
		"tool_name":       "Bash",
		"tool_use_id":     "toolu_123",
	}
	if _, err := srv.ingestAgentHook(agentHookTestRequest("codex"), done, agentHookTestRaw(t, done)); err != nil {
		t.Fatal(err)
	}

	agent, err := srv.agentForTask("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if agent.WaitingFor != nil || agent.Status == "waiting" {
		t.Fatalf("agent = %+v, want hook attention cleared", agent)
	}
	monitor := srv.uiMonitor([]uiAgent{*agent})
	if monitor.Approvals != 0 {
		t.Fatalf("monitor = %+v, want hook approval cleared", monitor)
	}
}

func TestAgentHookPreToolAskUserQuestionCreatesWaiting(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)
	sessionID := "019e2f71-82f0-76b0-9353-fbc4a662d442"
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', session_provider='codex', session_id=?, session_started=? WHERE slug='build-ui'`,
		sessionID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      sessionID,
		"tool_name":       "mcp__functions__request_user_input",
		"tool_input": map[string]any{
			"question": "Which branch should I use?",
		},
	}
	if _, err := srv.ingestAgentHook(agentHookTestRequest("codex"), payload, agentHookTestRaw(t, payload)); err != nil {
		t.Fatal(err)
	}

	agent, err := srv.agentForTask("build-ui")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != "waiting" || agent.WaitingFor == nil || agent.WaitingFor.Kind != "question" {
		t.Fatalf("agent = %+v, want waiting for question", agent)
	}
}

func agentHookTestRequest(provider string) *http.Request {
	return &http.Request{URL: &url.URL{RawQuery: "provider=" + provider}}
}

func agentHookTestRaw(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAgentAttentionNotificationCanBeDismissed(t *testing.T) {
	db, err := flowdb.OpenDB(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := &Server{cfg: Config{DB: db}}
	agents := []uiAgent{
		{
			Slug:      "switcher",
			Name:      "Switcher",
			Provider:  "codex",
			Status:    "waiting",
			SessionID: "019e2f71-82f0-76b0-9353-fbc4a662d442",
			WaitingFor: &uiWaitingFor{
				Kind: "permission",
				Why:  "Would you like to run the following command?",
			},
		},
	}
	if got := srv.uiMonitor(agents).Unread; got != 1 {
		t.Fatalf("unread before dismiss = %d, want 1", got)
	}

	resp, status := srv.updateNotification(actionRequest{Kind: "notification-dismiss", Target: "agent-switcher-permission"})
	if status != 200 || !resp.OK {
		t.Fatalf("dismiss response = %#v status %d", resp, status)
	}
	monitor := srv.uiMonitor(agents)
	if monitor.Unread != 0 || len(monitor.Notifications) != 0 {
		t.Fatalf("monitor after dismiss = %+v, want no agent notification", monitor)
	}
}
