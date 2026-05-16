package app

import (
	"database/sql"
	"errors"
	"flag"
	"flow/internal/flowdb"
	"os"
)

// flagSet creates a named flag.FlagSet that prints errors instead of exiting.
func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func leadingHelpArg(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help")
}

func parseFlagSet(fs *flag.FlagSet, args []string) (handled bool, rc int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, 0
		}
		return true, 2
	}
	return false, 0
}

// currentSessionID returns this process's Claude Code session UUID,
// or "" if not running inside a Claude Code session. Reads
// $CLAUDE_CODE_SESSION_ID, which Claude Code injects into every
// session's environment.
func currentSessionID() string {
	return os.Getenv("CLAUDE_CODE_SESSION_ID")
}

// currentSessionTask returns the task bound to this Claude session
// via tasks.session_id. Returns sql.ErrNoRows if the current session
// is unbound (dispatch session) or the env var is missing. This is
// the canonical "what task am I on?" lookup — replaces the legacy
// $FLOW_TASK env var.
func currentSessionTask(db *sql.DB) (*flowdb.Task, error) {
	return flowdb.TaskBySessionID(db, currentSessionID())
}

// isNoBindingErr is a small predicate for the dispatch-session case.
// Callers use it to differentiate "no current binding" from real
// scan errors when reverse-looking-up by $CLAUDE_CODE_SESSION_ID.
func isNoBindingErr(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
