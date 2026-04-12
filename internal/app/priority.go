package app

import (
	"flow/internal/flowdb"
	"fmt"
	"os"
)

// cmdPriority mutates a task or project's priority.
//
// Usage: flow priority <ref> high|medium|low
func cmdPriority(args []string) int {
	fs := flagSet("priority")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "error: priority requires <ref> and one of high|medium|low")
		return 2
	}
	ref := fs.Arg(0)
	prio := fs.Arg(1)
	switch prio {
	case "high", "medium", "low":
	default:
		fmt.Fprintf(os.Stderr, "error: priority must be high, medium, or low (got %q)\n", prio)
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

	kind, slug, err := ResolveTaskOrProject(db, ref, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := flowdb.NowISO()
	table := "tasks"
	if kind == "project" {
		table = "projects"
	}
	q := fmt.Sprintf("UPDATE %s SET priority = ?, updated_at = ? WHERE slug = ?", table)
	if _, err := db.Exec(q, prio, now, slug); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Priority of %s %s is now %s\n", kind, slug, prio)
	return 0
}
