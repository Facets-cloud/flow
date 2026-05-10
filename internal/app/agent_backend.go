package app

import (
	"fmt"
	"os"
	"strings"
)

type agentBackend string

const (
	backendClaude agentBackend = "claude"
	backendCodex  agentBackend = "codex"
)

func (b agentBackend) String() string {
	return string(b)
}

func parseAgentBackend(s string) (agentBackend, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude":
		return backendClaude, nil
	case "codex":
		return backendCodex, nil
	default:
		return "", fmt.Errorf("unknown backend %q (want claude or codex)", s)
	}
}

func detectAgentBackend() agentBackend {
	if forced := os.Getenv("FLOW_AGENT_BACKEND"); forced != "" {
		if b, err := parseAgentBackend(forced); err == nil {
			return b
		}
	}
	if os.Getenv("CODEX_THREAD_ID") != "" ||
		os.Getenv("CODEX_WORKING_DIR") != "" ||
		os.Getenv("CODEX_CI") != "" ||
		os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE") != "" {
		return backendCodex
	}
	if os.Getenv("CLAUDECODE") != "" ||
		os.Getenv("CLAUDE_AGENT_SDK_VERSION") != "" ||
		os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" ||
		os.Getenv("CLAUDE_CODE_SSE_PORT") != "" {
		return backendClaude
	}
	return backendClaude
}
