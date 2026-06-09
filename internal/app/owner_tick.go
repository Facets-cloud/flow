package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/spawner"
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

// ownerInteractiveLauncher spawns an interactive terminal tab running one
// owner tick with the human present (the hand-triggered / first-run path).
// It mints a throwaway session (not bound to the owner) and reuses the same
// spawn machinery as `flow do`. Overridable in tests.
var ownerInteractiveLauncher = func(o *flowdb.Owner, prompt string) error {
	var hname string
	if o.Harness.Valid {
		hname = o.Harness.String
	}
	h, err := harnessByName(hname)
	if err != nil {
		return err
	}
	sid, err := h.NewSessionID()
	if err != nil {
		return fmt.Errorf("new session id: %w", err)
	}
	command := h.LaunchCmd(sid, prompt, harness.LaunchOpts{})
	var spawnEnv map[string]string
	if root := os.Getenv("FLOW_ROOT"); root != "" {
		spawnEnv = map[string]string{"FLOW_ROOT": root}
	}
	return spawner.SpawnTab("owner: "+o.Slug, o.WorkDir, command, spawnEnv)
}

// ownerTickManual implements `flow owner tick <slug>`: a hand-triggered,
// on-demand tick (wake the owner now, regardless of schedule). Interactive
// by default — spawns a tab the user drives, ideal for the first tick or
// to check something early. `--auto` runs it headlessly instead. Neither
// path advances the owner's schedule; this is an extra tick on top of it.
func ownerTickManual(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "error: owner tick requires an owner slug")
		return 2
	}
	slug := args[0]
	fs := flagSet("owner tick")
	auto := fs.Bool("auto", false, "run the tick headlessly in the background (no tab, no human)")
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
	o, err := flowdb.GetOwner(db, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: no owner %q\n", slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// A paused/retired owner does not tick. Refuse the hand-triggered tick
	// with guidance rather than silently spawning a session that
	// contradicts the owner's state.
	if o.Status != "active" {
		fmt.Fprintf(os.Stderr,
			"error: owner %q is %s — resume it first with `flow owner start %s` (a paused/retired owner does not tick)\n",
			slug, o.Status, slug)
		return 1
	}

	if *auto {
		root, err := flowRoot()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		now := time.Now()
		ticksDir := filepath.Join(root, "owners", slug, "ticks")
		if err := os.MkdirAll(ticksDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		logPath := filepath.Join(ticksDir, now.UTC().Format("2006-01-02-150405")+".log")
		pid, err := ownerTickLauncher(slug, o.WorkDir, logPath, autoChildEnv())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if err := recordOwnerTickStarted(db, slug, pid, now.Format(time.RFC3339)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: record tick for %q: %v\n", slug, err)
		}
		fmt.Printf("dispatched a headless tick for owner %q (pid %d)\n", slug, pid)
		return 0
	}

	if err := ownerInteractiveLauncher(o, buildOwnerTickPromptInteractive(slug)); err != nil {
		fmt.Fprintf(os.Stderr, "error: spawn interactive tick: %v\n", err)
		return 1
	}
	// Record that an interactive tick was launched. We can't track its
	// completion (the user drives the tab, there's no process we wait on),
	// so we mark last_tick now with status 'interactive' — enough that
	// `flow owner show` reflects a tick ran instead of "(never)". Targeted
	// update (last_tick_* only) so it never overwrites next_wake_at — the
	// interactive session self-paces via `flow owner next` and that write
	// must win regardless of ordering.
	if err := recordOwnerInteractiveTick(db, slug); err != nil {
		fmt.Fprintf(os.Stderr, "warning: record interactive tick for %q: %v\n", slug, err)
	}
	fmt.Printf("opened an interactive tick for owner %q — drive it in the new tab\n", slug)
	return 0
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
		// Overlap guard: if a tick is already running for this owner (a live
		// tick_pid), don't stack another on top of it. A dead pid (crashed
		// tick) is not alive, so we fall through and dispatch a fresh one
		// (which overwrites the stale pid below).
		if o.TickPID.Valid && processAlive(int(o.TickPID.Int64)) {
			continue
		}
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
		// record the running tick (pid + start). Targeted column update —
		// NOT a full-row write — so it can't clobber last_tick_* if the
		// detached tick has already finished and recorded its result (the
		// dispatch-vs-finish race).
		if err := recordOwnerTickDispatched(db, o.Slug, pid,
			now.Format(time.RFC3339), now.Add(dur).Format(time.RFC3339)); err != nil {
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

	// Status guard: only active owners tick. The scheduler already filters
	// via DueOwners, but an owner can be paused/retired in the window
	// between dispatch and this detached run (or a manual --auto on a
	// paused owner). Skip cleanly without running the harness or recording
	// a tick.
	if o.Status != "active" {
		fmt.Printf("owner %q is %s (not active) — skipping tick\n", slug, o.Status)
		return 0
	}

	var hname string
	if o.Harness.Valid {
		hname = o.Harness.String
	}
	h, herr := harnessByName(hname)
	if herr != nil {
		fmt.Fprintf(os.Stderr, "error: resolve harness for %q: %v\n", slug, herr)
		_ = recordOwnerTick(db, slug, "error")
		return 1
	}

	prompt := buildOwnerTickPrompt(o.Slug)
	runErr := ownerTickRunner(h, prompt)

	status := "ok"
	if runErr != nil {
		status = "error"
	}
	if err := recordOwnerTick(db, slug, status); err != nil {
		fmt.Fprintf(os.Stderr, "warning: record tick for %q: %v\n", slug, err)
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "owner tick for %q failed: %v\n", slug, runErr)
		return 1
	}
	fmt.Printf("owner tick for %q finished: %s\n", slug, status)
	return 0
}

// The tick-bookkeeping writes below are deliberately TARGETED column
// updates rather than the full-row flowdb.UpdateOwner. The scheduler
// (parent) and the detached tick (child) write the owner row
// concurrently — a full-row write of either's stale in-memory struct
// clobbers the other's columns. By having each writer touch only the
// columns it owns, the writes commute:
//
//	dispatch/start → tick_pid, tick_started (+ next_wake_at for the scheduler)
//	finish         → last_tick_at, last_tick_status, clear tick_pid/tick_started
//	self-pace      → next_wake_at (written by the tick via `flow owner next`)
//
// The one remaining overlap is tick_pid: a tick that finishes before the
// scheduler records the pid leaves a stale (dead) pid set, which the
// dead-pid reconcile (reconcileOwnerTick / DueOwners overlap guard) heals
// on the next read or scan. No durable result is lost.

// recordOwnerTickStarted stamps the running-tick bookkeeping (pid + start)
// for a hand-triggered headless tick. It does not advance the schedule.
func recordOwnerTickStarted(db *sql.DB, slug string, pid int, started string) error {
	_, err := db.Exec(
		`UPDATE owners SET tick_pid=?, tick_started=?, updated_at=? WHERE slug=?`,
		pid, started, flowdb.NowISO(), slug,
	)
	return err
}

// recordOwnerTickDispatched is recordOwnerTickStarted plus advancing the
// schedule floor (next_wake_at) — the scheduler dispatch path.
func recordOwnerTickDispatched(db *sql.DB, slug string, pid int, started, nextWakeAt string) error {
	_, err := db.Exec(
		`UPDATE owners SET tick_pid=?, tick_started=?, next_wake_at=?, updated_at=? WHERE slug=?`,
		pid, started, nextWakeAt, flowdb.NowISO(), slug,
	)
	return err
}

// recordOwnerInteractiveTick marks a hand-triggered interactive tick as
// having run (last_tick_* only). It leaves next_wake_at untouched so the
// interactive session's own `flow owner next` self-pacing wins.
func recordOwnerInteractiveTick(db *sql.DB, slug string) error {
	now := flowdb.NowISO()
	_, err := db.Exec(
		`UPDATE owners SET last_tick_at=?, last_tick_status='interactive', updated_at=? WHERE slug=?`,
		now, now, slug,
	)
	return err
}

// recordOwnerTick stamps last_tick_at + last_tick_status and clears the
// running-tick bookkeeping (the tick is over). Targeted update — it never
// touches next_wake_at, so a next-wake the tick set mid-run via
// `flow owner next` survives.
func recordOwnerTick(db *sql.DB, slug, status string) error {
	now := flowdb.NowISO()
	_, err := db.Exec(
		`UPDATE owners SET last_tick_at=?, last_tick_status=?, tick_pid=NULL, tick_started=NULL, updated_at=? WHERE slug=?`,
		now, status, now, slug,
	)
	return err
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
	now := flowdb.NowISO()
	// Targeted + guarded: only promote to 'dead' while a tick_pid is still
	// recorded. If a real finish (recordOwnerTick) lands between the
	// processAlive check and this write, it clears tick_pid first and the
	// WHERE no longer matches — so we never overwrite a genuine 'ok'/'error'
	// result with 'dead'.
	if _, err := db.Exec(
		`UPDATE owners SET last_tick_status='dead', last_tick_at=COALESCE(last_tick_at, ?),
		 tick_pid=NULL, tick_started=NULL, updated_at=? WHERE slug=? AND tick_pid IS NOT NULL`,
		now, now, o.Slug,
	); err != nil {
		return
	}
	o.LastTickStatus = sql.NullString{String: "dead", Valid: true}
	if !o.LastTickAt.Valid {
		o.LastTickAt = sql.NullString{String: now, Valid: true}
	}
	o.TickPID = sql.NullInt64{}
	o.TickStarted = sql.NullString{}
}

// buildOwnerTickPromptInteractive is the prompt for a hand-triggered tick
// run WITH the user present (often the first tick). Same orchestrate +
// journal discipline as the headless tick, but the human can guide it: it
// MAY use AskUserQuestion, and it folds anything learned back into the
// charter so future headless ticks improve.
func buildOwnerTickPromptInteractive(slug string) string {
	return fmt.Sprintf(
		"You are running ONE tick of the flow OWNER %q — INTERACTIVELY, with the user present (often the FIRST tick, to help you navigate). You MAY use AskUserQuestion to clarify intent, confirm a plan, or resolve charter ambiguity, and the user can course-correct you. If the charter is unclear or wrong, work it out WITH the user and update owners/%s/charter.md so future HEADLESS ticks behave correctly.\n\n"+
			"You still ORCHESTRATE; you NEVER execute work inline. A tick gets no `flow done` close-out sweep, so route real work through tasks/playbooks that self-close:\n"+
			"  - RECURRING work → a PLAYBOOK: create if needed, then `flow run playbook <slug> --auto`. Tag the run owner:%s.\n"+
			"  - ONE-TIME work → a TASK: `flow add task \"<what>\" --tag owner:%s`, then `flow do --auto <task>`.\n"+
			"  - A decision for the user → just ASK them (you're interactive), or park a QUESTION task (`--tag question --tag owner:%s`) for later.\n\n"+
			"Do these in order:\n"+
			"1. Invoke the flow skill via the Skill tool.\n"+
			"2. Read your charter at owners/%s/charter.md.\n"+
			"3. Read recent notes under owners/%s/updates/ (your journal), then review what you own with `flow owner show %s` (tasks, playbook runs, and questions with status). Don't duplicate tracked work or re-spawn an in-progress run.\n"+
			"4. Observe per the charter and, together with the user, DISPATCH the needed playbook-runs / tasks / questions.\n"+
			"5. Be conservative with irreversible/outward actions unless the charter authorizes them (when unsure, ask the user).\n"+
			"6. Before finishing, WRITE a short note to owners/%s/updates/<today>-tick.md (what you observed, what you dispatched with slugs, what the next tick should check), and fold any lessons into the charter so headless ticks improve.\n"+
			"7. SELF-PACE the next wake: decide when you next need to run and set it with `flow owner next %s --in <duration>` (or --at <time>). You pick the cadence yourself each run — no need to ask the user about timing; --every is only a fallback.\n",
		slug, slug, slug, slug, slug, slug, slug, slug, slug, slug,
	)
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
			"6. WRITE a short note to owners/%s/updates/<today>-tick.md recording what you observed, what you dispatched (with the task/run slugs), and what your NEXT tick should check. This is your memory — the next tick starts from a blank session and knows only what you write here plus the task records.\n"+
			"7. SELF-PACE: decide when you next need to run and set it with `flow owner next %s --in <duration>` (e.g. +15m if watching a deploy/CI, +1h if a review is pending; use --at <time> for longer/idle gaps). You choose the cadence per-run; if you skip this, the owner falls back to its --every interval.\n"+
			"8. Then exit. Do not loop or wait.\n",
		slug, slug, slug, slug, slug, slug, slug, slug, slug,
	)
}
