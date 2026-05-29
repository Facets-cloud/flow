# Persistent, origin-driven task monitor with agent auto-recreation

**Date:** 2026-05-29
**Status:** Approved (brainstorming) → implementing

## Problem

Flow's inbox monitor today is **session-attach-driven and in-memory only**:

- A monitor (`internal/monitor/inbox_monitor.go`) tails a task's `inbox.jsonl`
  every 2s and, on new actionable events, injects a bracketed-paste wake
  prompt into the **live** agent PTY (`terminal_bridge.go` `wakeTask` →
  `terminalSession.write`). This is provider-agnostic.
- It is started only when a terminal is attached (`terminal_bridge.go:173`) and
  stopped when the process exits. The registry is an in-memory map
  (`inbox_monitor_manager.go`).
- Nothing is persisted; **server boot restores no monitors**. The liveness
  reconciler (`liveness.go`) marks dead sessions but never stops/recreates
  monitors or sessions.

External events keep flowing into `inbox.jsonl` regardless (Slack Socket Mode +
GitHub poller, boot singletons). The gap: when a task's agent is gone, those
events pile up unread — nothing wakes or respawns an agent — and a server
restart drops every monitor.

The monitor is **external to the agent on purpose**: Claude Code's native
background sessions and Codex's experimental background mechanisms die with the
agent, so they can't satisfy "if it stopped, recreate it". Flow's monitor lives
in the flow server, above the agent, and detects liveness via the OS process
table (`ps`) — not the agents' internal `/background`/`/ps`.

## Goal

Tasks that originate from Slack or GitHub, or that have a linked branch/PR,
get a **persistent** background monitor that:
- survives server restarts (re-derived on boot),
- recreates its own goroutine if it dies,
- and when a new actionable event arrives but no agent session is live,
  **respawns the agent** (Claude or Codex) — debounced — so the work gets
  picked up autonomously.

## Decisions (from brainstorming)

| Decision | Choice |
| --- | --- |
| Recreate behavior | **Auto-respawn the agent** when a monitored task has new actionable events and no live session; the fresh session bootstraps from `inbox.jsonl`. Debounced, active-only. |
| Needs-monitor rule | Slack-origin OR GitHub-origin (PR/issue), **AND** status ∈ {backlog, in-progress}. Off on done/archived. A bare worktree/branch is NOT a trigger — only a raised PR (`gh-pr:` tag) is. |
| Persistence | **Derive on boot from tags + status** (no schema change); reconcile loop keeps the live monitor set converged. |
| Branch/PR association (§6) | **v1**: monitoring keys off the `gh-pr:*` tag (the GitHub poller tracks it); a bare branch with no PR is not monitored. Auto-tagging a branch's PR on `flow done` is a follow-up. |
| Respawn debounce | **60s** minimum between respawns per task. |

## Architecture

Three units, each independently testable.

### 1. `taskNeedsMonitor(task, tags)` — pure predicate

```
taskNeedsMonitor(t, tags) =
    t.status ∈ {"backlog", "in-progress"}
    AND not archived AND not deleted
    AND anyMatch(tags, "slack-reply", "slack-thread:*", "gh-pr:*", "gh-issue:*")
```

The trigger is an **external event source**, not a git branch. A bare
worktree/branch is deliberately NOT a trigger — a branch with no PR has nothing
feeding its inbox. "We updated *the PR*" surfaces as a `gh-pr:` tag
(created/tracked by the GitHub poller), which is what flips this on. A plain
`slack` label is a manual tag, not an origin marker — only
`slack-reply`/`slack-thread:` count.

Lives in `internal/server` (e.g. `task_monitor.go`). Pure function over a
`flowdb.Task` + its tags — no I/O — so it is trivially table-tested.

### 2. Monitor reconcile loop — persistence + self-healing

A small reconciler (`monitorReconciler`, sibling of `livenessReconciler`),
started in `Server.ListenAndServe`, ticking on boot and every **30s**:

1. `flowdb.ListTasks(db, {IncludeArchived:false})`; load each task's tags.
2. Build `desired = { slug : taskNeedsMonitor(t, tags) }`.
3. For `slug` desired but not running → `inboxMonitors.start(slug)`.
4. For a running monitor whose slug is not desired → `inboxMonitors.stop(slug)`.

This restores monitors after a restart, recreates any goroutine that died, and
tears them down when a task finishes. The existing attach-time `start()` stays
for instant responsiveness; reconcile is the convergent source of truth.

`inboxMonitorManager` gains `running(slug) bool` and `runningSlugs() []string`
(or a `reconcile(desired)` method) plus a mutex-guarded read of its `cancel`
map.

### 3. Deliver-or-respawn — the agent recreation

The monitor's wake target (`inboxWakeTarget`, today holding `terminals`) is
widened to hold the `*Server` and implement `WakeTask(ctx, slug, entries)` as:

```
isHubLive  = terminals.running(slug)              // flow-managed PTY exists
isProcLive = task.SessionID ∈ cachedLiveAgentSessions()  // native/external agent

if isHubLive:
    inject bracketed-paste wake prompt into the PTY   // today's behavior
elif isProcLive:
    // a native (user-owned terminal) session is alive; flow has no PTY to
    // inject into and must not spawn a duplicate. Leave events unread +
    // (optional) notify. Do NOT respawn.
    return
else:
    // truly dead → recreate the agent
    if !taskActive(slug):            return   // status changed under us
    if !providerAvailable(provider): log + return
    if now - lastRespawn[slug] < 60s: return  // debounce
    lastRespawn[slug] = now
    openBrowserTerminalBridge(slug, "")        // stored provider; bootstraps inbox
    advance inbox.monitor.cursor to EOF        // fresh agent owns the backlog
```

`lastRespawn` is an in-memory `map[string]time.Time` guarded by a mutex (reset
on restart is acceptable — at most one extra respawn just after boot).

### 4. UI indicator — "monitor running"

The monitor-running state lives in `inboxMonitorManager` (the in-memory
`running(slug)` set). Surface it where the user already looks:

- `InboxFeedEntry` (left list) and `InboxConversation` (right pane) gain a
  `Monitored bool`, set from `s.inboxMonitors.running(slug)` in `handleInbox`
  / `handleInboxConversation`.
- Inbox UI: a small **"monitoring" pill/dot** on the conversation row and in
  the detail header (e.g. a pulsing radar/eye icon, distinct from the green
  "live session" dot — a task can be monitored without a live session).

This makes the persistent monitor visible: you can tell at a glance which
conversations flow is watching in the background, even when no agent is live.

### Provider-agnostic

- Wake = bracketed-paste PTY write — already provider-neutral.
- Respawn uses the task's stored `session_provider` (claude/codex) via
  `openBrowserTerminalBridge(slug, "")`.
- Codex liveness uses the existing "any codex process alive" heuristic.

## Data flow

```
inbox monitor (per needs-monitor task, persistent)
   │ tails inbox.jsonl (cursor)
   ▼ new actionable events
DeliverEvents
   ├─ hub PTY live  → inject wake prompt (bracketed paste + \r)
   ├─ native alive  → leave unread (no duplicate spawn)
   └─ dead          → respawn agent (debounced) → bootstraps inbox.jsonl
```

## Persistence model

- No DB schema change. `taskNeedsMonitor` is re-derived from tags+status every
  boot and every reconcile tick.
- `inbox.monitor.cursor` already persists per task.
- `lastRespawn` debounce map is in-memory only.

## Error handling

- Reconcile: a per-task error (tag lookup, etc.) is logged and skipped; the
  loop continues for other tasks (don't let one bad task stall reconciliation).
- Respawn: provider-unavailable or bridge error → log, leave events unread,
  retry on the next event (debounce still applies).
- Monitor goroutine crash → next reconcile tick restarts it.

## Testing

- **`taskNeedsMonitor`** — table tests: each qualifying tag, worktree set,
  each status (backlog/in-progress/done), archived/deleted → expected bool.
- **Reconcile** — fake monitor manager recording start/stop; seed tasks with
  varied tags/status; assert the running set converges to the needs-monitor
  set across ticks (including stop when a task goes done).
- **Deliver-or-respawn** — fakes for liveness + spawn:
  - hub live → wake called, no respawn;
  - native alive → neither respawn nor duplicate;
  - dead + active + provider available → respawn once;
  - second event within 60s → suppressed (debounce);
  - provider unavailable / task done → no respawn.

## Files to touch

- `internal/server/task_monitor.go` (new) — `taskNeedsMonitor`, `monitorReconciler`.
- `internal/server/inbox_monitor_manager.go` — `running`/`runningSlugs`/reconcile helpers.
- `internal/server/inbox_wake.go` — widen `inboxWakeTarget` to deliver-or-respawn.
- `internal/server/server.go` — start the monitor reconciler in `ListenAndServe`.
- Tests alongside the above.
- `README.md` — document the persistent monitor + auto-respawn behavior.

## Out of scope (YAGNI / follow-up)

- Auto-tagging a branch's PR via `gh pr view` (the §6 stretch).
- Stopping the monitor on PR merged/closed specifically (status-based stop
  covers the common case; a merged PR that reopens the task to backlog is
  still legitimately monitored).
- A per-task manual monitor on/off toggle.
- Respawn for native (user-terminal) sessions.
