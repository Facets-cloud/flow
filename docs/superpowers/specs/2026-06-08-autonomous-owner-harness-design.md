# Autonomous Owner Harness — Design

**Status:** Design (not yet build)
**Date:** 2026-06-08
**Task:** `auto-loop-harness` · Project: `flow`
**Author:** design brainstorm (Anshul + Claude)
**Build approach:** MVP-first — see §10 for the v0 slice (no Slack, no
notifications, no stats, no smart bootstrap).

---

## 1. Problem

flow today can start work autonomously but cannot *keep* a responsibility alive
over time:

- `flow do --auto` runs a task **once**, headlessly, and self-`flow done`s. One
  shot, then it stops.
- **Playbooks** are reusable procedures, but each run is manually (or
  externally, via launchd) triggered and is itself one-shot.

The gap: nothing **takes durable responsibility for an outcome** — re-waking,
re-evaluating the world, re-acting, creating follow-on work, asking the human
when it must, and continuing until the outcome holds. Today a "responsibility"
lives only in the user's head and in scattered task rows.

The thesis (borrowed, and it fits exactly):

> "You're not supposed to prompt Claude. You're supposed to build a system that
> prompts itself." — Daisy Hollman (Anthropic)

We want flow to host **self-prompting systems** that own an outcome: *create
tasks, trackers, callbacks, and playbooks to accomplish a goal — except the
owner is not a single Claude session.*

### Motivating use cases (the requirements, in the user's words)

1. **"Make sure all PRs raised have CI green."** A standing invariant over a
   *set* (every open PR in a repo). Membership changes over time; never "done."
2. **"Raise PR, answer/fix the PR review by humans/CodeRabbit, get it approved &
   merged, deploy and monitor (via Facets/other tools) the fix, and file new
   bugs when a new exception is encountered."** A multi-step responsibility that
   spawns new work (a discovered exception → a new bug → a new fix).

These are the two species the design must express with one mechanism.

---

## 2. Core concept: the `owner`

An **owner** is a durable, named, repo/goal-scoped **self-prompting
controller** — *not* a long-running Claude chat. It is **state + a clock**:

- **state** — a charter (its operating manual) and a ledger (what it's doing and
  has done)
- **a clock** — a self-paced *callback* that re-invokes the owner on a time
  interval

Each time the callback fires, the owner runs one **tick**: a *fresh* headless
Claude run that reads its durable state, observes the world, acts, schedules its
next wake, and exits. The owner persists across ticks; the sessions are
disposable workers it summons. This is precisely "a system that prompts itself,"
and it is explicitly **multi-session**.

> **Naming:** chosen as `owner` for the simplest reading of "the thing that takes
> responsibility." (`agent` was rejected as overloaded by Claude
> subagents / the `agent-factory` repo.)

### What an owner owns

```
┌──────────────────────────────────────────────────────────────────────┐
│ OWNER  "agent-factory-maintainer"                                      │
│   owns: maintenance + bug-fixes for Facets-cloud/agent-factory         │
│   charter: how to build/test/deploy/health-check, approval gates,      │
│            escalation rules, notify channel                            │
│   ledger: in-flight work units + their stage + running stats           │
│        │ each tick (every N): observe → act → set next wake → exit      │
│        ▼                                                                │
│   work units (flow tasks tagged owner:<slug>) ── "trackers"           │
│     #482 (in review)   #485 (CI red)   #491 (deploying)                │
│        │ a tick can spawn new units: exception found → flow add task    │
│        ▼                                                                │
│   open questions parked on the human (question-tagged tasks) → Slack ping│
└──────────────────────────────────────────────────────────────────────┘
```

### v1 simplification — single loop per owner

The **owner is the only looping thing.** Its single self-paced tick advances
*all* of its in-flight work units each wake. Work units are tracked as task rows
("trackers") but do **not** independently self-wake. No loops-within-loops.

Rationale: avoids nested-scheduler complexity, one wake-source per owner, trivial
to reason about. Per-unit independent loops (real parallelism across many
in-flight units) are a deliberate later extension, not v1.

### How the two use cases map onto one owner

```
"all PRs green"  (standing invariant)
  tick: gh pr list --state open
        for each PR with red CI → ensure a work-unit task exists & advance it
        set next wake (+30m)
  predicate is ~never permanently true (new PRs appear) → behaves standing

"raise→review→merge→deploy→monitor→file bug"  (multi-step responsibility)
  tick 1: raise PR        → park on CodeRabbit/CI       → waiting_on, sleep
  tick 2: answer review   → re-request review           → sleep
  tick 3: CI green+approved→ (needs human approve?) ask  → park, Slack, sleep
  tick 4: merge → deploy  → park on deploy               → sleep
  tick 5: monitor (facets)→ exception found → flow add task "fix exc Y" (new unit)
        outcome held (deployed + stable N hrs) → close that unit
```

Both are the *same* owner mechanism with different charters. (This is the
earlier "termination is just a predicate" unification: a bounded goal is the
special case where the predicate eventually stays true.)

---

## 3. Stateful systems

The heart of the new layer is **what persists between ticks**. Six systems:

| # | System | Stored as | Purpose |
|---|---|---|---|
| 1 | **Owner registry** | `owners` table | identity, scope, schedule, next callback, lifecycle |
| 2 | **Charter** | `owners/<slug>/charter.md` (+ structured fields) | operating knowledge: build/test/deploy/health-check, approval gates, escalation rules, notify channel. **Grows** as runtime answers arrive |
| 3 | **Ledger** | **task rows tagged `owner:<slug>`** + a summary in `ledger.md` | in-flight work units, their stage, and the running stats the user wants to see |
| 4 | **Human questions** | **question-tasks** — regular flow tasks tagged `question` + `owner:<slug>`, assigned to you, *not* run with `--auto` | open questions parked on the human, surfaced in your normal `flow list tasks` queue; answered in-context on the task |
| 5 | **Callback / scheduler** | `owners.next_wake_at` + a runner | the time-based trigger that fires the next tick |
| 6 | **Tick log** | `owners/<slug>/ticks/<ts>.log` files | append-only audit of what each tick observed / decided / did |

### On-disk layout (mirrors tasks/projects)

```
~/.flow/owners/<slug>/
  charter.md          # operating manual (grows over time)
  ledger.md           # human-readable rollup of in-flight work + stats
  updates/            # progress notes (same convention as tasks/projects)
  ticks/<ts>.log      # per-tick audit log (mirrors tasks/<slug>/auto-runs/)
```

### Schema addition (SQLite, additive + nullable — same migration discipline as PR #58)

The **only** new table is `owners`. Owner↔task linkage and the question marker
reuse flow's existing **tag** system, so there are **no new task columns and no
new `kind` value**.

```
owners (
  slug TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  work_dir TEXT NOT NULL,
  project_slug TEXT,                 -- nullable FK to projects
  status TEXT NOT NULL,              -- active | paused | retired
  every TEXT NOT NULL,               -- interval, e.g. "30m" (time-based v1)
  next_wake_at TEXT,                 -- RFC3339; the callback
  last_tick_at TEXT,
  last_tick_status TEXT,             -- ok | escalated | error
  harness TEXT,                      -- which harness runs its ticks
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

-- No new task columns, no new kind. Linkage + marking reuse the EXISTING tag
-- system (free-form key:value tags, already in flow — see skill §4.16a):
--   owner:<slug>   tag → links a task (work unit OR question) to its owner
--   question       tag → marks a human-answered task (never run with --auto)
```

**Questions need no new table and no new column** — they are tasks tagged
`question` + `owner:<slug>` (system #4 above). The owner creates the tagged task,
the human answers it in-context and marks it done, and the owner reads the answer
back on its next tick. (See §5.2.) Caveat: tags are a string convention, not an
FK — see §11 for the dangling-tag / behavioral-gate trade-off.

Tick history can stay as log files (system #6) rather than a table, mirroring how
`--auto` already logs to `tasks/<slug>/auto-runs/`.

---

## 4. The tick — what each callback runs

A tick **reuses the existing `--auto` machinery**: a detached, headless Claude
run launched via the harness `AutoRunArgv` shape. Unlike `--auto`, the tick is a
*fresh* session each time (the "not a single session" requirement) — its memory
is the durable ledger, not a resumed transcript.

The tick prompt (sketch):

```
You are owner <slug>. Your charter is owners/<slug>/charter.md; your ledger is
owners/<slug>/ledger.md; your work units are the tasks tagged owner:<slug>.

1. Read charter + ledger + owned task rows.
2. Observe the world per the charter (gh PRs/CI, praxis/facets health, etc.).
3. Decide & act:
     - advance or create work-unit tasks (flow add task --owner <slug>)
     - file bugs for new exceptions (= new work units)
     - reply to reviews, request human approval where the charter requires it
4. If blocked on a human decision/fact → enqueue an owner_question, park that
   work unit (waiting_on), notify via the charter's channel (Slack).
5. Update ledger + stats. Set next_wake_at. EXIT.
```

The hard-blocker / persist-until-closeable discipline from `--auto` carries over:
exhaust avenues before escalating; never spin uselessly; on a true blocker,
enqueue a question and sleep rather than loop.

---

## 5. Human interaction — how the owner asks questions

The owner is headless, so "ask the user" can never be a blocking prompt. Two
channels:

### 5.1 Setup-time — a guided interview (human present)

`flow add owner` runs an intake like flow's task-intake, but **operational**:
where's the repo, how to build/test, how to deploy, how to check health, what
counts as "broken," what must the human approve vs. what can it just do, where to
notify. Output = the **charter**.

Critically, **bootstrap from what flow already knows; ask only the gaps**:

- repo path → workdir registry + `gh`
- build/test/conventions → the repo's `CLAUDE.md`
- deploy timing, CI checks, branch protection → already in the user's **KB**
  (`processes.md` holds these for real repos today)
- health checks / deployment status → praxis/facets tooling the user already uses

Health-check command and approval/escalation rules are usually the only genuine
gaps, keeping setup short.

### 5.2 Runtime — questions are *tasks* for the human

A runtime question is **not** a new queue and is **not** an autonomous run. The
owner simply **creates a normal flow task for the human** — tagged `question` +
`owner:<slug>`, assigned to you, with the full context + the question in its
brief — and parks the dependent work unit on it. It is explicitly **not** run
with `--auto`: it's a human task that surfaces in your ordinary `flow list tasks`
queue.

```
  tick: hits unknown ──► flow add task --tag question --tag owner:<slug> --assignee you
                                  │  + park the work unit: waiting_on = that task
                                  ▼
                         MVP: NO notification — it just appears in your
                         `flow list tasks` queue. (Slack ping is a later add.)
                                  │
   ...you answer in-context, on your own time:
        - read it in `flow show task`, OR `flow do` it for a real back-and-forth
        - capture the answer on the task (an update / the session), mark it done
                                  ▼
  next tick: owner sees its question-task is done ──► reads the answer
                         ├──► applies it, unparks the work unit
                         └──► PERSISTS it into the charter (never asks again)
```

Why this beats a bespoke queue: it reuses tasks wholesale, the answer is captured
**in the context of that task** (durable, re-readable), and questions land in the
*same* inbox you already triage — no separate surface to check. `waiting_on`
already models "parked on you," and the owner just watches its own
`question`-tagged tasks for completion. (In the MVP you simply see the question in
your task queue; a Slack ping is a later add — see §10.)

**Consequence — the owner gets quieter over time.** Cold-start it asks a lot;
every answer becomes durable charter knowledge, so by week two it barely asks.
The "setup needs lots of answers" concern is front-loaded, partly pre-filled, and
self-extinguishing.

---

## 6. The accountability surface

This is the user-facing payoff: *an owner is responsible / here's what it owns /
here are its working stats.* `flow owner show <slug>`:

```
  OWNER  agent-factory-maintainer     owns: maintenance+bugfix for Facets-cloud/agent-factory
  ──────────────────────────────────────────────────────────────────────
  in flight   3 fixes:  #482 (review)  #485 (CI red)  #491 (deploying)
  this week   7 bugs filed · 5 PRs merged · 4 deploys verified
  parked on   you: 1 PR awaiting your approval (#482)
  escalated   1: cannot reproduce exception in #488 — needs your call
  next tick   in 22m   ·   last acted 8m ago   ·   health: ok
```

**MVP note:** v0 renders this as a plain *live list* — the owner's `owner:<slug>`
tasks + its open `question` tasks + next tick — *without* the computed weekly
stats (`7 bugs filed · 5 PRs merged …`). Those counts are a "Next" nicety (§10),
not part of the MVP.

---

## 7. New flow commands

```
Create + setup
  flow add owner "<name>" --work-dir <p> [--project <s>] [--slug <s>] [--every <dur>]
       → runs the setup interview (charter authoring), bootstrapping from
         CLAUDE.md + KB + workdir registry + gh; asks only the gaps

Lifecycle
  flow owner start  <slug>     begin ticking (sets next_wake_at = now)
  flow owner pause  <slug>     stop ticking, keep all state
  flow owner retire <slug>     archive (state preserved, like task archive)
  flow owner edit   <slug>     edit charter.md

Observe (accountability surface)
  flow owner list  (or flow list owners)   all owners: status, owns, next tick
  flow owner show  <slug>                   identity + owns + stats + parked + escalations + next tick
  flow owner log   <slug>                   recent tick logs / activity

Human Q&A (async) — questions are tagged tasks; reuses existing commands
  flow owner questions [<slug>]   convenience filter = flow list tasks --tag owner:<slug> --tag question
  (to answer: just answer the question-task — add a note or `flow do` it, then mark it done;
   the owner reads the answer on its next tick. No special 'answer' command, no new queue.)

Internal (not user-facing)
  flow __owner-tick <slug>     the detached tick supervisor (≈ flow __auto-exec)
  flow owner tick-due          scheduler entry: scan owners, spawn due ticks
```

Work-unit linkage: spawned tasks are tagged `owner:<slug>` (the tick passes
`--tag owner:<slug>`), so the ledger is just `flow list tasks --tag owner:<slug>`.
An optional `--owner <slug>` sugar flag on `flow add task` could expand to that
tag, but no new column is required.

---

## 8. The trigger substrate (time-based, v1) — and the architecture decision

Each owner has an interval (`--every 30m`). Something durable must, at
`next_wake_at`, re-spawn the tick after the previous one exits. **A live process
is never held across the interval** — every tick exits and hands a future
wake-time to the substrate. Three candidate substrates, which also decide the
in-flow-vs-outside-flow boundary:

| Substrate | Boundary | Mechanism | Trade-off |
|---|---|---|---|
| **launchd tick-scanner** *(recommended v1)* | mostly in-flow, reuses external OS timer | one launchd timer (or 1-min scanner) calls `flow owner tick-due`; it finds owners with `next_wake_at <= now` and detaches `flow __owner-tick <slug>` per the `--auto` pattern | reuses proven scheduled-playbook infra; survives reboot; **no new daemon**. macOS-only; ~1-min granularity |
| **flow daemon (`flow ownerd`)** | fully in-flow | a long-running flow process holds timers, spawns ticks, is event-ready for the future | cross-platform & self-contained, but a real daemon to install/supervise — heaviest new surface; *it* still needs launchd to survive reboot |
| **self-rescheduling one-shot** | in-between | each tick schedules exactly one future wake of itself before exiting | no central component, but rests on fragile primitives (`at`/detached `sleep`) that don't survive reboot well |

**Recommendation:** launchd tick-scanner for v1 — lowest new surface, reboot-safe,
reuses what scheduled playbooks already do. Revisit a daemon only if/when
event-based triggers (CI webhooks, etc.) are added (explicitly out of scope for
v1, where triggers are time-based only, like `/loop`).

### Approaches weighed (the in-flow vs outside-flow decision)

- **A — in-flow native `owner` primitive (RECOMMENDED).** flow grows the `owner`
  entity + `flow owner *` commands, reusing tasks/playbooks/`--auto`/sessions/
  `waiting_on`/Slack/launchd. A local, single-binary, laptop-native
  responsibility layer.
  *Pros:* maximal reuse, one tool, one DB, owner state lives next to the work it
  drives, smallest mental model. *Cons:* flow grows a meaningfully new surface
  (a scheduler concept and a long-lived entity) it didn't have before.

- **B — outside-flow supervisor layer.** A separate orchestrator owns
  charters/ledgers/scheduling and drives flow purely as an execution backend
  (`flow add task` + `flow do --auto`).
  *Pros:* keeps flow a clean execution substrate; the "responsibility brain"
  evolves independently. *Cons:* two tools, two stores, duplicated identity/state,
  and the ledger↔task linkage spans a process boundary; loses the reuse that
  makes A cheap.

- **C — standalone tool.** A new binary that reimplements task/session
  management for owners.
  *Cons:* throws away flow's entire execution substrate; rejected.

**Decision: A (in-flow native `owner`).** Every capability the owner needs
already exists in flow except the owner entity itself, its callback/scheduler,
and the question queue — so the cheapest, most coherent home is inside flow. B is
the fallback if the owner layer later needs to outgrow a personal CLI.

---

## 9. Reused vs. genuinely new

| Reused as-is | New |
|---|---|
| tasks + the **tag system** (= trackers/work units **and** human questions, via `owner:<slug>` + `question` tags), `waiting_on` (= parked-on-you), `assignee`, playbooks (= reusable pipelines), `flow do --auto` machinery (= the tick), sessions, harness abstraction, Slack (= optional notify), KB + CLAUDE.md (= charter bootstrap), launchd (= scheduler), the additive-nullable migration discipline | `owners` table + on-disk dir (**the only schema change**), charter, ledger/stats rollup, the `owner:<slug>` / `question` tag conventions, the `flow owner *` commands, the tick prompt, `flow __owner-tick` + `flow owner tick-due` |

---

## 10. Scope & build order — MVP first

Build the thinnest end-to-end loop first; layer niceties only after it works.

### MVP (v0) — the thinnest slice that takes responsibility

The smallest thing that wakes itself, acts, and asks the human when stuck —
**no Slack, no notifications, no smart bootstrap, no stats:**

- `owners` table + `owners/<slug>/charter.md` (free-form, **authored by hand**).
- `flow add owner "<name>" --work-dir <p> --every <dur>`, `flow owner start|pause`,
  and `flow owner show` (a **live view**: charter + its `owner:<slug>` tasks +
  open `question` tasks + next tick — *no* stats rollup).
- The tick: `flow __owner-tick <slug>` — a fresh headless run (reuses `--auto`)
  that reads the charter + its tagged tasks, acts (create/advance tasks tagged
  `owner:<slug>`), sets `next_wake_at`, exits. Single loop per owner.
- Scheduler: launchd tick-scanner → `flow owner tick-due`.
- Questions: the tick creates a task tagged `question` + `owner:<slug>`, assigned
  to you, and parks the work unit (`waiting_on`). **No notification of any kind** —
  you find it in your normal `flow list tasks` queue (and in `flow owner show`),
  answer it in-context, mark it done; the owner reads it on its next tick.

That is a complete responsibility loop with zero new surfaces beyond the `owners`
table, the tick, and the scheduler.

### Next (only after the MVP proves out)

- **Notifications** — a Slack ping when a question is created (Slack is already
  wired); later terminal/push. *(Explicitly NOT in the MVP.)*
- **Gaps-only setup bootstrap** — read CLAUDE.md + KB + workdir registry + `gh`
  so setup asks only what's missing (MVP: hand-write the charter).
- **Stats / generated `ledger.md`** — the weekly rollup shown in the §6 mock.
- **Lifecycle niceties** — `flow owner retire` (archive), `flow owner edit`,
  `flow owner log`, `flow owner questions` sugar.

### Later / out of scope

- Event-based triggers (CI webhooks, PR-comment hooks, prod-exception streams) —
  the substrate allows them later (they only move `next_wake_at` earlier).
- Per-work-unit independent loops / cross-unit parallel scheduling.
- A `flow ownerd` daemon (kept as a documented alternative).
- Non-claude harness support for owners (claude-only first, like `--auto`).
- Cross-machine / server-hosted owners.

---

## 11. Open questions

- **Runtime notify channel default** — Slack assumed; confirm vs. terminal/push/a
  review inbox.
- **Ledger source of truth** — derive stats purely from tagged task rows, or also
  let the tick maintain a hand-written `ledger.md` summary? (Leaning: tasks are
  the source; `ledger.md` is a generated human view.)
- **Answer read-back convention** — settled approach: the human marks the
  `question`-tagged task done and the owner reads the answer from its
  updates/transcript on the next tick. Open: is a lighter "answered" signal
  wanted (e.g. a note without full done), and does the answer need a designated
  field vs. free-form (the tick is a Claude run, so free-form is readable)?
- **Tags vs. structural column/kind** — linkage (`owner:<slug>`) and the question
  marker (`question`) are tags, not an FK column or a `kind` value. Trade-off:
  zero schema churn and free `flow list --tag` queries, but tags are string
  conventions — they can dangle if an owner is renamed/retired, and "never
  `--auto` a question-task" is a convention rather than a structural gate.
  Acceptable for v1 (nothing auto-runs tasks unasked); revisit if owners get
  renamed often or a hard gate is needed.
- **Charter format** — free-form markdown (agent uses judgment, flow-native) vs.
  some structured fields for approval-gates/health-checks. Leaning free-form +
  a few structured fields (`every`, `notify_channel`, `approval_required`).
- **Naming of sub-nouns** — "work unit" vs. "tracker" vs. just "task with an
  owner."

---

## 12. Done-when (from the task brief) — status

- [x] A written design doc capturing the harness architecture, the loop
  lifecycle, and the in-flow vs. outside-flow boundary decision. *(this doc)*
- [x] 2–3 approaches weighed with a recommendation. *(§8 — A/B/C + the substrate
  table, recommending in-flow native `owner` on a launchd tick-scanner)*
