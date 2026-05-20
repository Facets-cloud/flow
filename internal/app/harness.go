package app

import (
	"os"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

// allHarnesses returns every implemented harness adapter. The slice
// is the registry that ambient-harness detection and harnessByName
// consult. Adding codex/gemini = one line each here.
func allHarnesses() []harness.Harness {
	return []harness.Harness{
		claude.New(),
		// codex.New(),    // wired when the codex adapter lands
		// gemini.New(),   // wired when the gemini adapter lands
	}
}

// harnessByName looks up an adapter by stored Name. Empty or unknown
// names fall back to claude — every pre-harness-column DB row reads
// as NULL, which we want to treat as claude rather than error.
func harnessByName(name string) harness.Harness {
	for _, h := range allHarnesses() {
		if string(h.Name()) == name {
			return h
		}
	}
	return claude.New()
}

// harnessForTask returns the adapter for the task's stored harness.
// NULL/empty harness column → claude (back-compat). Used by code that
// already has a task row (cmdDone close-out sweep, cmdTranscript
// rendering, per-task [live] markers).
func harnessForTask(task *flowdb.Task) harness.Harness {
	if task != nil && task.Harness.Valid && task.Harness.String != "" {
		return harnessByName(task.Harness.String)
	}
	return claude.New()
}

// ambientHarness probes the current process env for each known
// harness's session-id env var. Returns the matching adapter if
// exactly one is set; returns nil if none are set OR if multiple are
// (defensive — shouldn't happen in practice, but if a user nests
// sessions we'd rather refuse to guess than pick wrong).
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
//  1. If the task already has a harness set, use it (the task is
//     committed to that adapter for its lifetime).
//  2. Otherwise, detect ambient — the harness running THIS `flow do`
//     process. The reasoning: if the user runs `flow do <new-task>`
//     from inside a codex shell, they almost certainly want the new
//     task to use codex too.
//  3. Otherwise, default to claude.
//
// flow's caller persists the result onto task.harness atomically with
// the session_id write, so step 1 dominates on every subsequent
// invocation.
func harnessForSpawn(task *flowdb.Task) harness.Harness {
	if task != nil && task.Harness.Valid && task.Harness.String != "" {
		return harnessByName(task.Harness.String)
	}
	if h := ambientHarness(); h != nil {
		return h
	}
	return claude.New()
}

// defaultHarness returns the adapter for code paths that have no
// task context (e.g. `flow init`, `flow skill install`, the
// SessionStart hook handler before bind). Probes ambient first so a
// user inside a codex/gemini shell gets the matching skill install;
// otherwise claude.
func defaultHarness() harness.Harness {
	if h := ambientHarness(); h != nil {
		return h
	}
	return claude.New()
}

// liveSessionsForTasks returns a merged id→count map across every
// unique harness referenced by the given task slice. Calls each
// harness's LiveSessionIDs at most once. ps failures are swallowed
// per-harness — the merged map only contains entries from harnesses
// whose ps probe succeeded. Used by `flow list tasks` to render
// [live] markers without scanning the same process table N times.
func liveSessionsForTasks(tasks []*flowdb.Task) map[string]int {
	seen := map[harness.Name]bool{}
	merged := map[string]int{}
	for _, t := range tasks {
		h := harnessForTask(t)
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
