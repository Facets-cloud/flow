package app

import (
	"fmt"
	"os"
	"strings"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
)

// allHarnesses returns every implemented harness adapter. The slice
// is the registry that ambient-harness detection and harnessByName
// consult. Adding codex/gemini = one line each here.
func allHarnesses() []harness.Harness {
	return []harness.Harness{
		claude.New(),
		codex.New(),
		// gemini.New(),   // wired when the gemini adapter lands
	}
}

// registeredHarnessNames returns the comma-joined list of harness
// Names this binary supports. Used in error messages so the user
// sees the available alternatives when a task is pinned to a name
// that isn't in the registry.
func registeredHarnessNames() string {
	names := make([]string, 0, len(allHarnesses()))
	for _, h := range allHarnesses() {
		names = append(names, string(h.Name()))
	}
	return strings.Join(names, ", ")
}

// harnessByName looks up an adapter by stored Name.
//
//   - Empty/NULL name → returns (claude, nil). Back-compat for
//     pre-harness-column DB rows where the column is always NULL.
//   - Known non-empty name → returns (matched adapter, nil).
//   - Unknown non-empty name → returns (nil, error). Callers decide
//     whether to error out (cmdDo, cmdTranscript), warn + skip
//     (cmdDone's close-out sweep, list.go's [live] markers), or
//     coerce. No silent fallback — the "set once on first bind"
//     column semantics break the moment a binary doesn't recognize
//     its own task's pin, and we'd rather refuse than corrupt
//     downstream state by running the wrong adapter.
func harnessByName(name string) (harness.Harness, error) {
	if name == "" {
		return claude.New(), nil
	}
	for _, h := range allHarnesses() {
		if string(h.Name()) == name {
			return h, nil
		}
	}
	return nil, fmt.Errorf(
		"task is pinned to harness %q which isn't supported by this flow binary (registered: %s) — upgrade flow, or update tasks.harness via sqlite",
		name, registeredHarnessNames(),
	)
}

// harnessForTask returns the adapter for the task's stored harness.
// NULL/empty harness column → claude+nil (back-compat). Unknown
// non-empty name → nil+error. Callers that can tolerate the error
// (e.g. list.go's per-row [live] marker) should skip the operation;
// callers that can't (cmdTranscript, cmdDo's resume path) should
// surface the error to the user and stop.
func harnessForTask(task *flowdb.Task) (harness.Harness, error) {
	if task == nil {
		return claude.New(), nil
	}
	var name string
	if task.Harness.Valid {
		name = task.Harness.String
	}
	return harnessByName(name)
}

// ambientHarness probes the current process env for each known
// harness's session-id env var. Returns the matching adapter if
// exactly one is set; returns nil if none are set OR if multiple
// are (defensive — shouldn't happen in practice, but if a user
// nests sessions we'd rather refuse to guess than pick wrong).
func ambientHarness() harness.Harness {
	var matches []harness.Harness
	for _, h := range allHarnesses() {
		if v := os.Getenv(h.SessionIDEnvVar()); v != "" {
			matches = append(matches, h)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return nil
}

// harnessForSpawn returns the harness to use when bootstrapping a
// new session for a task:
//
//  1. If the task already has a harness set, look it up by name —
//     unknown names error out so we don't silently spawn the wrong
//     adapter for a pinned task.
//  2. Otherwise, detect ambient — the harness running THIS `flow do`
//     process. If the user is inside a codex shell, the new task
//     adopts codex.
//  3. Otherwise, default to claude.
//
// flow's caller persists the result onto task.harness atomically
// with the session_id write (guarded by a COALESCE clause so an
// existing pin isn't overwritten), so step 1 dominates on every
// subsequent invocation.
func harnessForSpawn(task *flowdb.Task) (harness.Harness, error) {
	if task != nil && task.Harness.Valid && task.Harness.String != "" {
		return harnessByName(task.Harness.String)
	}
	if h := ambientHarness(); h != nil {
		return h, nil
	}
	return claude.New(), nil
}

// defaultHarness returns the adapter for code paths that have no
// task context (e.g. `flow init`, `flow skill install`, the
// SessionStart hook handler before bind). Probes ambient first so a
// user inside a codex/gemini shell gets the matching skill install;
// otherwise claude. Always returns a concrete adapter — no error
// path because there's no task pin to potentially mis-resolve.
func defaultHarness() harness.Harness {
	if h := ambientHarness(); h != nil {
		return h
	}
	return claude.New()
}

// liveSessionsForTasks returns a merged id→count map across every
// unique harness referenced by the given task slice. Calls each
// harness's LiveSessionIDs at most once. ps failures and unknown-
// harness errors are both swallowed per-task — the merged map only
// contains entries from harnesses that resolved AND whose probe
// succeeded. Used by `flow list tasks` to render [live] markers
// without scanning the same process table N times.
func liveSessionsForTasks(tasks []*flowdb.Task) map[string]int {
	seen := map[harness.Name]bool{}
	merged := map[string]int{}
	for _, t := range tasks {
		h, err := harnessForTask(t)
		if err != nil {
			// Task pinned to an unsupported harness — skip; the
			// row still renders, just without a [live] marker.
			continue
		}
		if seen[h.Name()] {
			continue
		}
		seen[h.Name()] = true
		if live, err := h.LiveSessionIDs(); err == nil {
			for id, n := range live {
				merged[id] += n
			}
		}
	}
	return merged
}
