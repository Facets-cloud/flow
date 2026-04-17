package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
)

// cmdRegisterSession writes the newly-spawned Claude session's ID back
// into the task row. It is called BY the execution session itself, as
// its first action after a fresh-bootstrap `flow do`. The contract is:
//
//  1. `flow do` spawns `claude "<bootstrap prompt>"` in the task's
//     work_dir. The DB row's session_id is still NULL.
//  2. Claude starts the session and writes its jsonl to
//     ~/.claude/projects/<encoded-cwd>/<new-uuid>.jsonl as it runs.
//  3. The bootstrap prompt instructs the session, as its first Bash
//     action, to run `flow register-session`.
//  4. This function locates the session by scanning the encoded-cwd
//     dir for the newest *.jsonl (the one currently being written —
//     its own), extracts the UUID, and UPDATEs tasks.session_id.
//  5. Subsequent `flow do <task>` sees a populated session_id and
//     spawns `claude --resume <uuid>` instead of bootstrapping again.
//
// Safety: the UPDATE only fires if session_id is still NULL on the row.
// A second concurrent `flow do` that spawned its own session will also
// call register-session, find session_id already populated, and print
// a non-fatal warning. The loser's session becomes an orphan — the user
// can close that tab; the DB remains consistent with the winner.
//
// Usage:
//
//	flow register-session             # slug from $FLOW_TASK
//	flow register-session <slug>      # explicit slug
//	flow register-session --force     # overwrite even if already set
func cmdRegisterSession(args []string) int {
	fs := flagSet("register-session")
	force := fs.Bool("force", false, "overwrite an existing session_id")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var slug string
	if fs.NArg() == 1 {
		slug = fs.Arg(0)
	} else if fs.NArg() == 0 {
		slug = os.Getenv("FLOW_TASK")
		if slug == "" {
			fmt.Fprintln(os.Stderr, "error: no slug given and $FLOW_TASK is not set")
			return 2
		}
	} else {
		fmt.Fprintln(os.Stderr, "error: register-session takes at most one task slug")
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

	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: task %q not found\n", slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if task.SessionID.Valid && !*force {
		fmt.Fprintf(os.Stderr,
			"warning: task %q already has session_id %s; leaving it alone (use --force to overwrite)\n",
			slug, task.SessionID.String)
		// Non-fatal: return 0 so the execution session's bootstrap step
		// doesn't fail just because another session won the race.
		return 0
	}

	// Find the newest jsonl for our work_dir. That file is the session
	// currently being written — i.e., ours.
	//
	// Primary path: compute the encoded dir and look at its newest jsonl.
	// Fast and covers every version of Claude Code whose encoding rule
	// matches EncodeCwdForClaude's table.
	//
	// Fallback path: if the primary misses (either the encoded dir is
	// empty or CC has changed its encoding rule in a future version),
	// scan ~/.claude/projects/* for any jsonl whose first recorded `cwd`
	// field equals our work_dir. This uses CC's own authoritative record
	// of cwd, so it survives any future encoding drift.
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: no home dir: %v\n", err)
		return 1
	}
	projectsRoot := filepath.Join(home, ".claude", "projects")
	encoded := EncodeCwdForClaude(task.WorkDir)
	sessionDir := filepath.Join(projectsRoot, encoded)
	sid := FindNewestSessionFile(sessionDir)
	if sid == "" {
		sid = FindSessionByWorkDir(projectsRoot, task.WorkDir)
	}
	if sid == "" {
		fmt.Fprintf(os.Stderr,
			"error: no *.jsonl found for work_dir %s (checked %s and scanned %s for a matching cwd) — is claude actually running in this work_dir?\n",
			task.WorkDir, sessionDir, projectsRoot)
		return 1
	}

	now := flowdb.NowISO()
	var res sql.Result
	if *force {
		res, err = db.Exec(
			`UPDATE tasks SET session_id=?, session_started=?, updated_at=? WHERE slug=?`,
			sid, now, now, slug,
		)
	} else {
		// Optimistic: only if still NULL. Concurrent register calls
		// serialize through SQLite's row-level locking, so at most one
		// UPDATE succeeds.
		res, err = db.Exec(
			`UPDATE tasks SET session_id=?, session_started=?, updated_at=? WHERE slug=? AND session_id IS NULL`,
			sid, now, now, slug,
		)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: update session_id: %v\n", err)
		return 1
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Another process wrote first. Re-read and relay what we see.
		updated, _ := flowdb.GetTask(db, slug)
		if updated != nil && updated.SessionID.Valid {
			fmt.Printf("session already registered as %s (not overwriting our %s)\n",
				updated.SessionID.String, sid)
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: UPDATE affected 0 rows but session_id is still NULL\n")
		return 1
	}

	fmt.Printf("Registered session %s for task %s\n", sid, slug)
	return 0
}
