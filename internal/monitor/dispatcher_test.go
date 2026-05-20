package monitor

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"

	"flow/internal/flowdb"

	_ "modernc.org/sqlite"
)

// spawnCall and tagCall record what the dispatcher requested. Used by
// tests to assert on the orchestration without actually running flow CLI
// subprocesses.
type spawnCall struct {
	Name  string
	Slug  string
	Brief string
}

type tagCall struct {
	Slug string
	Tag  string
}

// stubDispatcherIO swaps the package-level spawn/tag/open hooks for fakes
// and returns a teardown to restore the originals. Tests use the returned
// trackers to assert call patterns. Concurrency-safe so dispatch ordering
// doesn't matter for assertions.
func stubDispatcherIO(t *testing.T) (*[]spawnCall, *[]tagCall, *[]string, func()) {
	t.Helper()
	mu := &sync.Mutex{}
	var spawns []spawnCall
	var tags []tagCall
	var opens []string

	origSpawn := spawnFlowTask
	origTag := tagFlowTask
	origOpen := openSlackReplyTask

	spawnFlowTask = func(_ context.Context, name, slug, brief string) error {
		mu.Lock()
		defer mu.Unlock()
		spawns = append(spawns, spawnCall{Name: name, Slug: slug, Brief: brief})
		return nil
	}
	tagFlowTask = func(_ context.Context, slug, tag string) error {
		mu.Lock()
		defer mu.Unlock()
		tags = append(tags, tagCall{Slug: slug, Tag: tag})
		return nil
	}
	openSlackReplyTask = func(slug string) error {
		mu.Lock()
		defer mu.Unlock()
		opens = append(opens, slug)
		return nil
	}
	return &spawns, &tags, &opens, func() {
		spawnFlowTask = origSpawn
		tagFlowTask = origTag
		openSlackReplyTask = origOpen
	}
}

// dispatcherTestDB opens a real on-disk SQLite using flowdb.OpenDB so all
// migrations run, and returns the DB. Cleanup closes it. The temp dir is
// also wired as FLOW_ROOT so any inbox file ops in dispatch land inside
// it instead of the user's real ~/.flow.
func dispatcherTestDB(t *testing.T) *sql.DB {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FLOW_ROOT", root)
	t.Setenv("HOME", root)
	db, err := flowdb.OpenDB(root + "/flow.db")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedSlackTask inserts a task with the given slug and tags it with the
// slack-thread linkage so dispatcher lookups can find it. There's no
// public flowdb.AddTask helper today (CLI handles the INSERT), so we
// bypass with a direct INSERT — keeps the test focused on dispatch logic
// rather than CLI plumbing.
func seedSlackTask(t *testing.T, db *sql.DB, slug, threadKey string) {
	t.Helper()
	// status='backlog' satisfies the tasks invariant that non-backlog rows
	// must carry a session_id (or be a codex in-progress task). Seeded rows
	// don't have one yet; findTaskByThreadKey treats backlog as a valid
	// lookup result so this is enough.
	now := flowdb.NowISO()
	_, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, priority, work_dir, permission_mode, session_provider, status_changed_at, created_at, updated_at)
		 VALUES (?, ?, 'backlog', 'high', ?, 'default', 'claude', ?, ?, ?)`,
		slug, "seeded slack task", t.TempDir(), now, now, now,
	)
	if err != nil {
		t.Fatalf("seed task %s: %v", slug, err)
	}
	if err := flowdb.AddTaskTag(db, slug, "slack-reply"); err != nil {
		t.Fatalf("tag slack-reply: %v", err)
	}
	if err := flowdb.AddTaskTag(db, slug, SlackThreadTagPrefix+threadKey); err != nil {
		t.Fatalf("tag thread: %v", err)
	}
}

func TestDispatcher_NewThreadReactionSpawnsAndAppends(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", "U_me")
	t.Setenv("FLOW_SLACK_TRIGGER_EMOJI", "claude")
	t.Setenv("FLOW_SLACK_AUTOOPEN", "0") // tests assert on opens separately
	db := dispatcherTestDB(t)
	spawns, tags, opens, restore := stubDispatcherIO(t)
	defer restore()

	d := NewDispatcher(db, nil)
	reaction := mustParseReaction(t, "U_me", "claude", "C123", "1234.0010", "1234.0001")
	if err := d.Dispatch(context.Background(), reaction); err != nil {
		t.Fatalf("Dispatch err = %v", err)
	}

	if len(*spawns) != 1 {
		t.Fatalf("spawn count = %d, want 1", len(*spawns))
	}
	if !strings.HasPrefix((*spawns)[0].Slug, "slack-c123-1234") {
		t.Errorf("derived slug looks wrong: %q", (*spawns)[0].Slug)
	}
	if !strings.Contains((*spawns)[0].Brief, "thread_ts=1234.0001") {
		t.Errorf("brief missing thread_ts hint: %s", (*spawns)[0].Brief)
	}

	// Must apply both the marker tag and the thread-linkage tag.
	gotTags := map[string]bool{}
	for _, c := range *tags {
		gotTags[c.Tag] = true
	}
	if !gotTags["slack-reply"] || !gotTags["slack-thread:C123:1234.0001"] {
		t.Errorf("tags missing expected entries: %v", gotTags)
	}

	if len(*opens) != 0 {
		t.Errorf("AUTOOPEN=0 should suppress opens; got %v", *opens)
	}
}

func TestDispatcher_AutoOpenWhenEnabled(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", "U_me")
	t.Setenv("FLOW_SLACK_TRIGGER_EMOJI", "claude")
	t.Setenv("FLOW_SLACK_AUTOOPEN", "1")
	db := dispatcherTestDB(t)
	_, _, opens, restore := stubDispatcherIO(t)
	defer restore()

	d := NewDispatcher(db, nil)
	reaction := mustParseReaction(t, "U_me", "claude", "C123", "1234.0010", "1234.0001")
	if err := d.Dispatch(context.Background(), reaction); err != nil {
		t.Fatalf("Dispatch err = %v", err)
	}
	if len(*opens) != 1 {
		t.Fatalf("expected 1 open call; got %v", *opens)
	}
}

func TestDispatcher_ExistingThreadReactionSkipsSpawnAndAppends(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", "U_me")
	t.Setenv("FLOW_SLACK_TRIGGER_EMOJI", "claude")
	db := dispatcherTestDB(t)
	spawns, _, opens, restore := stubDispatcherIO(t)
	defer restore()

	threadKey := "C123:1234.0001"
	seedSlackTask(t, db, "preexisting-task", threadKey)

	d := NewDispatcher(db, nil)
	reaction := mustParseReaction(t, "U_me", "claude", "C123", "1234.0020", "1234.0001")
	if err := d.Dispatch(context.Background(), reaction); err != nil {
		t.Fatalf("Dispatch err = %v", err)
	}

	if len(*spawns) != 0 {
		t.Fatalf("spawn should NOT fire for existing thread; got %v", *spawns)
	}
	if len(*opens) != 0 {
		t.Fatalf("open should NOT fire for existing thread; got %v", *opens)
	}

	entries, err := ReadInboxEntries("preexisting-task")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox len = %d, want 1", len(entries))
	}
	if entries[0].Event.Kind != "reaction_added" {
		t.Errorf("inbox event kind = %q", entries[0].Event.Kind)
	}
}

func TestDispatcher_NonTriggerReactionIgnored(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", "U_me")
	t.Setenv("FLOW_SLACK_TRIGGER_EMOJI", "claude")
	db := dispatcherTestDB(t)
	spawns, tags, opens, restore := stubDispatcherIO(t)
	defer restore()

	d := NewDispatcher(db, nil)
	// Coworker reacted — not consent.
	noConsent := mustParseReaction(t, "U_coworker", "claude", "C123", "1.5", "1.1")
	// Wrong emoji.
	wrongEmoji := mustParseReaction(t, "U_me", "thumbsup", "C123", "1.6", "1.1")

	for _, ev := range []InboundEvent{noConsent, wrongEmoji} {
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("Dispatch err = %v", err)
		}
	}
	if len(*spawns)+len(*tags)+len(*opens) != 0 {
		t.Errorf("non-trigger events should have no side effects; spawns=%v tags=%v opens=%v",
			*spawns, *tags, *opens)
	}
}

func TestDispatcher_MessageInTrackedThreadAppends(t *testing.T) {
	t.Setenv("FLOW_SLACK_SELF_USER_IDS", "U_me")
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()

	threadKey := "C123:1700000000.000100"
	seedSlackTask(t, db, "live-thread", threadKey)

	// A follow-up message from a coworker in the tracked thread.
	msg := InboundEvent{
		Kind:        "message",
		Channel:     "C123",
		ChannelType: "channel",
		TS:          "1700000050.000001",
		ThreadTS:    "1700000000.000100",
		UserID:      "U_coworker",
		Text:        "another reply",
	}
	d := NewDispatcher(db, nil)
	if err := d.Dispatch(context.Background(), msg); err != nil {
		t.Fatalf("Dispatch err = %v", err)
	}

	entries, err := ReadInboxEntries("live-thread")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox len = %d, want 1", len(entries))
	}
	if entries[0].Event.Text != "another reply" || entries[0].Event.UserID != "U_coworker" {
		t.Errorf("inbox entry wrong: %+v", entries[0])
	}
}

func TestDispatcher_MessageInUntrackedThreadIgnored(t *testing.T) {
	// No matching task → message is dropped. This is the firehose-suppression
	// guarantee: only threads we've consented to track ever reach Claude.
	db := dispatcherTestDB(t)
	_, _, _, restore := stubDispatcherIO(t)
	defer restore()

	d := NewDispatcher(db, nil)
	msg := InboundEvent{
		Kind:    "message",
		Channel: "C_nothere",
		TS:      "1.5",
		Text:    "noise",
	}
	if err := d.Dispatch(context.Background(), msg); err != nil {
		t.Fatalf("Dispatch err = %v", err)
	}
	// No inbox file created anywhere
	root := strings.TrimSpace(getenv(t, "FLOW_ROOT"))
	if root == "" {
		t.Skip("FLOW_ROOT not set; skip filesystem check")
	}
	if entries, _ := ReadInboxEntries("slack-c-nothere-1-5"); len(entries) != 0 {
		t.Errorf("untracked thread should not produce inbox: %v", entries)
	}
}

func TestSlugForThread_Idempotent(t *testing.T) {
	// Same key in → same slug out. Required for re-fire safety.
	got1 := SlugForThread("C123:1234.0001")
	got2 := SlugForThread("C123:1234.0001")
	if got1 != got2 {
		t.Errorf("not deterministic: %q vs %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "slack-") {
		t.Errorf("missing prefix: %q", got1)
	}
	if strings.ContainsAny(got1, ":._") {
		t.Errorf("slug should not contain colons/dots/underscores: %q", got1)
	}
}

func TestSlugForThread_CollapsesDashes(t *testing.T) {
	// Adjacent separators (colon + dot or two dots from edge cases) shouldn't
	// produce double dashes in the slug.
	got := SlugForThread("C1..2")
	if strings.Contains(got, "--") {
		t.Errorf("doubled dash: %q", got)
	}
}

func TestSlugForThread_Empty(t *testing.T) {
	if got := SlugForThread(""); got != "" {
		t.Errorf("empty in → empty out, got %q", got)
	}
}

func getenv(t *testing.T, name string) string {
	t.Helper()
	return strings.TrimSpace(os.Getenv(name))
}
