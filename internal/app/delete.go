package app

import (
	"flow/internal/flowdb"
	"fmt"
	"os"
)

// cmdDelete soft-deletes a task, project, or playbook by setting deleted_at.
func cmdDelete(args []string) int {
	return setDeletedAt(args, true)
}

// cmdRestore clears deleted_at on a soft-deleted task, project, or playbook.
func cmdRestore(args []string) int {
	return setDeletedAt(args, false)
}

func setDeletedAt(args []string, deleted bool) int {
	verb := "delete"
	pastVerb := "Deleted"
	if !deleted {
		verb = "restore"
		pastVerb = "Restored"
	}
	fs := flagSet(verb)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "error: %s requires exactly one ref\n", verb)
		return 2
	}
	ref := fs.Arg(0)

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

	opts := resolveOptions{IncludeArchived: true}
	if !deleted {
		opts.IncludeDeleted = true
	}
	kind, slug, err := ResolveTaskProjectOrPlaybookWithOptions(db, ref, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	var table string
	switch kind {
	case "task":
		table = "tasks"
	case "project":
		table = "projects"
	case "playbook":
		table = "playbooks"
	default:
		fmt.Fprintf(os.Stderr, "error: unsupported ref kind %q\n", kind)
		return 1
	}

	now := flowdb.NowISO()
	var q string
	var qargs []any
	if deleted {
		q = fmt.Sprintf("UPDATE %s SET deleted_at = ?, updated_at = ? WHERE slug = ?", table)
		qargs = []any{now, now, slug}
	} else {
		q = fmt.Sprintf("UPDATE %s SET deleted_at = NULL, updated_at = ? WHERE slug = ?", table)
		qargs = []any{now, slug}
	}
	if _, err := db.Exec(q, qargs...); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s %s %s\n", pastVerb, kind, slug)
	return 0
}
