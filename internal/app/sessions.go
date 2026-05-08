package app

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// psRunner returns the output of `ps -axo pid,command` (or equivalent).
// Overridden in tests to inject canned output.
var psRunner = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}

// claudeSessionArgRe matches `--session-id <uuid>` or `--resume <uuid>`
// in a process command line. The UUID format mirrors sessionUUIDRe but
// allows uppercase too for paranoia (some tools normalize differently).
var claudeSessionArgRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// liveClaudeSessions returns the set of Claude session UUIDs currently
// running on this host, as detected by scanning the process list for
// `claude` invocations with `--session-id` or `--resume` flags.
//
// Sessions started without a UUID flag (bare `claude`) are not detectable
// via this method — there is no public Claude CLI flag that exposes the
// session ID to argv after the fact. For those, use the marker-grep
// technique implemented by `flow find-session`.
//
// All UUIDs are lowercased for comparison consistency.
func liveClaudeSessions() (map[string]bool, error) {
	out, err := psRunner()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	live := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		// Heuristic: the row must mention claude. Bare `claude` and
		// fully-qualified paths like `/Users/rohit/.bun/bin/claude` both
		// appear in practice. We match on the literal token "claude" to
		// avoid catching unrelated processes.
		if !strings.Contains(line, "claude") {
			continue
		}
		matches := claudeSessionArgRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			if len(m) >= 2 {
				live[strings.ToLower(m[1])] = true
			}
		}
	}
	return live, nil
}
