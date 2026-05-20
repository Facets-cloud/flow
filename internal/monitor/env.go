// Package monitor hosts flow's Slack integration: a Web API client
// (SlackWriter) for posting back to Slack, plus environment-derived
// configuration shared by the writer and (in future) the Socket Mode
// listener.
//
// Tokens come from environment variables with a deliberate precedence:
// explicit write tokens (FLOW_SLACK_WRITE_TOKEN, SLACK_WRITE_TOKEN) win
// when set so operators can grant a separate "post on my behalf" token
// without exposing the broader read-side token. Otherwise the bot or
// user token from Socket Mode setup is reused.
package monitor

import (
	"os"
	"strings"
)

// SlackBotToken resolves a bot/user token for read-side Slack API calls
// and as a fallback for writes. The Socket Mode listener uses this for
// any HTTP API call it needs to make alongside its WebSocket connection.
func SlackBotToken() string {
	return firstNonEmpty(
		os.Getenv("SLACK_BOT_TOKEN"),
		os.Getenv("FLOW_SLACK_TOKEN"),
		os.Getenv("SLACK_USER_TOKEN"),
		os.Getenv("SLACK_TOKEN"),
	)
}

// SlackUserToken resolves the xoxp- user token. Used when the listener
// needs to act on behalf of the user (chat.postMessage as them, not as
// a bot). Falls back to SlackBotToken's token family for single-token
// setups.
func SlackUserToken() string {
	return firstNonEmpty(
		os.Getenv("FLOW_SLACK_USER_TOKEN"),
		os.Getenv("SLACK_USER_TOKEN"),
		os.Getenv("FLOW_SLACK_TOKEN"),
		os.Getenv("SLACK_TOKEN"),
	)
}

// slackToken returns the token SlackWriter should use for outbound calls.
// Explicit write tokens win; otherwise we reuse the read-side token.
func slackToken() string {
	return firstNonEmpty(
		os.Getenv("FLOW_SLACK_WRITE_TOKEN"),
		os.Getenv("SLACK_WRITE_TOKEN"),
		SlackBotToken(),
		SlackUserToken(),
	)
}

// firstNonEmpty returns the first trimmed-nonempty string in values,
// or "" if all are empty/whitespace.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// envBoolDefault reads name as a boolean with fallback. Recognized truthy
// values: 1, true, yes, y, on. Recognized falsy: 0, false, no, n, off.
// Anything else returns fallback.
func envBoolDefault(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}
