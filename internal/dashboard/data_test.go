package dashboard

import (
	"database/sql"
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fixtureRoot creates a fresh flow root with an initialized DB at
// <tmp>/flow/flow.db plus the tasks/ and projects/ directories.
func fixtureRoot(t *testing.T) (string, *sql.DB) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "flow")
	for _, sub := range []string{"tasks", "projects", "kb"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	db, err := flowdb.OpenDB(filepath.Join(root, "flow.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return root, db
}

// insertTaskRow is a thin helper for the dashboard tests — it sets
// timestamps explicitly so the test owns "when" semantics rather than
// using time.Now.
type insertOpts struct {
	Project         string
	WaitingOn       string
	UpdatedAt       string // RFC3339
	StatusChangedAt string // RFC3339
}

func insertTaskRow(t *testing.T, db *sql.DB, slug, status, priority string, o insertOpts) {
	t.Helper()
	if o.UpdatedAt == "" {
		o.UpdatedAt = flowdb.NowISO()
	}
	createdAt := o.UpdatedAt

	var sessionID any
	if status != "backlog" {
		sessionID = "11111111-1111-4111-8111-" + padTo12(slug)
	}

	var projectSlug any
	if o.Project != "" {
		// Ensure project exists.
		_, _ = db.Exec(
			`INSERT OR IGNORE INTO projects (slug, name, work_dir, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			o.Project, o.Project, "/tmp/"+o.Project, createdAt, createdAt,
		)
		projectSlug = o.Project
	}

	var waitingOn any
	if o.WaitingOn != "" {
		waitingOn = o.WaitingOn
	}

	var statusChangedAt any
	if o.StatusChangedAt != "" {
		statusChangedAt = o.StatusChangedAt
	}

	_, err := db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, kind, priority, work_dir,
		                    waiting_on, status_changed_at, session_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'regular', ?, ?, ?, ?, ?, ?, ?)`,
		slug, slug, projectSlug, status, priority, "/tmp/"+slug,
		waitingOn, statusChangedAt, sessionID, createdAt, o.UpdatedAt,
	)
	if err != nil {
		t.Fatalf("insert task %s: %v", slug, err)
	}
}

// padTo12 ensures the synthetic session UUID stays the right length.
func padTo12(s string) string {
	out := s
	for len(out) < 12 {
		out += "0"
	}
	return out[:12]
}

// writeUpdate creates an updates/*.md file under root/tasks/<slug>/
// with the given headline as its body and explicit mtime.
func writeUpdate(t *testing.T, root, ownerKind, ownerSlug, filename, body string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(root, ownerKind, ownerSlug, "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir updates: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestSnapshotBuckets(t *testing.T) {
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	fixedNow := func() time.Time { return now }

	recent := now.Add(-2 * time.Hour).Format(time.RFC3339)
	stale := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)

	// working: in-progress, not stale, no waiting
	insertTaskRow(t, db, "alpha", "in-progress", "high", insertOpts{
		Project: "flow", UpdatedAt: recent,
	})
	// awaiting: in-progress + waiting_on
	insertTaskRow(t, db, "beta", "in-progress", "medium", insertOpts{
		WaitingOn: "@sam", UpdatedAt: recent,
	})
	// stale: in-progress, untouched 10d
	insertTaskRow(t, db, "gamma", "in-progress", "high", insertOpts{
		UpdatedAt: stale,
	})
	// backlog: should not appear in any of the three section lists
	insertTaskRow(t, db, "delta", "backlog", "medium", insertOpts{
		UpdatedAt: recent,
	})

	l := &Loader{DB: db, FlowRoot: root, Now: fixedNow}
	s, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if got := len(s.Working); got != 1 {
		t.Errorf("Working: want 1, got %d", got)
	}
	if got := len(s.Awaiting); got != 1 {
		t.Errorf("Awaiting: want 1, got %d", got)
	}
	if got := len(s.Stale); got != 1 {
		t.Errorf("Stale: want 1, got %d", got)
	}
	if got := len(s.Backlog); got != 1 {
		t.Errorf("Backlog: want 1, got %d", got)
	}
	if s.Counts.Backlog != 1 {
		t.Errorf("Counts.Backlog: want 1, got %d", s.Counts.Backlog)
	}
	if s.Counts.Working != 1 || s.Counts.Awaiting != 1 {
		t.Errorf("Counts mismatch: %+v", s.Counts)
	}
}

func TestBacklogSortedHighFirst(t *testing.T) {
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	updated := now.Format(time.RFC3339)
	insertTaskRow(t, db, "low-task", "backlog", "low", insertOpts{UpdatedAt: updated})
	insertTaskRow(t, db, "med-task", "backlog", "medium", insertOpts{UpdatedAt: updated})
	insertTaskRow(t, db, "hi-task", "backlog", "high", insertOpts{UpdatedAt: updated})

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }}
	s, _ := l.Snapshot()
	if len(s.Backlog) != 3 {
		t.Fatalf("Backlog: want 3, got %d", len(s.Backlog))
	}
	gotOrder := []string{
		s.Backlog[0].Task.Slug,
		s.Backlog[1].Task.Slug,
		s.Backlog[2].Task.Slug,
	}
	wantOrder := []string{"hi-task", "med-task", "low-task"}
	for i, w := range wantOrder {
		if gotOrder[i] != w {
			t.Errorf("Backlog[%d]: want %q, got %q (full: %v)", i, w, gotOrder[i], gotOrder)
		}
	}
}

func TestStaleExcludedFromWorkingAndAwaiting(t *testing.T) {
	// A stale task with waiting_on goes into Stale, not Awaiting.
	// Stale takes precedence so the user sees "this hasn't moved" rather
	// than a fresh-looking "waiting on X".
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	stale := now.Add(-9 * 24 * time.Hour).Format(time.RFC3339)
	insertTaskRow(t, db, "stuck", "in-progress", "high", insertOpts{
		WaitingOn: "@nobody", UpdatedAt: stale,
	})

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }}
	s, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(s.Working) != 0 || len(s.Awaiting) != 0 {
		t.Errorf("expected stuck task in Stale only; got working=%d awaiting=%d stale=%d",
			len(s.Working), len(s.Awaiting), len(s.Stale))
	}
	if len(s.Stale) != 1 {
		t.Errorf("Stale: want 1, got %d", len(s.Stale))
	}
}

func TestHeadlineAndWaitingArePreservedIndependently(t *testing.T) {
	// Headline (latest update's first non-heading line) is loaded
	// regardless of waiting_on. Task.WaitingOn is preserved verbatim
	// from the DB. The detail pane is responsible for displaying both.
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Hour).Format(time.RFC3339)
	insertTaskRow(t, db, "alpha", "in-progress", "medium", insertOpts{
		WaitingOn: "PR review", UpdatedAt: recent,
	})
	writeUpdate(t, root, "tasks", "alpha", "2026-05-14-poke.md",
		"# poke\n\nschema diff applied to staging",
		now.Add(-1*time.Hour))

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }}
	s, _ := l.Snapshot()
	if len(s.Awaiting) != 1 {
		t.Fatalf("Awaiting: want 1, got %d", len(s.Awaiting))
	}
	row := s.Awaiting[0]
	if row.Headline != "schema diff applied to staging" {
		t.Errorf("Headline: want %q, got %q", "schema diff applied to staging", row.Headline)
	}
	if !row.Task.WaitingOn.Valid || row.Task.WaitingOn.String != "PR review" {
		t.Errorf("Task.WaitingOn: want 'PR review', got %+v", row.Task.WaitingOn)
	}
}

func TestHeadlineSkipsHeadingLines(t *testing.T) {
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Hour).Format(time.RFC3339)
	insertTaskRow(t, db, "alpha", "in-progress", "medium", insertOpts{
		UpdatedAt: recent,
	})
	writeUpdate(t, root, "tasks", "alpha", "2026-05-14-progress.md",
		"# heading we skip\n\nschema diff applied to staging",
		now.Add(-1*time.Hour))

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }}
	s, _ := l.Snapshot()
	if got := s.Working[0].Headline; got != "schema diff applied to staging" {
		t.Errorf("Headline: want first non-heading line, got %q", got)
	}
}

func TestHeadlineUsesNewestFile(t *testing.T) {
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	insertTaskRow(t, db, "alpha", "in-progress", "medium", insertOpts{
		UpdatedAt: now.Format(time.RFC3339),
	})
	writeUpdate(t, root, "tasks", "alpha", "2026-05-10-old.md",
		"old headline", now.Add(-5*24*time.Hour))
	writeUpdate(t, root, "tasks", "alpha", "2026-05-14-new.md",
		"new headline", now.Add(-1*time.Hour))

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }}
	s, _ := l.Snapshot()
	if got := s.Working[0].Headline; got != "new headline" {
		t.Errorf("Headline: want newest, got %q", got)
	}
}

func TestActivityIncludesNoteAndStatusEvents(t *testing.T) {
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)

	startTs := now.Add(-3 * time.Hour).Format(time.RFC3339)
	insertTaskRow(t, db, "alpha", "in-progress", "high", insertOpts{
		UpdatedAt: startTs, StatusChangedAt: startTs,
	})
	doneTs := now.Add(-1 * 24 * time.Hour).Format(time.RFC3339)
	insertTaskRow(t, db, "beta", "done", "medium", insertOpts{
		UpdatedAt: doneTs, StatusChangedAt: doneTs,
	})
	writeUpdate(t, root, "tasks", "alpha", "2026-05-15-note.md",
		"drafted layout", now.Add(-1*time.Hour))

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }}
	s, _ := l.Snapshot()

	var kinds []string
	for _, e := range s.Activity {
		kinds = append(kinds, e.Kind+":"+e.TargetSlug)
	}
	wantContains := []string{"started:alpha", "done:beta", "note added:alpha"}
	for _, w := range wantContains {
		if !slices.Contains(kinds, w) {
			t.Errorf("Activity missing %q (got %v)", w, kinds)
		}
	}
}

func TestActivitySortedDescAndCapped(t *testing.T) {
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	insertTaskRow(t, db, "alpha", "in-progress", "high", insertOpts{
		UpdatedAt: now.Format(time.RFC3339),
	})
	// Five note files at known mtimes.
	for i := 1; i <= 5; i++ {
		writeUpdate(t, root, "tasks", "alpha",
			"2026-05-1"+itoa(i)+"-n.md", "headline "+itoa(i),
			now.Add(time.Duration(-i)*time.Hour))
	}

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }, ActivityN: 3}
	s, _ := l.Snapshot()
	if len(s.Activity) != 3 {
		t.Fatalf("cap: want 3, got %d", len(s.Activity))
	}
	for i := 1; i < len(s.Activity); i++ {
		if s.Activity[i-1].When.Before(s.Activity[i].When) {
			t.Errorf("Activity not desc-sorted at index %d", i)
		}
	}
}

func TestEventHashDeterministic(t *testing.T) {
	e := Event{
		When:       time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
		Kind:       "note added",
		TargetSlug: "alpha",
	}
	a := hashEvent(e)
	b := hashEvent(e)
	if a != b {
		t.Errorf("hash not deterministic: %q vs %q", a, b)
	}
	if len(a) != 4 {
		t.Errorf("hash length: want 4, got %d", len(a))
	}
}

func TestRelTimeDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h"},
		{3 * time.Hour, "3h"},
		{47 * time.Hour, "1d"},
		{49 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := relTimeDur(c.d); got != c.want {
			t.Errorf("relTimeDur(%v): want %q, got %q", c.d, c.want, got)
		}
	}
}

func TestDone7dCount(t *testing.T) {
	root, db := fixtureRoot(t)
	now := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	insertTaskRow(t, db, "old", "done", "low", insertOpts{
		UpdatedAt: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
	})
	insertTaskRow(t, db, "fresh1", "done", "low", insertOpts{
		UpdatedAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
	})
	insertTaskRow(t, db, "fresh2", "done", "low", insertOpts{
		UpdatedAt: now.Add(-5 * 24 * time.Hour).Format(time.RFC3339),
	})

	l := &Loader{DB: db, FlowRoot: root, Now: func() time.Time { return now }}
	s, _ := l.Snapshot()
	if s.Counts.Done7d != 2 {
		t.Errorf("Done7d: want 2, got %d", s.Counts.Done7d)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return ""
}
