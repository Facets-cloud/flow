package app

import (
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// cmdCheckpoint drafts a mid-session progress note for an in-flight task
// WITHOUT closing it. It is the non-terminal sibling of `flow done`:
// where done flips status and runs a strict KB + project-update sweep at
// the END of a task's life, checkpoint runs a lighter, task-local sweep
// at any breakpoint DURING the work.
//
// Why this exists: flow injects context at SessionStart but has no
// write-back. The dated notes under tasks/<slug>/updates/ are the durable
// per-session record that a future `flow do` reads back on resume — yet
// nothing prompts for them, so on heavy days they don't get written and
// the next resume reads a stale brief. checkpoint closes that gap by
// reusing the headless `claude -p` sweep engine to read the live
// transcript and draft ONE updates/ note for the user to review. No
// status change, no KB writes — deliberately local and repeatable.
//
// Resolution mirrors `flow show task`: an explicit ref resolves by slug;
// no ref reverse-looks-up the task bound to the current Claude session.
// Either way the task must carry a session_id — that's the transcript the
// sweep reads.
//
// Exit codes: 0 on a successful sweep (whether or not a note was
// warranted — an empty checkpoint is still a success, mirroring done's
// "empty output is a successful sweep"); 1 on resolution/IO failure or a
// non-zero sweep exit. Unlike `flow done`, checkpoint has no status flip
// to protect, so a failed sweep is the whole operation failing and is
// surfaced as rc=1 rather than swallowed.
func cmdCheckpoint(args []string) int {
	fs := flagSet("checkpoint")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ref := ""
	if fs.NArg() > 0 {
		ref = fs.Arg(0)
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	var task *flowdb.Task
	if ref == "" {
		// No explicit ref: reverse-lookup via the current Claude session,
		// exactly like `flow show task`. This is the common path —
		// checkpoint is most useful fired from inside the live session
		// whose work it's recording.
		bound, lookupErr := currentSessionTask(db)
		if lookupErr != nil {
			if isNoBindingErr(lookupErr) {
				if currentSessionID() == "" {
					fmt.Fprintln(os.Stderr, "error: no task ref given and not running inside a Claude session ($CLAUDE_CODE_SESSION_ID unset)")
				} else {
					fmt.Fprintln(os.Stderr, "error: no task ref given and this Claude session is not bound to a task — pass a slug or run `flow do --here <slug>` first")
				}
				return 1
			}
			fmt.Fprintf(os.Stderr, "error: lookup task by session: %v\n", lookupErr)
			return 1
		}
		task = bound
	} else {
		t, rc := findTask(db, ref)
		if rc != 0 {
			return rc
		}
		task = t
	}

	// The transcript is the whole point of a checkpoint. A task that was
	// never started via `flow do` has no session to read.
	if !task.SessionID.Valid || task.SessionID.String == "" {
		fmt.Fprintf(os.Stderr,
			"error: task %q has no session_id — checkpoint reads the task's Claude transcript, which requires at least one prior `flow do` (or `flow do --here`).\n",
			task.Slug)
		return 1
	}

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	updatesDir := filepath.Join(root, "tasks", task.Slug, "updates")

	h, lookupErr := harnessForTask(task)
	if lookupErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", lookupErr)
		return 1
	}

	// Snapshot the updates dir so we can report which file (if any) the
	// sweep produced — SkipPermissionsRun discards the headless session's
	// stdout, so a before/after diff is how we learn what it wrote.
	before := updatesSnapshot(updatesDir)

	fmt.Print("drafting checkpoint note...")
	if err := h.SkipPermissionsRun(buildCheckpointSweepPrompt(task.Slug)); err != nil {
		fmt.Println()
		fmt.Fprintf(os.Stderr, "warning: checkpoint sweep failed: %v\n", err)
		return 1
	}
	fmt.Println(" done")

	created := updatesCreatedSince(updatesDir, before)
	if len(created) == 0 {
		fmt.Println("no checkpoint note written — the transcript didn't warrant one yet.")
	} else {
		for _, f := range created {
			fmt.Printf("wrote: %s\n", filepath.Join(updatesDir, f))
		}
	}
	return 0
}

// updatesSnapshot returns the set of regular-file names currently in dir.
// A missing dir yields an empty set (not an error) — checkpoint may be
// the very first note a task ever gets.
func updatesSnapshot(dir string) map[string]bool {
	set := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return set
	}
	for _, e := range entries {
		if !e.IsDir() {
			set[e.Name()] = true
		}
	}
	return set
}

// updatesCreatedSince returns file names present in dir now but absent
// from the pre-sweep snapshot, sorted for stable output.
func updatesCreatedSince(dir string, before map[string]bool) []string {
	var created []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return created
	}
	for _, e := range entries {
		if e.IsDir() || before[e.Name()] {
			continue
		}
		created = append(created, e.Name())
	}
	sort.Strings(created)
	return created
}

// buildCheckpointSweepPrompt composes the headless prompt that drives a
// mid-session checkpoint. Unlike buildCloseoutSweepPrompt (KB + project
// update at the strict/forever bar), this writes exactly one TASK-level
// progress note at the §4.5 shape — the running log, not the durable KB.
// It deliberately does NOT touch the KB or change status: a checkpoint
// is local, cheap, and repeatable.
//
// The prompt is passed as a single positional arg to `claude -p` via
// exec.Command — no shell interpolation, so any characters are safe.
func buildCheckpointSweepPrompt(slug string) string {
	// Substitute the real flow root so the headless sweep isn't pointed
	// at ~/.flow when the user has $FLOW_ROOT set elsewhere.
	root := "~/.flow"
	if r, err := flowRoot(); err == nil {
		root = r
	}

	return fmt.Sprintf(
		"You are running an automated mid-session checkpoint for in-flight flow task %q.\n\n"+
			"The goal: capture this session's progress as ONE durable note so a future `flow do` resume isn't reading a stale brief. This is NOT a close-out — do NOT change task status, do NOT write to the knowledge base (%s/kb/).\n\n"+
			"## Steps\n\n"+
			"1. Invoke the flow skill via the Skill tool. This loads §4.5 (the progress-note shape).\n\n"+
			"2. Run: flow transcript %s\n"+
			"   Read the conversation transcript end to end — this is the session's work.\n\n"+
			"3. Read any notes already under %s/tasks/%s/updates/ first. Your job is to capture what THIS session did that ISN'T already recorded there — do not restate prior notes.\n\n"+
			"4. Write ONE new progress note at:\n"+
			"     %s/tasks/%s/updates/YYYY-MM-DD-<kebab-title>.md\n"+
			"   Shape per §4.5: under ~10 lines. Paragraph 1 — what got done this session, specific, no hedging. Paragraph 2 — what's next or now open. Optional trailing 'Blocked on: <X>' line. Prioritise decisions made, risks surfaced, and the next concrete step — those are what a resume most needs.\n\n"+
			"5. Bar: write the note only if the session did substantive work not already captured. If the work was trivial or everything is already logged, write nothing — that is a successful checkpoint too. Never write a 'no progress' placeholder.\n\n"+
			"6. Do not output a chat summary. Write the file silently and exit.\n",
		slug, root, slug, root, slug, root, slug,
	)
}
