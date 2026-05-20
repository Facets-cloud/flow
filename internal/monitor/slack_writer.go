package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// SlackWriter posts back to Slack on behalf of flow. It is intentionally a
// thin client over chat.postMessage, chat.postEphemeral, and reactions.add
// — flow does not need blocks, attachments, file uploads, or the rest of the
// Web API surface in v1.
//
// Two safety invariants live in this type:
//
//  1. Writes are off by default. Enabled is gated by FLOW_SLACK_WRITES_ENABLED;
//     when off, every method returns nil after silently no-op'ing — so a
//     fresh `flow ui serve` install can listen and route without ever
//     talking back to Slack until the operator explicitly opts in.
//
//  2. PostMessage refuses to broadcast top-level into a public/private/group
//     channel. If the channel ID doesn't look like a DM (D-prefix) the call
//     MUST carry a thread_ts, otherwise the writer returns ErrChannelBroadcast
//     without making the HTTP request. Write-back is intended as a reply to
//     a known message, never a fresh top-level post.
type SlackWriter struct {
	Token   string
	BaseURL string

	// Enabled mirrors FLOW_SLACK_WRITES_ENABLED at construction time. Held
	// on the struct so tests can flip it explicitly without touching env.
	Enabled bool

	// HTTPClient is injected so tests can route at an httptest server. nil
	// falls through to http.DefaultClient.
	HTTPClient *http.Client
}

// NewSlackWriter constructs a writer from the explicit write token env
// (FLOW_SLACK_WRITE_TOKEN / SLACK_WRITE_TOKEN), then the same token family
// Socket Mode uses (SLACK_BOT_TOKEN / FLOW_SLACK_TOKEN / SLACK_USER_TOKEN),
// plus FLOW_SLACK_WRITES_ENABLED for the off-by-default gate.
func NewSlackWriter() *SlackWriter {
	return &SlackWriter{
		Token:   slackToken(),
		BaseURL: strings.TrimRight(firstNonEmpty(os.Getenv("FLOW_SLACK_API_BASE_URL"), "https://slack.com/api"), "/"),
		Enabled: slackWritesEnabled(),
	}
}

// ErrChannelBroadcast is returned by PostMessage when the safety guard
// prevents a top-level message to a non-DM conversation. Callers should
// treat this as a programming-error signal: pass a thread_ts derived from
// the originating message's ts (Slack accepts the parent ts to start a
// thread off it).
var ErrChannelBroadcast = errors.New("slack writer refuses top-level post to non-DM channel; pass thread_ts")

// ErrNoToken is returned when a write is attempted but FLOW_SLACK_TOKEN /
// SLACK_USER_TOKEN / SLACK_BOT_TOKEN / SLACK_TOKEN are all unset. Distinct
// from disabled so callers can distinguish "operator opted out" from
// "operator forgot the token".
var ErrNoToken = errors.New("slack writer has no token; set FLOW_SLACK_TOKEN or equivalent")

// PostMessage sends text into channel, threaded under threadTS. threadTS
// may be the originating top-level ts (which Slack accepts as "create a
// thread off this message") or an existing thread parent. Returns
// ErrChannelBroadcast when channel is non-DM and threadTS is empty.
//
// When Enabled is false the call is a successful no-op: the writer
// pretends to have posted so callers don't have to short-circuit at every
// call site. Disabling writes is operationally indistinguishable from
// "the message was sent into the void", and callers should treat it that way.
func (w *SlackWriter) PostMessage(ctx context.Context, channel, threadTS, text string) error {
	if w == nil || !w.Enabled {
		return nil
	}
	channel = strings.TrimSpace(channel)
	threadTS = strings.TrimSpace(threadTS)
	text = strings.TrimSpace(text)
	if channel == "" || text == "" {
		return fmt.Errorf("slack post: channel and text required")
	}
	if !isDMChannel(channel) && threadTS == "" {
		return ErrChannelBroadcast
	}
	if w.Token == "" {
		return ErrNoToken
	}
	body := map[string]any{"channel": channel, "text": text}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	var resp struct{}
	return w.callJSON(ctx, "chat.postMessage", body, &resp)
}

// PostEphemeral sends text visible only to user in channel. threadTS is
// optional but callers should pass it so the private notice stays
// visually attached to the source thread.
func (w *SlackWriter) PostEphemeral(ctx context.Context, channel, userID, threadTS, text string) error {
	if w == nil || !w.Enabled {
		return nil
	}
	channel = strings.TrimSpace(channel)
	userID = strings.TrimSpace(userID)
	threadTS = strings.TrimSpace(threadTS)
	text = strings.TrimSpace(text)
	if channel == "" || userID == "" || text == "" {
		return fmt.Errorf("slack ephemeral post: channel, user, and text required")
	}
	if w.Token == "" {
		return ErrNoToken
	}
	body := map[string]any{"channel": channel, "user": userID, "text": text}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	var resp struct{}
	return w.callJSON(ctx, "chat.postEphemeral", body, &resp)
}

// AddReaction adds emoji on the message identified by (channel, ts). emoji
// is the Slack short name with no surrounding colons (Slack's reactions.add
// rejects ":eyes:" — wants "eyes"). Idempotent: Slack returns ok=false
// "already_reacted" which AddReaction treats as success.
func (w *SlackWriter) AddReaction(ctx context.Context, channel, ts, emoji string) error {
	if w == nil || !w.Enabled {
		return nil
	}
	channel = strings.TrimSpace(channel)
	ts = strings.TrimSpace(ts)
	emoji = strings.Trim(strings.TrimSpace(emoji), ":")
	if channel == "" || ts == "" || emoji == "" {
		return fmt.Errorf("slack reaction: channel, ts, and emoji required")
	}
	if w.Token == "" {
		return ErrNoToken
	}
	body := map[string]any{"channel": channel, "timestamp": ts, "name": emoji}
	var resp struct{}
	err := w.callJSON(ctx, "reactions.add", body, &resp)
	if err != nil && strings.Contains(err.Error(), "already_reacted") {
		return nil
	}
	return err
}

// callJSON POSTs a JSON body to the named Slack Web API method.
// Treats 429 as a typed retry error so callers can choose to back off
// rather than swallow.
func (w *SlackWriter) callJSON(ctx context.Context, method string, body any, target any) error {
	base := strings.TrimRight(firstNonEmpty(w.BaseURL, "https://slack.com/api"), "/")
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("slack %s marshal: %w", method, err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, base+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.Token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	client := w.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%s rate limited; retry after %s seconds", method, resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var probe struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &probe); err != nil {
		return fmt.Errorf("slack %s parse: %w", method, err)
	}
	if !probe.OK {
		if probe.Error == "" {
			probe.Error = "ok=false"
		}
		return fmt.Errorf("slack %s: %s", method, probe.Error)
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(respBody, target)
}

// isDMChannel reports whether a Slack channel ID is a direct-message
// conversation, where top-level messages are safe. Slack ID prefixes:
//
//	D...   = direct message (1:1)
//	G...   = group / private channel (legacy id form), also mpim
//	C...   = public channel
//
// Multi-party DMs (mpim) have IDs starting with "G" in the new Slack
// model — those are treated as channels here, requiring thread_ts.
// That's a deliberate over-restriction: posting top-level into an mpim
// surprises the other participants the same way posting into a channel
// does.
func isDMChannel(id string) bool {
	return len(id) > 0 && (id[0] == 'D' || id[0] == 'd')
}

func slackWritesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FLOW_SLACK_WRITES_ENABLED"))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
