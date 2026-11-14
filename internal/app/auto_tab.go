package app

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

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

// checkAutoTabPossible vets a tab auto run before any DB change: prx has
// no tab path yet, and the host must have a terminal flow can drive. An
// explicit --tab fails (rc 2 for prx, 1 for no terminal). A tab that only
// came from $FLOW_AUTO_TAB is a preference, so it falls back to headless —
// loudly, never silently.
func checkAutoTabPossible(h harness.Harness, explicit bool) (autoTab bool, rc int) {
	why, code := "", 0
	if h.Name() == harness.NamePrx {
		why, code = "--tab is not supported for the prx harness yet", 2
	} else if err := spawner.TerminalAvailable(); err != nil {
		why, code = "no supported terminal detected on this host, so --tab cannot open a tab", 1
	}
	if why == "" {
		return true, 0
	}
	if explicit {
		fmt.Fprintf(os.Stderr, "error: %s; drop --tab to run it headless\n", why)
		return false, code
	}
	fmt.Fprintf(os.Stderr, "note: $%s asks for a tab, but %s; running headless instead\n", autoTabEnvVar, why)
	return false, 0
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
