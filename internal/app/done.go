package app

import (
	"flow/internal/flowdb"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// claudeRunner invokes the headless `claude -p` CLI for the post-done
// close-out sweep. Tests override this var to capture invocations
// without spawning claude. Stdout/stderr are discarded — the sweep
// prompt instructs claude to write KB entries and (when applicable) a
// project update silently and produce no chat output.
//
// The sweep prompt names the task slug explicitly (it doesn't rely on
// any inherited env var); the headless session has its own brand-new
// $CLAUDE_CODE_SESSION_ID that doesn't match any flow task.
var claudeRunner = func(slug, prompt string) error {
	cmd := exec.Command("claude", "-p", prompt, "--dangerously-skip-permissions")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

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

	now := flowdb.NowISO()
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

	if task.SessionID.Valid && task.SessionID.String != "" {
		fmt.Print("updating kbs, project updates...")
		projectSlug := ""
		if task.ProjectSlug.Valid {
			projectSlug = task.ProjectSlug.String
		}
		if err := claudeRunner(task.Slug, buildCloseoutSweepPrompt(task.Slug, projectSlug)); err != nil {
			fmt.Println()
			fmt.Fprintf(os.Stderr, "warning: close-out sweep failed: %v\n", err)
		} else {
			fmt.Println(" done")
		}
	}
	return 0
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
			"1. Invoke the flow skill via the Skill tool. This loads §4.10 (KB rules) and §4.5 (update-file shape).\n\n"+
			"2. Run: flow transcript %s\n"+
			"   This prints the conversation transcript from the task's Claude session. Read it carefully end to end.\n\n"+
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
			"     c. **Future-relevant** — a future Claude session would change a decision because of it. If you can't picture that, skip.\n\n"+
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
