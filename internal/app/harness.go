package app

import (
	"strings"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/registry"
	"flow/internal/spawner"
)

// The helpers below are the app layer's view of the harness registry.
// They exist so command code reads in flow's own vocabulary ("the
// harness for this task", "the harness to spawn with") while the
// question of WHICH adapters exist stays in internal/harness/registry.
// Nothing in internal/app imports a concrete adapter.

// allHarnesses returns every implemented harness adapter.
func allHarnesses() []harness.Harness { return registry.All() }

// registeredHarnessNames lists every resolvable harness, for `flow do
// --harness`'s flag help and its invalid-value error. Registry-sourced,
// so a manifest dropped in $FLOW_ROOT/harnesses shows up in both.
func registeredHarnessNames() string { return registry.Names() }

// harnessByName looks up an adapter by stored Name. Empty name
// resolves to the back-compat fallback; an unknown non-empty name is
// an error rather than a silent coercion. See registry.ByName.
func harnessByName(name string) (harness.Harness, error) {
	return registry.ByName(name)
}

// harnessForTask returns the adapter for the task's stored harness.
// NULL/empty harness column → claude+nil (back-compat). Unknown
// non-empty name → nil+error. Callers that can tolerate the error
// (e.g. list.go's per-row [live] marker) should skip the operation;
// callers that can't (cmdTranscript, cmdDo's resume path) should
// surface the error to the user and stop.
func harnessForTask(task *flowdb.Task) (harness.Harness, error) {
	if task == nil {
		return registry.Fallback(), nil
	}
	var name string
	if task.Harness.Valid {
		name = task.Harness.String
	}
	return harnessByName(name)
}

// ambientProduct names the harness running THIS process for use in
// user-facing copy ("this Claude session is already bound to …").
// Falls back to a neutral word when no harness is detectable, so a
// message never claims the user is in a harness they aren't.
func ambientProduct() string {
	if h := ambientHarness(); h != nil {
		return h.Vocab().Product
	}
	return "agent"
}

// sessionEnvVarList returns the comma-joined, $-prefixed session-id env
// vars flow probes to detect an ambient harness. Used in errors so the
// user sees exactly which variables were looked for.
func sessionEnvVarList() string {
	all := allHarnesses()
	vars := make([]string, 0, len(all))
	for _, h := range all {
		// A harness that publishes no session id (codex, omp) has
		// nothing to list — naming "$" would be noise.
		if v := h.SessionIDEnvVar(); v != "" {
			vars = append(vars, "$"+v)
		}
	}
	if len(vars) == 0 {
		return "no registered harness publishes a session id"
	}
	return strings.Join(vars, ", ")
}

// backgroundCapableNames describes which registered harnesses can host
// background sessions, so capability-gate errors name the real answer
// instead of hardcoding one.
func backgroundCapableNames() string {
	var names []string
	for _, h := range allHarnesses() {
		if h.Background() != nil {
			names = append(names, string(h.Name()))
		}
	}
	if len(names) == 0 {
		return "no registered harness supports background sessions"
	}
	return "supported by: " + strings.Join(names, ", ")
}

// ambientHarness returns the harness running THIS flow process,
// detected from its session-id env var, or nil when that is
// ambiguous. See registry.Ambient.
func ambientHarness() harness.Harness { return registry.Ambient() }

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
	return registry.Fallback(), nil
}

// defaultHarness returns the adapter for code paths that have no
// task context (e.g. `flow init`, `flow skill install`, the
// SessionStart hook handler before bind). Probes ambient first so a
// user inside a non-default shell gets the matching skill install;
// otherwise the registry fallback. Always returns a concrete adapter
// — no error path because there's no task pin to mis-resolve.
func defaultHarness() harness.Harness { return registry.Default() }

// liveSessionsForTasks returns a merged id→count map across every
// unique harness referenced by the given task slice. Calls each
// harness's LiveSessionIDs at most once. ps failures and unknown-
// harness errors are both swallowed per-task — the merged map only
// contains entries from harnesses that resolved AND whose probe
// succeeded. Used by `flow list tasks` to render [live] markers
// without scanning the same process table N times.
//
// Background agents (claude --bg) typically don't carry --session-id /
// --resume in their argv, so the ps scan misses them. For harnesses that
// support background sessions, their registry (claude agents --json) is
// merged in too so bg-bound tasks still light up [live].
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
		// Only consult the background-agent registry in bg mode. The query
		// is a `claude agents` subprocess; firing it on every `flow list`
		// for non-bg users would be a latency regression (and they have no
		// bg sessions to surface). bg-mode invocations opt into the cost.
		if spawner.IsBackground() {
			if bg := h.Background(); bg != nil {
				if agents, err := bg.BackgroundAgents(); err == nil {
					for _, a := range agents {
						// Only a running process counts as "live". --all
						// surfaces exited/failed/done sessions too (pid 0);
						// those are recoverable but not currently running, so
						// they must not light up the [live] marker.
						if a.SessionID != "" && a.PID > 0 {
							merged[strings.ToLower(a.SessionID)]++
						}
					}
				}
			}
		}
	}
	return merged
}

// bgAgentStatus returns the live background-agent entry for a task's bound
// session, or nil if the task isn't bg-bound, has no session, the harness
// can't host background agents, or the session isn't currently running.
// Used by `flow show` to surface a bg session's status/state/pid via a
// per-render `claude agents --json` lookup.
func bgAgentStatus(t *flowdb.Task) *harness.BackgroundAgent {
	if t == nil || !t.SessionID.Valid || t.SessionID.String == "" {
		return nil
	}
	// Only consult the registry in bg mode — see liveSessionsForTasks.
	// Keeps `flow show` free of a `claude agents` subprocess for non-bg
	// users (and avoids surfacing bg state they never opted into).
	if !spawner.IsBackground() {
		return nil
	}
	h, err := harnessForTask(t)
	if err != nil {
		return nil
	}
	bg := h.Background()
	if bg == nil {
		return nil
	}
	agents, err := bg.BackgroundAgents()
	if err != nil {
		return nil
	}
	for i := range agents {
		if strings.EqualFold(agents[i].SessionID, t.SessionID.String) {
			return &agents[i]
		}
	}
	return nil
}
