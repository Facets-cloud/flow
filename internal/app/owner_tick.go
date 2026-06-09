package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// The owner tick + scheduler (time-based, MVP).
//
//	flow owner tick-due          (scheduler entry — call on an interval, e.g. launchd)
//	  ├─ DueOwners: active owners whose next_wake_at <= now
//	  ├─ for each: advance next_wake_at = now + every (so the next scan
//	  │  doesn't re-dispatch it) and launch a DETACHED tick
//	  └─ returns
//
//	flow __owner-tick <slug>     (the detached tick; hidden subcommand)
//	  ├─ builds the tick prompt, runs the owner's harness HEADLESSLY and
//	  │  SESSIONLESS (a fresh session each tick — owners are not one long
//	  │  Claude chat), blocking until it exits
//	  └─ records last_tick_at + last_tick_status ('ok' | 'error')
//
// Each tick reads the owner's charter, reviews what it owns (tasks tagged
// owner:<slug>), acts, and parks human decisions as question-tasks. MVP
// limitation: a tick that runs longer than `every` may overlap the next
// dispatch — there is no run-liveness guard yet.

// ownerTickRunner executes one headless, sessionless tick for the owner
// via its harness. SkipPermissionsRun starts a FRESH session each call
// (no pinned session id) — the owner's memory is its durable charter +
// ledger, not a resumed transcript. Overridable in tests.
var ownerTickRunner = func(h harness.Harness, prompt string) error {
	return h.SkipPermissionsRun(prompt)
}

// ownerTickLauncher starts the detached `flow __owner-tick <slug>`
// process: own session (Setsid), cwd=workDir, stdout/stderr→logPath.
// Overridable in tests.
var ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate flow binary: %w", err)
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logF.Close()

	cmd := exec.Command(self, "__owner-tick", slug)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start owner tick: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// ownerTickDue implements `flow owner tick-due`: the scheduler pass that
// the OS timer (launchd) invokes on an interval.
func ownerTickDue(args []string) int {
	fs := flagSet("owner tick-due")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	root, err := flowRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	now := time.Now()
	due, err := flowdb.DueOwners(db, now.Format(time.RFC3339))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	dispatched := 0
	for _, o := range due {
		dur, derr := time.ParseDuration(o.Every)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "warning: owner %q has invalid every %q: %v; skipping\n", o.Slug, o.Every, derr)
			continue
		}
		ticksDir := filepath.Join(root, "owners", o.Slug, "ticks")
		if err := os.MkdirAll(ticksDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: mkdir ticks for %q: %v\n", o.Slug, err)
			continue
		}
		logPath := filepath.Join(ticksDir, now.UTC().Format("2006-01-02-150405")+".log")
		pid, err := ownerTickLauncher(o.Slug, o.WorkDir, logPath, autoChildEnv())
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: launch tick for %q: %v\n", o.Slug, err)
			continue
		}
		// Advance the schedule (so the next scan doesn't re-dispatch) and
		// record the running tick (pid + start) in one write.
		o.NextWakeAt = sql.NullString{String: now.Add(dur).Format(time.RFC3339), Valid: true}
		o.TickPID = sql.NullInt64{Int64: int64(pid), Valid: true}
		o.TickStarted = sql.NullString{String: now.Format(time.RFC3339), Valid: true}
		if err := flowdb.UpdateOwner(db, o); err != nil {
			fmt.Fprintf(os.Stderr, "warning: record tick for %q: %v\n", o.Slug, err)
			continue
		}
		dispatched++
	}
	fmt.Printf("dispatched %d owner tick(s)\n", dispatched)
	return 0
}

// cmdOwnerTick is the hidden `flow __owner-tick <slug>` supervisor that
// runs inside the detached process: build the tick prompt, run the
// owner's harness headlessly to completion, then record the tick.
func cmdOwnerTick(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: __owner-tick requires an owner slug")
		return 2
	}
	slug := args[0]

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	o, err := flowdb.GetOwner(db, slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load owner %q: %v\n", slug, err)
		return 1
	}

	var hname string
	if o.Harness.Valid {
		hname = o.Harness.String
	}
	h, herr := harnessByName(hname)
	if herr != nil {
		fmt.Fprintf(os.Stderr, "error: resolve harness for %q: %v\n", slug, herr)
		_ = recordOwnerTick(db, o, "error")
		return 1
	}

	prompt := buildOwnerTickPrompt(o.Slug)
	runErr := ownerTickRunner(h, prompt)

	status := "ok"
	if runErr != nil {
		status = "error"
	}
	if err := recordOwnerTick(db, o, status); err != nil {
		fmt.Fprintf(os.Stderr, "warning: record tick for %q: %v\n", slug, err)
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "owner tick for %q failed: %v\n", slug, runErr)
		return 1
	}
	fmt.Printf("owner tick for %q finished: %s\n", slug, status)
	return 0
}

// recordOwnerTick stamps last_tick_at + last_tick_status on the owner and
// clears the running-tick bookkeeping (the tick is over).
func recordOwnerTick(db *sql.DB, o *flowdb.Owner, status string) error {
	o.LastTickAt = sql.NullString{String: flowdb.NowISO(), Valid: true}
	o.LastTickStatus = sql.NullString{String: status, Valid: true}
	o.TickPID = sql.NullInt64{}
	o.TickStarted = sql.NullString{}
	return flowdb.UpdateOwner(db, o)
}

// reconcileOwnerTick promotes a stale running tick to 'dead' when its
// supervisor pid is no longer alive (crash, kill, reboot — anything that
// prevented recordOwnerTick from running). No-op when no tick is recorded
// running or the pid is still alive. Mutates both the DB and the in-memory
// owner so the caller renders the reconciled state. Best-effort.
func reconcileOwnerTick(db *sql.DB, o *flowdb.Owner) {
	if !o.TickPID.Valid {
		return
	}
	if processAlive(int(o.TickPID.Int64)) {
		return // genuinely running
	}
	o.LastTickStatus = sql.NullString{String: "dead", Valid: true}
	if !o.LastTickAt.Valid {
		o.LastTickAt = sql.NullString{String: flowdb.NowISO(), Valid: true}
	}
	o.TickPID = sql.NullInt64{}
	o.TickStarted = sql.NullString{}
	_ = flowdb.UpdateOwner(db, o)
}

// buildOwnerTickPrompt is the bootstrap prompt for one headless owner
// tick. The owner reads its charter, reviews what it owns, acts, and
// parks any human decision as a question-task rather than blocking.
func buildOwnerTickPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the autonomous OWNER %q running ONE tick. You are headless: NO human is watching and there is no terminal — do NOT use AskUserQuestion and do NOT wait for input.\n\n"+
			"YOU ORCHESTRATE; YOU NEVER EXECUTE WORK INLINE. A tick is a sessionless run with NO `flow done` close-out, so any real work you do directly here is LOST to the knowledge base and leaves no transcript. So do NOT do substantive work yourself in this tick — instead route EVERY piece of work through a task or a playbook, which run as their own sessions and self-close with the `flow done` KB + project sweep:\n"+
			"  - RECURRING work → a PLAYBOOK. If one doesn't exist, create it (flow add playbook ...); then trigger a run: `flow run playbook <slug> --auto`. Tag the run owner:%s.\n"+
			"  - ONE-TIME work → a TASK. Create it: `flow add task \"<what to do>\" --tag owner:%s`, then run it headlessly: `flow do --auto <task>`. It self-`flow done`s, firing the close-out sweep that captures learnings.\n"+
			"  - A decision only the HUMAN can make → a QUESTION task: `flow add task \"<the question>\" --tag question --tag owner:%s`, then move on.\n\n"+
			"Do these in order:\n"+
			"1. Invoke the flow skill via the Skill tool.\n"+
			"2. Read your charter at owners/%s/charter.md — your operating manual (what you own, how to observe, when to ask, when to escalate).\n"+
			"3. Catch up on yourself: read the most recent note(s) under owners/%s/updates/ — that is your JOURNAL from prior ticks (what you dispatched, what you were waiting on, what to check now). Then review everything you own with `flow owner show %s` — it lists your in-flight tasks, playbook runs, AND open questions with their current status (do NOT use `flow list tasks`, which hides playbook runs). Advance or check on what's there; never duplicate work already tracked and never re-spawn a run that is still in progress.\n"+
			"4. Observe the world per your charter, then DISPATCH what needs doing as playbook-runs / tasks / questions per the rule above. Keep the tick SHORT — spin work out, never perform it inline.\n"+
			"5. Be conservative with irreversible or outward-facing actions (push, PR, deploy, messaging) unless your charter EXPLICITLY authorizes them.\n"+
			"6. Before you exit, WRITE a short note to owners/%s/updates/<today>-tick.md recording what you observed, what you dispatched (with the task/run slugs), and what your NEXT tick should check. This is your memory — the next tick starts from a blank session and knows only what you write here plus the task records.\n"+
			"7. Then exit. Your next tick is scheduled automatically. Do not loop or wait.\n",
		slug, slug, slug, slug, slug, slug, slug, slug,
	)
}
