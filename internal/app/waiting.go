package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"strings"
)

// cmdWaiting sets or clears waiting_on on a task.
//
// Usage:
//
//	flow waiting <ref> "<who or what>"
//	flow waiting <ref> --clear
//
// Tasks only — applying to a project errors. Does not change status.
func cmdWaiting(args []string) int {
	// Pre-separate --clear and any text arg from the ref so the user can
	// write `flow waiting <ref> --clear` as well as `flow waiting --clear <ref>`.
	// Go's flag package stops at the first positional, so we split manually.
	clearFlag := false
	var positional []string
	for _, a := range args {
		if a == "--clear" || a == "-clear" {
			clearFlag = true
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, "error: waiting requires a task ref")
		return 2
	}
	ref := positional[0]
	rest := positional[1:]

	var text string
	switch {
	case clearFlag && len(rest) > 0:
		fmt.Fprintln(os.Stderr, "error: --clear cannot be combined with a text argument")
		return 2
	case clearFlag:
		// leave text empty
	case len(rest) == 0:
		fmt.Fprintln(os.Stderr, `error: waiting requires a text argument or --clear`)
		return 2
	case len(rest) > 1:
		fmt.Fprintln(os.Stderr, "error: waiting takes at most one text argument; quote it if it has spaces")
		return 2
	default:
		text = rest[0]
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

	slug, err := resolveTaskForWaiting(db, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := flowdb.NowISO()
	if clearFlag {
		if _, err := db.Exec("UPDATE tasks SET waiting_on = NULL, updated_at = ? WHERE slug = ?", now, slug); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("%s no longer waiting\n", slug)
		return 0
	}
	if _, err := db.Exec("UPDATE tasks SET waiting_on = ?, updated_at = ? WHERE slug = ?", text, now, slug); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s waiting on: %s\n", slug, text)
	return 0
}

// resolveTaskForWaiting resolves ref to a task. Errors if ref matches a
// project (waiting is tasks-only). Includes archived tasks.
func resolveTaskForWaiting(db *sql.DB, ref string) (string, error) {
	if strings.HasPrefix(ref, "project/") {
		return "", fmt.Errorf("waiting is valid on tasks only, got project ref %q", ref)
	}
	ref = strings.TrimPrefix(ref, "task/")

	t, err := ResolveTask(db, ref, true)
	if err != nil {
		// Check if it's a project to give a better error.
		if p, pErr := ResolveProject(db, ref, true); pErr == nil {
			return "", fmt.Errorf("waiting is valid on tasks only, %q is a project", p.Slug)
		}
		return "", err
	}
	return t.Slug, nil
}
