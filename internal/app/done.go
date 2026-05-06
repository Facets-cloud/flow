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
var claudeRunner = func(slug, prompt string) error {
	cmd := exec.Command("claude", "-p", prompt, "--dangerously-skip-permissions")
	cmd.Env = append(os.Environ(), "FLOW_TASK="+slug)
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
	header := fmt.Sprintf(
		"You are running an automated close-out sweep for completed flow task %q. Do this:\n\n"+
			"1. Invoke the flow skill via the Skill tool. This loads §4.10 (the scoop-mode KB rules) and §4.5 (update-file shape) which you must follow exactly.\n\n"+
			"2. Run: flow transcript %s\n"+
			"   This prints the conversation transcript from the task's Claude session. Read it carefully.\n\n"+
			"3. KB sweep. For each of these five files, decide whether the transcript revealed any durable facts that belong there per §4.10's bucket table:\n"+
			"   - ~/.flow/kb/user.md\n"+
			"   - ~/.flow/kb/org.md\n"+
			"   - ~/.flow/kb/products.md\n"+
			"   - ~/.flow/kb/processes.md\n"+
			"   - ~/.flow/kb/business.md\n\n"+
			"4. For each KB file you decide needs new entries, Read it first to check for duplicates, then append entries using the §4.10 entry format (one dated bullet per fact, never invent, never embellish, deduplicate against existing entries).\n\n",
		slug, slug,
	)

	tailNum := "5"
	projectStep := ""
	if projectSlug != "" {
		projectStep = fmt.Sprintf(
			"5. Project update. This task is attached to project %q. Consider whether the transcript shows substantive project-level decisions, learnings, or status changes that future sessions on sibling tasks should benefit from. If so, write ONE update file at:\n"+
				"   ~/.flow/projects/%s/updates/YYYY-MM-DD-<kebab-title>.md\n"+
				"   - Filename: today's date in YYYY-MM-DD followed by a 3-5 word kebab-case title.\n"+
				"   - Shape per skill §4.5: <=10 lines, two paragraphs. Paragraph 1: what got decided/learned/shipped at the project level. Paragraph 2: what is next or now open. Optional trailing 'Blocked on: <X>' line.\n"+
				"   - Substance gate: write ONLY if the session moved the project forward in a way future sibling-task sessions should know about. Mechanical work, narrow bug fixes, and local-only refactors usually do not warrant a project update — skip in that case. There is no obligation to produce a file.\n"+
				"   - Do NOT write a template or placeholder. Either write a real update or skip.\n\n",
			projectSlug, projectSlug,
		)
		tailNum = "6"
	}

	tail := fmt.Sprintf(
		"%s. Do not output a chat summary. Just write the files silently and exit.\n\n"+
			"If the transcript is empty or contains nothing durable, do nothing. This is normal — most tasks will not yield new entries.",
		tailNum,
	)

	return header + projectStep + tail
}
