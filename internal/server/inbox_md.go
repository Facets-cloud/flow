package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/monitor"
)

// slackIDTokenRe matches a bare Slack id token (optionally @/# prefixed). A
// real id is an uppercase type letter — U/W (user), C/G/D (channel) — followed
// by 8–10 uppercase-alnum chars. The digit requirement (checked separately)
// keeps ordinary all-caps words like "UPDATED" from matching.
var slackIDTokenRe = regexp.MustCompile(`([@#]?)([UWCDG][A-Z0-9]{8,10})`)

func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// scrubSlackIDs swaps bare Slack user/channel ids embedded in a string —
// typically a task name generated before name resolution existed (e.g. a DM
// task literally named "U01RKJ5J9EK", or "#chan - @U03… msg") — for resolved
// display names, so no raw id ever reaches the UI. Unresolvable ids degrade to
// a neutral label rather than leak. Cheap for the common case: the regex only
// matches id-shaped tokens, and resolution is cached.
func (s *Server) scrubSlackIDs(ctx context.Context, text string) string {
	if text == "" {
		return text
	}
	return slackIDTokenRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := slackIDTokenRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		prefix, id := sub[1], sub[2]
		if !containsDigit(id) {
			return match // an ordinary uppercase word, not an id
		}
		switch id[0] {
		case 'U', 'W':
			name := s.nameResolver.UserName(ctx, id)
			if name == "" {
				name = "unknown"
			}
			if prefix == "@" {
				return "@" + name
			}
			return name
		case 'C', 'G', 'D':
			if name := s.nameResolver.ChannelName(ctx, id); name != "" {
				return name // already #-prefixed
			}
			return "channel"
		}
		return match
	})
}

// readInboxEntries parses ~/.flow/tasks/<slug>/inbox.md into structured
// entries. The format is the one `flow tell <slug>` writes — `## TIMESTAMP
// — from: WHO` headers separating message bodies. This is the per-task
// agent-to-agent inbox, distinct from any external notification system.
func readInboxEntries(path string) ([]InboxEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []InboxEntry
	var current *InboxEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") && strings.Contains(line, " — from: ") {
			if current != nil {
				current.Body = strings.TrimSpace(current.Body)
				entries = append(entries, *current)
			}
			head := strings.TrimPrefix(line, "## ")
			parts := strings.SplitN(head, " — from: ", 2)
			current = &InboxEntry{Timestamp: parts[0]}
			if len(parts) == 2 {
				current.Sender = parts[1]
			}
			continue
		}
		if current == nil {
			continue // pre-header preamble (`# Inbox` etc.)
		}
		current.Body += line + "\n"
	}
	if err := scanner.Err(); err != nil {
		return entries, err
	}
	if current != nil {
		current.Body = strings.TrimSpace(current.Body)
		entries = append(entries, *current)
	}
	return entries, nil
}

// unreadInboxCount returns how many entries are newer than the task's
// inbox_seen_at watermark. Entries whose timestamps fail to parse are
// skipped (treated as "old enough") to avoid over-counting on malformed
// lines.
func unreadInboxCount(task *flowdb.Task, entries []InboxEntry) int {
	if len(entries) == 0 {
		return 0
	}
	if !task.InboxSeenAt.Valid || strings.TrimSpace(task.InboxSeenAt.String) == "" {
		return len(entries)
	}
	seenAt, err := time.Parse(time.RFC3339, task.InboxSeenAt.String)
	if err != nil {
		return len(entries)
	}
	count := 0
	for _, e := range entries {
		entryAt, err := time.Parse("2006-01-02 15:04:05Z", e.Timestamp)
		if err != nil {
			continue
		}
		if entryAt.After(seenAt) {
			count++
		}
	}
	return count
}

// inboxFeedBodySnippet trims an inbox body for list-view display. We keep
// the full body in the response so the UI can expand on demand, but the
// list itself shouldn't carry an unbounded blob per row.
func inboxFeedBodySnippet(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		body = body[:idx]
	}
	if len(body) > 220 {
		body = body[:220] + "…"
	}
	return body
}

// inboxFeedTimestampSortKey normalises a timestamp string for sort/compare.
// Handles inbox.md headers ("2006-01-02 15:04:05Z") AND inbox.jsonl
// enqueued_at (RFC3339). Falls back to the raw string on parse failure.
func inboxFeedTimestampSortKey(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return ""
	}
	if t, err := time.Parse("2006-01-02 15:04:05Z", ts); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return ts
}

// inboxJSONLTitle returns a short human-readable title for an InboundEvent
// — used as the row "from" label and sort-stable title in the global feed.
// Kept terse: "pr_review_requested" → "PR review requested".
func inboxJSONLTitle(ev monitor.InboundEvent) string {
	kind := strings.TrimSpace(ev.Kind)
	if kind == "" {
		return "event"
	}
	switch kind {
	case "pr_review_requested":
		return "PR review requested"
	case "pr_review_comment":
		return "PR review comment"
	case "pr_review_changes_requested":
		return "PR changes requested"
	case "pr_head_updated":
		return "PR head updated"
	case "issue_opened":
		return "Issue opened"
	case "issue_comment":
		return "Issue comment"
	case "message":
		return "Slack message"
	case "app_mention":
		return "Slack mention"
	case "reaction_added":
		if ev.Reaction != "" {
			return "Reaction :" + ev.Reaction + ":"
		}
		return "Reaction"
	}
	// Fallback: humanise the snake_case kind.
	return strings.ReplaceAll(kind, "_", " ")
}

// inboxJSONLSender picks the most useful "from" label for a structured
// event. Falls back to the channel when no user id is attached
// (PR head_updated, CI-triggered events, etc.).
func inboxJSONLSender(ev monitor.InboundEvent) string {
	if u := strings.TrimSpace(ev.UserID); u != "" {
		return u
	}
	if a := strings.TrimSpace(ev.ItemAuthor); a != "" {
		return a
	}
	if c := strings.TrimSpace(ev.Channel); c != "" {
		return c
	}
	if c := strings.TrimSpace(ev.ChannelType); c != "" {
		return c
	}
	return "unknown"
}

// inboxJSONLEntryBody renders an event body for the feed snippet. Title +
// blank line + Text preserves PR descriptions and Slack messages
// in their original form; pure reaction events just get the title.
func inboxJSONLEntryBody(ev monitor.InboundEvent) string {
	title := inboxJSONLTitle(ev)
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return title
	}
	return title + "\n\n" + text
}

// handleInbox aggregates every non-archived task's inbox.jsonl into a
// single feed. inbox.jsonl is the structured event log appended by the
// monitor listeners — one line per Slack message or GitHub PR event
// (monitor.InboundEvent shape).
//
// The legacy per-task inbox.md (free-form "flow tell" notes) is
// deliberately not aggregated here: it was a dumping ground for the
// pre-d2900a0 attention-monitor lifecycle that has since been removed.
// The per-task /api/tasks/<slug>/inbox endpoint still surfaces it for
// the task detail drawer.
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	if r.URL.Path != "/api/inbox" {
		http.NotFound(w, r)
		return
	}
	tasks, err := flowdb.ListTasks(s.cfg.DB, flowdb.TaskFilter{IncludeArchived: false})
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	live, _ := s.cachedLiveAgentSessions()
	entries := make([]InboxFeedEntry, 0, 64)
	unread := 0
	taskCount := 0
	for _, t := range tasks {
		jsonlEntries, jerr := monitor.ReadInboxEntries(t.Slug)
		if jerr != nil || len(jsonlEntries) == 0 {
			continue
		}
		taskLive := t.SessionID.Valid && t.SessionID.String != "" && live[strings.ToLower(t.SessionID.String)]
		taskMonitored := s.inboxMonitors != nil && s.inboxMonitors.running(t.Slug)
		taskName := s.scrubSlackIDs(ctx, t.Name)
		var project *string
		if t.ProjectSlug.Valid && t.ProjectSlug.String != "" {
			ps := t.ProjectSlug.String
			project = &ps
		}
		var seenAt time.Time
		hasSeen := false
		if t.InboxSeenAt.Valid && strings.TrimSpace(t.InboxSeenAt.String) != "" {
			if v, perr := time.Parse(time.RFC3339, t.InboxSeenAt.String); perr == nil {
				seenAt = v
				hasSeen = true
			}
		}
		// A task with an active or completed agent has effectively been
		// "read" by that agent — the events were delivered into its session.
		// Only backlog tasks (no agent yet) should count entries as unread.
		agentHandled := t.Status == "in-progress" || t.Status == "done"
		taskCount++
		for _, je := range jsonlEntries {
			// enqueued_at is the wall-clock when the listener received the
			// event — preferred for "what's new on the inbox" sorting since
			// it's both monotonic-ish and human-readable.
			ts := strings.TrimSpace(je.EnqueuedAt)
			if ts == "" {
				ts = strings.TrimSpace(je.Event.TS)
			}
			isUnread := false
			if !agentHandled {
				isUnread = !hasSeen
				if hasSeen {
					if entryAt, perr := time.Parse(time.RFC3339, ts); perr == nil && entryAt.After(seenAt) {
						isUnread = true
					}
				}
			}
			if isUnread {
				unread++
			}
			body := inboxJSONLEntryBody(je.Event)
			entries = append(entries, InboxFeedEntry{
				TaskSlug:    t.Slug,
				TaskName:    taskName,
				ProjectSlug: project,
				Status:      t.Status,
				Timestamp:   ts,
				Sender:      inboxJSONLSender(je.Event),
				Body:        body,
				BodySnippet: inboxFeedBodySnippet(body),
				Unread:      isUnread,
				Source:      inboxEntrySource(je),
				Live:        taskLive,
				Monitored:   taskMonitored,
			})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return inboxFeedTimestampSortKey(entries[i].Timestamp) > inboxFeedTimestampSortKey(entries[j].Timestamp)
	})
	writeJSON(w, InboxFeed{
		Entries:     entries,
		UnreadCount: unread,
		TaskCount:   taskCount,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// inboxEntrySource returns "slack" | "github" | "" for one stored event,
// preferring the persisted meta and falling back to live classification.
func inboxEntrySource(je monitor.InboxEntry) string {
	src := strings.TrimSpace(je.Meta.Source)
	if src == "" || src == "unknown" {
		src = monitor.ClassifyInboxEvent(je.Event).Source
	}
	if src == "unknown" {
		return ""
	}
	return src
}

// handleInboxConversation returns one task's full inbox thread, oldest first,
// with every Slack user/channel ID resolved to a display name. This is the
// lazy, per-conversation companion to /api/inbox: name resolution only runs
// for the conversation the user actually opens.
func (s *Server) handleInboxConversation(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		writeError(w, errors.New("slug query parameter is required"), http.StatusBadRequest)
		return
	}
	task, err := flowdb.GetTask(s.cfg.DB, slug)
	if err != nil {
		writeNotFoundOrError(w, err)
		return
	}

	live, _ := s.cachedLiveAgentSessions()
	taskLive := task.SessionID.Valid && task.SessionID.String != "" && live[strings.ToLower(task.SessionID.String)]
	provider := task.SessionProvider
	if provider == "" {
		provider = "claude"
	}
	var project *string
	if task.ProjectSlug.Valid && task.ProjectSlug.String != "" {
		ps := task.ProjectSlug.String
		project = &ps
	}

	ctx := r.Context()
	jsonlEntries, _ := monitor.ReadInboxEntries(task.Slug)
	messages := make([]InboxConversationMessage, 0, len(jsonlEntries))
	sources := map[string]bool{}
	channelName := ""
	for _, je := range jsonlEntries {
		msg := s.inboxConversationMessage(ctx, je)
		messages = append(messages, msg)
		if msg.Source != "" {
			sources[msg.Source] = true
		}
		if channelName == "" && msg.Source == "slack" {
			channelName = s.nameResolver.ChannelName(ctx, je.Event.Channel)
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		return inboxFeedTimestampSortKey(messages[i].Timestamp) < inboxFeedTimestampSortKey(messages[j].Timestamp)
	})

	source := ""
	switch len(sources) {
	case 1:
		for k := range sources {
			source = k
		}
	default:
		if len(sources) > 1 {
			source = "mixed"
		}
	}

	writeJSON(w, InboxConversation{
		Slug:        task.Slug,
		Name:        s.scrubSlackIDs(ctx, task.Name),
		ProjectSlug: project,
		Status:      task.Status,
		Provider:    provider,
		Live:        taskLive,
		Monitored:   s.inboxMonitors != nil && s.inboxMonitors.running(task.Slug),
		Source:      source,
		ChannelName: channelName,
		Messages:    messages,
	})
}

// inboxConversationMessage renders one stored event into a thread message,
// resolving Slack IDs to names. Never emits a raw ID in SenderName or Body.
func (s *Server) inboxConversationMessage(ctx context.Context, je monitor.InboxEntry) InboxConversationMessage {
	ev := je.Event
	source := inboxEntrySource(je)
	ts := strings.TrimSpace(je.EnqueuedAt)
	if ts == "" {
		ts = strings.TrimSpace(ev.TS)
	}
	return InboxConversationMessage{
		Source:     source,
		Kind:       ev.Kind,
		SenderName: s.inboxSenderName(ctx, ev, source),
		Timestamp:  ts,
		Title:      inboxJSONLTitle(ev),
		Body:       s.inboxMessageBody(ctx, ev, source),
		Permalink:  inboxPermalink(ev, source),
		Reaction:   ev.Reaction,
	}
}

// inboxSenderName resolves the human name of an event's author. Slack ids go
// through the cached resolver (never returned raw); GitHub senders are already
// logins. Falls back to "unknown" rather than leak an id.
func (s *Server) inboxSenderName(ctx context.Context, ev monitor.InboundEvent, source string) string {
	if source == "slack" {
		if name := s.nameResolver.UserName(ctx, ev.UserID); name != "" {
			return name
		}
		if name := s.nameResolver.UserName(ctx, ev.ItemAuthor); name != "" {
			return name
		}
		return "unknown"
	}
	// GitHub (and any other source) carries human-readable logins / labels.
	if sender := inboxJSONLSender(ev); sender != "" {
		return sender
	}
	return "unknown"
}

// inboxMessageBody returns the message text with Slack markup (mentions,
// links) cleaned so no raw ids surface. GitHub bodies pass through unchanged.
func (s *Server) inboxMessageBody(ctx context.Context, ev monitor.InboundEvent, source string) string {
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return ""
	}
	if source == "slack" {
		return s.nameResolver.CleanText(ctx, text)
	}
	return text
}

// inboxPermalink returns a best-effort deep link to the source message:
// the event's own URL when present (always set for GitHub), else a Slack
// app deep link built from team/channel/ts. Empty when nothing is derivable.
func inboxPermalink(ev monitor.InboundEvent, source string) string {
	if u := strings.TrimSpace(ev.URL); u != "" {
		return u
	}
	if source == "slack" {
		team := strings.TrimSpace(ev.TeamID)
		channel := strings.TrimSpace(ev.Channel)
		ts := strings.TrimSpace(ev.TS)
		if team != "" && channel != "" && ts != "" {
			return fmt.Sprintf("slack://channel?team=%s&id=%s&message=%s", team, channel, ts)
		}
	}
	return ""
}
