package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inboxTestSlug picks a slug-shaped string and points TaskDir at a fresh
// temp dir by setting FLOW_ROOT. Tests get isolated filesystem state and
// never touch the user's real ~/.flow.
func inboxTestSlug(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FLOW_ROOT", root)
	slug := "test-task-" + strings.ToLower(t.Name())
	return slug
}

func TestInboxPaths_FollowFlowRoot(t *testing.T) {
	slug := inboxTestSlug(t)
	root := os.Getenv("FLOW_ROOT")
	wantDir := filepath.Join(root, "tasks", slug)
	if got := TaskDir(slug); got != wantDir {
		t.Errorf("TaskDir = %q, want %q", got, wantDir)
	}
	if got := InboxPath(slug); got != filepath.Join(wantDir, "inbox.jsonl") {
		t.Errorf("InboxPath = %q", got)
	}
	if got := CursorPath(slug); got != filepath.Join(wantDir, "inbox.cursor") {
		t.Errorf("CursorPath = %q", got)
	}
}

func TestAppendInboxEvent_CreatesFileAndDir(t *testing.T) {
	slug := inboxTestSlug(t)
	ev := InboundEvent{
		Kind:     "message",
		Channel:  "C123",
		TS:       "1234.0001",
		ThreadTS: "1234.0001",
		UserID:   "U999",
		Text:     "hello",
	}
	if err := AppendInboxEvent(slug, ev); err != nil {
		t.Fatalf("Append err = %v", err)
	}
	// File must exist
	if _, err := os.Stat(InboxPath(slug)); err != nil {
		t.Fatalf("inbox.jsonl not created: %v", err)
	}
}

func TestAppendInboxEvent_AppendsMultipleLines(t *testing.T) {
	slug := inboxTestSlug(t)
	for i, text := range []string{"first", "second", "third"} {
		ev := InboundEvent{
			Kind:    "message",
			Channel: "C123",
			TS:      "1234.000" + string(rune('0'+i)),
			Text:    text,
		}
		if err := AppendInboxEvent(slug, ev); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	entries, err := ReadInboxEntries(slug)
	if err != nil {
		t.Fatalf("read err = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}
	if entries[0].Event.Text != "first" || entries[2].Event.Text != "third" {
		t.Errorf("entries out of order: %+v", entries)
	}
	if entries[0].EnqueuedAt == "" {
		t.Errorf("EnqueuedAt empty: %+v", entries[0])
	}
}

func TestAppendInboxEvent_RoundtripPreservesFields(t *testing.T) {
	slug := inboxTestSlug(t)
	ev := InboundEvent{
		Kind:        "reaction_added",
		Channel:     "C42",
		ChannelType: "channel",
		TS:          "1234.0010",
		ThreadTS:    "1234.0001",
		UserID:      "U_me",
		Reaction:    "claude",
		ItemChannel: "C42",
		ItemTS:      "1234.0001",
		ItemAuthor:  "U_other",
		TeamID:      "T1",
		APIAppID:    "A1",
		RawJSON:     `{"some":"raw"}`,
	}
	if err := AppendInboxEvent(slug, ev); err != nil {
		t.Fatalf("append err = %v", err)
	}
	entries, err := ReadInboxEntries(slug)
	if err != nil {
		t.Fatalf("read err = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len = %d", len(entries))
	}
	got := entries[0].Event
	if got.Reaction != "claude" || got.ItemAuthor != "U_other" || got.ChannelType != "channel" {
		t.Errorf("roundtrip lost fields: %+v", got)
	}
	if got.RawJSON != `{"some":"raw"}` {
		t.Errorf("RawJSON lost: %q", got.RawJSON)
	}
}

func TestReadInboxEntries_MissingFileReturnsEmpty(t *testing.T) {
	slug := inboxTestSlug(t)
	entries, err := ReadInboxEntries(slug)
	if err != nil {
		t.Fatalf("err = %v, want nil for missing file", err)
	}
	if len(entries) != 0 {
		t.Errorf("len = %d, want 0", len(entries))
	}
}

func TestReadInboxEntries_SkipsMalformedLines(t *testing.T) {
	slug := inboxTestSlug(t)
	// Hand-write a file with a good line, a garbage line, an empty line, and another good line.
	good := InboxEntry{EnqueuedAt: "2026-01-01T00:00:00Z", Event: InboundEvent{Kind: "message", Text: "ok"}}
	good2 := InboxEntry{EnqueuedAt: "2026-01-01T00:00:01Z", Event: InboundEvent{Kind: "message", Text: "fine"}}
	b1, _ := json.Marshal(good)
	b2, _ := json.Marshal(good2)
	content := string(b1) + "\n{garbage not json\n\n" + string(b2) + "\n"

	if err := os.MkdirAll(TaskDir(slug), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InboxPath(slug), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadInboxEntries(slug)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2 (garbage skipped, empty skipped)", len(entries))
	}
	if entries[0].Event.Text != "ok" || entries[1].Event.Text != "fine" {
		t.Errorf("got = %+v", entries)
	}
}

func TestInboxCursor_Roundtrip(t *testing.T) {
	slug := inboxTestSlug(t)
	if got, err := ReadInboxCursor(slug); err != nil || got != "" {
		t.Fatalf("missing cursor: got %q err %v, want empty", got, err)
	}
	if err := WriteInboxCursor(slug, "1234.0050"); err != nil {
		t.Fatalf("write cursor: %v", err)
	}
	got, err := ReadInboxCursor(slug)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if got != "1234.0050" {
		t.Errorf("cursor = %q, want 1234.0050", got)
	}
	// Overwrite — atomic rename should replace cleanly.
	if err := WriteInboxCursor(slug, "9999.9999"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ = ReadInboxCursor(slug)
	if got != "9999.9999" {
		t.Errorf("overwritten cursor = %q", got)
	}
}

func TestInboxPaths_NoFlowRootAndNoHomeFails(t *testing.T) {
	// When neither FLOW_ROOT nor HOME resolves, paths should empty and
	// downstream writes should error rather than write to "" silently.
	t.Setenv("FLOW_ROOT", "")
	t.Setenv("HOME", "")
	if got := TaskDir("anything"); got != "" {
		// Some platforms may still resolve HOME via getpwuid; skip if so.
		t.Skip("home still resolvable on this platform")
	}
	if err := AppendInboxEvent("anything", InboundEvent{Kind: "message"}); err == nil {
		t.Errorf("expected error when no paths resolvable")
	}
}
