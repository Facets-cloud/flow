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
		fmt.Fprintln(os.Stderr, "error: owner requires a subcommand (list, show, start, pause)")
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

// ownerStart marks an owner active and schedules its first tick now, so
// the next scheduler pass picks it up. Then it ticks every Every.
func ownerStart(args []string) int {
	o, db, rc := loadOwnerArg(args, "owner start")
	if rc != 0 {
		return rc
	}
	defer db.Close()
	o.Status = "active"
	o.NextWakeAt = sql.NullString{String: flowdb.NowISO(), Valid: true}
	if err := flowdb.UpdateOwner(db, o); err != nil {
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
	o.Status = "paused"
	if err := flowdb.UpdateOwner(db, o); err != nil {
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
	every := fs.String("every", "", "tick interval, e.g. 30m, 1h (required)")
	mkdir := fs.Bool("mkdir", false, "create --work-dir if it does not exist")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if *workDir == "" {
		fmt.Fprintln(os.Stderr, "error: --work-dir is required for owners")
		return 2
	}
	if *every == "" {
		fmt.Fprintln(os.Stderr, "error: --every is required (tick interval, e.g. 30m)")
		return 2
	}
	if _, err := time.ParseDuration(*every); err != nil {
		fmt.Fprintf(os.Stderr, "error: --every %q is not a valid duration (try 30m, 1h): %v\n", *every, err)
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
		Every:       *every,
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
	fmt.Printf("Next: edit the charter, then start the owner so it begins ticking every %s\n", *every)
	return 0
}
