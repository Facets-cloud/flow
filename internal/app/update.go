package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// cmdUpdate dispatches `flow update task|project`. The command is the
// canonical lane for in-place field edits — including legacy field
// setters (priority, due-date, waiting) that used to have their own
// top-level mini-commands.
func cmdUpdate(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: update requires 'task' or 'project' and a ref")
		return 2
	}
	switch args[0] {
	case "task":
		return cmdUpdateTask(args[1:])
	case "project":
		return cmdUpdateProject(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown update target %q (expected 'task' or 'project')\n", args[0])
	return 2
}

// sessionUUIDRe mirrors claude's --session-id contract: standard v4
// UUID, lowercase hex, with the version/variant bits enforced.
var sessionUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// cmdUpdateTask implements `flow update task <ref> [--session-id <uuid>]
// [--work-dir <path>] [--mkdir] [--status <s>] [--assignee <name>]
// [--due-date <date>]`. At least one field-changing flag must be given.
//
// The command is the manual escape hatch for fields that don't have a
// dedicated friendly setter. Status accepts any of backlog|in-progress|
// done — including reverse transitions like done → in-progress, which
// is the documented way to reopen a closed task.
func cmdUpdateTask(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "error: update task requires a task ref")
		return 2
	}
	ref := args[0]
	fs := flagSet("update task")
	sessionID := fs.String("session-id", "", "new Claude session UUID (v4 format)")
	workDir := fs.String("work-dir", "", "new absolute work directory")
	mkdir := fs.Bool("mkdir", false, "create --work-dir if it does not exist")
	status := fs.String("status", "", "new status: backlog|in-progress|done")
	priority := fs.String("priority", "", "new priority: high|medium|low")
	assignee := fs.String("assignee", "", "new assignee (use \"\" to keep, --clear-assignee to clear)")
	clearAssignee := fs.Bool("clear-assignee", false, "clear the assignee (back to default = self)")
	dueDate := fs.String("due-date", "", "new due date (YYYY-MM-DD, today, tomorrow, monday, 3d)")
	clearDue := fs.Bool("clear-due", false, "clear the due date")
	waiting := fs.String("waiting", "", "set waiting_on freeform note (\"<who or what>\")")
	clearWaiting := fs.Bool("clear-waiting", false, "clear waiting_on")
	var addTags stringSliceFlag
	fs.Var(&addTags, "tag", "add a tag (repeatable: --tag foo --tag bar)")
	var removeTags stringSliceFlag
	fs.Var(&removeTags, "remove-tag", "remove a tag (repeatable)")
	clearTags := fs.Bool("clear-tags", false, "remove all tags from the task")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	anyField := *sessionID != "" || *workDir != "" || *status != "" || *priority != "" ||
		*assignee != "" || *clearAssignee || *dueDate != "" || *clearDue ||
		*waiting != "" || *clearWaiting ||
		len(addTags) > 0 || len(removeTags) > 0 || *clearTags
	if !anyField {
		fmt.Fprintln(os.Stderr, "error: give at least one of --session-id, --work-dir, --status, --priority, --assignee, --clear-assignee, --due-date, --clear-due, --waiting, --clear-waiting, --tag, --remove-tag, --clear-tags")
		return 2
	}

	if *sessionID != "" && !sessionUUIDRe.MatchString(*sessionID) {
		fmt.Fprintf(os.Stderr,
			"error: --session-id must be a lowercase v4 UUID (got %q)\n", *sessionID)
		return 2
	}

	if *status != "" && !isValidStatus(*status) {
		fmt.Fprintf(os.Stderr,
			"error: --status must be backlog|in-progress|done (got %q)\n", *status)
		return 2
	}

	if *priority != "" && !isValidPriority(*priority) {
		fmt.Fprintf(os.Stderr,
			"error: --priority must be high|medium|low (got %q)\n", *priority)
		return 2
	}

	if *clearAssignee && *assignee != "" {
		fmt.Fprintln(os.Stderr, "error: --assignee and --clear-assignee are mutually exclusive")
		return 2
	}
	if *clearDue && *dueDate != "" {
		fmt.Fprintln(os.Stderr, "error: --due-date and --clear-due are mutually exclusive")
		return 2
	}
	if *clearWaiting && *waiting != "" {
		fmt.Fprintln(os.Stderr, "error: --waiting and --clear-waiting are mutually exclusive")
		return 2
	}
	if *clearTags && len(removeTags) > 0 {
		fmt.Fprintln(os.Stderr, "error: --clear-tags and --remove-tag are mutually exclusive (clear-tags removes everything anyway)")
		return 2
	}

	var absWorkDir string
	if *workDir != "" {
		abs, err := resolveWorkDir(*workDir, *mkdir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		absWorkDir = abs
	}

	var dueParsed string
	if *dueDate != "" {
		d, err := parseDueDate(*dueDate, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: --due-date: %v\n", err)
			return 2
		}
		dueParsed = d.Format("2006-01-02")
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	task, err := ResolveTask(db, ref, true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: task %q not found\n", ref)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := flowdb.NowISO()
	if *sessionID != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET session_id=?, session_started=?, updated_at=? WHERE slug=?`,
			*sessionID, now, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: update session_id: %v\n", err)
			return 1
		}
		fmt.Printf("session_id → %s\n", *sessionID)
	}
	if absWorkDir != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET work_dir=?, updated_at=? WHERE slug=?`,
			absWorkDir, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: update work_dir: %v\n", err)
			return 1
		}
		fmt.Printf("work_dir → %s\n", absWorkDir)
	}
	if *status != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET status=?,
			 status_changed_at = CASE WHEN status != ? THEN ? ELSE status_changed_at END,
			 updated_at=? WHERE slug=?`,
			*status, *status, now, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: update status: %v\n", err)
			return 1
		}
		fmt.Printf("status → %s\n", *status)
	}
	if *priority != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET priority=?, updated_at=? WHERE slug=?`,
			*priority, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: update priority: %v\n", err)
			return 1
		}
		fmt.Printf("priority → %s\n", *priority)
	}
	if *waiting != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET waiting_on=?, updated_at=? WHERE slug=?`,
			*waiting, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: update waiting: %v\n", err)
			return 1
		}
		fmt.Printf("waiting_on → %s\n", *waiting)
	}
	if *clearWaiting {
		if _, err := db.Exec(
			`UPDATE tasks SET waiting_on=NULL, updated_at=? WHERE slug=?`,
			now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: clear waiting: %v\n", err)
			return 1
		}
		fmt.Println("waiting_on cleared")
	}
	if *assignee != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET assignee=?, updated_at=? WHERE slug=?`,
			*assignee, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: update assignee: %v\n", err)
			return 1
		}
		fmt.Printf("assignee → %s\n", *assignee)
	}
	if *clearAssignee {
		if _, err := db.Exec(
			`UPDATE tasks SET assignee=NULL, updated_at=? WHERE slug=?`,
			now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: clear assignee: %v\n", err)
			return 1
		}
		fmt.Println("assignee cleared")
	}
	if dueParsed != "" {
		if _, err := db.Exec(
			`UPDATE tasks SET due_date=?, updated_at=? WHERE slug=?`,
			dueParsed, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: update due_date: %v\n", err)
			return 1
		}
		fmt.Printf("due_date → %s\n", dueParsed)
	}
	if *clearDue {
		if _, err := db.Exec(
			`UPDATE tasks SET due_date=NULL, updated_at=? WHERE slug=?`,
			now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: clear due_date: %v\n", err)
			return 1
		}
		fmt.Println("due_date cleared")
	}
	if *clearTags {
		if err := flowdb.ClearTaskTags(db, task.Slug); err != nil {
			fmt.Fprintf(os.Stderr, "error: clear tags: %v\n", err)
			return 1
		}
		fmt.Println("tags cleared")
	}
	for _, raw := range removeTags {
		t := flowdb.NormalizeTag(raw)
		if t == "" {
			fmt.Fprintf(os.Stderr, "error: tag %q is empty after normalization\n", raw)
			return 2
		}
		if err := flowdb.RemoveTaskTag(db, task.Slug, t); err != nil {
			fmt.Fprintf(os.Stderr, "error: remove tag: %v\n", err)
			return 1
		}
		fmt.Printf("tag - %s\n", t)
	}
	for _, raw := range addTags {
		t := flowdb.NormalizeTag(raw)
		if t == "" {
			fmt.Fprintf(os.Stderr, "error: tag %q is empty after normalization\n", raw)
			return 2
		}
		if err := flowdb.AddTaskTag(db, task.Slug, t); err != nil {
			fmt.Fprintf(os.Stderr, "error: add tag: %v\n", err)
			return 1
		}
		fmt.Printf("tag + %s\n", t)
	}
	return 0
}

// stringSliceFlag adapts a *[]string into a flag.Value so the same
// flag name can be passed multiple times and accumulate. Used for
// repeatable `--tag` / `--remove-tag` on `flow update task`.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// isValidStatus checks the task status enum.
func isValidStatus(s string) bool {
	return s == "backlog" || s == "in-progress" || s == "done"
}

// cmdUpdateProject implements `flow update project <ref> [--priority <p>]`.
// Project field edits are minimal — priority is the only post-creation
// field with a real reason to change. Status and other shape edits go
// through `flow archive` / `flow edit`.
func cmdUpdateProject(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "error: update project requires a project ref")
		return 2
	}
	ref := args[0]
	fs := flagSet("update project")
	priority := fs.String("priority", "", "new priority: high|medium|low")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *priority == "" {
		fmt.Fprintln(os.Stderr, "error: give at least one of --priority")
		return 2
	}
	if !isValidPriority(*priority) {
		fmt.Fprintf(os.Stderr,
			"error: --priority must be high|medium|low (got %q)\n", *priority)
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	p, err := ResolveProject(db, ref, true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: project %q not found\n", ref)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := flowdb.NowISO()
	if _, err := db.Exec(
		`UPDATE projects SET priority=?, updated_at=? WHERE slug=?`,
		*priority, now, p.Slug,
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: update priority: %v\n", err)
		return 1
	}
	fmt.Printf("priority → %s\n", *priority)
	return 0
}

// parseDueDate converts a human-friendly date expression to a time.Time.
// Accepts: YYYY-MM-DD, today, tomorrow, weekday names (next occurrence),
// Nd (N days from now). `now` is passed in for testability.
//
// Lives here (rather than in a setter command) because the legacy
// `flow due` / `flow waiting` / `flow priority` mini-commands have
// been folded into `flow update task`. parseDueDate is shared with
// `flow add task --due`.
func parseDueDate(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	switch s {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	case "tomorrow":
		y, m, d := now.AddDate(0, 0, 1).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	}

	weekdays := map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday,
		"tuesday": time.Tuesday, "wednesday": time.Wednesday,
		"thursday": time.Thursday, "friday": time.Friday,
		"saturday": time.Saturday,
	}
	if target, ok := weekdays[s]; ok {
		current := now.Weekday()
		delta := int(target) - int(current)
		if delta <= 0 {
			delta += 7
		}
		d := now.AddDate(0, 0, delta)
		y, m, dd := d.Date()
		return time.Date(y, m, dd, 0, 0, 0, 0, now.Location()), nil
	}

	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		var n int
		if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil && n >= 0 {
			d := now.AddDate(0, 0, n)
			y, m, dd := d.Date()
			return time.Date(y, m, dd, 0, 0, 0, 0, now.Location()), nil
		}
	}

	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unrecognized date %q (want YYYY-MM-DD, today, tomorrow, monday..sunday, Nd)", s)
}
