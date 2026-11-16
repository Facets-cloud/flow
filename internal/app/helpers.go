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
//
// A leading "--" is the escape: the argument after it is the positional
// whatever it starts with (`flow add task -- "-5 degrees fix"`).
func splitPositional(args []string) (pos string, rest []string, missing bool) {
	if len(args) >= 2 && args[0] == "--" && args[1] != "" {
		return args[1], args[2:], false
	}
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return "", args, true
	}
	return args[0], args[1:], false
}

// parsePositional parses fs over args that carry one optional leading-or-
// trailing positional (a ref/slug) and returns it ("" if none). Unlike a
// bare fs.Parse, flags AFTER the positional are parsed too — package flag
// stops at the first non-flag, so `flow owner start x --help` used to run
// instead of printing usage. A leading `--` still escapes a dash-prefixed
// positional; anything after the positional stays in fs.Args().
func parsePositional(fs *flag.FlagSet, args []string) (string, error) {
	pos, rest, missing := splitPositional(args)
	if err := fs.Parse(rest); err != nil {
		return "", err
	}
	if missing && fs.NArg() > 0 {
		pos = fs.Arg(0)
		after := fs.Args()[1:]
		if consumedTerminator(fs, rest, len(rest)-fs.NArg()) {
			// `… -- pos …`: everything after the terminator is positional;
			// re-parse behind a fresh `--` only to drop pos from fs.Args().
			after = append([]string{"--"}, after...)
		}
		if err := fs.Parse(after); err != nil {
			return "", err
		}
	}
	return pos, nil
}

// consumedTerminator reports whether fs.Parse stopped on a `--` terminator
// within args[:consumed], replaying package flag's own token walk so a `--`
// that was a flag's VALUE (`--status --`) is not mistaken for one.
func consumedTerminator(fs *flag.FlagSet, args []string, consumed int) bool {
	for i := 0; i < consumed; i++ {
		a := args[i]
		if a == "--" {
			return true
		}
		name := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		i++ // the next token is this flag's value
	}
	return false
}
