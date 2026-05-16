package server

import (
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

func TestAttentionDetectionCatchesPermissionsAndQuestions(t *testing.T) {
	kind, excerpt := attentionFromText("Would you like to run the following command?\n$ kill 123\n1. Yes, proceed")
	if kind != "permission" || excerpt == "" {
		t.Fatalf("permission attention = kind %q excerpt %q", kind, excerpt)
	}
	action, question, kind := latestTranscriptAction([]uiTranscript{{Type: "assistant", Text: "Should I proceed with the deploy?"}})
	if kind != "question" || question == "" || action == "" {
		t.Fatalf("question action=%q question=%q kind=%q", action, question, kind)
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
