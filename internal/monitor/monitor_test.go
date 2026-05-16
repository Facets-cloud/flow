package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func openMonitorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := flowdb.OpenDB(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPollGitHubStoresReviewRequestNotification(t *testing.T) {
	db := openMonitorTestDB(t)
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "gh" && strings.Contains(joined, "review-requested:@me"):
			return []byte(`[{"number":48,"title":"Add monitor daemon","url":"https://github.com/acme/flow/pull/48","repository":{"nameWithOwner":"acme/flow"}}]`), nil
		case name == "gh":
			return []byte(`[]`), nil
		default:
			t.Fatalf("unexpected command: %s %s", name, joined)
		}
		return nil, nil
	}
	summaries, err := (Poller{DB: db, Runner: runner}).Poll(context.Background(), "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Events != 1 || summaries[0].New != 1 {
		t.Fatalf("summary = %+v", summaries)
	}
	events, err := flowdb.ListMonitorEvents(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != "github" || events[0].Kind != "review_requested" {
		t.Fatalf("events = %+v", events)
	}
	notifications, err := flowdb.ListMonitorNotifications(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 || notifications[0].Level != "approval" {
		t.Fatalf("notifications = %+v", notifications)
	}
}

func TestPollGitHubMergedLinkedPRMarksTaskDone(t *testing.T) {
	db := openMonitorTestDB(t)
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, priority, work_dir, session_id, session_started, created_at, updated_at)
		 VALUES ('monitor-pr', 'Monitor PR', 'in-progress', 'medium', ?, '11111111-1111-4111-8111-111111111111', ?, ?, ?)`,
		t.TempDir(), now, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertTaskPRLink(db, "monitor-pr", "acme/flow", 48, "https://github.com/acme/flow/pull/48"); err != nil {
		t.Fatal(err)
	}
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "gh" && strings.Contains(joined, "pr view"):
			return []byte(`{"state":"MERGED","mergedAt":"2026-05-15T08:00:00Z","url":"https://github.com/acme/flow/pull/48","number":48,"title":"Add monitor daemon"}`), nil
		case name == "gh":
			return []byte(`[]`), nil
		default:
			t.Fatalf("unexpected command: %s %s", name, joined)
		}
		return nil, nil
	}
	if _, err := (Poller{DB: db, Runner: runner}).Poll(context.Background(), "github"); err != nil {
		t.Fatal(err)
	}
	task, err := flowdb.GetTask(db, "monitor-pr")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" {
		t.Fatalf("status = %s, want done", task.Status)
	}
	links, err := flowdb.ListTaskPRLinks(db, "monitor-pr")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].State != "merged" {
		t.Fatalf("links = %+v", links)
	}
}

func TestGitHubNotificationsUseWebURLs(t *testing.T) {
	events, err := githubNotifications([]byte(`[{
		"id":"n1",
		"repository":{"full_name":"acme/flow"},
		"subject":{"title":"Review me","type":"PullRequest","url":"https://api.github.com/repos/acme/flow/pulls/48"}
	}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].URL != "https://github.com/acme/flow/pull/48" {
		t.Fatalf("url = %q", events[0].URL)
	}
}

func TestPollSlackWebAPIStoresDMAndMention(t *testing.T) {
	db := openMonitorTestDB(t)
	t.Setenv("FLOW_SLACK_TOKEN", "xoxp-test")
	t.Setenv("FLOW_SLACK_POLL_CMD", "")
	t.Setenv("FLOW_SLACK_LOOKBACK", "24h")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth.test":
			writeSlackTestJSON(t, w, map[string]any{"ok": true, "user_id": "U123", "team_id": "T123"})
		case "/api/users.conversations":
			writeSlackTestJSON(t, w, map[string]any{"ok": true, "channels": []map[string]any{
				{"id": "D1", "is_im": true, "user": "U234"},
				{"id": "C1", "name": "eng", "is_channel": true},
			}})
		case "/api/conversations.history":
			switch r.URL.Query().Get("channel") {
			case "D1":
				writeSlackTestJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{
					{"type": "message", "user": "U234", "text": "Can you review this?", "ts": "1710000000.000001"},
				}})
			case "C1":
				writeSlackTestJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{
					{"type": "message", "user": "U345", "text": "Heads up <@U123>", "ts": "1710000001.000001"},
					{"type": "message", "user": "U345", "text": "general chatter", "ts": "1710000002.000001"},
				}})
			default:
				writeSlackTestJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{}})
			}
		case "/api/chat.getPermalink":
			writeSlackTestJSON(t, w, map[string]any{"ok": true, "permalink": "https://slack.example/archives/" + r.URL.Query().Get("channel") + "/p1"})
		default:
			t.Fatalf("unexpected slack API path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	summaries, err := (Poller{DB: db, SlackAPIBaseURL: srv.URL + "/api"}).Poll(context.Background(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Events != 2 || summaries[0].New != 2 || len(summaries[0].Errors) != 0 {
		t.Fatalf("summary = %+v", summaries)
	}
	events, err := flowdb.ListMonitorEvents(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, event := range events {
		kinds[event.Kind] = true
	}
	if !kinds["dm"] || !kinds["mention"] || kinds["channel_message"] {
		t.Fatalf("events = %+v", events)
	}
	notifications, err := flowdb.ListMonitorNotifications(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 2 || notifications[0].Level != "approval" || notifications[1].Level != "approval" {
		t.Fatalf("notifications = %+v", notifications)
	}
}

func writeSlackTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}
