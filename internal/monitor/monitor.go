package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"flow/internal/flowdb"
)

type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type Poller struct {
	DB              *sql.DB
	Runner          CommandRunner
	SlackAPIBaseURL string
}

type PollSummary struct {
	Source   string   `json:"source"`
	Events   int      `json:"events"`
	New      int      `json:"new"`
	Errors   []string `json:"errors,omitempty"`
	LastSync string   `json:"last_sync"`
}

func (p Poller) Poll(ctx context.Context, source string) ([]PollSummary, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" || source == "all" {
		sources := []string{"github", "slack"}
		out := make([]PollSummary, 0, len(sources))
		for _, src := range sources {
			sum, err := p.pollOne(ctx, src)
			if err != nil {
				sum.Source = src
				sum.Errors = append(sum.Errors, err.Error())
			}
			out = append(out, sum)
		}
		return out, nil
	}
	sum, err := p.pollOne(ctx, source)
	if err != nil {
		sum.Source = source
		sum.Errors = append(sum.Errors, err.Error())
		return []PollSummary{sum}, nil
	}
	return []PollSummary{sum}, nil
}

func (p Poller) pollOne(ctx context.Context, source string) (PollSummary, error) {
	if p.DB == nil {
		return PollSummary{}, errors.New("monitor poller has no database")
	}
	if err := flowdb.EnsureDefaultAutomationRules(p.DB); err != nil {
		return PollSummary{}, err
	}
	switch source {
	case "github", "gh":
		return p.pollGitHub(ctx)
	case "slack":
		return p.pollSlack(ctx)
	default:
		return PollSummary{Source: source}, fmt.Errorf("unsupported monitor source %q", source)
	}
}

func (p Poller) pollGitHub(ctx context.Context) (PollSummary, error) {
	sum := PollSummary{Source: "github", LastSync: flowdb.NowISO()}
	commands := []struct {
		kind string
		args []string
	}{
		{kind: "review_requested", args: []string{"pr", "list", "--search", "review-requested:@me is:open", "--json", "number,title,url,author,updatedAt"}},
		{kind: "ci_failed", args: []string{"pr", "list", "--search", "author:@me is:open", "--json", "number,title,url,statusCheckRollup,updatedAt"}},
		{kind: "assigned_issue", args: []string{"issue", "list", "--assignee", "@me", "--state", "open", "--json", "number,title,url,updatedAt"}},
	}
	for _, cmd := range commands {
		out, err := p.run(ctx, "gh", cmd.args...)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
			continue
		}
		events, err := githubEvents(cmd.kind, out)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
			continue
		}
		kept, newCount, err := p.storeEvents(events)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
			continue
		}
		sum.Events += kept
		sum.New += newCount
	}
	out, err := p.run(ctx, "gh", "api", "notifications")
	if err == nil {
		events, err := githubNotifications(out)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
		} else {
			kept, newCount, err := p.storeEvents(events)
			if err != nil {
				sum.Errors = append(sum.Errors, err.Error())
			}
			sum.Events += kept
			sum.New += newCount
		}
	}
	closed, err := p.closeMergedLinkedPRs(ctx)
	if err != nil {
		sum.Errors = append(sum.Errors, err.Error())
	} else if closed > 0 {
		sum.Events += closed
	}
	return sum, nil
}

func (p Poller) pollSlack(ctx context.Context) (PollSummary, error) {
	sum := PollSummary{Source: "slack", LastSync: flowdb.NowISO()}
	if custom := strings.TrimSpace(os.Getenv("FLOW_SLACK_POLL_CMD")); custom != "" {
		out, err := p.run(ctx, "sh", "-lc", custom)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
			return sum, nil
		}
		events, err := slackEvents(out)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
			return sum, nil
		}
		sum.Events, sum.New, err = p.storeEvents(events)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
		}
		return sum, nil
	}
	if token := slackToken(); token != "" {
		events, err := p.slackAPIEvents(ctx, token)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
			return sum, nil
		}
		sum.Events, sum.New, err = p.storeEvents(events)
		if err != nil {
			sum.Errors = append(sum.Errors, err.Error())
		}
		return sum, nil
	}
	out, err := p.run(ctx, "slack", "notifications", "list", "--json")
	if err != nil {
		sum.Errors = append(sum.Errors, "slack polling needs FLOW_SLACK_TOKEN/SLACK_USER_TOKEN/SLACK_BOT_TOKEN or FLOW_SLACK_POLL_CMD; installed slack CLI has no inbox API: "+err.Error())
		return sum, nil
	}
	events, err := slackEvents(out)
	if err != nil {
		sum.Errors = append(sum.Errors, err.Error())
		return sum, nil
	}
	sum.Events, sum.New, err = p.storeEvents(events)
	if err != nil {
		sum.Errors = append(sum.Errors, err.Error())
	}
	return sum, nil
}

func (p Poller) slackAPIEvents(ctx context.Context, token string) ([]flowdb.MonitorEventInput, error) {
	var auth slackAuthResponse
	if err := p.slackAPICall(ctx, token, "auth.test", nil, &auth); err != nil {
		return nil, fmt.Errorf("slack auth.test: %w", err)
	}
	if auth.UserID == "" {
		return nil, errors.New("slack auth.test returned no user_id")
	}
	conversations, err := p.slackConversations(ctx, token)
	if err != nil {
		return nil, err
	}
	oldest := slackOldest()
	includeChannelMessages := envBool("FLOW_SLACK_INCLUDE_CHANNEL_MESSAGES")
	allowlist := slackChannelAllowlist()
	out := []flowdb.MonitorEventInput{}
	for _, conv := range conversations {
		if conv.ID == "" || conv.IsArchived {
			continue
		}
		configured := allowlist[conv.ID] || (conv.Name != "" && allowlist[strings.ToLower(conv.Name)])
		if len(allowlist) > 0 && !configured && !conv.IsIM && !conv.IsMPIM {
			continue
		}
		messages, err := p.slackHistory(ctx, token, conv.ID, oldest)
		if err != nil {
			return out, err
		}
		for _, msg := range messages {
			if msg.Type != "" && msg.Type != "message" {
				continue
			}
			if msg.Subtype != "" && msg.Subtype != "bot_message" {
				continue
			}
			text := strings.TrimSpace(msg.Text)
			if text == "" || msg.TS == "" {
				continue
			}
			mention := strings.Contains(text, "<@"+auth.UserID+">")
			kind := "channel_message"
			if conv.IsIM || conv.IsMPIM {
				kind = "dm"
			} else if mention {
				kind = "mention"
			} else if !includeChannelMessages && !configured {
				continue
			}
			permalink := ""
			if link, err := p.slackPermalink(ctx, token, conv.ID, msg.TS); err == nil {
				permalink = link
			}
			raw, _ := json.Marshal(map[string]any{"conversation": conv, "message": msg})
			out = append(out, flowdb.MonitorEventInput{
				Source:   "slack",
				Kind:     kind,
				SourceID: conv.ID + ":" + msg.TS,
				Title:    "Slack " + strings.ReplaceAll(kind, "_", " ") + " in " + conv.Label(),
				Body:     text,
				URL:      permalink,
				Severity: "medium",
				RawJSON:  string(raw),
			})
		}
	}
	return out, nil
}

func (p Poller) slackConversations(ctx context.Context, token string) ([]slackConversation, error) {
	maxConversations := envInt("FLOW_SLACK_MAX_CONVERSATIONS", 60)
	if maxConversations <= 0 {
		maxConversations = 60
	}
	var out []slackConversation
	cursor := ""
	for len(out) < maxConversations {
		params := url.Values{
			"exclude_archived": {"true"},
			"limit":            {"200"},
			"types":            {"im,mpim,private_channel,public_channel"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp slackConversationsResponse
		if err := p.slackAPICall(ctx, token, "users.conversations", params, &resp); err != nil {
			return out, fmt.Errorf("slack users.conversations: %w", err)
		}
		out = append(out, resp.Channels...)
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(out) > maxConversations {
		out = out[:maxConversations]
	}
	return out, nil
}

func (p Poller) slackHistory(ctx context.Context, token, channelID, oldest string) ([]slackMessage, error) {
	params := url.Values{"channel": {channelID}, "limit": {"25"}}
	if oldest != "" {
		params.Set("oldest", oldest)
	}
	var resp slackHistoryResponse
	if err := p.slackAPICall(ctx, token, "conversations.history", params, &resp); err != nil {
		return nil, fmt.Errorf("slack conversations.history %s: %w", channelID, err)
	}
	return resp.Messages, nil
}

func (p Poller) slackPermalink(ctx context.Context, token, channelID, ts string) (string, error) {
	params := url.Values{"channel": {channelID}, "message_ts": {ts}}
	var resp slackPermalinkResponse
	if err := p.slackAPICall(ctx, token, "chat.getPermalink", params, &resp); err != nil {
		return "", err
	}
	return resp.Permalink, nil
}

func (p Poller) slackAPICall(ctx context.Context, token, method string, params url.Values, target any) error {
	base := strings.TrimRight(firstNonEmpty(p.SlackAPIBaseURL, os.Getenv("FLOW_SLACK_API_BASE_URL"), "https://slack.com/api"), "/")
	u, err := url.Parse(base + "/" + method)
	if err != nil {
		return err
	}
	q := u.Query()
	for key, values := range params {
		for _, value := range values {
			q.Add(key, value)
		}
	}
	u.RawQuery = q.Encode()
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%s rate limited; retry after %s seconds", method, resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var probe struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return err
	}
	if !probe.OK {
		if probe.Error == "" {
			probe.Error = "ok=false"
		}
		return errors.New(probe.Error)
	}
	return json.Unmarshal(body, target)
}

func (p Poller) storeEvents(events []flowdb.MonitorEventInput) (int, int, error) {
	kept := 0
	newCount := 0
	for _, event := range events {
		ev, isNew, err := flowdb.UpsertMonitorEvent(p.DB, event)
		if err != nil {
			return kept, newCount, err
		}
		if ev == nil {
			continue
		}
		kept++
		if isNew {
			newCount++
		}
		if err := p.applyRule(*ev); err != nil {
			return kept, newCount, err
		}
	}
	return kept, newCount, nil
}

func (p Poller) closeMergedLinkedPRs(ctx context.Context) (int, error) {
	links, err := flowdb.ListOpenTaskPRLinks(p.DB)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, link := range links {
		out, err := p.run(ctx, "gh", "pr", "view", link.PRURL, "--json", "state,mergedAt,url,number,title")
		if err != nil {
			return closed, err
		}
		var data map[string]any
		if err := json.Unmarshal(out, &data); err != nil {
			return closed, fmt.Errorf("parse gh pr view for %s: %w", link.PRURL, err)
		}
		state := strings.ToUpper(stringField(data, "state"))
		mergedAt := stringField(data, "mergedAt")
		if state != "MERGED" && mergedAt == "" {
			continue
		}
		if err := flowdb.MarkTaskPRMerged(p.DB, link.TaskSlug, link.Repo, link.PRNumber, mergedAt); err != nil {
			return closed, err
		}
		if _, err := flowdb.MarkTaskDoneIfSessionBound(p.DB, link.TaskSlug); err != nil {
			return closed, err
		}
		closed++
	}
	return closed, nil
}

func (p Poller) applyRule(event flowdb.MonitorEvent) error {
	mode, err := flowdb.AutomationModeFor(p.DB, event.Source, event.Kind)
	if err != nil {
		return err
	}
	switch mode {
	case "off", "log":
		return nil
	case "approval":
		return flowdb.CreateNotificationForEvent(p.DB, event, "approval")
	case "auto_agent", "auto_agent_draft_only":
		return flowdb.CreateNotificationForEvent(p.DB, event, "success")
	case "auto_task", "summarize", "notify":
		return flowdb.CreateNotificationForEvent(p.DB, event, "info")
	default:
		return flowdb.CreateNotificationForEvent(p.DB, event, "info")
	}
}

func (p Poller) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	runner := p.Runner
	if runner == nil {
		runner = defaultRunner
	}
	return runner(ctx, name, args...)
}

func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if text != "" {
			return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func githubEvents(kind string, data []byte) ([]flowdb.MonitorEventInput, error) {
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse gh %s json: %w", kind, err)
	}
	out := []flowdb.MonitorEventInput{}
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		number := jsonNumber(row["number"])
		title := stringField(row, "title")
		url := stringField(row, "url")
		repo := firstNonEmpty(repoName(row["repository"]), repoFromGitHubURL(url))
		sourceID := fmt.Sprintf("%s:%s:%d", kind, repo, number)
		if kind == "ci_failed" && !looksFailed(row["statusCheckRollup"]) {
			continue
		}
		displayKind := kind
		if kind == "review_requested" {
			displayKind = "review requested"
		} else if kind == "ci_failed" {
			displayKind = "CI failed"
		} else if kind == "assigned_issue" {
			displayKind = "assigned issue"
		}
		out = append(out, flowdb.MonitorEventInput{
			Source:   "github",
			Kind:     kind,
			SourceID: sourceID,
			Title:    fmt.Sprintf("%s: %s #%d", displayKind, repo, number),
			Body:     title,
			URL:      url,
			Severity: severityForKind(kind),
			RawJSON:  string(raw),
		})
	}
	return out, nil
}

func githubNotifications(data []byte) ([]flowdb.MonitorEventInput, error) {
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse gh notifications json: %w", err)
	}
	out := []flowdb.MonitorEventInput{}
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		subject, _ := row["subject"].(map[string]any)
		repo := repoName(row["repository"])
		id := stringField(row, "id")
		title := stringField(subject, "title")
		url := stringField(subject, "url")
		if url == "" {
			url = stringField(row, "url")
		}
		if webURL := githubWebURL(url); webURL != "" {
			url = webURL
		}
		if id == "" || title == "" {
			continue
		}
		out = append(out, flowdb.MonitorEventInput{
			Source:   "github",
			Kind:     "notification",
			SourceID: "notification:" + id,
			Title:    "GitHub notification: " + title,
			Body:     repo,
			URL:      url,
			Severity: "medium",
			RawJSON:  string(raw),
		})
	}
	return out, nil
}

type slackEnvelope struct {
	OK               bool `json:"ok"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

type slackAuthResponse struct {
	slackEnvelope
	UserID string `json:"user_id"`
	TeamID string `json:"team_id"`
	URL    string `json:"url"`
}

type slackConversationsResponse struct {
	slackEnvelope
	Channels []slackConversation `json:"channels"`
}

type slackHistoryResponse struct {
	slackEnvelope
	Messages []slackMessage `json:"messages"`
}

type slackPermalinkResponse struct {
	slackEnvelope
	Permalink string `json:"permalink"`
}

type slackConversation struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	User       string `json:"user"`
	IsIM       bool   `json:"is_im"`
	IsMPIM     bool   `json:"is_mpim"`
	IsChannel  bool   `json:"is_channel"`
	IsGroup    bool   `json:"is_group"`
	IsArchived bool   `json:"is_archived"`
}

func (c slackConversation) Label() string {
	if c.Name != "" {
		return "#" + c.Name
	}
	if c.User != "" {
		return c.User
	}
	return c.ID
}

type slackMessage struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	User     string `json:"user"`
	Username string `json:"username"`
	BotID    string `json:"bot_id"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
}

func slackEvents(data []byte) ([]flowdb.MonitorEventInput, error) {
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse slack json: %w", err)
	}
	out := []flowdb.MonitorEventInput{}
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		id := firstString(row, "source_id", "id", "ts", "timestamp")
		text := firstString(row, "text", "body", "message", "title")
		channel := firstString(row, "channel", "channel_name", "conversation")
		url := firstString(row, "url", "permalink")
		kind := firstString(row, "kind", "type")
		if kind == "" {
			kind = "mention"
		}
		if id == "" {
			id = channel + ":" + text
		}
		if text == "" {
			continue
		}
		title := "Slack " + strings.ReplaceAll(kind, "_", " ")
		if channel != "" {
			title += " in " + channel
		}
		out = append(out, flowdb.MonitorEventInput{
			Source:   "slack",
			Kind:     kind,
			SourceID: id,
			Title:    title,
			Body:     text,
			URL:      url,
			Severity: firstNonEmpty(firstString(row, "severity"), "medium"),
			RawJSON:  string(raw),
		})
	}
	return out, nil
}

func repoName(v any) string {
	if m, ok := v.(map[string]any); ok {
		return firstNonEmpty(stringField(m, "nameWithOwner"), stringField(m, "full_name"), stringField(m, "name"))
	}
	return ""
}

func repoFromGitHubURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Host, "github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func githubWebURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if strings.EqualFold(u.Host, "github.com") {
		return raw
	}
	if !strings.EqualFold(u.Host, "api.github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "repos" {
		return ""
	}
	switch parts[3] {
	case "pulls":
		return "https://github.com/" + parts[1] + "/" + parts[2] + "/pull/" + parts[4]
	case "issues":
		return "https://github.com/" + parts[1] + "/" + parts[2] + "/issues/" + parts[4]
	default:
		return ""
	}
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := stringField(m, key); s != "" {
			return s
		}
	}
	return ""
}

func jsonNumber(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func looksFailed(v any) bool {
	raw, _ := json.Marshal(v)
	text := strings.ToLower(string(raw))
	return strings.Contains(text, "failure") || strings.Contains(text, "failed") || strings.Contains(text, "error") || strings.Contains(text, "cancelled")
}

func severityForKind(kind string) string {
	if kind == "ci_failed" {
		return "high"
	}
	return "medium"
}

func slackToken() string {
	return firstNonEmpty(
		os.Getenv("FLOW_SLACK_TOKEN"),
		os.Getenv("SLACK_USER_TOKEN"),
		os.Getenv("SLACK_BOT_TOKEN"),
		os.Getenv("SLACK_TOKEN"),
	)
}

func slackOldest() string {
	lookback := 2 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("FLOW_SLACK_LOOKBACK")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			lookback = parsed
		}
	}
	ts := float64(time.Now().Add(-lookback).UnixNano()) / float64(time.Second)
	return strconv.FormatFloat(ts, 'f', 6, 64)
}

func slackChannelAllowlist() map[string]bool {
	out := map[string]bool{}
	for _, raw := range strings.Split(os.Getenv("FLOW_SLACK_CHANNELS"), ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		item = strings.TrimPrefix(item, "#")
		out[item] = true
		out[strings.ToLower(item)] = true
	}
	return out
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
