package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"regexp"
)

// cmdUpdate dispatches `flow update <kind>`. v1 supports `task` only;
// the command exists as a manual escape hatch when the session_id or
// work_dir drifts from what the DB has (e.g. the user spawned claude
// outside `flow do`, or moved a repo on disk).
func cmdUpdate(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: update requires 'task' and a ref")
		return 2
	}
	switch args[0] {
	case "task":
		return cmdUpdateTask(args[1:])
	}
	fmt.Fprintf(os.Stderr, "error: unknown update target %q (expected 'task')\n", args[0])
	return 2
}

// sessionUUIDRe mirrors claude's --session-id contract: standard v4
// UUID, lowercase hex, with the version/variant bits enforced.
var sessionUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// cmdUpdateTask implements `flow update task <ref> [--session-id <uuid>]
// [--work-dir <path>] [--mkdir]`. At least one field-changing flag must
// be given.
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
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *sessionID == "" && *workDir == "" {
		fmt.Fprintln(os.Stderr, "error: give --session-id or --work-dir (or both)")
		return 2
	}

	if *sessionID != "" && !sessionUUIDRe.MatchString(*sessionID) {
		fmt.Fprintf(os.Stderr,
			"error: --session-id must be a lowercase v4 UUID (got %q)\n", *sessionID)
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
	return 0
}
