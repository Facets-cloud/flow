package main

import (
	"fmt"
	"os"
)

// cmdArchive sets archived_at = now() on the matching row.
func cmdArchive(args []string) int {
	return setArchivedAt(args, true)
}

// cmdUnarchive clears archived_at.
func cmdUnarchive(args []string) int {
	return setArchivedAt(args, false)
}

// setArchivedAt is the shared mutation: archive=true sets archived_at to
// now, archive=false clears it.
func setArchivedAt(args []string, archive bool) int {
	verb := "archive"
	pastVerb := "Archived"
	if !archive {
		verb = "unarchive"
		pastVerb = "Unarchived"
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
	db, err := OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	// For unarchive we must include archived rows; for archive we exclude them.
	includeArchived := !archive
	kind, slug, err := ResolveTaskOrProject(db, ref, includeArchived)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := nowISO()
	table := "tasks"
	if kind == "project" {
		table = "projects"
	}
	var q string
	var qargs []any
	if archive {
		q = fmt.Sprintf("UPDATE %s SET archived_at = ?, updated_at = ? WHERE slug = ?", table)
		qargs = []any{now, now, slug}
	} else {
		q = fmt.Sprintf("UPDATE %s SET archived_at = NULL, updated_at = ? WHERE slug = ?", table)
		qargs = []any{now, slug}
	}
	if _, err := db.Exec(q, qargs...); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s %s %s\n", pastVerb, kind, slug)
	return 0
}
