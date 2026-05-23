package monitor

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func seedGitHubTask(t *testing.T, slug string, db *sql.DB, tag string) {
	t.Helper()
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, priority, work_dir, permission_mode, session_provider, status_changed_at, created_at, updated_at)
		 VALUES (?, ?, 'backlog', 'high', ?, 'default', 'claude', ?, ?, ?)`,
		slug, "seeded github task", t.TempDir(), now, now, now,
	); err != nil {
		t.Fatalf("seed github task %s: %v", slug, err)
	}
	if err := flowdb.AddTaskTag(db, slug, tag); err != nil {
		t.Fatalf("tag %s: %v", tag, err)
	}
}

func TestGitHubDispatcher_PRReviewRequestCreatesTask(t *testing.T) {
	t.Setenv("FLOW_GH_AUTOOPEN", "0")
	db := dispatcherTestDB(t)
	spawns, tags, opens, restore := stubDispatcherIO(t)
	defer restore()

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:     GitHubEventPRReviewRequested,
		Owner:    "Facets-cloud",
		Repo:     "flow-manager",
		Number:   42,
		Title:    "Add GitHub integration",
		Body:     "Please review the new polling path.",
		URL:      "https://github.com/Facets-cloud/flow-manager/pull/42",
		Author:   "octo",
		BaseRef:  "main",
		HeadRef:  "feature/github",
		HeadSHA:  "abc123",
		Labels:   []string{"flow:codex"},
		EventKey: "pr:Facets-cloud/flow-manager#42:review_requested",
		RawJSON:  `{"number":42}`,
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if len(*spawns) != 1 {
		t.Fatalf("spawn count = %d, want 1", len(*spawns))
	}
	spawn := (*spawns)[0]
	if spawn.Slug != "gh-pr-facets-cloud-flow-manager-42" {
		t.Fatalf("slug = %q", spawn.Slug)
	}
	if spawn.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", spawn.Provider)
	}
	for _, want := range []string{
		"Pull request: Facets-cloud/flow-manager#42",
		"base: main",
		"head: feature/github",
		"https://github.com/Facets-cloud/flow-manager/pull/42",
	} {
		if !strings.Contains(spawn.Brief, want) {
			t.Fatalf("brief missing %q\n%s", want, spawn.Brief)
		}
	}
	gotTags := map[string]bool{}
	for _, c := range *tags {
		gotTags[c.Tag] = true
	}
	for _, want := range []string{"github", "gh-pr:Facets-cloud/flow-manager#42"} {
		if !gotTags[want] {
			t.Fatalf("missing tag %q from %v", want, gotTags)
		}
	}
	if len(*opens) != 0 {
		t.Fatalf("autoopen off should suppress opens: %v", *opens)
	}
	seen, err := flowdb.HasGitHubEvent(db, gitHubPRHeadEventKey("Facets-cloud", "flow-manager", 42, "abc123"))
	if err != nil {
		t.Fatalf("HasGitHubEvent: %v", err)
	}
	if !seen {
		t.Fatal("initial PR task creation should mark the current head SHA as already seen")
	}
}

func TestGitHubDispatcher_SecondPREventAppendsWithoutDuplicateTask(t *testing.T) {
	t.Setenv("FLOW_GH_AUTOOPEN", "0")
	db := dispatcherTestDB(t)
	spawns, _, _, restore := stubDispatcherIO(t)
	defer restore()
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")

	d := NewGitHubDispatcher(db, nil)
	second := GitHubEvent{
		Kind:     GitHubEventPRReviewRequested,
		Owner:    "Facets-cloud",
		Repo:     "flow-manager",
		Number:   42,
		Title:    "Add GitHub integration",
		URL:      "https://github.com/Facets-cloud/flow-manager/pull/42",
		EventKey: "pr:Facets-cloud/flow-manager#42:review_requested",
	}

	if err := d.Dispatch(context.Background(), second); err != nil {
		t.Fatalf("dispatch second: %v", err)
	}
	if len(*spawns) != 0 {
		t.Fatalf("spawn count = %d, want 0", len(*spawns))
	}
	entries, err := ReadInboxEntries("tracked-pr")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox entries = %d, want 1", len(entries))
	}
}

func TestGitHubDispatcher_ReviewCommentAppendsToTrackedPR(t *testing.T) {
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")
	if _, err := db.Exec(`UPDATE tasks SET status='done', updated_at=? WHERE slug='tracked-pr'`, flowdb.NowISO()); err != nil {
		t.Fatalf("seed done status: %v", err)
	}

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:      GitHubEventPRReviewComment,
		Owner:     "Facets-cloud",
		Repo:      "flow-manager",
		Number:    42,
		CommentID: "PRRC_kwDOAAABBB",
		Author:    "reviewer",
		Body:      "Please tighten the idempotency test.",
		URL:       "https://github.com/Facets-cloud/flow-manager/pull/42#discussion_r1",
		EventKey:  "review-comment:PRRC_kwDOAAABBB",
		RawJSON:   `{"node_id":"PRRC_kwDOAAABBB"}`,
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	entries, err := ReadInboxEntries("tracked-pr")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox entries = %d, want 1", len(entries))
	}
	if entries[0].Event.Kind != string(GitHubEventPRReviewComment) || entries[0].Event.Text != ev.Body {
		t.Fatalf("entry event = %+v", entries[0].Event)
	}
}

func TestGitHubDispatcher_ChangesRequestedReviewReopensTrackedPR(t *testing.T) {
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")
	if _, err := db.Exec(`UPDATE tasks SET status='done', updated_at=? WHERE slug='tracked-pr'`, flowdb.NowISO()); err != nil {
		t.Fatalf("seed done status: %v", err)
	}

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:     GitHubEventPRReviewChangesRequested,
		Owner:    "Facets-cloud",
		Repo:     "flow-manager",
		Number:   42,
		Author:   "reviewer",
		Body:     "Please add a regression test.",
		URL:      "https://github.com/Facets-cloud/flow-manager/pull/42#pullrequestreview-44",
		EventKey: "review:PRR_kwDOAAABBB",
		RawJSON:  `{"node_id":"PRR_kwDOAAABBB"}`,
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	entries, err := ReadInboxEntries("tracked-pr")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox entries = %d, want 1", len(entries))
	}
	if entries[0].Event.Kind != string(GitHubEventPRReviewChangesRequested) || !entries[0].Meta.Actionable {
		t.Fatalf("entry = %+v", entries[0])
	}
	task, err := flowdb.GetTask(db, "tracked-pr")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != "backlog" {
		t.Fatalf("status = %q, want backlog after changes requested", task.Status)
	}
}

func TestGitHubDispatcher_ApprovedReviewAppendsWithoutReopeningTrackedPR(t *testing.T) {
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")
	if _, err := db.Exec(`UPDATE tasks SET status='done', updated_at=? WHERE slug='tracked-pr'`, flowdb.NowISO()); err != nil {
		t.Fatalf("seed done status: %v", err)
	}

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:     GitHubEventPRReviewApproved,
		Owner:    "Facets-cloud",
		Repo:     "flow-manager",
		Number:   42,
		Author:   "reviewer",
		Body:     "Looks good.",
		URL:      "https://github.com/Facets-cloud/flow-manager/pull/42#pullrequestreview-45",
		EventKey: "review:PRR_kwDOAAACCC",
		RawJSON:  `{"node_id":"PRR_kwDOAAACCC"}`,
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	entries, err := ReadInboxEntries("tracked-pr")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox entries = %d, want 1", len(entries))
	}
	if entries[0].Event.Kind != string(GitHubEventPRReviewApproved) || entries[0].Meta.Actionable {
		t.Fatalf("entry = %+v", entries[0])
	}
	task, err := flowdb.GetTask(db, "tracked-pr")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != "done" {
		t.Fatalf("status = %q, want done after approved review", task.Status)
	}
}

func TestGitHubDispatcher_IssueAssignmentCreatesTask(t *testing.T) {
	t.Setenv("FLOW_GH_AUTOOPEN", "0")
	db := dispatcherTestDB(t)
	spawns, tags, _, restore := stubDispatcherIO(t)
	defer restore()

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:      GitHubEventIssueAssigned,
		Owner:     "Facets-cloud",
		Repo:      "flow-manager",
		Number:    7,
		Title:     "Document GitHub polling",
		Body:      "Add docs for the new env vars.",
		URL:       "https://github.com/Facets-cloud/flow-manager/issues/7",
		Author:    "octo",
		Labels:    []string{"docs"},
		Milestone: "v1",
		EventKey:  "issue:Facets-cloud/flow-manager#7:assigned",
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(*spawns) != 1 {
		t.Fatalf("spawn count = %d, want 1", len(*spawns))
	}
	if (*spawns)[0].Slug != "gh-issue-facets-cloud-flow-manager-7" {
		t.Fatalf("slug = %q", (*spawns)[0].Slug)
	}
	gotTags := map[string]bool{}
	for _, c := range *tags {
		gotTags[c.Tag] = true
	}
	if !gotTags["gh-issue:Facets-cloud/flow-manager#7"] {
		t.Fatalf("missing issue tag from %v", gotTags)
	}
}

func TestGitHubDispatcher_DuplicateEventKeySuppressesAppend(t *testing.T) {
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:      GitHubEventPRReviewComment,
		Owner:     "Facets-cloud",
		Repo:      "flow-manager",
		Number:    42,
		CommentID: "PRRC_kwDOAAABBB",
		Body:      "same comment",
		EventKey:  "review-comment:PRRC_kwDOAAABBB",
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("dispatch duplicate: %v", err)
	}
	entries, err := ReadInboxEntries("tracked-pr")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("duplicate event should append once, got %d", len(entries))
	}
}

func TestGitHubDispatcher_HeadUpdatedAppendsToTrackedPR(t *testing.T) {
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:     GitHubEventPRHeadUpdated,
		Owner:    "Facets-cloud",
		Repo:     "flow-manager",
		Number:   42,
		HeadRef:  "feature/github",
		HeadSHA:  "abc123",
		Body:     "Pull request head changed; review again.",
		EventKey: "pr-head:Facets-cloud/flow-manager#42:abc123",
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	entries, err := ReadInboxEntries("tracked-pr")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 || entries[0].Event.Kind != string(GitHubEventPRHeadUpdated) {
		t.Fatalf("entries = %#v", entries)
	}
	task, err := flowdb.GetTask(db, "tracked-pr")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != "backlog" {
		t.Fatalf("status = %q, want backlog after new PR head", task.Status)
	}
}

func TestGitHubDispatcher_MergedPRMarksTrackedTaskDone(t *testing.T) {
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()
	seedGitHubTask(t, "tracked-pr", db, "gh-pr:Facets-cloud/flow-manager#42")

	d := NewGitHubDispatcher(db, nil)
	ev := GitHubEvent{
		Kind:     GitHubEventPRMerged,
		Owner:    "Facets-cloud",
		Repo:     "flow-manager",
		Number:   42,
		HeadSHA:  "abc123",
		Body:     "Pull request merged.",
		EventKey: "pr-merged:Facets-cloud/flow-manager#42",
	}
	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	task, err := flowdb.GetTask(db, "tracked-pr")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != "done" {
		t.Fatalf("status = %q, want done", task.Status)
	}
	entries, err := ReadInboxEntries("tracked-pr")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 || entries[0].Event.Kind != string(GitHubEventPRMerged) {
		t.Fatalf("entries = %#v", entries)
	}
}
