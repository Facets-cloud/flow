package app

import "testing"

func TestDetectAgentBackend(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want agentBackend
	}{
		{name: "override codex", env: map[string]string{"FLOW_AGENT_BACKEND": "codex", "CLAUDECODE": "1"}, want: backendCodex},
		{name: "override claude", env: map[string]string{"FLOW_AGENT_BACKEND": "claude", "CODEX_THREAD_ID": "abc"}, want: backendClaude},
		{name: "codex marker", env: map[string]string{"CODEX_WORKING_DIR": "/tmp/repo"}, want: backendCodex},
		{name: "claude marker", env: map[string]string{"CLAUDECODE": "1"}, want: backendClaude},
		{name: "empty fallback", env: map[string]string{}, want: backendClaude},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{
				"FLOW_AGENT_BACKEND",
				"CODEX_THREAD_ID",
				"CODEX_WORKING_DIR",
				"CODEX_CI",
				"CODEX_INTERNAL_ORIGINATOR_OVERRIDE",
				"CLAUDECODE",
				"CLAUDE_AGENT_SDK_VERSION",
				"CLAUDE_CODE_ENTRYPOINT",
				"CLAUDE_CODE_SSE_PORT",
			} {
				t.Setenv(key, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := detectAgentBackend(); got != tt.want {
				t.Fatalf("detectAgentBackend() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestParseAgentBackendRejectsUnknown(t *testing.T) {
	if _, err := parseAgentBackend("other"); err == nil {
		t.Fatal("expected error")
	}
}
