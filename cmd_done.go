package main

import (
	"fmt"
	"os"
)

// cmdDone marks a task done. Per spec §5.3 this is a single UPDATE that
// does NOT touch the iTerm tab, kill the Claude session, or clear
// session_id — the session can still be resumed via `flow do` after
// manually reopening the task if the user ever needs to.
func cmdDone(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: done requires a task ref")
		return 2
	}
	query := args[0]
	fs := flagSet("done")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}

	now := nowISO()
	res, err := db.Exec(
		`UPDATE tasks SET status='done', status_changed_at=?, updated_at=? WHERE slug=?`,
		now, now, task.Slug,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: mark done: %v\n", err)
		return 1
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		fmt.Fprintf(os.Stderr, "error: task %q not updated\n", task.Slug)
		return 1
	}
	fmt.Printf("Marked %s as done\n", task.Slug)
	return 0
}
