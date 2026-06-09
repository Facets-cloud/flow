package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"strings"
	"testing"
	"time"
)

func TestOwnerTickDueDispatchesAndReschedules(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)

	past := sql.NullString{String: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Valid: true}
	future := sql.NullString{String: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Valid: true}
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "due", Name: "D", WorkDir: "/x", Every: "30m", NextWakeAt: past}); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "later", Name: "L", WorkDir: "/y", Every: "30m", NextWakeAt: future}); err != nil {
		t.Fatal(err)
	}

	var dispatched []string
	old := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) {
		dispatched = append(dispatched, slug)
		return 0, nil
	}
	t.Cleanup(func() { ownerTickLauncher = old })

	if rc := cmdOwner([]string{"tick-due"}); rc != 0 {
		t.Fatalf("tick-due rc=%d", rc)
	}

	if len(dispatched) != 1 || dispatched[0] != "due" {
		t.Fatalf("dispatched = %v, want [due]", dispatched)
	}

	// The dispatched owner's next tick must be advanced into the future so
	// the next scan doesn't re-fire it.
	o, err := flowdb.GetOwner(db, "due")
	if err != nil {
		t.Fatal(err)
	}
	next, err := time.Parse(time.RFC3339, o.NextWakeAt.String)
	if err != nil {
		t.Fatalf("parse next_wake_at %q: %v", o.NextWakeAt.String, err)
	}
	if !next.After(time.Now()) {
		t.Errorf("next_wake_at = %q not advanced into the future", o.NextWakeAt.String)
	}
}

func TestOwnerTickDueRecordsRunningPID(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	past := sql.NullString{String: time.Now().Add(-time.Hour).Format(time.RFC3339), Valid: true}
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m", Status: "active", NextWakeAt: past,
	}); err != nil {
		t.Fatal(err)
	}

	old := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) { return 4242, nil }
	t.Cleanup(func() { ownerTickLauncher = old })

	if rc := cmdOwner([]string{"tick-due"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.TickPID.Int64 != 4242 {
		t.Errorf("TickPID = %+v, want 4242 recorded on dispatch", o.TickPID)
	}
	if !o.TickStarted.Valid {
		t.Errorf("TickStarted should be set on dispatch")
	}
}

// A tick that finishes BEFORE the scheduler records its pid must not have
// its result clobbered. The scheduler dispatch write must touch only the
// running-tick + schedule columns, never last_tick_*. Regression for the
// dispatch-vs-finish race (full-row UpdateOwner overwrote the child's
// last_tick_status with the stale pre-tick value).
func TestOwnerTickDueFastFinishingChildKeepsResult(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	past := sql.NullString{String: time.Now().Add(-time.Hour).Format(time.RFC3339), Valid: true}
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m", Status: "active", NextWakeAt: past,
	}); err != nil {
		t.Fatal(err)
	}

	old := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) {
		// Simulate the detached child completing (recording 'ok' and
		// clearing its pid) before the parent's dispatch write lands.
		if _, err := db.Exec(
			`UPDATE owners SET last_tick_at=?, last_tick_status='ok', tick_pid=NULL, tick_started=NULL WHERE slug=?`,
			flowdb.NowISO(), slug,
		); err != nil {
			t.Fatal(err)
		}
		return 7777, nil
	}
	t.Cleanup(func() { ownerTickLauncher = old })

	if rc := cmdOwner([]string{"tick-due"}); rc != 0 {
		t.Fatalf("tick-due rc=%d", rc)
	}

	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.LastTickStatus.String != "ok" {
		t.Errorf("LastTickStatus = %q, want ok (child result clobbered by dispatch write)", o.LastTickStatus.String)
	}
	// The schedule must still advance (the dispatch did its own job).
	next, perr := time.Parse(time.RFC3339, o.NextWakeAt.String)
	if perr != nil || !next.After(time.Now()) {
		t.Errorf("next_wake_at = %q not advanced", o.NextWakeAt.String)
	}
}

// A tick that self-paces mid-run (sets next_wake_at via `flow owner next`)
// must keep that value — the finish write must not overwrite next_wake_at
// with the stale value loaded when the tick started.
func TestCmdOwnerTickPreservesSelfPacedNextWake(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	initial := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m",
		NextWakeAt: sql.NullString{String: initial, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	selfPaced := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)

	old := ownerTickRunner
	ownerTickRunner = func(h harness.Harness, prompt string) error {
		// Simulate the tick calling `flow owner next o1 --in 5m`.
		if _, err := db.Exec(`UPDATE owners SET next_wake_at=? WHERE slug=?`, selfPaced, "o1"); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	t.Cleanup(func() { ownerTickRunner = old })

	if rc := cmdOwnerTick([]string{"o1"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.NextWakeAt.String != selfPaced {
		t.Errorf("next_wake_at = %q, want self-paced %q (finish write clobbered it)", o.NextWakeAt.String, selfPaced)
	}
	if o.LastTickStatus.String != "ok" {
		t.Errorf("LastTickStatus = %q, want ok", o.LastTickStatus.String)
	}
}

// The detached tick (`flow __owner-tick`) must not run the harness for a
// non-active owner (paused/retired) — a guard for the window between
// dispatch and execution, and for any manual --auto on a paused owner.
func TestCmdOwnerTickDetachedSkipsNonActive(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m", Status: "paused",
	}); err != nil {
		t.Fatal(err)
	}
	ran := false
	old := ownerTickRunner
	ownerTickRunner = func(h harness.Harness, prompt string) error { ran = true; return nil }
	t.Cleanup(func() { ownerTickRunner = old })

	cmdOwnerTick([]string{"o1"})

	if ran {
		t.Errorf("harness should NOT run for a paused owner")
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.LastTickStatus.Valid {
		t.Errorf("no tick should be recorded for a skipped paused owner, got %q", o.LastTickStatus.String)
	}
}

// The hand-triggered `flow owner tick` must refuse a non-active owner with
// guidance rather than silently spawning a session.
func TestCmdOwnerTickManualRefusesNonActive(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m", Status: "paused",
	}); err != nil {
		t.Fatal(err)
	}
	spawned := false
	old := ownerInteractiveLauncher
	ownerInteractiveLauncher = func(o *flowdb.Owner, prompt string) error { spawned = true; return nil }
	t.Cleanup(func() { ownerInteractiveLauncher = old })

	if rc := ownerTickManual([]string{"o1"}); rc == 0 {
		t.Errorf("expected non-zero rc for a paused owner, got 0")
	}
	if spawned {
		t.Errorf("should NOT spawn an interactive tick for a paused owner")
	}
}

func TestCmdOwnerTickClearsTickPID(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m",
		TickPID: sql.NullInt64{Int64: 999, Valid: true}, TickStarted: sql.NullString{String: "2026-06-09T00:00:00Z", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	old := ownerTickRunner
	ownerTickRunner = func(h harness.Harness, prompt string) error { return nil }
	t.Cleanup(func() { ownerTickRunner = old })

	if rc := cmdOwnerTick([]string{"o1"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.TickPID.Valid {
		t.Errorf("TickPID should be cleared when the tick finishes, got %+v", o.TickPID)
	}
	if o.LastTickStatus.String != "ok" {
		t.Errorf("LastTickStatus = %q, want ok", o.LastTickStatus.String)
	}
}

func TestOwnerShowShowsRunningTick(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m",
		TickPID: sql.NullInt64{Int64: 4242, Valid: true}, TickStarted: sql.NullString{String: "2026-06-09T00:00:00Z", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	oldAlive := processAlive
	processAlive = func(pid int) bool { return pid == 4242 }
	t.Cleanup(func() { processAlive = oldAlive })

	out := captureStdout(t, func() {
		if rc := cmdOwner([]string{"show", "o1"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "running") || !strings.Contains(out, "4242") {
		t.Errorf("expected a running tick line with pid 4242; got:\n%s", out)
	}
}

func TestOwnerShowReconcilesDeadTick(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m",
		TickPID: sql.NullInt64{Int64: 4242, Valid: true}, TickStarted: sql.NullString{String: "2026-06-09T00:00:00Z", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	oldAlive := processAlive
	processAlive = func(pid int) bool { return false } // the tick pid is dead
	t.Cleanup(func() { processAlive = oldAlive })

	out := captureStdout(t, func() {
		if rc := cmdOwner([]string{"show", "o1"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if strings.Contains(out, "running") {
		t.Errorf("a dead tick pid must not display as running; got:\n%s", out)
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.TickPID.Valid {
		t.Errorf("dead tick pid should be reconciled (cleared), got %+v", o.TickPID)
	}
	if o.LastTickStatus.String != "dead" {
		t.Errorf("reconciled status = %q, want dead", o.LastTickStatus.String)
	}
}

func TestOwnerTickDueSkipsOwnerWithRunningTick(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	past := sql.NullString{String: time.Now().Add(-time.Hour).Format(time.RFC3339), Valid: true}
	// Due, but a tick is already running (live pid) → must be skipped.
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "busy", Name: "B", WorkDir: "/x", Every: "30m", Status: "active", NextWakeAt: past,
		TickPID: sql.NullInt64{Int64: 5555, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	// Due, with a DEAD tick pid → should still be dispatched.
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "free", Name: "F", WorkDir: "/y", Every: "30m", Status: "active", NextWakeAt: past,
		TickPID: sql.NullInt64{Int64: 6666, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	oldAlive := processAlive
	processAlive = func(pid int) bool { return pid == 5555 } // 5555 alive, 6666 dead
	t.Cleanup(func() { processAlive = oldAlive })

	var dispatched []string
	oldL := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) {
		dispatched = append(dispatched, slug)
		return 1, nil
	}
	t.Cleanup(func() { ownerTickLauncher = oldL })

	if rc := cmdOwner([]string{"tick-due"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	for _, s := range dispatched {
		if s == "busy" {
			t.Errorf("an owner with a LIVE tick must be skipped, got dispatched=%v", dispatched)
		}
	}
	free := false
	for _, s := range dispatched {
		if s == "free" {
			free = true
		}
	}
	if !free {
		t.Errorf("an owner with a DEAD tick pid should be dispatched, got %v", dispatched)
	}
}

func TestCmdOwnerTickRecordsOkStatus(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	var gotPrompt string
	old := ownerTickRunner
	ownerTickRunner = func(h harness.Harness, prompt string) error {
		gotPrompt = prompt
		return nil
	}
	t.Cleanup(func() { ownerTickRunner = old })

	if rc := cmdOwnerTick([]string{"o1"}); rc != 0 {
		t.Fatalf("tick rc=%d", rc)
	}

	if !strings.Contains(gotPrompt, "o1") {
		t.Errorf("tick prompt should name the owner; got:\n%s", gotPrompt)
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if !o.LastTickAt.Valid || o.LastTickAt.String == "" {
		t.Errorf("LastTickAt not recorded")
	}
	if o.LastTickStatus.String != "ok" {
		t.Errorf("LastTickStatus = %q, want ok", o.LastTickStatus.String)
	}
}

func TestCmdOwnerTickRecordsErrorStatus(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	old := ownerTickRunner
	ownerTickRunner = func(h harness.Harness, prompt string) error {
		return errTickBoom
	}
	t.Cleanup(func() { ownerTickRunner = old })

	if rc := cmdOwnerTick([]string{"o1"}); rc != 1 {
		t.Errorf("tick rc=%d, want 1 on runner error", rc)
	}
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.LastTickStatus.String != "error" {
		t.Errorf("LastTickStatus = %q, want error", o.LastTickStatus.String)
	}
}

func TestBuildOwnerTickPromptRoutesWorkThroughTasksAndPlaybooks(t *testing.T) {
	p := strings.ToLower(buildOwnerTickPrompt("desk"))
	// A tick is sessionless and gets no `flow done` KB sweep, so it must
	// route substantive work through tasks/playbooks (which self-close with
	// the sweep) rather than doing the work inline.
	for _, want := range []string{
		"do not do substantive work", // the core rule
		"flow run playbook",          // recurring → playbook
		"flow do --auto",             // one-time → task run that self-flow-dones
		"flow done",                  // the rationale: the sweep
		"owner:desk",                 // tag work to the owner
	} {
		if !strings.Contains(p, want) {
			t.Errorf("tick prompt must mention %q (orchestrate-don't-execute); prompt:\n%s", want, p)
		}
	}
}

func TestOwnerTickManualInteractiveSpawnsTab(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	var spawnedOwner, spawnedPrompt string
	oldI := ownerInteractiveLauncher
	ownerInteractiveLauncher = func(o *flowdb.Owner, prompt string) error {
		spawnedOwner, spawnedPrompt = o.Slug, prompt
		return nil
	}
	t.Cleanup(func() { ownerInteractiveLauncher = oldI })

	var headlessCalled bool
	oldH := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) {
		headlessCalled = true
		return 1, nil
	}
	t.Cleanup(func() { ownerTickLauncher = oldH })

	if rc := cmdOwner([]string{"tick", "o1"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if spawnedOwner != "o1" {
		t.Errorf("interactive launcher should run for o1, got %q", spawnedOwner)
	}
	if headlessCalled {
		t.Errorf("a hand-triggered tick must be interactive, not headless")
	}
	if !strings.Contains(spawnedPrompt, "o1") {
		t.Errorf("interactive prompt should name the owner")
	}

	// An interactive tick must be recorded on the owner (it ran), so
	// `flow owner show` doesn't report "last tick: (never)".
	o, err := flowdb.GetOwner(db, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if !o.LastTickAt.Valid || o.LastTickAt.String == "" {
		t.Errorf("interactive tick should record last_tick_at")
	}
	if o.LastTickStatus.String != "interactive" {
		t.Errorf("interactive tick status = %q, want interactive", o.LastTickStatus.String)
	}
}

func TestOwnerTickManualAutoRunsHeadless(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "30m"}); err != nil {
		t.Fatal(err)
	}

	var interactiveCalled bool
	oldI := ownerInteractiveLauncher
	ownerInteractiveLauncher = func(o *flowdb.Owner, prompt string) error { interactiveCalled = true; return nil }
	t.Cleanup(func() { ownerInteractiveLauncher = oldI })

	oldH := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) { return 4242, nil }
	t.Cleanup(func() { ownerTickLauncher = oldH })

	if rc := cmdOwner([]string{"tick", "o1", "--auto"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if interactiveCalled {
		t.Errorf("--auto must run headless, not spawn an interactive tab")
	}
	o, _ := flowdb.GetOwner(db, "o1")
	if o.TickPID.Int64 != 4242 {
		t.Errorf("--auto tick should record a running pid, got %+v", o.TickPID)
	}
}

func TestBuildOwnerTickPromptInteractiveAllowsHuman(t *testing.T) {
	p := strings.ToLower(buildOwnerTickPromptInteractive("desk"))
	if !strings.Contains(p, "askuserquestion") {
		t.Errorf("interactive prompt should permit AskUserQuestion (human present)")
	}
	if strings.Contains(p, "do not use askuserquestion") {
		t.Errorf("interactive prompt must NOT forbid AskUserQuestion")
	}
	// Still orchestrates + journals like the headless tick.
	for _, want := range []string{"flow owner show desk", "owners/desk/updates", "never execute work inline"} {
		if !strings.Contains(p, want) {
			t.Errorf("interactive prompt missing %q", want)
		}
	}
}

func TestCmdOwnerNextSetsNextWake(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{Slug: "o1", Name: "O", WorkDir: "/x", Every: "24h"}); err != nil {
		t.Fatal(err)
	}

	// --in <dur>: wake this far from now.
	if rc := cmdOwner([]string{"next", "o1", "--in", "15m"}); rc != 0 {
		t.Fatalf("--in rc=%d", rc)
	}
	o, _ := flowdb.GetOwner(db, "o1")
	if !o.NextWakeAt.Valid {
		t.Fatal("next_wake not set by --in")
	}
	w, err := time.Parse(time.RFC3339, o.NextWakeAt.String)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(w); d < 14*time.Minute || d > 16*time.Minute {
		t.Errorf("next wake should be ~15m from now, got %v", d)
	}

	// --at <ts>: wake at an absolute time.
	at := time.Now().Add(3 * time.Hour).Format(time.RFC3339)
	if rc := cmdOwner([]string{"next", "o1", "--at", at}); rc != 0 {
		t.Fatalf("--at rc=%d", rc)
	}
	o, _ = flowdb.GetOwner(db, "o1")
	if o.NextWakeAt.String != at {
		t.Errorf("next_wake = %q, want %q", o.NextWakeAt.String, at)
	}

	// exactly one of --in / --at.
	if rc := cmdOwner([]string{"next", "o1"}); rc != 2 {
		t.Errorf("no flag: rc=%d, want 2", rc)
	}
	if rc := cmdOwner([]string{"next", "o1", "--in", "1h", "--at", at}); rc != 2 {
		t.Errorf("both flags: rc=%d, want 2", rc)
	}
}

func TestTickPromptsSelfPaceNextWake(t *testing.T) {
	for label, p := range map[string]string{
		"headless":    buildOwnerTickPrompt("desk"),
		"interactive": buildOwnerTickPromptInteractive("desk"),
	} {
		if !strings.Contains(strings.ToLower(p), "flow owner next desk") {
			t.Errorf("%s tick prompt must instruct self-paced scheduling via `flow owner next`; got:\n%s", label, p)
		}
	}
}

func TestBuildOwnerTickPromptReadsAndWritesJournal(t *testing.T) {
	p := strings.ToLower(buildOwnerTickPrompt("desk"))
	for _, want := range []string{
		"flow owner show desk", // review via owner show (includes runs), not list-tasks
		"owners/desk/updates",  // journal location (like playbook/task updates)
		"write a short note",   // record what it did for the next tick
		"this is your memory",  // rationale
	} {
		if !strings.Contains(p, want) {
			t.Errorf("tick prompt must mention %q (read/write journal + owner show); prompt:\n%s", want, p)
		}
	}
	// The review step must NOT use the runs-blind `flow list tasks --tag`
	// (it excludes playbook_run, so the owner would miss its own runs).
	if strings.Contains(p, "flow list tasks --tag owner:desk") {
		t.Errorf("review step should use `flow owner show` (includes playbook runs), not `flow list tasks --tag`")
	}
}

var errTickBoom = &tickTestErr{}

type tickTestErr struct{}

func (*tickTestErr) Error() string { return "boom" }
