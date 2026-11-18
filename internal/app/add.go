package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const projectBriefStub = `# %s

What this project is, why it matters, success criteria. Edit this freely
or let the flow skill interview you and rewrite it.
`

const taskBriefStub = `# %s

Edit this brief freely via ` + "`flow edit`" + ` or by composing a flow skill
session. Sections to cover: What / Why / Where / Done when / Out of scope /
Open questions.
`

// driverBriefStub is the brief a human-added task starts with (skill §4.2).
const driverBriefStub = "# %s\n\nRole: driver, follow flow skill references/driver.md\n\n## Goal\n\n## Done when\n" +
	doneWhenPlaceholder + "\n\n## Context\n\n## Decisions\n"

// hasOwnerTag reports whether tags include an owner-managed tag.
func hasOwnerTag(tags []string) bool {
	for _, t := range tags {
		if isOwnerTag(strings.ToLower(strings.TrimSpace(t))) {
			return true
		}
	}
	return false
}

// isOwnerTag is the owner ledger's exact form: owner:<slug>, slug non-empty
// with no surrounding space.
func isOwnerTag(tag string) bool {
	slug, ok := strings.CutPrefix(tag, "owner:")
	return ok && slug != "" && slug == strings.TrimSpace(slug)
}

// cmdAdd dispatches `flow add project|task|playbook ...`.
func cmdAdd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: add requires 'project', 'task', or 'playbook'")
		return 2
	}
	switch args[0] {
	case "project":
		return addProject(args[1:])
	case "task":
		return addTask(args[1:])
	case "playbook":
		return addPlaybook(args[1:])
	case "owner":
		return addOwner(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown add subcommand %q\n", args[0])
	return 2
}

func addProject(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "error: add project requires a name")
		return 2
	}
	name := args[0]
	fs := flagSet("add project")
	slugFlag := fs.String("slug", "", "short user-chosen slug (default: auto-generated from name)")
	workDir := fs.String("work-dir", "", "absolute path to the project's work directory (required)")
	priority := fs.String("priority", "medium", "high|medium|low")
	mkdir := fs.Bool("mkdir", false, "create --work-dir if it does not exist")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if !isValidPriority(*priority) {
		fmt.Fprintf(os.Stderr, "error: priority must be high|medium|low, got %q\n", *priority)
		return 2
	}
	if *workDir == "" {
		fmt.Fprintln(os.Stderr, "error: --work-dir is required for projects")
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
		slug, err = uniqueSlug(db, "projects", baseSlug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at)
		 VALUES (?, ?, 'active', ?, ?, ?, ?)`,
		slug, name, *priority, abs, now, now,
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: insert project: %v\n", err)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	projDir := filepath.Join(root, "projects", slug)
	if err := os.MkdirAll(filepath.Join(projDir, "updates"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	briefPath := filepath.Join(projDir, "brief.md")
	if _, err := os.Stat(briefPath); os.IsNotExist(err) {
		if err := os.WriteFile(briefPath, []byte(fmt.Sprintf(projectBriefStub, name)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	if err := flowdb.UpsertWorkdir(db, abs, "", "", ""); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("Created project %q at %s\n", slug, projDir)
	fmt.Printf("Next: flow add task \"<name>\" --project %s\n", slug)
	return 0
}

func addTask(args []string) int { return createTask(args, true) }

// createTask is `flow add task`. hereNote=false skips hereBindNote: `flow
// auto` creates a child for a new session, so binding the creating session
// to it with --here is never the plan.
func createTask(args []string, hereNote bool) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "error: add task requires a name")
		return 2
	}
	name := args[0]
	fs := flagSet("add task")
	slugFlag := fs.String("slug", "", "short user-chosen slug (default: auto-generated from name)")
	project := fs.String("project", "", "parent project slug (optional)")
	workDir := fs.String("work-dir", "", "work directory (overrides project default)")
	priority := fs.String("priority", "medium", "high|medium|low")
	dueFlag := fs.String("due", "", "due date (YYYY-MM-DD, today, tomorrow, monday, 3d)")
	assigneeFlag := fs.String("assignee", "", "optional assignee (default: self)")
	var tags stringSliceFlag
	fs.Var(&tags, "tag", "attach a tag (repeatable: --tag foo --tag bar)")
	mkdir := fs.Bool("mkdir", false, "with --work-dir: create it if missing; alone: give the task a fresh workspace dir (ignores the project dir)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if !isValidPriority(*priority) {
		fmt.Fprintf(os.Stderr, "error: priority must be high|medium|low, got %q\n", *priority)
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

	var slug string
	if *slugFlag != "" {
		slug = *slugFlag
		if isReservedBusName(slug) {
			fmt.Fprintf(os.Stderr, "error: task slug %q is reserved — it is the human's bus address (flow message user); pick another --slug\n", slug)
			return 2
		}
	} else {
		baseSlug, err := Slugify(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		if isReservedBusName(baseSlug) {
			baseSlug += "-task"
		}
		slug, err = uniqueSlug(db, "tasks", baseSlug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	// From inside a task-bound session, the bound task is the new task's
	// parent: record it as parent_slug so spin-offs link back to it.
	var parentSlug any = nil
	parent, perr := currentSessionTask(db)
	if perr == nil {
		parentSlug = parent.Slug
	}

	// Resolve project first, since it may supply the default work_dir.
	// No --project from inside a task-bound session: inherit the bound
	// task's project, so tasks an agent spawns land next to their parent.
	var projectSlug any = nil
	var projectWorkDir string
	inheritedFrom := ""
	if *project == "" && parent != nil && parent.ProjectSlug.Valid {
		*project = parent.ProjectSlug.String
		inheritedFrom = parent.Slug
	}
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
		projectSlug = p.Slug
		projectWorkDir = p.WorkDir
	}

	// Resolve work_dir with the three-way decision from spec §5.2:
	//   - --work-dir given       → use it (must exist or --mkdir)
	//   - --project given, no wd → inherit from project
	//   - --mkdir alone, or none → auto-create ~/.flow/tasks/<slug>/workspace/
	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	taskDir := filepath.Join(root, "tasks", slug)

	var absWorkDir, workDirSource string
	newWorkspace := false
	switch {
	case *workDir != "":
		absWorkDir, err = resolveWorkDir(*workDir, *mkdir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	case projectWorkDir != "" && !*mkdir:
		// --mkdir alone opts out of the project's dir: fresh workspace.
		absWorkDir = projectWorkDir
		workDirSource = fmt.Sprintf(" (from project %q)", projectSlug)
	default:
		absWorkDir = filepath.Join(taskDir, "workspace")
		workDirSource = " (new workspace)"
		newWorkspace = true
		if err := os.MkdirAll(absWorkDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: create workspace %s: %v\n", absWorkDir, err)
			return 1
		}
	}

	// Parse optional due date.
	var dueDate any = nil
	if *dueFlag != "" {
		d, err := parseDueDate(*dueFlag, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --due: %v\n", err)
			return 2
		}
		dueDate = d.Format("2006-01-02")
	}

	var assignee any = nil
	if a := strings.TrimSpace(*assigneeFlag); a != "" {
		assignee = a
	}

	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, priority, work_dir, due_date, assignee, parent_slug, status_changed_at, created_at, updated_at)
		 VALUES (?, ?, ?, 'backlog', ?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, name, projectSlug, *priority, absWorkDir, dueDate, assignee, parentSlug, now, now, now,
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: insert task: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(filepath.Join(taskDir, "updates"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	stub := taskBriefStub
	if hereNote && !isAutoRunSession(parent) && !hasOwnerTag(tags) {
		stub = driverBriefStub // a human add: its placeholder drives INTAKE TURN
	}
	briefPath := filepath.Join(taskDir, "brief.md")
	if _, err := os.Stat(briefPath); os.IsNotExist(err) {
		if err := os.WriteFile(briefPath, []byte(fmt.Sprintf(stub, name)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	if err := flowdb.UpsertWorkdir(db, absWorkDir, "", "", ""); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	for _, tg := range tags {
		if err := flowdb.AddTaskTag(db, slug, tg); err != nil {
			fmt.Fprintf(os.Stderr, "error: add tag %q: %v\n", tg, err)
			return 1
		}
	}

	if projectSlug == nil {
		fmt.Printf("Created floating task %q at %s\n", slug, taskDir)
	} else {
		fmt.Printf("Created task %q in project %q\n", slug, *project)
		if inheritedFrom != "" {
			fmt.Printf("  (project inherited from current task %q; pass --project to override)\n", inheritedFrom)
		}
	}
	fmt.Printf("  work_dir: %s%s\n", absWorkDir, workDirSource)
	// No note for a new workspace (no session ever starts there) or under
	// a parent (this session is bound to the parent, so --here refuses
	// the child whatever its work_dir).
	if hereNote && parent == nil && !newWorkspace {
		if note := hereBindNote(slug, absWorkDir); note != "" {
			fmt.Print(note)
		}
	}
	if parent != nil {
		fmt.Printf("  (parent: %q)\n", parent.Slug)
		// The parent session watches its child so the child's broadcasts
		// land in the parent's inbox without a manual `flow watch`.
		watcher := busSender{Assignee: busUser, TaskSlug: parent.Slug}.identity()
		if err := flowdb.AddWatch(db, watcher, slug); err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-watch %s -> %s: %v\n", watcher, slug, err)
		} else {
			fmt.Printf("  (%s now watches %s)\n", watcher, slug)
		}
	}
	fmt.Printf("Next: flow do %s\n", slug)
	return 0
}

// resolveWorkDir canonicalizes path to an absolute path, verifies it
// exists (or mkdirs it if create=true). Returns the abs path.
func resolveWorkDir(path string, create bool) (string, error) {
	if path == "" {
		return "", fmt.Errorf("work-dir is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("work-dir %s is not a directory", abs)
		}
		return abs, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", abs, err)
	}
	if !create {
		return "", fmt.Errorf("work-dir %s does not exist (pass --mkdir to create)", abs)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", abs, err)
	}
	return abs, nil
}

// uniqueSlug returns base if no row with that slug exists in table;
// otherwise appends -2, -3, ... until it finds an unused one.
func uniqueSlug(db *sql.DB, table, base string) (string, error) {
	slug := base
	n := 2
	for {
		var exists int
		// nolint:gosec — table name is hardcoded ("projects" or "tasks").
		q := "SELECT 1 FROM " + table + " WHERE slug = ?"
		err := db.QueryRow(q, slug).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return slug, nil
		}
		if err != nil {
			return "", err
		}
		slug = fmt.Sprintf("%s-%d", base, n)
		n++
		if n > 1000 {
			return "", fmt.Errorf("slug %q: too many collisions", base)
		}
	}
}

func isValidPriority(p string) bool {
	return p == "high" || p == "medium" || p == "low"
}

// hereBindNote warns when the harness session running `flow add task`
// could not later bind itself to the new task with `flow do --here`,
// because it was started somewhere other than workDir. Saying so at
// creation, with the directory that would work, beats the refusal at bind
// time (GH #173). Returns "" outside a harness session, when the bind
// would succeed, or when the session's start directory is unknown.
func hereBindNote(slug, workDir string) string {
	h := ambientHarness()
	if h == nil {
		return ""
	}
	sid := os.Getenv(h.SessionIDEnvVar())
	if h.ValidateSessionID(sid) != nil || h.ValidateSession(workDir, sid) == nil {
		return ""
	}
	started := sessionStartDir(h, sid)
	if started == "" {
		return ""
	}
	return fmt.Sprintf(
		"  note: this session started in %s, so it can't bind to this task with `flow do --here %s`.\n"+
			"        to continue the task in this session, use that directory as --work-dir; `flow do %s` in a new tab is unaffected.\n",
		started, slug, slug)
}
