package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const ownerCharterStub = `# %s — charter

This owner's operating manual. Edit freely or via a flow skill session.

## Owns
<the outcome this owner is responsible for>

## Each tick
- observe: <what to look at, e.g. open PRs / CI / prod health>
- act: <what to do when something is off-target>
- ask: <when to create a question-task for the human instead of guessing>

## Done / escalate
- <when an outcome counts as met, and when to escalate to the human>
`

// cmdOwner dispatches `flow owner list|show|start|pause`.
func cmdOwner(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: owner requires a subcommand (list, show, start, pause, tick, next, retire)")
		return 2
	}
	switch args[0] {
	case "list":
		return ownerList(args[1:])
	case "show":
		return ownerShow(args[1:])
	case "start":
		return ownerStart(args[1:])
	case "pause":
		return ownerPause(args[1:])
	case "next":
		return ownerNext(args[1:])
	case "retire":
		return ownerRetire(args[1:])
	case "tick":
		return ownerTickManual(args[1:])
	case "tick-due":
		return ownerTickDue(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown owner subcommand %q\n", args[0])
	return 2
}

// loadOwnerArg parses a single owner-slug argument, opens the DB, and
// loads the owner. On any error it reports to stderr, closes the DB,
// and returns a non-zero rc with a nil owner/db. On success the caller
// owns the returned *sql.DB and must Close it.
func loadOwnerArg(args []string, name string) (*flowdb.Owner, *sql.DB, int) {
	fs := flagSet(name)
	if err := fs.Parse(args); err != nil {
		return nil, nil, 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "error: %s requires an owner slug\n", name)
		return nil, nil, 2
	}
	slug := fs.Arg(0)

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, nil, 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, nil, 1
	}
	o, err := flowdb.GetOwner(db, slug)
	if err != nil {
		db.Close()
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: no owner %q\n", slug)
			return nil, nil, 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, nil, 1
	}
	return o, db, 0
}

// ownerShow renders the owner's accountability surface: metadata, next
// tick, and a live list of what it owns (tasks tagged owner:<slug>),
// split into in-flight work units and questions parked for the human.
func ownerShow(args []string) int {
	o, db, rc := loadOwnerArg(args, "owner show")
	if rc != 0 {
		return rc
	}
	defer db.Close()

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Reconcile a stale running tick (pid died before finalizing) so the
	// display reflects reality.
	reconcileOwnerTick(db, o)

	now := time.Now()
	fmt.Printf("owner:        %s\n", o.Slug)
	fmt.Printf("name:         %s\n", o.Name)
	fmt.Printf("status:       %s\n", o.Status)
	fmt.Printf("every:        %s\n", o.Every)
	if o.ProjectSlug.Valid && o.ProjectSlug.String != "" {
		fmt.Printf("project:      %s\n", o.ProjectSlug.String)
	}
	fmt.Printf("work_dir:     %s\n", o.WorkDir)
	if o.TickPID.Valid {
		since := ""
		if o.TickStarted.Valid && o.TickStarted.String != "" {
			since = ", since " + o.TickStarted.String
		}
		fmt.Printf("tick:         running (pid %d%s)\n", o.TickPID.Int64, since)
	}
	fmt.Printf("next tick:    %s\n", ownerNextTickLabel(o, now))
	lastTick := "(never)"
	if o.LastTickAt.Valid && o.LastTickAt.String != "" {
		lastTick = o.LastTickAt.String
		if o.LastTickStatus.Valid && o.LastTickStatus.String != "" {
			lastTick += "  [" + o.LastTickStatus.String + "]"
		}
	}
	fmt.Printf("last tick:    %s\n", lastTick)
	fmt.Printf("charter:      %s\n", filepath.Join(root, "owners", o.Slug, "charter.md"))

	// The ledger: tasks tagged owner:<slug>, split into work units and
	// questions (those also tagged 'question').
	owned, err := flowdb.ListTasks(db, flowdb.TaskFilter{Tag: flowdb.NormalizeTag("owner:" + o.Slug)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	slugs := make([]string, len(owned))
	for i, tk := range owned {
		slugs[i] = tk.Slug
	}
	tagsBySlug, err := flowdb.GetTaskTagsBatch(db, slugs)
	if err != nil {
		tagsBySlug = map[string][]string{}
	}
	isQuestion := func(s string) bool {
		for _, tg := range tagsBySlug[s] {
			if tg == "question" {
				return true
			}
		}
		return false
	}
	var inflight, runs, questions []*flowdb.Task
	for _, tk := range owned {
		switch {
		case isQuestion(tk.Slug):
			questions = append(questions, tk)
		case tk.Kind == "playbook_run":
			runs = append(runs, tk)
		case tk.Status == "done":
			// A completed owned task is not "in flight" — drop it from the
			// active list so the section reflects only outstanding work.
		default:
			inflight = append(inflight, tk)
		}
	}
	printOwnerTaskSection("in flight", inflight)
	printOwnerTaskSection("playbook runs", runs)
	printOwnerTaskSection("questions for you", questions)
	return 0
}

func printOwnerTaskSection(label string, tasks []*flowdb.Task) {
	if len(tasks) == 0 {
		fmt.Printf("%s: (none)\n", label)
		return
	}
	fmt.Printf("%s:\n", label)
	for _, tk := range tasks {
		fmt.Printf("  - %-30s [%s]\n", tk.Slug, tk.Status)
	}
}

// ownerRetire implements `flow owner retire <slug> [--delete]`. Graceful
// retire (default) marks the owner status='retired' + archived: it stops
// ticking and disappears from the default list, but its charter, journal,
// tick logs, and owned tasks are preserved. `--delete` instead hard-removes
// the row and the owners/<slug>/ directory (the supported replacement for
// hand-deleting). Either way, owned tasks (tagged owner:<slug>) are left
// intact — the work the owner spawned outlives it.
func ownerRetire(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "error: owner retire requires an owner slug")
		return 2
	}
	slug := args[0]
	fs := flagSet("owner retire")
	del := fs.Bool("delete", false, "permanently delete the owner row + its directory (instead of archiving)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if _, err := flowdb.GetOwner(db, slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: no owner %q\n", slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if *del {
		if err := flowdb.DeleteOwner(db, slug); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		dir := filepath.Join(root, "owners", slug)
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove %s: %v\n", dir, err)
		}
		fmt.Printf("deleted owner %q (row + %s). Owned tasks (tag owner:%s) are untouched.\n", slug, dir, slug)
		return 0
	}

	if err := flowdb.RetireOwner(db, slug); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("retired owner %q — it will no longer tick. Files preserved under owners/%s/. (Use --delete to remove entirely.)\n", slug, slug)
	return 0
}

// ownerNext implements `flow owner next <slug> --in <dur> | --at <when>`:
// set the owner's next tick time. This is how a tick SELF-PACES — at the
// end of each run it decides when it next needs to act and sets it here,
// overriding the `--every` fallback. Exactly one of --in/--at is required.
func ownerNext(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "error: owner next requires an owner slug")
		return 2
	}
	slug := args[0]
	fs := flagSet("owner next")
	in := fs.String("in", "", "set next tick this far from now (e.g. 15m, 2h)")
	at := fs.String("at", "", "set next tick at an absolute time (RFC3339, or YYYY-MM-DD/today/tomorrow)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if (*in == "") == (*at == "") {
		fmt.Fprintln(os.Stderr, "error: give exactly one of --in or --at")
		return 2
	}

	var next time.Time
	if *in != "" {
		d, err := time.ParseDuration(*in)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --in %q is not a valid duration (try 15m, 2h): %v\n", *in, err)
			return 2
		}
		next = time.Now().Add(d)
	} else {
		t, err := parseOwnerWhen(*at)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --at %q: %v\n", *at, err)
			return 2
		}
		next = t
	}

	// Reject a wake time in the past: it would leave the owner perpetually
	// due (ticking every scheduler pass). A tick self-paces FORWARD, so a
	// past time is always a mistake (stale --at or negative --in).
	if next.Before(time.Now()) {
		fmt.Fprintf(os.Stderr, "error: next tick %s is in the past — pick a future time (ticks self-pace forward)\n", next.Format(time.RFC3339))
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	if _, err := flowdb.GetOwner(db, slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: no owner %q\n", slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// Targeted write (only next_wake_at) so a self-pacing tick or a manual
	// `owner next` can't clobber the tick bookkeeping written concurrently.
	if err := flowdb.SetOwnerNextWake(db, slug, next.Format(time.RFC3339)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("owner %q next tick set to %s\n", slug, next.Format(time.RFC3339))
	return 0
}

// parseOwnerWhen accepts an RFC3339 timestamp or a date expression
// (YYYY-MM-DD, today, tomorrow, weekday, Nd) for `flow owner next --at`.
func parseOwnerWhen(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	d, err := parseDueDate(s, time.Now())
	if err != nil {
		return time.Time{}, fmt.Errorf("not an RFC3339 time or a recognizable date: %w", err)
	}
	return d, nil
}

// ownerStart marks an owner active and schedules its first tick now, so
// the next scheduler pass picks it up. Then it ticks every Every.
func ownerStart(args []string) int {
	o, db, rc := loadOwnerArg(args, "owner start")
	if rc != 0 {
		return rc
	}
	defer db.Close()
	// Targeted write that also clears archived_at — so `start` un-retires a
	// retired owner (else DueOwners' archived_at filter would leave it
	// active-but-never-ticking and hidden from the list).
	if err := flowdb.ActivateOwner(db, o.Slug, flowdb.NowISO()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Started owner %q — first tick due now, then every %s\n", o.Slug, o.Every)
	return 0
}

// ownerPause stops an owner from ticking while preserving all its state.
func ownerPause(args []string) int {
	o, db, rc := loadOwnerArg(args, "owner pause")
	if rc != 0 {
		return rc
	}
	defer db.Close()
	if err := flowdb.PauseOwner(db, o.Slug); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Paused owner %q\n", o.Slug)
	return 0
}

// ownerList implements `flow owner list` — one row per owner with its
// status, interval, and next scheduled tick.
func ownerList(args []string) int {
	fs := flagSet("owner list")
	includeArchived := fs.Bool("include-archived", false, "include archived owners")
	statusFilter := fs.String("status", "", "active|paused|retired")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	owners, err := flowdb.ListOwners(db, flowdb.OwnerFilter{
		Status:          *statusFilter,
		IncludeArchived: *includeArchived,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(owners) == 0 {
		fmt.Println(`No owners. Create one with: flow add owner "<name>" --work-dir <path> --every <dur>`)
		return 0
	}

	now := time.Now()
	fmt.Printf("%-20s %-8s %-6s %s\n", "SLUG", "STATUS", "EVERY", "NEXT TICK")
	for _, o := range owners {
		fmt.Printf("%-20s %-8s %-6s %s\n", o.Slug, o.Status, o.Every, ownerNextTickLabel(o, now))
	}
	return 0
}

// ownerNextTickLabel renders the "next tick" column: the scheduled
// next_wake_at with a relative hint, or a parenthetical status when the
// owner isn't actively scheduled.
func ownerNextTickLabel(o *flowdb.Owner, now time.Time) string {
	switch o.Status {
	case "paused":
		return "(paused)"
	case "retired":
		return "(retired)"
	}
	if !o.NextWakeAt.Valid || o.NextWakeAt.String == "" {
		return "(not started)"
	}
	label := o.NextWakeAt.String
	if rel := relativeWake(o.NextWakeAt.String, now); rel != "" {
		label += "  " + rel
	}
	return label
}

// relativeWake formats an RFC3339 wake time relative to now, e.g.
// "(in 22m)" or "(overdue 5m)". Returns "" if ts can't be parsed.
func relativeWake(ts string, now time.Time) string {
	w, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	d := w.Sub(now)
	if d <= 0 {
		return fmt.Sprintf("(overdue %s)", roundDur(-d))
	}
	return fmt.Sprintf("(in %s)", roundDur(d))
}

func roundDur(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

// addOwner implements `flow add owner "<name>" --work-dir <p> --every <dur>`.
func addOwner(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "error: add owner requires a name")
		return 2
	}
	name := args[0]
	fs := flagSet("add owner")
	slugFlag := fs.String("slug", "", "short user-chosen slug (default: auto-generated from name)")
	workDir := fs.String("work-dir", "", "absolute path to the owner's work directory (required)")
	project := fs.String("project", "", "parent project slug (optional)")
	every := fs.String("every", "", "fallback/max tick interval (heartbeat floor), e.g. 1h, 24h; default 24h — ticks self-pace via `flow owner next`")
	mkdir := fs.Bool("mkdir", false, "create --work-dir if it does not exist")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if *workDir == "" {
		fmt.Fprintln(os.Stderr, "error: --work-dir is required for owners")
		return 2
	}
	// --every is optional: it's only the fallback heartbeat floor (so a tick
	// that never sets its next wake still re-wakes). The tick decides the
	// real cadence per-run via `flow owner next`.
	everyVal := *every
	if everyVal == "" {
		everyVal = "24h"
	} else if _, err := time.ParseDuration(everyVal); err != nil {
		fmt.Fprintf(os.Stderr, "error: --every %q is not a valid duration (try 1h, 24h): %v\n", *every, err)
		return 2
	}
	abs, err := resolveWorkDir(*workDir, *mkdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	var slug string
	if *slugFlag != "" {
		slug = *slugFlag
		// An explicit slug is taken as-is (not auto-mangled like the
		// generated path). Pre-check for a collision so the user gets a
		// friendly message instead of a raw UNIQUE-constraint error.
		if _, err := flowdb.GetOwner(db, slug); err == nil {
			fmt.Fprintf(os.Stderr, "error: owner slug %q already exists — pick another --slug\n", slug)
			return 1
		} else if !errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	} else {
		baseSlug, err := Slugify(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		slug, err = uniqueSlug(db, "owners", baseSlug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	var projectSlug sql.NullString
	if *project != "" {
		p, err := flowdb.GetProject(db, *project)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fmt.Fprintf(os.Stderr, "error: project %q not found\n", *project)
				return 1
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		projectSlug = sql.NullString{String: p.Slug, Valid: true}
	}

	o := &flowdb.Owner{
		Slug:        slug,
		Name:        name,
		WorkDir:     abs,
		ProjectSlug: projectSlug,
		Every:       everyVal,
	}
	if err := flowdb.CreateOwner(db, o); err != nil {
		fmt.Fprintf(os.Stderr, "error: create owner: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	ownerDir := filepath.Join(root, "owners", slug)
	if err := os.MkdirAll(filepath.Join(ownerDir, "updates"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	charterPath := filepath.Join(ownerDir, "charter.md")
	if _, err := os.Stat(charterPath); os.IsNotExist(err) {
		if err := os.WriteFile(charterPath, []byte(fmt.Sprintf(ownerCharterStub, name)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	if err := flowdb.UpsertWorkdir(db, abs, "", "", ""); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("Created owner %q at %s\n", slug, ownerDir)
	fmt.Printf("Next: edit the charter, then start the owner (it self-paces; %s is the fallback interval)\n", everyVal)
	return 0
}
