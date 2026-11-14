package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"fmt"
	"os"
)

// cmdDone marks a task done. Per spec §5.3 this is a single UPDATE that
// does NOT touch the iTerm tab, kill the Claude session, or clear
// session_id — the session can still be resumed via `flow do` after
// manually reopening the task if the user ever needs to.
//
// After the status flip, if the task has a session_id, done synchronously
// spawns a single headless `claude -p` session that loads the flow skill,
// reads the task's transcript, and runs a two-part close-out sweep:
//  1. KB scoop per §4.10 → ~/.flow/kb/*.md.
//  2. If the task is attached to a project, optionally write one
//     project-level update at
//     ~/.flow/projects/<slug>/updates/<date>-<title>.md capturing
//     decisions/learnings worth carrying forward to sibling-task
//     sessions. Substance gating is delegated to the LLM — empty or
//     purely-mechanical sessions yield no file.
//
// The CLI prints "updating kbs, project updates..." while it waits.
// A failed sweep (missing claude binary, non-zero exit) only emits a
// warning — the status flip is the contract; the sweep is best-effort.
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
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}

	// Done implies a session existed and produced learnings. A backlog
	// task that was never started has nothing to sweep. The task-table
	// CHECK constraint would reject the UPDATE with a cryptic error;
	// we surface the right verbs here instead.
	if task.Status == "backlog" && (!task.SessionID.Valid || task.SessionID.String == "") {
		fmt.Fprintf(os.Stderr,
			"error: task %q has no session_id — flow done requires at least one prior `flow do` (or `flow do --here`) to have attached a session whose transcript the sweep can read.\n"+
				"  options:\n"+
				"    - if you've been working on this task in the current Claude session, bind it now and retry close-out:\n"+
				"        flow do --here %s   (then re-run: flow done %s)\n"+
				"    - if it was never worked on and isn't relevant, archive it instead:\n"+
				"        flow archive %s\n",
			task.Slug, task.Slug, task.Slug, task.Slug)
		return 1
	}

	// Idempotent: a task that is already done keeps its status_changed_at
	// and gets no second sweep — two sweeps would rewrite kb/*.md
	// concurrently and the last writer would win. The flip is conditional
	// on status, so of two concurrent `flow done` calls exactly one wins
	// the flip (and the sweep); the other lands here.
	if task.Status == "done" {
		fmt.Printf("%s is already done; close-out sweep skipped (it ran, or is running, for the first done)\n", task.Slug)
		return 0
	}
	beforeDoneFlip()
	now := flowdb.NowISO()
	// RETURNING reads the auto-run columns in the same statement as the
	// flip, so the deferral decision below sees the row as it was at the
	// flip — not the copy loaded earlier, which a supervisor may have
	// finalized since. SQLite serializes this with the supervisor's
	// conditional finalize (finalizeAutoRunUnlessDone): if the run is
	// still 'running' here, that finalize has not happened, will now see
	// status='done', and the supervisor takes the done path (and sweeps).
	err = db.QueryRow(
		`UPDATE tasks SET status='done', status_changed_at=?, updated_at=? WHERE slug=? AND status != 'done'
		 RETURNING auto_run_status, auto_run_pid`,
		now, now, task.Slug,
	).Scan(&task.AutoRunStatus, &task.AutoRunPID)
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Printf("%s is already done; close-out sweep skipped (it ran, or is running, for the first done)\n", task.Slug)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: mark done: %v\n", err)
		return 1
	}
	if err := flowdb.CleanupTaskBus(db, task.Slug); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err) // best-effort hygiene
	}
	fmt.Printf("Marked %s as done\n", task.Slug)
	if err := completeTabAutoRunOnDone(db, task.Slug); err != nil {
		fmt.Fprintf(os.Stderr, "warning: record tab auto run completed: %v\n", err)
	}

	if task.SessionID.Valid && task.SessionID.String != "" {
		h, lookupErr := harnessForTask(task)
		if lookupErr != nil {
			// Task pinned to an unsupported harness — skip the
			// sweep but keep the status flip (the flip is the
			// contract; the sweep is best-effort).
			fmt.Fprintf(os.Stderr, "warning: close-out sweep skipped: %v\n", lookupErr)
		} else if closeoutDeferred(h, task) {
			fmt.Println("close-out sweep deferred: the auto-run supervisor runs it after the harness exits")
		} else if tabSweepDetached(h, task) && launchDetachedSweep(task.Slug) {
			fmt.Println("close-out sweep detached: it runs in its own process (this harness cuts long inline commands short)")
		} else {
			fmt.Print("updating kbs, project updates...")
			if err := runCloseoutSweep(h, task); err != nil {
				fmt.Println()
				fmt.Fprintf(os.Stderr, "warning: close-out sweep failed: %v\n", err)
			} else {
				fmt.Println(" done")
			}
		}
	}
	// Last, after all of done's own output: the one-time star note on
	// stdout for the agent to relay (nudge.go). Never fails done.
	maybeStarNudge(db)
	return 0
}

// beforeDoneFlip is a test seam: it runs between loading the task and
// flipping it to done, where a supervisor can finalize its run.
var beforeDoneFlip = func() {}

// closeoutDeferred reports whether the task's auto-run supervisor will run
// the close-out sweep, so this `flow done` must not (one sweeper). That is
// the case on a harness that defers (can't host a nested sweep) when
// either this `flow done` runs inside the task's own auto run, or the
// task has a live auto run — e.g. a human closing it mid-run. The
// supervisor sweeps after the harness exits if the task is done.
func closeoutDeferred(h harness.Harness, task *flowdb.Task) bool {
	if !harness.DefersCloseoutInAutoRun(h) {
		return false
	}
	if os.Getenv(autoRunEnvVar) == task.Slug {
		return true
	}
	return task.AutoRunStatus.Valid && task.AutoRunStatus.String == "running" &&
		task.AutoRunPID.Valid && processAlive(int(task.AutoRunPID.Int64))
}

// runCloseoutSweep runs the headless close-out sweep for task via h.
// Shared by the inline `flow done` path and the deferred supervisor path.
func runCloseoutSweep(h harness.Harness, task *flowdb.Task) error {
	projectSlug := ""
	if task.ProjectSlug.Valid {
		projectSlug = task.ProjectSlug.String
	}
	return h.SkipPermissionsRun(buildCloseoutSweepPrompt(task.Slug, projectSlug))
}

// buildCloseoutSweepPrompt composes the headless prompt that drives
// the post-done close-out sweep. The prompt is passed as a single
// positional arg to `claude -p` via exec.Command — no shell
// interpolation, so any characters are safe.
//
// Two responsibilities, executed in order by the same headless session:
//  1. KB scoop — append durable facts to ~/.flow/kb/*.md per §4.10.
//  2. (only when projectSlug != "") project update — optionally write
//     one ~/.flow/projects/<projectSlug>/updates/<date>-<title>.md
//     capturing project-level decisions/learnings worth carrying
//     forward to future sibling-task sessions. Same shape as task
//     updates per §4.5.
//
// All dedupe/append discipline and update-file shape lives in the flow
// skill, not in this prompt. The prompt's job is just: load the skill,
// read the transcript, apply the rules. Substance gating is delegated
// to the LLM — empty or purely-mechanical sessions yield no new
// entries and no project update.
func buildCloseoutSweepPrompt(slug, projectSlug string) string {
	// Substitute the real flow root so the headless sweep doesn't get
	// pointed at ~/.flow when the user has $FLOW_ROOT set elsewhere.
	root := "~/.flow"
	if r, err := flowRoot(); err == nil {
		root = r
	}

	mindset := "## How to think about this sweep\n\n" +
		"**KB entries** (in " + root + "/kb/*.md) are forever — they sit at the top of every future task brief. Bar: very strict. Default: write nothing.\n\n"
	if projectSlug != "" {
		mindset = "## How to think about this sweep\n\n" +
			"Two things happen here, with deliberately DIFFERENT bars:\n" +
			"  - **KB entries** (in " + root + "/kb/*.md) are forever — they sit at the top of every future task brief. Bar: very strict. Default: write nothing.\n" +
			"  - The **project log entry** is local to the project and a sibling task may benefit from a richer narrative. Bar: looser. A bit rich is fine. Default: write something if the session moved the project forward.\n\n"
	}

	preamble := fmt.Sprintf(
		"You are running an automated close-out sweep for completed flow task %q.\n\n"+
			"%s"+
			"## Steps\n\n"+
			"1. "+flowSkillStep()+". This loads §4.10 (KB rules) and §4.5 (update-file shape).\n\n"+
			"2. Run: flow transcript %s\n"+
			"   This prints the conversation transcript from the task's agent session. Read it carefully end to end.\n\n"+
			"3. KB sweep — strict bar, distill the essence.\n\n"+
			"   For each of these five files, ask: across the WHOLE transcript, is there a durable fact about the user, their org, products, processes, or business that belongs there per §4.10's bucket table AND meets ALL three bars below?\n"+
			"     - %s/kb/user.md\n"+
			"     - %s/kb/org.md\n"+
			"     - %s/kb/products.md\n"+
			"     - %s/kb/processes.md\n"+
			"     - %s/kb/business.md\n\n"+
			"   The three bars (ALL must be met):\n"+
			"     a. **Durable** — still true / still relevant in three months. Not 'today I felt X', not 'we tried approach Y for this one PR'.\n"+
			"     b. **Surprising or non-obvious** — not derivable from the code, the README, or what a sibling task would already know.\n"+
			"     c. **Future-relevant** — a future agent session would change a decision because of it. If you can't picture that, skip.\n\n"+
			"   Most task transcripts contribute nothing to the KB. Mechanical work, narrow bug fixes, local refactors, routine debugging — these almost never produce KB entries. The expected answer for most files on most tasks is 'no'. Don't reach.\n\n"+
			"4. Writing KB entries — INTERPRET the essence; do not transcribe.\n\n"+
			"   This is the close-out mode of §4.10 and is DIFFERENT from real-time scoop. In real-time scoop you capture what the user just said, mostly verbatim, because it's a single fresh fact. Here you've read the whole conversation — your job is to SYNTHESIZE: pull out the durable insight in compact paraphrase, in your own words, capturing the essence and (where helpful) the why. Avoid quote dumps. One concise dated bullet per insight.\n\n"+
			"   For each KB file you decide needs an entry, Read it first to check for duplicates (in any form — paraphrase, near-duplicate, superset). If something similar already exists, skip; do not append. Append using the §4.10 entry format: one dated bullet per insight, your own paraphrase capturing the essence, never invent or embellish beyond what the transcript supports.\n\n",
		slug, mindset, slug, root, root, root, root, root,
	)

	tailNum := "5"
	projectStep := ""
	if projectSlug != "" {
		projectStep = fmt.Sprintf(
			"5. Project update — looser bar, narrative OK.\n\n"+
				"   This task is attached to project %q. The project log is local and lives next to the work, so a richer entry is fine — capture what got decided, what shipped, what was tried, what's now open. Sibling-task sessions will read this to catch up.\n\n"+
				"   Write ONE file at:\n"+
				"     %s/projects/%s/updates/YYYY-MM-DD-<kebab-title>.md\n"+
				"   Shape per skill §4.5: roughly two paragraphs (can stretch a little if there's real substance). Paragraph 1: what got decided / learned / shipped at the project level. Paragraph 2: what is next or now open. Optional trailing 'Blocked on: <X>' line.\n\n"+
				"   The (still real, but looser) bar: write ONLY if the session moved the project forward — a decision was made, something shipped, a learning emerged, a blocker was added/removed, an approach was chosen, etc. Skip when the work was purely mechanical with no project-level narrative (e.g. a single typo fix). Do NOT write a template or a 'task X was marked done' summary. The goal is something a sibling-task session would actually want to read; if you can't picture that, skip.\n\n",
			projectSlug, root, projectSlug,
		)
		tailNum = "6"
	}

	tail := fmt.Sprintf(
		"%s. Do not output a chat summary. Write any files silently and exit. Empty output is a successful sweep when the transcript didn't warrant entries.\n",
		tailNum,
	)

	return preamble + projectStep + tail
}
