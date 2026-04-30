# Playbooks, intake-minimal, scope-check fix, flowde removal, aux files

Date: 2026-04-30
Status: Draft — pending user review
Scope: five independent changes bundled because they all land in `~/flow`

## Overview

Five changes:

1. **Playbook** — new third entity for reusable runnable definitions with
   per-invocation history. Each invocation is a task (kind=`playbook_run`)
   so all task machinery (sessions, transcripts, briefs, updates) is reused.
   Playbook definitions live as `brief.md` files (same filename used by
   tasks and projects — uniform tooling, no new file convention).
2. **Intake-minimal** — skill-only change. Task intake captures one-liner
   + slug + work_dir + priority first. Detailed brief is deferred to task
   start (or never, if user doesn't want to write it).
3. **Substantive-unrelated-work check** — skill-only change. Convert the
   one-shot SessionStart instruction into an ongoing skill behavior that
   fires mid-conversation when unrelated substantive work emerges.
4. **Remove flowde** — drop the `flowde` wrapper. `flow do` invokes
   `claude` directly. Skill freshness becomes a manual `flow skill update`
   step until we design a real upgrade story.
5. **Auxiliary markdown files** — `flow show task|project|playbook`
   enumerates any `.md` files in the entity's directory beyond `brief.md`
   and `updates/`, and the bootstrap prompts tell spawned sessions to
   read those too. Lets users drop sidecar context (`research.md`,
   `design.md`, transcripts of prior conversations, etc.) into a task
   dir and have Claude pick it up automatically.

These ship as one logical change because they all live in `~/flow` and
interlock (the skill changes reference playbook commands; the flowde
removal touches the same `do.go` that playbook runs reuse).

---

## 1. Playbook

### Concept

A **playbook** is a reusable, runnable definition. Defined once, invoked
many times. Each invocation is fresh.

In the existing model:
- **Project** = grouping (durable context, optional brief)
- **Task** = one piece of work, one start → one done, one session
- **Playbook (new)** = a function. Defined once, called N times, each call
  independent.

### What it captures that nothing else does

The thing flow is uniquely good at — captured context delivered automatically
to a session — is wasted on `/schedule` today. A playbook delivers that
captured context to repeated, manually-invoked runs:

- **Instructions** — the literal steps to perform each run
- **Inherited context** — kb files, optionally a parent project's brief,
  optionally a `work_dir`'s CLAUDE.md
- **Run log** — a dated task per invocation with its own brief, updates,
  transcript

### What this is NOT

- Not a workflow engine (no DAG, retries, steps)
- Not scheduled (no cron, no trigger; manual `flow run playbook` only)
- Not a templating system (instructions are static prose; no per-invocation
  parameters)
- Not a replacement for skills (see comparison below)

### Playbooks vs skills (orthogonal)

| | Skill | Playbook |
|---|---|---|
| Lifecycle | Loaded *into* running session | *Launches* new session |
| Selection | Auto-triggered by Claude on description match | User explicitly invokes |
| Context | Inherits parent session's running context | Inherits flow's captured context |
| Audit | None | Run log per invocation, transcript |
| Mental model | Procedure applied mid-task | Job that gets its own session |

A playbook session can invoke skills mid-run; the reverse is rarer but
mechanically possible (a skill can call `flow run playbook` via Bash).

### Schema

**New `playbooks` table:**

```sql
CREATE TABLE IF NOT EXISTS playbooks (
    slug          TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    project_slug  TEXT REFERENCES projects(slug),
    work_dir      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    archived_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_playbooks_project ON playbooks(project_slug);
```

**Extend `tasks` table** (via `runMigrations`):

```sql
ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'regular'
  CHECK (kind IN ('regular','playbook_run'));
ALTER TABLE tasks ADD COLUMN playbook_slug TEXT REFERENCES playbooks(slug);

CREATE INDEX IF NOT EXISTS idx_tasks_kind          ON tasks(kind);
CREATE INDEX IF NOT EXISTS idx_tasks_playbook_slug ON tasks(playbook_slug);
```

`kind='regular'` is the default for backward compatibility — existing
tasks become regular tasks with no migration needed beyond schema.

### Filesystem layout

```
~/.flow/
  playbooks/<slug>/
    brief.md            # the playbook's prose definition (live, editable)
    updates/            # optional, for cross-invocation notes
  tasks/<run-slug>/
    brief.md            # snapshot of the playbook's brief.md at run time
    updates/            # per-invocation notes
```

A playbook directory is structurally identical to a project directory.
Per-invocation run-tasks sit in the existing `tasks/` directory and
look identical to regular task dirs. **All three entities (project,
task, playbook) use `brief.md` as the canonical filename** — uniform
tooling (`flow edit`, the Read tool, the snapshot copy) works without
special-casing.

### Run slug format

`<playbook-slug>--YYYY-MM-DD-HH-MM` — sortable, dated, scannable.

Example: a playbook with slug `triage-cs-inbox` invoked at 10:30 on
2026-04-30 produces a run task with slug
`triage-cs-inbox--2026-04-30-10-30`.

**Conventions:**

- Double-dash `--` separates the playbook slug from the timestamp so the
  boundary is visible despite both halves containing single dashes.
- Full year (`YYYY`), not two-digit. Future-proof and unambiguous.
- Default precision is minute (`HH-MM`). Seconds are not included unless
  needed for collision avoidance.

**Collision handling (cascading precision):**

1. Try `<playbook-slug>--YYYY-MM-DD-HH-MM`. If unique, use it.
2. If a row already exists with that slug, retry with seconds appended:
   `<playbook-slug>--YYYY-MM-DD-HH-MM-SS`. If unique, use it.
3. If seconds also collide (extremely rare — same playbook fired twice
   within one second), append `-N` starting at `-2`:
   `<playbook-slug>--YYYY-MM-DD-HH-MM-SS-2`, `-3`, etc.

The cascade keeps the common case short (no seconds) while still
guaranteeing uniqueness in adversarial timing.

### Snapshot semantics

At `flow run playbook <slug>` time, `~/.flow/playbooks/<slug>/brief.md`
is **copied verbatim** into the new run-task's `brief.md` at
`~/.flow/tasks/<run-slug>/brief.md`. Reproducibility wins: future edits
to the playbook's brief do not retroactively change past run briefs.
The run-task's session executes against the snapshot, not the live
playbook content.

The "playbook brief" and the "run brief" are the same shape and same
filename; they're the same content at the moment a run is created, and
diverge only if the playbook is edited afterwards.

### Brief shape (playbook `brief.md`)

Different sections from a task brief — no "Done when", since playbooks
are never done:

```markdown
# <name>

## What
<one sentence>

## Why
<short paragraph>

## Where
work_dir: <path>

## Each run does
- <step 1>
- <step 2>

## Out of scope
- <non-goal>

## Signals to watch for
- <signal 1>

---
*Run with `flow run playbook <slug>`. Each run gets its own session
and a snapshot of this brief at run time. Editing this file does not
retroactively change past runs.*
```

### Commands

**New:**

```
flow add playbook "<name>" [--slug <s>] [--project <slug>]
                           [--work-dir <path>] [--mkdir]
flow show playbook [<ref>]              # default: $FLOW_PLAYBOOK if set
flow list playbooks [--project <slug>] [--include-archived]
flow run playbook <slug>                # creates kind=playbook_run task,
                                        # copies instructions.md → brief.md,
                                        # then internally calls cmdDo
flow list runs [<playbook-slug>]
                [--status backlog|in-progress|done]
                [--include-archived]
```

**Existing commands extended to handle playbook refs:**

```
flow edit <playbook-ref>     # opens playbooks/<slug>/brief.md
flow archive <playbook-ref>
flow unarchive <playbook-ref>
```

**Existing commands unchanged but work on run-tasks for free:**

```
flow do <run-slug>           # resume a specific run's session
flow done <run-slug>
flow show task <run-slug>
flow transcript <run-slug>
flow archive <run-slug>
```

### `flow show playbook` output

Mirrors `flow show task` structure so the user (and Claude sessions) read
both the same way. Example:

```
slug:        triage-cs-inbox
name:        Triage CS inbox
project:     (floating)
work_dir:    /Users/rohit/code/cs-tools  [known]
created:     2026-04-30T10:00:00+05:30
updated:     2026-04-30T10:00:00+05:30
brief:       /Users/rohit/.flow/playbooks/triage-cs-inbox/brief.md
updates:     (none)
runs (last 5):
  triage-cs-inbox--2026-04-30-10-30   [DN]   18 min
  triage-cs-inbox--2026-04-29-09-15   [DN]   22 min
  triage-cs-inbox--2026-04-28-08-45   [DN]   15 min
kb:
  - /Users/rohit/.flow/kb/user.md
  - /Users/rohit/.flow/kb/org.md
  - /Users/rohit/.flow/kb/products.md
  - /Users/rohit/.flow/kb/processes.md
  - /Users/rohit/.flow/kb/business.md
```

Key contract: the `brief:` path is prominent so anyone (user or session)
can `Read` it directly. Path is the source of truth for the playbook
content; the show command does not inline brief content (consistent with
`flow show task` and `flow show project`).

### Default filtering

`flow list tasks` adds `kind='regular'` to its WHERE clause so playbook
runs do not pollute the personal-task view. Override with
`--kind playbook_run` or `--kind all` (new flags) if needed.

`flow list runs` is the explicit entry point for playbook-run listings.

### `flow run playbook <slug>` semantics

1. Load playbook by slug from `playbooks` table.
2. Generate run slug: `<playbook-slug>--YYYYMMDD-HHMM` (with collision
   suffix if needed).
3. Insert a new row in `tasks` with `kind='playbook_run'`,
   `playbook_slug=<slug>`, `status='backlog'`, `priority='medium'`,
   `work_dir` from playbook, `project_slug` from playbook (nullable).
4. Create `~/.flow/tasks/<run-slug>/` and copy
   `~/.flow/playbooks/<slug>/brief.md` to
   `~/.flow/tasks/<run-slug>/brief.md` verbatim.
5. Invoke the same logic as `cmdDo(<run-slug>)` — atomic status flip,
   bootstrap UUID, spawn iTerm tab. The bootstrap prompt distinguishes
   playbook runs (mentions "this is a playbook run for `<playbook-slug>`")
   so the spawned session knows its provenance.

### Bootstrap prompt for playbook runs

`buildBootstrapPrompt` branches on `kind`. For `kind='playbook_run'` it
emits a prompt structurally identical to the task variant — same numbered
steps, same "load context before doing anything" pattern — but with an
extra step to load the playbook's definition for context, and a sharper
"your brief is the authoritative instructions" line:

> "You are running playbook `<playbook-slug>` as run `<run-slug>`. Do
> ALL of the following in order before executing anything:
>
> 1. Invoke the flow skill via the Skill tool. This loads the operating
>    manual that governs how this session works.
> 2. Run: `flow show playbook <playbook-slug>`. This shows the playbook's
>    definition and recent runs — context only, not your instructions.
>    Note any files listed under `other:` — they're sidecar references
>    (research notes, decision trees, etc.) you can `Read` on demand if
>    they become relevant; do not eagerly load them.
> 3. Run: `flow show task`. Read the file at the `brief:` path AND every
>    file under `updates:`. Files listed under `other:` are references
>    for THIS run; load them on demand when relevant. **The brief is
>    your authoritative instructions** — it was snapshotted from the
>    playbook at the moment this run started. Execute against this, not
>    the live playbook brief.
> 4. If a project is listed on the task, run: `flow show project
>    <project-slug>`. Read its brief and every file under updates:.
>    Files under `other:` are references — load on demand.
> 5. Read CLAUDE.md in your work_dir.
> 6. Only then begin executing your brief."

The contrast with the task bootstrap is just step 2 (load playbook
definition for context) and the explicit "your brief is the
authoritative instructions" framing in step 3. Steps 4–5 are identical
to the task variant.

### Skill changes (for playbooks)

Playbooks need to be threaded through many sections of the skill, not
just a couple of new ones. The skill is the user-facing spec for how
Claude interacts with flow — every section that talks about projects
or tasks needs to acknowledge playbooks where relevant.

**Section-by-section changes to `internal/app/skill/SKILL.md`:**

- **§2 The model** — add a paragraph describing playbooks as the third
  entity:
  > "**Playbooks** are reusable, runnable definitions. A playbook has a
  > name, slug, work_dir, optional `project_slug`, and a `brief.md` that
  > describes what each invocation should do. Each invocation creates a
  > **playbook-run** — a task with `kind=playbook_run` — that has its
  > own session, its own snapshotted `brief.md`, and its own
  > `updates/`. Editing a playbook's `brief.md` does not affect past
  > runs; runs are reproducible."

- **§4 Command reference** — add the new commands (`flow add playbook`,
  `flow run playbook`, `flow list playbooks`, `flow show playbook`,
  `flow list runs`). Note that `flow do`, `flow done`, `flow transcript`,
  `flow archive`/`unarchive`, `flow priority`, `flow waiting` all work
  on run slugs because runs are tasks.

- **§5.1 Start the day** — clarify behavior: the standard summary still
  filters `kind='regular'` (so playbook runs don't pollute the in-flight
  list). But surface a small "Active playbooks" subsection if any
  playbook has had a run in the past 7 days, listing the playbook and
  the date of its most recent run.

- **§5.2 Add a task interview** — keep the existing task interview, but
  add a sentinel at the top: if the user's request is "add a playbook"
  (or "track this as a playbook" / "this is something I'll re-run"),
  branch to §5.12 instead.

- **§5.5 Save a progress note** — clarify two cases:
  - For a **playbook run**, notes go under
    `~/.flow/tasks/<run-slug>/updates/` (same as any task — runs are
    tasks).
  - For a **playbook definition**, notes go under
    `~/.flow/playbooks/<slug>/updates/` and capture cross-invocation
    observations ("noticed this playbook produces flaky output when X",
    "next iteration should consolidate steps 2 and 3").

- **§5.7 Mark done** — clarify:
  - Run-tasks support `flow done <run-slug>` like any task.
  - Playbook **definitions** are never "done" — they're archived
    (`flow archive <playbook-slug>`) when no longer in use. There is
    no `flow done playbook`.

- **§5.8 Archive / cleanup** — extend to playbooks:
  - `flow archive <playbook-slug>` hides the playbook from
    `flow list playbooks` but does not affect past runs (independent
    rows in `tasks`).
  - For run-tasks, prefer `flow archive <run-slug>` over leaving
    completed runs in the listing.
  - "Bulk clean up done runs" pattern: `flow list runs --status done`
    + archive each.

- **§5.9 Weekly review** — add a section to the digest:
  > "## Playbook activity
  > - `<playbook-slug>` — N runs this week, most recent <date>"
  After "Workdir hygiene." Pulls from `flow list runs --since monday`
  grouped by playbook.

- **§5.11 Scope-creep detection** — applies inside playbook-run sessions
  too. Triggers and recipe identical to a regular task session. Add a
  one-line note clarifying that "the bootstrapped task" includes
  playbook-run tasks — not just regular tasks.

- **§5.12 Add a playbook (NEW)** — full interview-driven workflow.
  Sections asked: What / Why / Where / Each run does / Out of scope /
  Signals to watch for. Slug suggestion + project attachment + work_dir
  recipe identical to §5.2. **No "Done when"** (playbooks are never
  done). End with `flow add playbook` and overwrite the stub `brief.md`.

- **§5.13 Run a playbook (NEW)** — triggers: "run the X playbook",
  "trigger X", "fire the X agent", or bare "`flow run playbook X`".
  Recipe: ask session-mode (§5.4 reuses), then `flow run playbook <slug>`.
  Result: a new run-task and an iTerm tab.

- **§5.14 Substantive-unrelated-work check** — covered in §3 of this
  spec.

- **§6 Work_dir question** — same recipe applies for playbook intake;
  add a sentence noting that.

- **§7 Brief format** — already updated to include the playbook brief
  template alongside task and project templates.

- **§8 Anti-patterns** — add three playbook-specific items:
  > "- **Do not auto-fire `flow run playbook`.** Playbooks are
  > manual-trigger only. Even if a user mentions a playbook by name in
  > passing, do NOT run it without an explicit verb ("run", "trigger",
  > "fire", "start").
  > - **Do not edit a run-task's `brief.md` to change the playbook's
  > behavior for future runs.** That brief is a frozen snapshot. To
  > change behavior, edit the playbook's `brief.md` and start a new
  > run.
  > - **Do not propose scheduling during playbook intake.** Scheduled
  > invocation is out of scope for v1; playbooks are manual."

- **§9 Bootstrap contract** — add the playbook-run branch:
  > "If `flow show task` indicates `kind: playbook_run`, also run
  > `flow show playbook <playbook-slug>` first (for context: the
  > playbook's intent and recent runs). Note any files under the
  > playbook's `other:` section — they're sidecar references you can
  > load on demand, not eagerly. Then read your task's `brief.md` —
  > that's the snapshot taken when this run started, and it's your
  > authoritative instructions. The playbook's live `brief.md` may have
  > evolved since; you don't need to re-read it."

- **§10 Environment variables** — note that playbook-run sessions get
  the same `FLOW_TASK=<run-slug>` and `FLOW_PROJECT=<project-slug>`
  env vars (because runs are tasks). No new env var. The session knows
  it's a playbook run via `flow show task` (which displays
  `kind: playbook_run` and `playbook: <slug>`), not via env.

- **§11 When in doubt** — already references §5.14; no playbook-specific
  change beyond the existing "ask before mutating" rule.

### Open questions

- Do playbooks accept the `--priority` flag (and what does it mean for
  the playbook itself if all runs default to medium)? **Decision:**
  no priority on playbook definitions; runs default to medium and can
  be bumped via `flow priority <run-slug>`.
- Can a playbook be archived while having active runs? **Decision:**
  yes — archiving the playbook hides it from `flow list playbooks` but
  does not affect existing runs (they're independent task rows).

---

## 2. Intake-minimal

### Goal

Lower the cost of `flow add task` so the user captures work the moment
they think of it, instead of bouncing off a 6-section interview. Detailed
brief sections become optional; the user can flesh them out at task start
or skip them entirely.

### Today's intake (§5.2)

Asks: What / Why / Where / Done when / Out of scope / Open questions →
draft brief → confirm → save.

The first three are non-negotiable per the current skill. The last three
are the parts that drag the conversation.

### New intake

**Required (always asked):**

1. **Name** — one-sentence description of the work. (= `What`)
2. **Slug** — auto-suggested from name; user picks or accepts.
3. **Work_dir** — same §6 recipe as today.
4. **Priority** — High / Medium / Low.

**Optional (offered, can be deferred):**

After capturing the four required fields, offer:

> "Want to capture more detail now (Why, Done when, Out of scope, Open
> questions), or defer until you start the task?"

- **Detail now:** run the rest of the §5.2 sections.
- **Defer:** save the task with a thin brief (see template below).

### Thin brief template

```markdown
# <name>

## What
<one sentence from intake>

## Why
*Deferred — fill in at task start.*

## Where
work_dir: <path>

## Done when
*Deferred — fill in at task start.*

## Out of scope
*Deferred*

## Open questions
*Deferred*

---
*This brief is thin. Before you start substantive work, the bootstrap
session will prompt you to fill in the deferred sections.*
```

### Bootstrap-time prompt for thin briefs

In §9 (bootstrap contract), add a step: after reading the brief, if any
section body is the literal `*Deferred — fill in at task start.*` (or
similar marker), pause and walk the user through the missing sections
**before** beginning work. Use AskUserQuestion to offer:

- Fill in now (run a mini-§5.2 interview for just the missing sections)
- Skip — proceed without them (acceptable for known scope)

This shifts the interview burden from intake-time to task-start-time, where
the user has more context anyway and is more motivated to think about
acceptance criteria.

### Skill changes (for intake-minimal)

- Rewrite §5.2 sections to "required first, optional second" structure.
- Add the deferred-sections check to §9 bootstrap contract.
- Update the brief template at the bottom of §7 to show the thin-brief variant.
- Add a note: "If the user asks to add a task in 5 seconds, run intake-minimal.
  If the user asks for a 'full intake' or says 'I have time, walk me through
  it', run the full §5.2 interview."

### CLI changes (for intake-minimal)

**None.** `flow add task` already accepts only `name`, optional `--slug`,
optional `--project`, optional `--work-dir`, optional `--priority`,
optional `--mkdir`. The brief.md it writes is a stub. The skill's job
is to overwrite that stub — with full content (existing behavior) or
thin content (new behavior).

### Open question

- Should we add a CLI flag like `flow show task --thin` that highlights
  deferred sections so the user can quickly see which tasks have unfilled
  briefs? **Decision (default):** no, the bootstrap-time prompt is sufficient.
  Revisit if users complain.

---

## 3. Substantive-unrelated-work check

### Bug

Today, the skill delegates the "is this work that should have its own
task?" check to the SessionStart hook, which injects an instruction once
at session start. If the user enters a dispatch session and starts
substantive work mid-conversation (e.g., an unrelated brainstorm 5 turns
in), the skill has no mechanism to re-evaluate.

This was observed in the conversation that produced this spec: the
session started with `/flow` (status check), then pivoted to a multi-turn
design discussion that should have been its own task. The skill never
prompted to create a task because the trigger only fires at start.

### Fix

Convert from one-shot to ongoing. Add a new skill section that the
assistant re-evaluates on every turn.

### New skill section: §5.14 — Substantive-unrelated-work check

(Numbering: §5.12 is "Add a playbook", §5.13 is "Run a playbook", so the
scope check becomes §5.14. §5.11 is the existing bound-session scope-creep
check; §5.14 is the dispatch-session counterpart and references §5.11
for the bound-session triggers.)

> **§5.14 Substantive-unrelated-work check (passive, ongoing)**
>
> This is a passive workflow that runs alongside every other workflow.
> It fires when substantive work emerges that doesn't belong to the
> current task binding.
>
> **Triggers (any one is enough):**
>
> - In a **dispatch session** (FLOW_TASK unset): the assistant has been
>   in active design / brainstorming / debugging discussion for ≥ 2
>   turns about a concrete topic, OR has made any Edit/Write tool calls.
> - In a **bound session** (FLOW_TASK set): same triggers as §5.11
>   (work moved off the bootstrapped task's scope).
>
> **NOT a trigger:**
>
> - One-off question answered in a single turn.
> - Reading files / running queries to inform an answer.
> - The very first message after session start (you don't yet know if
>   this is one-off or substantive).
>
> **Recipe:**
>
> 1. Pause current work.
> 2. Run `flow list tasks --status in-progress` and
>    `flow list tasks --status backlog --priority high` to see candidates.
> 3. Use AskUserQuestion to offer three options:
>    - **Create a new flow task** for this work (run §5.2 minimal intake,
>      then optionally `flow do <new-slug>`).
>    - **Switch to an existing task** (list candidates as options;
>      on selection, spawn `flow do <slug>`).
>    - **Proceed ad-hoc** (user accepts no resumability, no context
>      accumulation).
>
> **Important: this is an ongoing check, not one-shot.** Re-evaluate the
> triggers each turn — especially when transitioning into design /
> implementation / debugging work. The SessionStart hook gets you the
> first check; you are responsible for every subsequent check.

### Replacement of the SessionStart hook text

Today, `flow hook session-start` injects a paragraph that includes the
three-choice prompt. Update that text to instead inject a one-liner
that points at §5.13:

> "This session is not bound to any flow task (FLOW_TASK is unset).
> When substantive work emerges, run §5.14 of the flow skill to offer
> the user a flow task. The check is ongoing, not one-shot — re-evaluate
> on every turn."

This keeps the SessionStart hook lean and moves the actual logic into
the skill where it can be referenced repeatedly.

### Skill changes (for the scope-check)

- Add §5.14 to `internal/app/skill/SKILL.md` (verbatim above).
- Update `flow hook session-start` output to use the one-liner pointer.
- Update §11 ("When in doubt") to reference §5.14.

### CLI changes (for the scope-check)

- Update `internal/app/hook.go` to emit the new one-liner.

### Open question

- Should §5.14 also fire when the assistant invokes a process skill
  (brainstorming, debugging, writing-plans)? **Decision:** yes —
  add a concrete bullet to triggers: "any time you invoke
  superpowers:brainstorming, superpowers:writing-plans, or
  superpowers:debugging skills in a dispatch session, treat that as a
  substantive-work signal. Ordering: load the process skill first
  (so the user sees the right tool engage), then before taking the
  skill's first concrete action, run the §5.14 check. If the user
  picks 'create a new task' or 'switch to existing task', the
  process skill resumes inside the new session, not this one."

---

## 4. Remove flowde

### Why

`flowde` exists solely to call `flow skill install --force` on every
launch. The user wants the skill update story to be explicit (manual
`flow skill update`) rather than implicit (every claude launch). The
upgrade story for flow itself will be designed separately later.

### Changes

**Delete:**

- `cmd/flowde/` directory (main.go and main_test.go).

**Modify:**

- `Makefile`:
  - Drop `WRAPPER := flowde` line.
  - Drop `go build -o $(WRAPPER) ./cmd/flowde` from the `build` target.
  - Drop `rm -f $(WRAPPER)` from the `clean` target.
  - Drop `flowde` references from any help text.
- `internal/app/do.go`:
  - In `cmdDo`, replace `flowde --session-id ...` with
    `claude --session-id ...`.
  - Replace `flowde --resume <uuid>` with `claude --resume <uuid>`.
  - Update the comment block above the spawn command (remove the
    flowde rationale paragraph).
- `README.md`:
  - Drop the "run `flowde`" guidance from the install section. Replace
    with "run `claude` and say 'let's get to work.'"
  - Drop the "How it works under the hood" mention of flowde.
  - Add a small note: "to refresh the skill after upgrading flow, run
    `flow skill update` (or `make install` again)."
- Delete the existing `flowde` binary on disk via `make clean`.

### What stays

- `flow skill install`, `flow skill update`, `flow skill uninstall` are
  unchanged. Manual skill management is the new upgrade path.
- The SessionStart hook stays. The skill itself stays. The only thing
  removed is the wrapper that auto-refreshed the skill on every launch.

### Migration for existing users

After this lands:

```bash
cd ~/flow
git pull
make build         # build new flow without flowde
make install       # reinstalls skill from updated embedded copy
rm flowde          # leftover binary
```

We add a one-line note to README's upgrade section. No data migration
needed (no schema change).

### Skill changes (for flowde removal)

- Remove all references to `flowde` from `internal/app/skill/SKILL.md`.
  (There are mentions in §9 bootstrap contract and possibly §10.)
- Update SKILL.md to instruct sessions to invoke `claude` directly.

### Open questions

- Does `flow skill update` need to also rebuild the skill SHA check or
  similar to know when an update is available? **Decision:** out of
  scope — manual reinstall is fine for now. Revisit when we design the
  full upgrade story.

---

## 5. Auxiliary markdown files in entity directories

### Why

Today the entity dir contract is rigid: `brief.md` + an `updates/` subdir
of dated notes. Any other file is invisible to the spawned session — the
bootstrap prompt doesn't mention it and `flow show` doesn't list it.

Real workflows want sidecar context. Examples:

- A user pastes a long Slack thread into `tasks/auth/research.md` for
  reference during the session.
- A spec doc `tasks/foo/design-options.md` captures three approaches
  considered before writing the brief.
- A playbook author drops `playbooks/triage-cs-inbox/decision-tree.md`
  describing how to classify incoming threads.

These files exist on disk; we just need to surface them.

### Behavior

For each entity (`task`, `project`, `playbook`):

- `flow show <entity>` lists `.md` files in the entity's dir under a new
  `other:` section, **excluding** `brief.md` (already shown under `brief:`)
  and the `updates/` subdir contents (already shown under `updates:`).
- The bootstrap prompts (task variant in `do.go`, playbook variant added
  in §1) **reference** these files — telling the spawned session they
  exist as sidecar context — but do **not** instruct eager loading.
  Same lazy-load principle as KB files (skill §5.10): the session reads
  them on demand when they become relevant to the current work, not
  preemptively.

This trades two-line-of-text awareness for token cost. A 200-line
research dump dropped into a task dir might be useless for 90% of
work in that session — eagerly loading it wastes context and clutters
the conversation. Making it referenceable lets Claude pull it in when
the brief or a user question makes it relevant.

### `flow show` output addition

```
brief:       /Users/rohit/.flow/tasks/auth/brief.md
updates:     - /Users/rohit/.flow/tasks/auth/updates/2026-04-30-oauth-decision.md
other:       - /Users/rohit/.flow/tasks/auth/research.md
             - /Users/rohit/.flow/tasks/auth/design-options.md
```

If the dir contains no auxiliary markdown files, the line reads
`other:       (none)` — same convention as `updates: (none)`.

### What's NOT included

- **Non-markdown files** (images, JSON, YAML, etc.) — out of scope.
  If a user wants those referenced, they mention them in `brief.md`.
- **Subdirectories other than `updates/`** — not enumerated. Keeps
  the contract bounded.
- **Files in nested directories** — only top-level `.md` files in the
  entity dir count.

### Skill changes (for aux files)

- §9 bootstrap contract: update the "Load the task context" step (and
  the parallel project-context step) to mention `other:` as
  on-demand references — not eagerly loaded. Wording template:
  > "Files listed under `other:` are sidecar references for this entity.
  > Do **not** read them eagerly. Read them on demand when something in
  > the brief, in user input, or in the work makes them relevant. This
  > matches the lazy-load principle for KB files (§5.10)."
- §5.10 (KB lazy-load section): add a parallel paragraph noting that
  the same lazy-load discipline applies to entity-dir auxiliary files
  surfaced under `other:`.
- §7 brief template footer: add a one-liner mentioning that other
  `.md` files in the same directory are surfaced by `flow show` as
  on-demand references.

### CLI changes (for aux files)

- `internal/app/show.go` (`flow show task`, `flow show project`,
  and the new `flow show playbook`):
  enumerate `*.md` in the entity dir, exclude `brief.md`, format
  under `other:`. Use the same enumeration logic for all three.
- `internal/app/do.go` (`buildBootstrapPrompt`): add the
  "every file under `other:`" instruction in both the task variant
  and the playbook-run variant.

### Test plan addition

- `TestShowEntityListsAuxFiles` — drop `notes.md` and `research.md`
  in a task dir, verify `flow show task` includes them under `other:`
  and excludes `brief.md` and the `updates/` subdir.
- `TestShowEntityNoAuxFiles` — empty `other:` shows `(none)`.

---

## Test plan

For each change, an integration test in `internal/app/`:

**Playbook:**
- `TestPlaybookCRUD` — add, list, show, edit, archive, unarchive.
- `TestPlaybookRun` — `flow run playbook` creates a task with
  `kind='playbook_run'`, snapshots playbook `brief.md` to the run-task's
  `brief.md`, spawns the iTerm tab via the mocked Runner.
- `TestPlaybookRunSnapshotIsolation` — editing the playbook's `brief.md`
  after a run started does NOT change the run-task's `brief.md`.
- `TestPlaybookRunSlugCollision` — two runs in the same minute get the
  seconds suffix (`...-10-30-45`); two runs in the same second get
  `-2`/`-3` after seconds.
- `TestListTasksHidesPlaybookRuns` — default `flow list tasks` filters
  out `kind='playbook_run'`.
- `TestListRuns` — `flow list runs <playbook>` shows only that
  playbook's runs.

**Intake-minimal:**
- Skill change only; covered by manual verification + skill self-test
  if we have one. (Skill changes have historically been verified
  manually; flag if we want to add an automated skill linter.)

**Substantive-unrelated-work check:**
- Same — skill change only.
- Update the existing test for `flow hook session-start` output to
  match the new one-liner format.

**Flowde removal:**
- Delete `cmd/flowde/main_test.go` along with the package.
- Update `do_test.go` to assert `claude` is invoked, not `flowde`.
- Update any e2e test that mentions flowde.

---

## Out of scope

- Scheduling playbooks (`/schedule` integration). Playbooks are
  manually invoked only in v1.
- Per-invocation parameters for playbooks (templating).
- Cross-task dependencies between playbook runs.
- Auto-upgrade story for the flow binary or skill (deferred per user
  decision).
- Pruning old playbook runs automatically.

---

## Implementation order (recommended)

1. **flowde removal** — smallest, low-risk; gets it out of the way and
   simplifies the diff for subsequent changes. Verify `flow do` still
   works against `claude` directly.
2. **Auxiliary markdown enumeration** — small standalone change to
   `flow show task|project` plus `do.go` bootstrap prompt. Lands before
   playbook so playbook can reuse the same enumeration logic.
3. **Schema migration** — add `kind` and `playbook_slug` columns, add
   `playbooks` table, add indexes. Run migrations idempotent.
4. **Playbook commands** — `flow add playbook`, `flow list playbooks`,
   `flow show playbook` (uses aux-file enumeration from step 2),
   `flow edit` (extended), `flow archive`/`unarchive` (extended).
5. **`flow run playbook`** — the key new command; reuses cmdDo logic
   and the playbook-run bootstrap prompt variant.
6. **`flow list runs`** — playbook-run listing.
7. **Default `flow list tasks` filter** — add `kind='regular'` to its
   default WHERE clause.
8. **Skill updates** — playbook intake (§5.12), playbook run (§5.13),
   bootstrap contract for playbook runs and aux files (§9),
   command reference (§4).
9. **Intake-minimal skill changes** — rewrite §5.2 with required-then-optional
   structure, update §7 brief template, add bootstrap-time deferred-section
   prompt to §9.
10. **Substantive-unrelated-work check** — add §5.14, update SessionStart
    hook output, update §11.
11. **Verification** — run full test suite, manual test of each new
    command, manual test of skill behavior in a fresh dispatch session.

Step 7 and steps 8–9 reorder the skill, but the changes are largely
additive within §5; sequencing them avoids merge collisions.

---

## File-by-file impact summary

| File | Changes |
|---|---|
| `internal/flowdb/db.go` | Add `playbooks` table to schemaDDL; add `Playbook` struct, `ScanPlaybook`, `GetPlaybook`, `ListPlaybooks`, `UpsertPlaybook`. Add `Kind` and `PlaybookSlug` fields to `Task`. Update `TaskCols` and `ScanTask`. Add `Kind` filter to `TaskFilter`. Add `runMigrations` ALTERs for `tasks.kind` and `tasks.playbook_slug`. |
| `internal/app/add.go` | Add `flow add playbook` handler. |
| `internal/app/list.go` | Add `flow list playbooks`. Add `flow list runs`. Add default `kind='regular'` filter to `flow list tasks` (with override flag). |
| `internal/app/show.go` | Add `flow show playbook` handler. Update `flow show task` to display `kind` and `playbook_slug` when present. Add aux-file enumeration (`other:` section) to `flow show task`, `flow show project`, and `flow show playbook` — uses a shared helper since the logic is identical for all three. |
| `internal/app/run.go` (new) | Implement `flow run playbook <slug>` — slug generation, brief snapshot, delegation to cmdDo logic. |
| `internal/app/do.go` | Replace `flowde` invocation with `claude`. Update `buildBootstrapPrompt`: (a) handle playbook runs via a `kind`-aware branch; (b) add the "every file under `other:`" instruction in both task and playbook variants. Update comment block. |
| `internal/app/edit.go` | Extend ref resolution to include playbooks (opens `instructions.md`). |
| `internal/app/archive.go` | Extend ref resolution to include playbooks. |
| `internal/app/resolve.go` | Add `ResolvePlaybook`. |
| `internal/app/hook.go` | Update output to one-liner pointing at §5.13. |
| `internal/app/skill/SKILL.md` | Add §5.12 (playbook intake), §5.13 (playbook run), §5.14 (sub-unrelated-work). Rewrite §5.2 for intake-minimal. Update §7 brief template. Update §9 bootstrap contract. Update §4 command reference. Update §11. Remove flowde mentions. |
| `cmd/flowde/` | DELETE. |
| `Makefile` | Drop WRAPPER, flowde build, flowde clean. |
| `README.md` | Drop flowde guidance; document `flow skill update`. |
| `internal/app/do_test.go` | Assert `claude` (not `flowde`) is invoked. Add tests for playbook-run bootstrap prompt variant. |
| `internal/app/list_test.go` | Add tests for default kind filter and `flow list runs`. |
| `internal/app/run_test.go` (new) | Tests for `flow run playbook`. |
| `internal/flowdb/db_test.go` | Tests for playbooks table CRUD and tasks.kind migration. |

---

## Approval

This spec covers four independent changes bundled because they all
land in `~/flow` and the skill changes are interleaved.

Before writing the implementation plan: please review and flag anything
to revise.
