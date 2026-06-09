package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCmdAddOwnerHappyPath(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()

	rc := cmdAdd([]string{"owner", "agent-factory maintenance",
		"--work-dir", wd, "--every", "30m", "--slug", "af-maint"})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	o, err := flowdb.GetOwner(db, "af-maint")
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if o.Name != "agent-factory maintenance" {
		t.Errorf("name = %q", o.Name)
	}
	if o.WorkDir != wd {
		t.Errorf("work_dir = %q, want %q", o.WorkDir, wd)
	}
	if o.Every != "30m" {
		t.Errorf("every = %q, want 30m", o.Every)
	}
	if o.Status != "active" {
		t.Errorf("status = %q, want active", o.Status)
	}
	// A freshly added owner is not yet started → no next tick scheduled.
	if o.NextWakeAt.Valid {
		t.Errorf("NextWakeAt should be NULL until started, got %q", o.NextWakeAt.String)
	}

	// charter.md + updates/ should exist on disk.
	if _, err := os.Stat(filepath.Join(root, "owners", "af-maint", "charter.md")); err != nil {
		t.Errorf("charter.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "owners", "af-maint", "updates")); err != nil {
		t.Errorf("updates/ dir missing: %v", err)
	}
}

// `flow owner next` must reject a wake time already in the past — a stale
// --at (or a negative --in) would leave the owner perpetually due, ticking
// every scheduler pass.
func TestCmdOwnerNextRejectsPastTime(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m",
		NextWakeAt: sql.NullString{String: future, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	if rc := cmdOwner([]string{"next", "o1", "--at", "2020-01-01T00:00:00Z"}); rc == 0 {
		t.Fatalf("expected non-zero rc for a past --at, got 0")
	}
	// A negative --in must be rejected the same way.
	if rc := cmdOwner([]string{"next", "o1", "--in", "-5m"}); rc == 0 {
		t.Fatalf("expected non-zero rc for a negative --in, got 0")
	}

	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.NextWakeAt.String != future {
		t.Errorf("next_wake_at = %q, want unchanged %q (past time should not be written)", o.NextWakeAt.String, future)
	}
}

// An explicit --slug that collides with an existing owner should fail with
// a friendly message, not a raw SQL UNIQUE-constraint error.
func TestCmdAddOwnerDuplicateSlugFriendlyError(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"owner", "first", "--work-dir", wd, "--slug", "dup"}); rc != 0 {
		t.Fatalf("seed owner rc=%d", rc)
	}

	read := captureStderr(t)
	rc := cmdAdd([]string{"owner", "second", "--work-dir", wd, "--slug", "dup"})
	stderr := read()
	if rc == 0 {
		t.Fatalf("expected non-zero rc for a duplicate --slug, got 0")
	}
	if strings.Contains(stderr, "UNIQUE") || strings.Contains(stderr, "constraint") {
		t.Errorf("expected a friendly message, got raw SQL error:\n%s", stderr)
	}
	if !strings.Contains(stderr, "dup") || !strings.Contains(strings.ToLower(stderr), "exists") {
		t.Errorf("expected message naming the slug and 'exists', got:\n%s", stderr)
	}
}

func TestCmdAddOwnerEveryOptionalWithDefault(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()

	// --every is now optional (just the fallback heartbeat floor) → defaults.
	if rc := cmdAdd([]string{"owner", "no every", "--work-dir", wd, "--slug", "noev"}); rc != 0 {
		t.Fatalf("missing --every should succeed now (defaults), rc=%d", rc)
	}
	db := openFlowDB(t)
	o, err := flowdb.GetOwner(db, "noev")
	if err != nil {
		t.Fatal(err)
	}
	if o.Every != "24h" {
		t.Errorf("default every = %q, want 24h", o.Every)
	}

	// --work-dir still required; bad --every still rejected.
	if rc := cmdAdd([]string{"owner", "no workdir", "--every", "30m"}); rc != 2 {
		t.Errorf("missing --work-dir: rc=%d, want 2", rc)
	}
	if rc := cmdAdd([]string{"owner", "bad every", "--work-dir", wd, "--every", "soon"}); rc != 2 {
		t.Errorf("invalid --every: rc=%d, want 2", rc)
	}
}

func TestCmdOwnerListShowsNextTick(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)

	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "started", Name: "S", WorkDir: "/x", Every: "30m",
		NextWakeAt: sql.NullString{String: "2026-06-08T13:00:00Z", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "fresh", Name: "F", WorkDir: "/y", Every: "1h",
	}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdOwner([]string{"list"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})

	if !strings.Contains(out, "started") || !strings.Contains(out, "2026-06-08T13:00:00Z") {
		t.Errorf("expected started owner with its next tick in output, got:\n%s", out)
	}
	if !strings.Contains(out, "fresh") || !strings.Contains(out, "not started") {
		t.Errorf("expected fresh owner marked 'not started', got:\n%s", out)
	}
}

// flow list owners is a verb-first alias for `flow owner list` — both
// render the same listing (the surface owners share with task/project/
// playbook). Only list and show are dual-formed; lifecycle verbs stay
// grouped under `flow owner`.
func TestCmdListOwnersAlias(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "started", Name: "S", WorkDir: "/x", Every: "30m",
		NextWakeAt: sql.NullString{String: "2026-06-08T13:00:00Z", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	grouped := captureStdout(t, func() {
		if rc := cmdOwner([]string{"list"}); rc != 0 {
			t.Fatalf("owner list rc=%d", rc)
		}
	})
	alias := captureStdout(t, func() {
		if rc := cmdList([]string{"owners"}); rc != 0 {
			t.Fatalf("list owners rc=%d", rc)
		}
	})
	if alias != grouped {
		t.Errorf("`flow list owners` must match `flow owner list`\n--- owner list ---\n%s\n--- list owners ---\n%s", grouped, alias)
	}
	if !strings.Contains(alias, "started") {
		t.Errorf("expected owner in `flow list owners` output, got:\n%s", alias)
	}
}

// flow show owner <slug> is a verb-first alias for `flow owner show <slug>`.
func TestCmdShowOwnerAlias(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	grouped := captureStdout(t, func() {
		if rc := cmdOwner([]string{"show", "o1"}); rc != 0 {
			t.Fatalf("owner show rc=%d", rc)
		}
	})
	alias := captureStdout(t, func() {
		if rc := cmdShow([]string{"owner", "o1"}); rc != 0 {
			t.Fatalf("show owner rc=%d", rc)
		}
	})
	if alias != grouped {
		t.Errorf("`flow show owner o1` must match `flow owner show o1`\n--- owner show ---\n%s\n--- show owner ---\n%s", grouped, alias)
	}
	if !strings.Contains(alias, "o1") {
		t.Errorf("expected owner slug in `flow show owner` output, got:\n%s", alias)
	}
}

func TestCmdOwnerStartSchedulesTick(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	if rc := cmdOwner([]string{"start", "o1"}); rc != 0 {
		t.Fatalf("start rc=%d", rc)
	}

	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if o.Status != "active" {
		t.Errorf("status = %q, want active", o.Status)
	}
	if !o.NextWakeAt.Valid || o.NextWakeAt.String == "" {
		t.Errorf("start should schedule a next tick, NextWakeAt = %+v", o.NextWakeAt)
	}
}

func TestCmdOwnerPause(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	if rc := cmdOwner([]string{"pause", "o1"}); rc != 0 {
		t.Fatalf("pause rc=%d", rc)
	}

	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if o.Status != "paused" {
		t.Errorf("status = %q, want paused", o.Status)
	}
}

func TestCmdOwnerStartUnknownErrors(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdOwner([]string{"start", "nope"}); rc != 1 {
		t.Errorf("start unknown owner: rc=%d, want 1", rc)
	}
}

func TestCmdAddTaskWithTags(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()

	rc := cmdAdd([]string{"task", "fix flaky", "--work-dir", wd, "--slug", "fix-flaky",
		"--tag", "question", "--tag", "owner:af-maint"})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	tags, err := flowdb.GetTaskTags(db, "fix-flaky")
	if err != nil {
		t.Fatalf("GetTaskTags: %v", err)
	}
	want := map[string]bool{"question": true, "owner:af-maint": true}
	for _, tg := range tags {
		delete(want, tg)
	}
	if len(want) != 0 {
		t.Errorf("missing tags %v; got %v", want, tags)
	}
}

func TestCmdOwnerRetireGraceful(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "1h"}); err != nil {
		t.Fatal(err)
	}

	if rc := cmdOwner([]string{"retire", "o1"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatalf("owner row should still exist after graceful retire: %v", err)
	}
	if o.Status != "retired" || !o.ArchivedAt.Valid {
		t.Errorf("expected retired+archived, got status=%q archived=%v", o.Status, o.ArchivedAt.Valid)
	}

	if rc := cmdOwner([]string{"retire", "nope"}); rc != 1 {
		t.Errorf("retiring unknown owner: rc=%d, want 1", rc)
	}
}

func TestCmdOwnerRetireDelete(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "1h"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "owners", "o1")
	if err := os.MkdirAll(filepath.Join(dir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}

	if rc := cmdOwner([]string{"retire", "o1", "--delete"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := flowdb.GetOwner(db, "o1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("owner row should be deleted, got err=%v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("owner directory should be removed")
	}
}

func TestCmdOwnerShowSeparatesPlaybookRuns(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	insertTask(t, db, "fix-1", "fix it", "backlog", "medium", "/x", nil)
	if err := flowdb.AddTaskTag(db, "fix-1", "owner:o1"); err != nil {
		t.Fatal(err)
	}
	// A playbook run owned by the owner.
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, priority, work_dir, created_at, updated_at)
		 VALUES ('run-1', 'a run', 'backlog', 'playbook_run', 'medium', '/x', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.AddTaskTag(db, "run-1", "owner:o1"); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdOwner([]string{"show", "o1"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})

	if !strings.Contains(out, "playbook runs") {
		t.Errorf("expected a 'playbook runs' section; got:\n%s", out)
	}
	// The run goes under playbook runs; the regular task stays in flight.
	if !strings.Contains(out, "run-1") || !strings.Contains(out, "fix-1") {
		t.Errorf("expected both run-1 and fix-1 in output; got:\n%s", out)
	}
	// run-1 must NOT be rendered in the in-flight section (it precedes
	// 'playbook runs:'), so the in-flight line for run-1 should not exist.
	inflightIdx := strings.Index(out, "in flight:")
	runIdx := strings.Index(out, "run-1")
	pbIdx := strings.Index(out, "playbook runs:")
	if !(runIdx > pbIdx && pbIdx > inflightIdx) {
		t.Errorf("run-1 should appear under 'playbook runs:' (after it), layout wrong:\n%s", out)
	}
}

func TestCmdOwnerShowListsOwnedTasksAndQuestions(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)

	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "af-maint", Name: "agent-factory maintenance", WorkDir: "/x", Every: "30m",
		NextWakeAt: sql.NullString{String: "2026-06-08T13:00:00Z", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	// A work unit and a question, both tagged to the owner.
	insertTask(t, db, "fix-485", "fix #485", "in-progress", "medium", "/x", nil)
	if err := flowdb.AddTaskTag(db, "fix-485", "owner:af-maint"); err != nil {
		t.Fatal(err)
	}
	insertTask(t, db, "q-flaky", "is the flaky test worth fixing?", "backlog", "medium", "/x", nil)
	if err := flowdb.AddTaskTag(db, "q-flaky", "owner:af-maint"); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.AddTaskTag(db, "q-flaky", "question"); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdOwner([]string{"show", "af-maint"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})

	for _, want := range []string{
		"af-maint",                // slug
		"agent-factory maintenance", // name
		"30m",                     // every
		"2026-06-08T13:00:00Z",    // next tick
		"fix-485",                 // owned work unit
		"q-flaky",                 // owned question
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q; got:\n%s", want, out)
		}
	}
}
