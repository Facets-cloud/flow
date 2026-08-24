package app

import (
	"flow/internal/flowdb"
	"flow/internal/spawner"
	"fmt"
	"os"
	"strings"
)

// cmdFocus implements `flow focus <session-id|slug>`.
//
// It is the CLI surface for spawner.FocusSession, which until now was
// reachable only from inside `flow do` (do.go's live-session guard).
// The notification hook needs to focus a session by ID from outside any
// flow session — a notification banner's click action runs
// `flow focus <session-id>` — so the capability had to become a command.
//
// The argument accepts either a session UUID (what the Notification
// hook payload carries) or a task slug (what a human at a prompt would
// naturally type). Slugs are tried only when the argument doesn't look
// like a UUID, so a task that is somehow slugged like a UUID can't
// shadow a real session.
//
// Exit codes follow the repo convention: 0 = focused, 1 = runtime error
// or no matching tab, 2 = usage error. The "no matching tab" miss is a
// 1 rather than a 0 so a caller can branch on whether the focus
// actually happened.
func cmdFocus(args []string) int {
	fs := flagSet("focus")
	quiet := fs.Bool("quiet", false, "suppress output; report result via exit code only")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: flow focus <session-id|task-slug> [--quiet]")
		return 2
	}
	ref := strings.TrimSpace(rest[0])
	if ref == "" {
		fmt.Fprintln(os.Stderr, "error: focus requires a session id or task slug")
		return 2
	}

	sessionID, task := resolveFocusTarget(ref)
	if sessionID == "" {
		fmt.Fprintf(os.Stderr, "error: no session found for %q — pass a session id, or a task slug that has been opened with `flow do`\n", ref)
		return 1
	}

	// Resolve the harness from the task when we have one: the backends
	// filter the process table by the harness binary name, and a task
	// opened under codex/gemini won't match a hardcoded "claude".
	// harnessForSpawn(nil) falls back to ambient-then-claude, which is
	// the right guess when the session id isn't tracked by any task.
	h, err := harnessForSpawn(task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	focused, err := spawner.FocusSession(sessionID, h.Binary())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: focus failed: %v\n", err)
		return 1
	}
	if !focused {
		if !*quiet {
			// Not an error the user caused — several backends (Warp,
			// Ghostty) cannot select a specific tab at all, and a
			// session may simply not be open in this terminal.
			fmt.Fprintf(os.Stderr, "no matching tab found for session %s in the active terminal (%s)\n",
				sessionID, spawner.Detect())
		}
		return 1
	}
	if !*quiet {
		if task != nil {
			fmt.Printf("focused: %s\n", task.Slug)
		} else {
			fmt.Printf("focused: %s\n", sessionID)
		}
	}
	return 0
}

// resolveFocusTarget maps the user's argument to a session UUID and,
// when known, the task carrying it. Returns ("", nil) when nothing
// resolves.
//
// A UUID-shaped argument is used directly; the task lookup is a
// best-effort enrichment so we can name the task in output and pick the
// right harness, and its failure is not fatal — focusing a session that
// flow doesn't track is still a legitimate request.
func resolveFocusTarget(ref string) (string, *flowdb.Task) {
	if looksLikeUUID(ref) {
		return ref, taskBySessionIDQuiet(ref)
	}

	dbPath, err := flowDBPath()
	if err != nil {
		return "", nil
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		return "", nil
	}
	defer db.Close()

	// includeArchived: an archived task can still have a live session
	// in a tab the user wants to reach.
	t, err := ResolveTask(db, ref, true)
	if err != nil || t == nil {
		return "", nil
	}
	if !t.SessionID.Valid || t.SessionID.String == "" {
		return "", nil
	}
	return t.SessionID.String, t
}

// taskBySessionIDQuiet reverse-looks-up a task by session id, swallowing
// every error. Callers use it for enrichment only.
func taskBySessionIDQuiet(sessionID string) *flowdb.Task {
	dbPath, err := flowDBPath()
	if err != nil {
		return nil
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()
	t, err := flowdb.TaskBySessionID(db, sessionID)
	if err != nil {
		return nil
	}
	return t
}

// looksLikeUUID reports whether s has the shape 8-4-4-4-12 hex digits.
// Deliberately laxer than the version/variant-pinned regex the focus
// backends use on ps output: this only decides whether to treat the
// argument as a session id or a slug, and a harness could legitimately
// mint a non-v4 id.
func looksLikeUUID(s string) bool {
	groups := strings.Split(s, "-")
	if len(groups) != 5 {
		return false
	}
	for i, want := range []int{8, 4, 4, 4, 12} {
		if len(groups[i]) != want {
			return false
		}
		for _, r := range groups[i] {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
