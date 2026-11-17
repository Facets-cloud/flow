package app

import (
	"bytes"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"strings"
)

// cmdTranscript implements `flow transcript <task-slug>`. It delegates
// to the task's harness for both path resolution and on-disk format
// decoding — each harness has its own transcript layout AND its own
// schema (claude jsonl, codex event log, gemini single-object json).
// The harness writes a normalized human-readable form to stdout.
func cmdTranscript(args []string) int {
	// Management subcommands come before the read path.
	if len(args) > 0 {
		switch args[0] {
		case "backfill":
			return cmdTranscriptBackfill(args[1:])
		case "purge":
			return cmdTranscriptPurge(args[1:])
		}
	}

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
					fmt.Fprintln(os.Stderr, "error: no task ref given and not running inside a harness session ($CLAUDE_CODE_SESSION_ID, $CODEX_THREAD_ID and $PRAXIS_SESSION_ID unset)")
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

	// Spawn cwd == work_dir is an invariant for any task with a
	// session_id (enforced at bind time in cmdDo + cmdDoHere). So
	// the transcript file lives under work_dir's encoded path.
	// Rows from earlier flow versions that violated the invariant
	// will simply not find their transcript here; the user fixes
	// them by hand (cd to the real cwd and re-bind, or release the
	// session via `flow done` / `--fresh`).
	h, err := harnessForTask(task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// Prefer the harness's own transcript when it is still on disk — it is
	// authoritative and may hold the live turn the flow copy's Stop-hook
	// capture hasn't appended yet. Fall back to flow's harness-agnostic copy
	// when the harness has deleted/rotated its transcript (the gap this
	// store exists to close).
	var buf bytes.Buffer
	if err := h.RenderTranscript(task.WorkDir, task.SessionID.String, *compact, &buf); err == nil {
		os.Stdout.Write(buf.Bytes())
		return 0
	}
	if copyPath, ok := flowCopyPathForTask(task); ok {
		var fbuf bytes.Buffer
		if rerr := renderFlowCopy(copyPath, *compact, &fbuf); rerr == nil {
			os.Stdout.Write(fbuf.Bytes())
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "error: no transcript found for %q (harness transcript gone and no flow copy)\n", task.Slug)
	fmt.Fprintf(os.Stderr,
		"hint: flow assumed the harness was started at work_dir=%q. "+
			"if the session was actually started elsewhere (e.g. an older `flow do --here` bind), "+
			"the transcript lives under that other directory. a `flow transcript backfill` while the "+
			"harness transcript still exists would have seeded a flow copy.\n",
		task.WorkDir)
	return 1
}
