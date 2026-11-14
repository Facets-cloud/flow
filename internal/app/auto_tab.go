package app

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/spawner"
)

// resolveAutoTab decides whether this `flow do` is a tab auto run
// (`--auto --tab`) and whether --tab was given explicitly. An explicit
// --tab / --tab=false wins; otherwise $FLOW_AUTO_TAB makes --auto default
// to a tab. --tab without --auto is a usage error (rc 2): a plain
// `flow do` already opens a tab.
func resolveAutoTab(fs *flag.FlagSet, auto, tab bool) (autoTab, explicit bool, rc int) {
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "tab" {
			explicit = true
		}
	})
	switch {
	case explicit && tab && !auto:
		fmt.Fprintln(os.Stderr, "error: --tab only applies to --auto (a plain flow do already opens an interactive tab)")
		return false, true, 2
	case !auto:
		return false, explicit, 0
	case explicit:
		return tab, true, 0
	default:
		return autoTabDefault(), false, 0
	}
}

// checkAutoTabPossible vets a tab auto run before any DB change: the host
// must have a terminal flow can drive. An explicit --tab fails (rc 1). A
// tab that only came from $FLOW_AUTO_TAB is a preference, so it falls back
// to headless — loudly, never silently.
func checkAutoTabPossible(explicit bool) (autoTab bool, rc int) {
	if spawner.TerminalAvailable() == nil {
		return true, 0
	}
	const why = "no supported terminal detected on this host, so --tab cannot open a tab"
	if explicit {
		fmt.Fprintf(os.Stderr, "error: %s; drop --tab to run it headless\n", why)
		return false, 1
	}
	fmt.Fprintf(os.Stderr, "note: $%s asks for a tab, but %s; running headless instead\n", autoTabEnvVar, why)
	return false, 0
}

// closeoutLauncher starts the close-out sweep for slug in a detached
// process (`flow __closeout-sweep <slug>`) and returns at once. Used by a
// tab run's own `flow done` on a harness that defers the sweep in auto
// runs (harness.CloseoutDeferrer): such a harness backgrounds or kills a
// long inline command (prx after 5 minutes, codex after ~10s), and a tab
// run has no supervisor to defer to. Output goes to a log under the
// task's auto-runs/ directory.
var closeoutLauncher = func(slug string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate flow binary: %w", err)
	}
	root, err := flowRoot()
	if err != nil {
		return err
	}
	runsDir := filepath.Join(root, "tasks", slug, "auto-runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", runsDir, err)
	}
	logPath := filepath.Join(runsDir, time.Now().UTC().Format("2006-01-02-150405")+"-closeout.log")
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logF.Close()
	cmd := exec.Command(self, closeoutSweepArg, slug)
	cmd.Env = autoChildEnv()
	cmd.Stdout, cmd.Stderr = logF, logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start close-out sweep: %w", err)
	}
	return cmd.Process.Release()
}

// closeoutSweepArg is the hidden subcommand closeoutLauncher re-enters.
const closeoutSweepArg = "__closeout-sweep"

// cmdCloseoutSweep is the hidden `flow __closeout-sweep <slug>` entry
// point: run the close-out sweep (KB + project update) for a task.
func cmdCloseoutSweep(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "error: "+closeoutSweepArg+" requires a task slug")
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
	task, err := flowdb.GetTask(db, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load task %q: %v\n", args[0], err)
		return 1
	}
	h, err := harnessForTask(task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := runCloseoutSweep(h, task); err != nil {
		fmt.Fprintf(os.Stderr, "close-out sweep for %q failed: %v\n", task.Slug, err)
		return 1
	}
	fmt.Printf("close-out sweep for %q done\n", task.Slug)
	return 0
}

// launchDetachedSweep starts the detached sweep, reporting success; on
// failure it warns so the caller can sweep inline instead.
func launchDetachedSweep(slug string) bool {
	if err := closeoutLauncher(slug); err != nil {
		fmt.Fprintf(os.Stderr, "warning: detach close-out sweep: %v; sweeping inline\n", err)
		return false
	}
	return true
}

// tabSweepDetached reports whether this `flow done` should detach the
// close-out sweep: the task is a running tab auto run on a harness that
// defers its sweep in auto runs. task is the row as loaded before the flip.
func tabSweepDetached(h harness.Harness, task *flowdb.Task) bool {
	return isTabAutoRun(task) && task.AutoRunStatus.Valid && task.AutoRunStatus.String == "running" &&
		harness.DefersCloseoutInAutoRun(h)
}

// autoTabPrompt builds the tab-mode autonomous prompt for task.
func autoTabPrompt(task *flowdb.Task) string {
	playbookSlug, parentSlug := "", ""
	if task.PlaybookSlug.Valid {
		playbookSlug = task.PlaybookSlug.String
	}
	if task.ParentSlug.Valid {
		parentSlug = task.ParentSlug.String
	}
	return buildAutoTabBootstrapPrompt(task.Slug, task.Kind, playbookSlug, parentSlug)
}

// joinInjection appends a --with instruction to the autonomous prompt
// behind the injection marker, the way AutoRunArgv does for a headless run.
func joinInjection(prompt, inject string) string {
	if inject == "" {
		return prompt
	}
	return prompt + "\n\n" + harness.InjectionMarker + "\n" + inject
}

// deadRunEvidence names where to look when an auto run ended dead: the
// supervisor's log for a headless run; a tab run has no log (its tab was
// closed or its harness exited before flow done), so its transcript.
func deadRunEvidence(t *flowdb.Task) string {
	if isTabAutoRun(t) {
		return "its transcript (flow transcript " + t.Slug + ")"
	}
	return "its auto-run log"
}

// rollbackFreshBootstrap undoes a fresh bootstrap's pre-allocated session
// and status flip after the launch failed before the harness could write
// a transcript, so the next attempt bootstraps cleanly. The session-id
// invariant (in-progress requires session_id) makes backlog the only
// consistent state. Guarded on session_id so a concurrent `flow do` that
// already re-bound the row is left alone.
func rollbackFreshBootstrap(db *sql.DB, slug, sessionID string) {
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=NULL, session_started=NULL,
			status='backlog', status_changed_at=NULL, updated_at=?
		 WHERE slug=? AND session_id=?`,
		flowdb.NowISO(), slug, sessionID,
	); err != nil {
		fmt.Fprintf(os.Stderr, "warning: rollback pre-allocated session after launch failure: %v\n", err)
	}
}
