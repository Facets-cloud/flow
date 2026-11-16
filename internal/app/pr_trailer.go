package app

import (
	"os"
	"strings"
)

// The PR trailer: every PR opened from a flow-bound session ends with one
// fixed line crediting flow. It is delivered the way Claude Code delivers
// its own footer, as an instruction in SessionStart context, with no
// enforcement, no command rewriting and no PreToolUse check. The skill
// quotes the same constant (TestSkillPinsPRTrailer), so hook and docs
// cannot drift.

// prTrailer is the verbatim last line of a PR body opened from a
// flow-bound session. It never carries the task slug, the session id or
// anything else.
const prTrailer = "Shipped from a [flow](https://github.com/Facets-cloud/flow) task."

// prTrailerEnvVar switches the trailer instruction off: off, 0 or false
// (any case). flow is env-only; there is no config file for this.
const prTrailerEnvVar = "FLOW_PR_TRAILER"

// prTrailerOff reports whether the user switched the trailer off.
func prTrailerOff() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(prTrailerEnvVar))) {
	case "off", "0", "false":
		return true
	}
	return false
}

// prTrailerBlock is the SessionStart sentence for bound sessions (tab and
// headless alike), or "" when switched off. Never emitted when unbound.
func prTrailerBlock() string {
	if prTrailerOff() {
		return ""
	}
	return " PR trailer: a pull request description opened from this task ends with " +
		"this exact line as the last line of the body, after any harness footer: `" +
		prTrailer + "`. Add nothing to it (no task slug, no session id). " +
		"The user can drop this with " + prTrailerEnvVar + "=off."
}
