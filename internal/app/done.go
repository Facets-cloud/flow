package app

import (
	"flow/internal/flowdb"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// claudeRunner invokes the headless `claude -p` CLI for the KB sweep.
// Tests override this var to capture invocations without spawning claude.
// Stdout/stderr are discarded — the sweep prompt instructs claude to
// write KB entries silently and produce no chat output.
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
// spawns a headless `claude -p` session that loads the flow skill, reads
// the task's transcript, and applies §4.10 scoop rules to ~/.flow/kb/*.md.
// The CLI prints "updating kbs..." while it waits. A failed sweep
// (missing claude binary, non-zero exit) only emits a warning — the
// status flip is the contract; the sweep is best-effort.
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
		fmt.Print("updating kbs...")
		if err := claudeRunner(task.Slug, buildKBSweepPrompt(task.Slug)); err != nil {
			fmt.Println()
			fmt.Fprintf(os.Stderr, "warning: kb sweep failed: %v\n", err)
		} else {
			fmt.Println(" done")
		}
	}
	return 0
}

// buildKBSweepPrompt composes the headless prompt that drives the
// post-done KB sweep. The prompt is passed as a single positional arg
// to `claude -p` via exec.Command — no shell interpolation, so any
// characters are safe.
//
// All dedupe/append discipline lives in the flow skill (§4.10), not in
// this prompt. The prompt's job is just: load the skill, read the
// transcript, apply the rules. The skill takes it from there.
func buildKBSweepPrompt(slug string) string {
	return fmt.Sprintf(
		"You are running an automated KB sweep for completed flow task %q. Do this:\n\n"+
			"1. Invoke the flow skill via the Skill tool. This loads §4.10 (the scoop-mode KB rules) which you must follow exactly.\n\n"+
			"2. Run: flow transcript %s\n"+
			"   This prints the conversation transcript from the task's Claude session. Read it carefully.\n\n"+
			"3. For each of these five files, decide whether the transcript revealed any durable facts that belong there per §4.10's bucket table:\n"+
			"   - ~/.flow/kb/user.md\n"+
			"   - ~/.flow/kb/org.md\n"+
			"   - ~/.flow/kb/products.md\n"+
			"   - ~/.flow/kb/processes.md\n"+
			"   - ~/.flow/kb/business.md\n\n"+
			"4. For each file you decide needs new entries, Read it first to check for duplicates, then append entries using the §4.10 entry format (one dated bullet per fact, never invent, never embellish, deduplicate against existing entries).\n\n"+
			"5. Do not output a chat summary. Just write the files silently and exit.\n\n"+
			"If the transcript is empty or contains no durable facts, do nothing. This is normal — most tasks won't yield new KB entries.",
		slug, slug,
	)
}
