package app

import (
	"database/sql"
	"errors"
	"flag"
	"flow/internal/flowdb"
	"os"
	"strings"
)

// flagSet creates a named flag.FlagSet that prints errors instead of exiting.
func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// currentSessionID returns this process's harness session id, or ""
// if not running inside any known harness. Inside a hook the stdin
// payload's session_id wins; otherwise probes every implemented
// harness's session-id env var and returns the one that's set.
func currentSessionID() string {
	if hookSessionID != "" {
		return hookSessionID
	}
	if h := ambientHarness(); h != nil {
		return os.Getenv(h.SessionIDEnvVar())
	}
	return ""
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

// flagParseRC maps a fs.Parse error to an exit code: -h/--help already
// printed usage (flag.ErrHelp) and is a successful request (0); anything
// else is a usage error (2).
func flagParseRC(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// isHelpArg reports whether a is one of the help flags flag.FlagSet knows.
func isHelpArg(a string) bool {
	return a == "-h" || a == "-help" || a == "--help"
}

// splitPositional splits a leading positional (name/ref/slug) off args.
// A leading dash-argument is never a positional — `flow add task --help`
// once created a task named "--help". In that case missing is true and
// rest is all of args, so the caller still runs fs.Parse(rest) (which
// prints usage and returns flag.ErrHelp for -h/--help) before reporting
// the missing positional.
func splitPositional(args []string) (pos string, rest []string, missing bool) {
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return "", args, true
	}
	return args[0], args[1:], false
}
