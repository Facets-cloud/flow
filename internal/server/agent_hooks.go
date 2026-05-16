package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flow/internal/flowdb"
)

const agentHookMonitorSource = "agent_hook"

type agentHookIngestResponse struct {
	OK             bool   `json:"ok"`
	Provider       string `json:"provider,omitempty"`
	Event          string `json:"event,omitempty"`
	Kind           string `json:"kind,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	Task           string `json:"task,omitempty"`
	NotificationID string `json:"notification_id,omitempty"`
}

func (s *Server) handleAgentHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var payload map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&payload); err != nil {
		writeError(w, fmt.Errorf("decode hook payload: %w", err), http.StatusBadRequest)
		return
	}
	raw, _ := json.Marshal(payload)
	resp, err := s.ingestAgentHook(r, payload, string(raw))
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) ingestAgentHook(r *http.Request, payload map[string]any, raw string) (agentHookIngestResponse, error) {
	provider := agentHookProvider(r, payload)
	eventName := agentHookString(payload, "hook_event_name", "hookEventName")
	if eventName == "" {
		return agentHookIngestResponse{}, fmt.Errorf("hook_event_name is required")
	}
	sessionID := agentHookString(payload, "session_id", "sessionID")
	if sessionID == "" {
		sessionID = agentHookString(payload, "thread_id", "threadID")
	}
	kind := agentHookKind(eventName, payload)
	if kind == "" {
		kind = normalizeAgentHookPart(eventName)
	}
	resp := agentHookIngestResponse{
		OK:        true,
		Provider:  provider,
		Event:     eventName,
		Kind:      kind,
		SessionID: sessionID,
	}
	if sessionID != "" {
		if task, err := flowdb.TaskBySessionID(s.cfg.DB, sessionID); err == nil {
			resp.Task = task.Slug
		}
	}

	if agentHookClearsAttention(eventName, payload) && sessionID != "" {
		_ = s.clearAgentHookAttention(provider, sessionID)
	}
	if agentHookIsLowValueEvent(kind) {
		return resp, nil
	}

	sourceID := agentHookSourceID(provider, sessionID, kind, payload)
	title := agentHookTitle(provider, kind, resp.Task, payload)
	body := agentHookBody(payload)
	event, _, err := flowdb.UpsertMonitorEvent(s.cfg.DB, flowdb.MonitorEventInput{
		Source:   agentHookMonitorSource,
		Kind:     kind,
		SourceID: sourceID,
		Title:    title,
		Body:     body,
		URL:      agentHookURL(resp.Task),
		Severity: agentHookSeverity(kind),
		RawJSON:  raw,
	})
	if err != nil {
		return agentHookIngestResponse{}, err
	}
	if agentHookShouldNotify(kind) {
		level := agentHookNotificationLevel(kind)
		if err := flowdb.CreateNotificationForEvent(s.cfg.DB, *event, level); err != nil {
			return agentHookIngestResponse{}, err
		}
		resp.NotificationID = "notif-" + event.ID
	}
	return resp, nil
}

func (s *Server) clearAgentHookAttention(provider, sessionID string) error {
	prefix := agentHookSourceIDPrefix(provider, sessionID)
	rows, err := s.cfg.DB.Query(
		`SELECT id FROM monitor_events
		 WHERE source = ? AND source_id LIKE ? AND status IN ('new','notified')
		   AND kind IN ('permission_request','permission_prompt','elicitation','elicitation_dialog','idle_prompt')`,
		agentHookMonitorSource, prefix+"%",
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		_ = flowdb.UpdateMonitorEventStatus(s.cfg.DB, id, "done")
		_ = flowdb.UpdateNotificationStatus(s.cfg.DB, "notif-"+id, "actioned")
	}
	return nil
}

func (s *Server) agentHookWaitingFor(tv TaskView) *uiWaitingFor {
	if tv.SessionID == nil || strings.TrimSpace(*tv.SessionID) == "" {
		return nil
	}
	provider := "claude"
	if tv.SessionProvider != nil && strings.TrimSpace(*tv.SessionProvider) != "" {
		provider = *tv.SessionProvider
	}
	prefix := agentHookSourceIDPrefix(provider, *tv.SessionID)
	row := s.cfg.DB.QueryRow(
		`SELECT kind, title, body FROM monitor_events
		 WHERE source = ? AND source_id LIKE ? AND status IN ('new','notified')
		   AND kind IN ('permission_request','permission_prompt','elicitation','elicitation_dialog','idle_prompt')
		 ORDER BY last_seen_at DESC LIMIT 1`,
		agentHookMonitorSource, prefix+"%",
	)
	var kind, title string
	var body sql.NullString
	if err := row.Scan(&kind, &title, &body); err != nil {
		return nil
	}
	waitKind := "agent"
	switch kind {
	case "permission_request", "permission_prompt":
		waitKind = "permission"
	case "elicitation", "elicitation_dialog", "idle_prompt":
		waitKind = "question"
	}
	why := title
	if body.Valid && strings.TrimSpace(body.String) != "" {
		why = body.String
	}
	return &uiWaitingFor{Kind: waitKind, Cmd: "Open session " + tv.Slug, Why: truncateText(why, 220)}
}

func agentHookProvider(r *http.Request, payload map[string]any) string {
	provider := ""
	if r != nil {
		provider = strings.TrimSpace(r.URL.Query().Get("provider"))
	}
	if provider == "" {
		provider = agentHookString(payload, "provider", "session_provider", "sessionProvider")
	}
	if provider == "" {
		path := agentHookString(payload, "transcript_path", "transcriptPath")
		switch {
		case strings.Contains(path, ".codex"):
			provider = "codex"
		case strings.Contains(path, ".claude"):
			provider = "claude"
		}
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "codex":
		return "codex"
	default:
		return "claude"
	}
}

func agentHookKind(eventName string, payload map[string]any) string {
	event := normalizeAgentHookPart(eventName)
	if event == "notification" {
		if typ := normalizeAgentHookPart(agentHookString(payload, "notification_type", "notificationType")); typ != "" {
			return typ
		}
	}
	if event == "pre_tool_use" {
		tool := normalizeAgentHookPart(agentHookString(payload, "tool_name", "toolName"))
		switch {
		case tool == "ask_user_question", tool == "exit_plan_mode", tool == "request_user_input", strings.Contains(tool, "request_user_input"):
			return "elicitation"
		}
	}
	if event == "teammate_idle" {
		return "idle_prompt"
	}
	return event
}

func agentHookClearsAttention(eventName string, payload map[string]any) bool {
	switch normalizeAgentHookPart(eventName) {
	case "user_prompt_submit", "post_tool_use", "post_tool_use_failure", "post_tool_batch", "elicitation_result", "permission_denied", "session_start", "stop", "stop_failure", "session_end":
		return true
	case "notification":
		switch normalizeAgentHookPart(agentHookString(payload, "notification_type", "notificationType")) {
		case "elicitation_complete", "elicitation_response", "auth_success":
			return true
		}
	}
	return false
}

func agentHookIsLowValueEvent(kind string) bool {
	switch kind {
	case "post_tool_use", "post_tool_use_failure", "post_tool_batch", "user_prompt_submit", "elicitation_result":
		return true
	default:
		return false
	}
}

func agentHookShouldNotify(kind string) bool {
	switch kind {
	case "permission_request", "permission_prompt", "elicitation", "elicitation_dialog", "idle_prompt", "permission_denied", "session_start", "subagent_start", "subagent_stop", "task_created", "task_completed", "stop", "stop_failure", "session_end":
		return true
	default:
		return false
	}
}

func agentHookAttentionKind(kind string) bool {
	switch kind {
	case "permission_request", "permission_prompt", "elicitation", "elicitation_dialog", "idle_prompt":
		return true
	default:
		return false
	}
}

func agentHookNotificationLevel(kind string) string {
	switch kind {
	case "permission_request", "permission_prompt", "elicitation", "elicitation_dialog", "idle_prompt":
		return "approval"
	case "permission_denied":
		return "warning"
	case "stop_failure":
		return "error"
	case "session_start", "subagent_start", "subagent_stop", "task_completed", "session_end", "stop":
		return "info"
	default:
		return "info"
	}
}

func agentHookSeverity(kind string) string {
	switch agentHookNotificationLevel(kind) {
	case "approval", "error":
		return "high"
	case "warning":
		return "medium"
	default:
		return "low"
	}
}

func agentHookTitle(provider, kind, task string, payload map[string]any) string {
	label := provider
	if task != "" {
		label += " " + task
	}
	switch kind {
	case "permission_request", "permission_prompt":
		return label + " needs approval"
	case "permission_denied":
		return label + " permission denied"
	case "elicitation", "elicitation_dialog", "idle_prompt":
		return label + " needs input"
	case "stop":
		return label + " stopped"
	case "stop_failure":
		return label + " stopped with an error"
	case "session_start":
		return label + " started"
	case "subagent_start":
		if agentType := agentHookString(payload, "agent_type", "agentType"); agentType != "" {
			return label + " subagent started: " + agentType
		}
		return label + " subagent started"
	case "subagent_stop":
		if agentType := agentHookString(payload, "agent_type", "agentType"); agentType != "" {
			return label + " subagent stopped: " + agentType
		}
		return label + " subagent stopped"
	case "task_created":
		if subject := agentHookString(payload, "task_subject", "taskSubject"); subject != "" {
			return label + " created task: " + subject
		}
		return label + " created a task"
	case "task_completed":
		if subject := agentHookString(payload, "task_subject", "taskSubject"); subject != "" {
			return label + " completed task: " + subject
		}
		return label + " completed a task"
	case "session_end":
		reason := agentHookString(payload, "reason")
		if reason != "" {
			return label + " session ended: " + reason
		}
		return label + " session ended"
	default:
		return label + " " + strings.ReplaceAll(kind, "_", " ")
	}
}

func agentHookBody(payload map[string]any) string {
	for _, key := range []string{"message", "title", "reason", "last_assistant_message", "prompt"} {
		if value := agentHookString(payload, key); value != "" {
			return truncateText(value, 600)
		}
	}
	tool := agentHookString(payload, "tool_name", "toolName")
	if tool != "" {
		if b, ok := payload["tool_input"]; ok {
			if raw, err := json.Marshal(b); err == nil {
				return truncateText(tool+" "+string(raw), 600)
			}
		}
		return tool
	}
	return ""
}

func agentHookURL(task string) string {
	if task == "" {
		return ""
	}
	return "/session/" + task
}

func agentHookSourceID(provider, sessionID, kind string, payload map[string]any) string {
	parts := []string{provider}
	if sessionID != "" {
		parts = append(parts, sessionID)
	} else if path := agentHookString(payload, "transcript_path", "transcriptPath"); path != "" {
		parts = append(parts, path)
	} else {
		parts = append(parts, agentHookString(payload, "cwd"))
	}
	parts = append(parts, kind)
	for _, key := range []string{"tool_use_id", "toolUseID", "turn_id", "turnID", "notification_type", "notificationType", "reason"} {
		if v := agentHookString(payload, key); v != "" {
			parts = append(parts, v)
			return strings.Join(parts, ":")
		}
	}
	parts = append(parts, fmt.Sprintf("%d", time.Now().UnixNano()))
	return strings.Join(parts, ":")
}

func agentHookSourceIDPrefix(provider, sessionID string) string {
	return provider + ":" + sessionID + ":"
}

func agentHookString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			switch v := value.(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			case fmt.Stringer:
				if strings.TrimSpace(v.String()) != "" {
					return strings.TrimSpace(v.String())
				}
			}
		}
	}
	return ""
}

func normalizeAgentHookPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	var prevLower bool
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			if prevLower {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			prevLower = false
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevLower = true
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevLower = false
		default:
			if b.Len() > 0 {
				b.WriteByte('_')
			}
			prevLower = false
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return strings.Trim(out, "_")
}
