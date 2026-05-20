package app

import (
	"flow/internal/flowdb"
	"fmt"
	"os"
	"strings"
	"time"
)

// cmdTranscript implements `flow transcript <task-slug>`. It delegates
// to the task's harness for both path resolution and on-disk format
// decoding — each harness has its own transcript layout AND its own
// schema (claude jsonl, codex event log, gemini single-object json).
// The harness writes a normalized human-readable form to stdout.
func cmdTranscript(args []string) int {
	// Positional arg first, then flags (same pattern as cmdDo).
	ref := ""
	flagArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref = args[0]
		flagArgs = args[1:]
	}

	fs := flagSet("transcript")
	compact := fs.Bool("compact", false, "omit tool results and thinking blocks")
	if err := fs.Parse(flagArgs); err != nil {
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

	var task *flowdb.Task
	if ref == "" {
		bound, lookupErr := currentSessionTask(db)
		if lookupErr != nil {
			if isNoBindingErr(lookupErr) {
				if currentSessionID() == "" {
					fmt.Fprintln(os.Stderr, "error: no task ref given and not running inside a Claude session ($CLAUDE_CODE_SESSION_ID unset)")
				} else {
					fmt.Fprintln(os.Stderr, "error: no task ref given and this Claude session is not bound to a task")
				}
				return 2
			}
			fmt.Fprintf(os.Stderr, "error: lookup task by session: %v\n", lookupErr)
			return 1
		}
		task = bound
	} else {
		task, err = resolveTaskRef(db, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	if !task.SessionID.Valid || task.SessionID.String == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no session — run `flow do %s` first\n", task.Slug, task.Slug)
		return 1
	}

	// Compute the cutoff from session_started so the transcript output
	// is scoped to the task's own conversation, not pre-bind dispatch
	// chatter that --here-bound tasks accumulate. NULL/unparseable
	// session_started → zero cutoff → filter is a no-op.
	var cutoff time.Time
	if task.SessionStarted.Valid && task.SessionStarted.String != "" {
		if ts, perr := time.Parse(time.RFC3339Nano, task.SessionStarted.String); perr == nil {
			cutoff = ts
		}
	}

	// Use the recorded session_cwd if set — that's the cwd the
	// harness was started in, which determines where the transcript
	// file lives. Falls back to work_dir for legacy rows that
	// predate the session_cwd column (NULL means "we didn't record
	// it; the safe bet is task.work_dir, which equals session_cwd
	// for fresh `flow do` spawns by construction").
	cwd := task.WorkDir
	if task.SessionCwd.Valid && task.SessionCwd.String != "" {
		cwd = task.SessionCwd.String
	}
	h, err := harnessForTask(task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := h.RenderTranscript(cwd, task.SessionID.String, *compact, cutoff, os.Stdout); err != nil {
		// Better error: tell the user what we looked for and why
		// it might be missing, with hints for the most common
		// recovery paths.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if !task.SessionCwd.Valid || task.SessionCwd.String == "" {
			fmt.Fprintf(os.Stderr,
				"hint: this task predates the session_cwd column; flow assumed claude was started in %q. "+
					"if claude was actually started elsewhere (e.g. via `flow do --here` from a different directory), "+
					"the transcript lives under that other directory. re-bind via `flow do --here %s` from inside the claude session to record the correct cwd.\n",
				task.WorkDir, task.Slug)
		} else if task.SessionCwd.String != task.WorkDir {
			fmt.Fprintf(os.Stderr,
				"hint: session_cwd=%q differs from work_dir=%q (likely a --here-bound session). "+
					"verify the harness session is actually running and its transcript is on disk.\n",
				task.SessionCwd.String, task.WorkDir)
		}
		return 1
	}
	return 0
}
