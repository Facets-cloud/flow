package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flow/internal/monitor"
)

// serverFakeSlackClient implements monitor.SlackTitleClient for endpoint tests,
// so the conversation handler resolves names without a real Slack token.
type serverFakeSlackClient struct {
	users map[string]monitor.SlackUser
	chans map[string]monitor.SlackConversation
}

func (f serverFakeSlackClient) ConversationInfo(_ context.Context, id string) (monitor.SlackConversation, error) {
	if c, ok := f.chans[id]; ok {
		return c, nil
	}
	return monitor.SlackConversation{}, errors.New("not found")
}
func (f serverFakeSlackClient) ConversationReplies(_ context.Context, _, _ string, _ int) ([]monitor.SlackMessage, error) {
	return nil, nil
}
func (f serverFakeSlackClient) UsersInConversation(_ context.Context, _ string, _ int) ([]string, error) {
	return nil, nil
}
func (f serverFakeSlackClient) UserInfo(_ context.Context, id string) (monitor.SlackUser, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return monitor.SlackUser{}, errors.New("not found")
}

func insertBacklogTask(t *testing.T, srv *Server, slug, name string) {
	t.Helper()
	now := "2026-05-28T10:00:00Z"
	if _, err := srv.cfg.DB.Exec(
		`INSERT INTO tasks (slug, name, status, kind, priority, work_dir, created_at, updated_at, session_provider)
		 VALUES (?, ?, 'backlog', 'regular', 'medium', '/tmp', ?, ?, 'claude')`,
		slug, name, now, now,
	); err != nil {
		t.Fatal(err)
	}
}

func TestInboxConversationResolvesNamesAndOmitsRawIDs(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	insertBacklogTask(t, srv, "slack-coinswitch", "coinswitch slack thread")

	mustAppend(t, "slack-coinswitch", monitor.InboundEvent{
		Kind: "message", ChannelType: "slack", Channel: "C1", TS: "1779359538.001",
		ThreadTS: "1779359538.001", UserID: "U1", Text: "hey <@U2> can you look at the deploy", TeamID: "T1",
	})
	mustAppend(t, "slack-coinswitch", monitor.InboundEvent{
		Kind: "message", ChannelType: "slack", Channel: "C1", TS: "1779359600.002",
		UserID: "U2", Text: "on it, checking logs",
	})
	mustAppend(t, "slack-coinswitch", monitor.InboundEvent{
		Kind: "pr_review_comment", ChannelType: "github", Channel: "Facets-cloud/flow",
		UserID: "octocat", Text: "requested changes on auth.go",
		URL: "https://github.com/Facets-cloud/flow/pull/42#discussion_r1",
	})

	srv.nameResolver = monitor.NewSlackNameResolverWithClient(serverFakeSlackClient{
		users: map[string]monitor.SlackUser{
			"U1": {ID: "U1", DisplayName: "Vishnu"},
			"U2": {ID: "U2", DisplayName: "Priya"},
		},
		chans: map[string]monitor.SlackConversation{
			"C1": {ID: "C1", Name: "coinswitch", IsChannel: true},
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/inbox/conversation?slug=slack-coinswitch", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var conv InboxConversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}
	if conv.Name != "coinswitch slack thread" {
		t.Errorf("Name = %q", conv.Name)
	}
	if conv.Source != "mixed" {
		t.Errorf("Source = %q, want mixed", conv.Source)
	}
	if conv.ChannelName != "#coinswitch" {
		t.Errorf("ChannelName = %q, want #coinswitch", conv.ChannelName)
	}
	if conv.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", conv.Provider)
	}
	if len(conv.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(conv.Messages))
	}

	// Chronological (append order): Vishnu, Priya, octocat.
	m0, m1, m2 := conv.Messages[0], conv.Messages[1], conv.Messages[2]
	if m0.SenderName != "Vishnu" {
		t.Errorf("m0.SenderName = %q, want Vishnu", m0.SenderName)
	}
	if m0.Body != "hey @Priya can you look at the deploy" {
		t.Errorf("m0.Body = %q (mention should resolve to @Priya)", m0.Body)
	}
	if m0.Permalink != "slack://channel?team=T1&id=C1&message=1779359538.001" {
		t.Errorf("m0.Permalink = %q", m0.Permalink)
	}
	if m1.SenderName != "Priya" || m1.Body != "on it, checking logs" {
		t.Errorf("m1 = %+v", m1)
	}
	if m1.Permalink != "" {
		t.Errorf("m1.Permalink = %q, want empty (no team id)", m1.Permalink)
	}
	if m2.Source != "github" || m2.SenderName != "octocat" {
		t.Errorf("m2 = %+v", m2)
	}
	if m2.Title != "PR review comment" {
		t.Errorf("m2.Title = %q", m2.Title)
	}
	if m2.Permalink != "https://github.com/Facets-cloud/flow/pull/42#discussion_r1" {
		t.Errorf("m2.Permalink = %q", m2.Permalink)
	}

	// No raw Slack ids may surface in any rendered field.
	for i, m := range conv.Messages {
		for _, raw := range []string{"U1", "U2", "C1"} {
			if strings.Contains(m.SenderName, raw) || strings.Contains(m.Body, raw) {
				t.Errorf("message %d leaks raw id %q: sender=%q body=%q", i, raw, m.SenderName, m.Body)
			}
		}
	}
}

func TestInboxConversationWithoutTokenFallsBackNoRawIDs(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	insertBacklogTask(t, srv, "slack-thread", "some slack thread")
	mustAppend(t, "slack-thread", monitor.InboundEvent{
		Kind: "message", ChannelType: "slack", Channel: "C9", TS: "100.1", UserID: "U9", Text: "ping <@U8>",
	})
	srv.nameResolver = nil // simulate "no Slack token configured"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/inbox/conversation?slug=slack-thread", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var conv InboxConversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}
	if len(conv.Messages) != 1 {
		t.Fatalf("got %d messages", len(conv.Messages))
	}
	m := conv.Messages[0]
	if m.SenderName != "unknown" {
		t.Errorf("SenderName = %q, want unknown (never raw id) when no token", m.SenderName)
	}
	if m.Body != "ping @user" {
		t.Errorf("Body = %q, want %q (mention stripped to @user)", m.Body, "ping @user")
	}
	if strings.Contains(m.SenderName, "U9") || strings.Contains(m.Body, "U8") {
		t.Errorf("leaked raw id: sender=%q body=%q", m.SenderName, m.Body)
	}
}

func TestInboxConversationErrors(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/inbox/conversation", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing slug: status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/inbox/conversation?slug=nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/inbox/conversation?slug=x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status = %d, want 405", rec.Code)
	}
}

func TestInboxFeedEnrichesSourceAndLive(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	insertBacklogTask(t, srv, "slack-thread", "slack thread")
	mustAppend(t, "slack-thread", monitor.InboundEvent{
		Kind: "message", ChannelType: "slack", Channel: "C1", TS: "1.0", UserID: "U1", Text: "hello",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var feed InboxFeed
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("got %d entries", len(feed.Entries))
	}
	if feed.Entries[0].Source != "slack" {
		t.Errorf("Source = %q, want slack", feed.Entries[0].Source)
	}
	if feed.Entries[0].Live {
		t.Errorf("Live = true, want false for a backlog task with no session")
	}
}

func TestInboxMarkReadActionClearsBacklogUnread(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	insertBacklogTask(t, srv, "slack-thread", "slack thread")
	mustAppend(t, "slack-thread", monitor.InboundEvent{
		Kind: "message", ChannelType: "slack", Channel: "C1", TS: "1.0", UserID: "U1", Text: "hello",
	})

	handler := srv.Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pre-read status = %d", rec.Code)
	}
	var before InboxFeed
	if err := json.Unmarshal(rec.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.UnreadCount != 1 {
		t.Fatalf("pre-read unread = %d, want 1", before.UnreadCount)
	}

	resp, status := srv.runAction(actionRequest{Kind: "mark-read", Target: "slack-thread"})
	if status != http.StatusOK || !resp.OK {
		t.Fatalf("mark-read status = %d resp = %+v", status, resp)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("post-read status = %d", rec.Code)
	}
	var after InboxFeed
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.UnreadCount != 0 {
		t.Fatalf("post-read unread = %d, want 0", after.UnreadCount)
	}
	if len(after.Entries) != 1 || after.Entries[0].Unread {
		t.Fatalf("post-read entries = %+v, want unread cleared", after.Entries)
	}
}

func TestScrubSlackIDsInTaskName(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	srv.nameResolver = monitor.NewSlackNameResolverWithClient(serverFakeSlackClient{
		users: map[string]monitor.SlackUser{"U01RKJ5J9EK": {ID: "U01RKJ5J9EK", DisplayName: "Rohit"}},
		chans: map[string]monitor.SlackConversation{"C0B3L0D8QG1": {ID: "C0B3L0D8QG1", Name: "internal-migrations", IsChannel: true}},
	})
	ctx := context.Background()

	cases := []struct{ in, want string }{
		{"U01RKJ5J9EK", "Rohit"}, // whole name is a bare user id
		{"#test-kv - @U03HNAFLVAN what happened?", "#test-kv - @unknown what happened?"}, // mention, unresolved → @unknown (no id)
		{"C0B3L0D8QG1 thread", "#internal-migrations thread"},                            // channel id → #name
		{"coinswitch slack thread", "coinswitch slack thread"},                           // clean name untouched
		{"UPDATED the GCP module", "UPDATED the GCP module"},                             // all-caps words (no digit) untouched
	}
	for _, c := range cases {
		if got := srv.scrubSlackIDs(ctx, c.in); got != c.want {
			t.Errorf("scrubSlackIDs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func mustAppend(t *testing.T, slug string, ev monitor.InboundEvent) {
	t.Helper()
	if err := monitor.AppendInboxEvent(slug, ev); err != nil {
		t.Fatalf("AppendInboxEvent: %v", err)
	}
}
