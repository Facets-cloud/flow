---
name: flow
description: |
  Personal task and Claude session manager. CLI binary is `flow` (assumed
  on PATH) and stores metadata in ~/.flow/flow.db (SQLite). Use this skill when the
  user asks about their work, tasks, or projects in any natural phrasing —
  including but not limited to: "what's left", "what's remaining",
  "what's pending", "what do I need to do", "what's on my plate",
  "what should I work on", "status", "give me a status", "anything
  urgent", "what's overdue", "what's stale", "show me my work",
  "how's my week looking", "what did I ship", "what's in progress",
  "what's next", "what am I working on", "where did I leave off",
  "start my day", "what should I do", "what should I do today".
  Also use for task/project management actions: "flow", "add a task",
  "add a project", "resume work", "pick up where I left off", "save a
  note", "log progress", "write an update", "note that", "I'm waiting
  on", "blocked on", "stuck until", "mark done", "archive", "weekly
  review", "clean up my tasks", or when the user invokes any
  `flow <subcommand>` directly. Also use whenever the user asks you to
  bootstrap a new Claude session on a task or tell them about their
  in-flight work.
---

# flow — task and session manager skill

## 1. What flow is

`flow` is a small CLI (assumed on `$PATH`) that the user uses to track
personal work and bootstrap per-task Claude sessions. Metadata (projects,
tasks, workdirs, session IDs) lives in a single SQLite database at
`~/.flow/flow.db`. Free-form plan content lives on disk as markdown
"briefs" at `~/.flow/projects/<slug>/brief.md` and
`~/.flow/tasks/<slug>/brief.md`. Progress notes accumulate as dated
markdown files under each entity's `updates/` subdirectory. The user runs
one long-lived Claude session per task in its own iTerm tab, resumed via
`flow do <task>`.

You are speaking inside one of those Claude sessions (or the user's
ambient "dispatch" session). Your job is to interpret the user's natural
language requests and turn them into the exact `flow` commands and file
edits they imply. You never edit `flow.db` directly. You never solve
problems during task intake — you interview, then write what the user
said.

## 2. The model

- **Projects** group related tasks. Every project has a name, a slug, a
  `work_dir` (a path on disk), a priority, a status (`active` or `done`),
  and a `brief.md` file describing the project's intent.
- **Tasks** are units of work. Every task has a name, a slug (short,
  user-chosen via `--slug` at creation time), a `work_dir` (mandatory —
  either the project's work_dir, a user-supplied path, or an auto-created
  `~/.flow/tasks/<slug>/workspace/` for floating tasks), a priority, a
  status (`backlog`, `in-progress`, `done`), an optional `project_slug`,
  an optional `waiting_on` freeform note, and a `brief.md`. Tasks also
  carry a Claude `session_id` once `flow do` has bootstrapped a session
  for them.
- **Playbooks** are reusable, runnable definitions. A playbook has a
  name, slug, work_dir, optional `project_slug`, and a `brief.md` that
  describes what each invocation should do. Each invocation creates a
  **playbook-run** — a task with `kind=playbook_run` — that has its
  own session, its own snapshotted `brief.md`, and its own
  `updates/`. Editing a playbook's `brief.md` does not affect past
  runs; runs are reproducible.
- **Workdirs** is a convenience registry of known local repo paths. It
  exists so this skill can match repo intent ("the budgeting app")
  to a path on disk. It is not the source of truth for any task's
  work_dir — `tasks.work_dir` is.
- **Updates** are dated markdown files under
  `~/.flow/tasks/<slug>/updates/YYYY-MM-DD-<kebab>.md` (and the same under
  `projects/`). They are progress notes. They are written by you (this
  skill) via the `Write` tool when the user asks you to save a note. They
  are not in the database. They are permanent — archiving a task never
  deletes them.
- **Status is 3 values.** `backlog`, `in-progress`, `done`. There is no
  `blocked` state anymore. If the user is waiting on something or
  someone, set `waiting_on` (see §5.6). If the user has set a task aside
  permanently, `archive` it.

## 3. First-run detection (once per session)

The **first time in a session** you're about to run a `flow` command,
run `flow list tasks` or `flow list projects` as a probe:

- If the command **succeeds** (even with zero results): flow is
  initialized. Proceed normally. **Do not check again this session.**
- If the command **errors** with a message about a missing database:
  the user hasn't run `flow init` yet. Use `AskUserQuestion` (header:
  "Run init?", options: "Yes, run flow init" / "No, I'll do it later")
  with question text explaining that `flow init` sets up the data
  directory (`$FLOW_ROOT` if set, otherwise `~/.flow`) and database.
  If "Yes", run `flow init` and then enter the **first-run coaching**
  below. If "No", stop.

### First-run coaching

After `flow init` succeeds for a brand-new user, walk them through the
basics in this order:

1. **Explain what just happened.** "`flow init` created `~/.flow/` with
   an empty database and 5 knowledge-base files."

2. **Create their first project.** "Let's set up a project — what's the
   main thing you're working on right now?" Then enter the §5.3
   add-project interview. This gets them a project and at least one task
   immediately.

3. **Show how to start work.** After the first task exists, use
   `AskUserQuestion` (header: "Open it now?", options:
   "Open it now" / "Later, just save") to ask whether to run
   `flow do <slug>`. Briefly explain in the question: a dedicated
   Claude session gets the brief, updates, and repo conventions
   automatically. If "Open it now", proceed to §4.4. If "Later",
   stop here.

4. **Mention the knowledge base.** "As we work together, I'll
   automatically note durable facts about you and your org in
   `~/.flow/kb/`. These notes carry across sessions so future Claude
   conversations have context without you repeating yourself."

5. **Point to daily use.** "From any session, just say 'what should I
   work on' or 'start my day' and I'll pull up your task list. Say
   'add a task' to capture new work."

Keep the coaching conversational and brief — don't dump all five points
in one wall of text. Let the user respond between steps. If they want
to skip ahead ("I know, just set it up"), respect that and stop
coaching.

## 4. Command reference

This is a terse cheat sheet. Use `flow <command> --help` for up-to-date
flags.

```
Setup
  flow init                                 create ~/.flow/, init DB, install skill
  flow skill install [--force]              (re)install the skill file
  flow skill uninstall                      remove the skill
  flow skill update                         install --force after upgrading the binary

Create
  flow add project "<name>" --work-dir <path> [--slug <s>] [--priority h|m|l] [--mkdir]
  flow add task    "<name>" [--slug <s>] [--project <slug>] [--work-dir <path>] [--mkdir]
                           [--priority high|medium|low]
  flow add playbook "<name>" --work-dir <path> [--slug <s>] [--project <slug>] [--mkdir]

Sessions
  flow do               <ref> [--fresh] [--dangerously-skip-permissions]
  flow done             <ref>

Playbook runs
  flow run playbook <slug>          spawn a fresh run session (new task with kind=playbook_run)
  flow list runs [<playbook-slug>]  list playbook runs (filter by playbook optional)

Read
  flow show task    [<ref>]     (defaults to $FLOW_TASK)
  flow show project [<ref>]     (defaults to $FLOW_PROJECT)
  flow show playbook    [<ref>]
  flow transcript   [<ref>] [--compact]    (readable transcript from session jsonl)
  flow list tasks    [--status backlog|in-progress|done] [--project <slug>]
                     [--priority high|medium|low] [--since today|monday|7d|YYYY-MM-DD]
                     [--include-archived]
  flow list projects [--status active|done] [--include-archived]
  flow list playbooks   [--project <slug>] [--include-archived]

Edit / mutate
  flow edit        <ref>           opens brief.md in $EDITOR, bumps updated_at
  flow update task <ref> [--session-id <uuid>] [--work-dir <path>] [--mkdir]
  flow priority    <ref> high|medium|low
  flow waiting     <ref> "<who or what>"
  flow waiting     <ref> --clear
  flow archive     <ref>
  flow unarchive   <ref>
  (flow edit, flow archive, flow unarchive also accept playbook refs)

Workdirs
  flow workdir list
  flow workdir add <path> [--name <nickname>]
  flow workdir remove <path>
  flow workdir scan [<root>] [--add]
```

All references (`<ref>`) resolve by **exact slug match only**. There is
no fuzzy or substring matching. Use `--slug` to pick a short, memorable
slug at creation time (e.g. `--slug caas-exit`). If omitted, a slug is
auto-generated from the name (truncated to ~6 words).

## 4a. Interactive choices (use `AskUserQuestion` everywhere)

**This section overrides any inline prose phrasing later in the skill.**
If a later section says "offer X", "ask Y", or "confirm Z", that
always means "invoke `AskUserQuestion` with appropriate options" —
never a prose question typed into the chat.

Every choice the user makes — always AskUserQuestion, never a prose
question. Yes/no confirmations, pick-one-of-several, priority, slug
suggestions, project attachment, mutation confirmations, "want me to
do X?" — every single one runs through the tool so the user can click
to select instead of typing. Common patterns:

| Pattern | Options |
|---------|---------|
| Yes / No | Two options with contextual labels (e.g. "Save it" / "Revise", "Open now" / "Not now") |
| Pick from list | One option per candidate (tasks, projects, slugs) |
| Priority | "High", "Medium", "Low" |
| Mutation confirm | "Yes, do it" / "No, wait" with the action named in the description |

Keep `header` under 12 chars. Put enough context in `question` so
the choice is clear without scrolling back. If the user already
answered in their message, don't re-ask — just use their answer.

**Prose questions are deprecated.** Don't write "Want me to do X?"
or "Should I do Y?" or "(yes/no)" in chat — those force the user to
type a free-text reply. The tool produces clickable options; always
prefer the tool.

## 5. Core workflows

These are the load-bearing part of the skill. When the user says one of
the trigger phrases, follow the corresponding recipe exactly.

### 4.1 Start the day

**Triggers:** "start my day", "what should I do today", "what am I
working on", "where did I leave off", "give me a status".

**Recipe:**

1. Run `flow list projects` and `flow list tasks --status in-progress`.
2. Run `flow list tasks --status backlog --priority high`.
3. Read the `waiting_on` and stale markers in the tasks output.
4. Summarize in 4 sections:
   - **In flight** (`in-progress`): 1 bullet per task, include any ⚠
     stale marker and any `[waiting: ...]` note.
   - **High-priority backlog**: 1 bullet per backlog task marked high.
   - **Waiting on someone**: pull out tasks with `waiting_on` set so the
     user can see the whole block at once.
   - **Stale** (anything with the ⚠ marker): call these out explicitly.
   - **Active playbooks**: any playbook with a run in the past 7 days.
     Pull from `flow list runs --since 7d` grouped by playbook; show
     playbook slug + most recent run timestamp. Skip if there are no
     runs in the window — don't show an empty header.
5. Use `AskUserQuestion` to let the user pick which task to work on.
   List each in-progress and high-priority backlog task as an option
   (label = slug, description = one-line summary). Include an "Add a
   new task" option if appropriate.

Do not auto-run `flow do` after listing. Wait for the user to pick.

### 4.2 Add a task — INTERVIEW MODE (mandatory)

**Triggers:** "add a task", "new task", "track this work", "let me add a
flow task for X".

**The interview is the whole point.** The skill's value vs. "just run
`flow add task`" is that you interview the user before saving. You NEVER
solution during intake. You NEVER fill blanks with guesses. If a section
is unclear, ask. If the user says "I don't know yet", write "Open
question: ..." in the brief and move on.

**Required sections (always asked, in this order):**

1. **Name** — one-sentence description of the work. Example: "Add OAuth
   login to the budgeting app."
2. **Slug** — short, memorable, ASCII. Use AskUserQuestion to suggest 2–3
   candidates derived from the name. User picks one or types a custom
   slug.
3. **Where?** — work_dir for the task. Use the §6 recipe.
4. **Priority** — High / Medium / Low via AskUserQuestion. Default Medium.

**Optional sections (offered, can be deferred):**

After the four required fields, use AskUserQuestion:

> "Want to capture more detail now (Why, Done when, Out of scope, Open
> questions), or defer until you start the task?"
> - Detail now (recommended for tasks you'll start later)
> - Defer until you start the task

**Detail now:** run the rest of the original §4.2 sections — Why, Done
when, Out of scope, Open questions — and draft the full brief. Use the
full task-brief template from §7.

**Defer:** save the task with a thin brief (template in §7). The
bootstrap-time prompt (§9 deferred-section prompt) will walk the user
through the missing sections when they `flow do` the task — at which
point the user has more context and is more motivated to think about
acceptance criteria.

**Confirmation flow** (both paths):
- Show the drafted brief.
- AskUserQuestion: "Brief — Save it / Revise"
- Save → `flow add task ...` → overwrite stub brief with content.

**Then, BEFORE calling `flow add task`:**

- **Ask for a short slug.** Suggest 2–3 slug candidates derived from
  the task name (e.g. for "Add OAuth to budgeting app" suggest `oauth`,
  `auth-budget`, `oauth-budget`). Present them via `AskUserQuestion`
  so the user can click one (the "Other" option lets them type a custom
  slug). If the user picks Other and leaves it blank, omit `--slug`.
- **Project attachment.** Use `AskUserQuestion` with one option per
  existing project (label = slug, description = project name) plus a
  "None (floating task)" option. If there are no projects, skip.
- **Priority.** Use `AskUserQuestion` with "High", "Medium (Recommended)",
  "Low". Skip if the user already stated priority.
- **`--mkdir`** if the `work_dir` doesn't exist yet. Use `AskUserQuestion`
  with "Yes, create it" / "No, I'll fix the path".

**Draft the brief. Show it to the user.** Then use `AskUserQuestion`
(header: "Brief", options: "Save it" / "Revise") to confirm. Do not
run `flow add task` until the user picks "Save it". If they pick
"Revise", ask what to change, update the draft, and re-confirm.

**After `flow add task` succeeds**, it will print the task slug and the
absolute path to a stub `brief.md`. Use the `Write` tool to overwrite
that file with the drafted content. Use this literal template:

```markdown
# <name>

## What
<one sentence>

## Why
<short paragraph>

## Where
work_dir: <path>

## Done when
- <criterion 1>
- <criterion 2>
- <criterion 3>

## Out of scope
- <non-goal 1>

## Open questions
- <question 1>

---
*Before you start on this task, read CLAUDE.md in the work_dir.*
```

Finally, use `AskUserQuestion` (header: "Open now?", options:
"Yes, open it" / "No, keep in backlog") to offer `flow do <slug>`.
If yes, proceed to the §4.4 recipe (which will ask session mode).
If no, stop.

### 4.3 Add a project

**Triggers:** "add a project", "new project", "track this initiative".

Similar to §5.2 but shorter. Sections: **What / Why / Where / Scope**.
No "done when" (projects are ongoing containers, not completable units).
Confirm the `work_dir`. Draft. Show. Wait for "save it". Run `flow add
project`. Overwrite stub brief with the drafted content.

Do not offer `flow do` on the project itself — you `do` tasks, not
projects.

**MANDATORY follow-up: create at least one task under the project.**
A project with zero tasks is a dead container — the user will forget
why they made it. Immediately after `flow add project` succeeds:

1. Say: "Project created. A project needs at least one task to be
   useful — what's the first concrete thing you want to do under
   <project-slug>?" (Use the project's actual slug.)
2. When the user answers, enter the task-intake workflow (§5.2)
   with `--project <slug>` pre-filled. Interview for What / Why /
   Where / Done when / Out of scope / Open questions as usual.
3. If the user says "I don't know yet" or "just create the project
   for now", DO NOT create a placeholder task and DO NOT silently
   drop it. Instead, explicitly tell them: "OK, no task for now.
   Remember: run `flow add task --project <slug>` before `flow do`,
   because there's nothing to do yet on this project."
4. If the user describes several tasks at once, create them all via
   sequential §5.2 interviews. Don't try to batch-extract; one
   interview per task.
5. Only after the first task exists (or the user has explicitly
   declined), use `AskUserQuestion` (header: "Open it now?", options:
   "Yes, open it" / "No, keep in backlog") to offer
   `flow do <first-task>`. If "Yes", proceed to §4.4. If "No", stop.

The rule is about pushing the user one step further than
`flow add project` — project creation is not a complete action on
its own, it's the start of a two-or-more-step workflow.

### 4.4 Start / resume work on a task

**Triggers — any of these means "run `flow do <ref>`":**
- "resume X" / "pick up X" / "continue X" / "open X"
- "let me work on X" / "lets work on X" / "let's work on X"
- "lets do X" / "let's do X" / "do X" / "do the X"
- "start X" / "start on X" / "begin X" / "get going on X"
- A bare "`flow do X`" typed as command-like input

**Recipe:**

1. **Ask the user which session mode they want** before running anything.
   Use the `AskUserQuestion` tool so the user can click to choose:

   ```
   AskUserQuestion({
     questions: [{
       question: "Which session mode for <task-slug>?",
       header: "Session mode",
       options: [
         { label: "Regular",          description: "Normal Claude session with tool-approval prompts (safer)" },
         { label: "Skip permissions", description: "Pass --dangerously-skip-permissions (faster, no prompts)" }
       ],
       multiSelect: false
     }]
   })
   ```

   If the user already specified a mode in their request (e.g. "do X
   with skip permissions", "do X normally"), use that — don't re-ask.
2. Run: `flow do <user's ref>`. Pass the slug the user gave as one
   positional argument. Resolution is exact slug match. Append
   `--dangerously-skip-permissions` if the user chose skip-permissions.
3. If the command errors with "no task matching", ask the user to clarify
   or offer `flow add task` instead.
4. Pass `--fresh` ONLY if the user explicitly asked for a fresh session
   (e.g. "start over", "fresh session", "--fresh"). Never on your own.

**After `flow do` succeeds** it has already spawned an iTerm tab and
exported the env vars. Your job is done. Report "opened tab: <title>"
and stop. Do NOT:

- Run diagnostic commands like `pgrep`, `ls ~/.claude/projects/...`,
  or `osascript` to try to verify the tab opened.
- Try to spawn an iTerm tab yourself with osascript. `flow do` already
  did this.
- Re-run `flow do` unless the user explicitly asked for a retry.
- Try to peek into the new session's activity. It's a separate
  conversation; you have no access to it.

If `flow do` itself errored (rc != 0), relay the error and stop. Do
not attempt workarounds; the user will decide what to do next.

### 4.5 Save a progress note

**Triggers:** "save a note", "log progress", "write an update", "note
that…", "record that I…", "document that I just…".

**Recipe:**

1. Compose a filename: `YYYY-MM-DD-<kebab-short-title>.md`. The kebab
   title is 3–5 words summarizing the note. Use today's date.
2. Compose the note content. **Under 10 lines.** Exactly two paragraphs
   plus an optional blockers line:
   - Paragraph 1: what got done. Specific. No hedging.
   - Paragraph 2: what's next or what the user is thinking about next.
   - Optional blockers: "Blocked on: <X>" if applicable.
3. **Show the filename and the content to the user.** Then use
   `AskUserQuestion` (header: "Save note?", options: "Save it" /
   "Revise") to confirm. Do not write silently.
4. Determine the entity:
   - For a **regular task**, notes go under
     `~/.flow/tasks/<slug>/updates/`. Slug from `$FLOW_TASK` or asked.
   - For a **playbook run**, notes ALSO go under
     `~/.flow/tasks/<run-slug>/updates/` (runs are tasks).
   - For a **playbook definition**, notes go under
     `~/.flow/playbooks/<slug>/updates/` for cross-invocation observations
     ("noticed flaky output when X", "next iteration should consolidate
     steps 2 and 3"). Use this when capturing things that should inform
     the playbook itself, not a single run.
5. Use the `Write` tool to create
   `~/.flow/tasks/<slug>/updates/<filename>.md` with the confirmed
   content. If the user is noting project-level progress, use
   `~/.flow/projects/<slug>/updates/` instead.
6. Confirm to the user: "saved: <absolute path>".

Do NOT run any `flow` command for this — updates are just files.

### 4.6 Waiting on someone

**Triggers:** "I'm waiting on <X>", "blocked on <Y>", "stuck until <Z>",
"need <person> to respond", "pinged <X>".

**Recipe:** run `flow waiting <current-task> "<who or what>"`. The
status stays `in-progress`; `waiting_on` is just a freeform note that
will show up in `flow list` and `flow show task` so the user remembers.

**Unblocking triggers:** "X came back", "got the answer", "unblocked",
"no longer waiting on X". Before mutating, confirm via
`AskUserQuestion` (header: "Clear waiting?", options:
"Yes, clear it" / "Wait, not yet"). On "Yes", run
`flow waiting <task> --clear`. On "Wait, not yet", stop and let the
user clarify. (This matches the §8 "do not mark done without
confirmation" anti-pattern philosophy — clearing `waiting_on` is a
state mutation and deserves the same explicit click.)

Do not infer the task slug silently — use `$FLOW_TASK` if set,
otherwise use `AskUserQuestion` listing in-progress tasks as options
to disambiguate which task this is for.

### 4.7 Mark done

**Triggers:** "mark X done", "finish X", "X is done", "close out X".

**Recipe:**

1. Confirm via `AskUserQuestion` (header: "Mark done?", options:
   "Yes, `flow done <slug>`" / "No, not yet") before mutating. Per
   §8, never mark done without explicit confirmation — even if the
   user says "great, I finished that".
2. If the user hasn't just saved a progress note, use
   `AskUserQuestion` (header: "Closing note?", options:
   "Yes, save a note first" / "No, just mark done") to offer.
   On "Yes", run the §4.5 recipe first, then continue.
3. Run `flow done <ref>`. **Do not close the iTerm tab** and **do
   not kill the Claude session** — `flow done` deliberately leaves
   both intact. The session_id stays on the task row so a future
   reopen can still resume it.

**Playbook-specific notes:**

- **Run-tasks** (kind=playbook_run) support `flow done <run-slug>` like
  any task.
- Note: **playbook definitions are never "done" — they're archived.**
  When a playbook is no longer in use, run `flow archive <playbook-slug>`.
  There is no `flow done playbook` command.

### 4.8 Archive / cleanup

**Triggers:** "archive X", "clean up", "clean up my done tasks", "hide
finished work".

**Recipe:**

- Single task/project: confirm via `AskUserQuestion` (header:
  "Archive?", options: "Yes, archive `<slug>`" / "No, keep it"),
  then on "Yes" run `flow archive <ref>`.
- Bulk "archive everything done": run `flow list tasks --status done`.
  Show the list to the user. Then, unless the user already said
  "archive all done" explicitly, use `AskUserQuestion` (header:
  "Archive all?", options: "Yes, archive all listed" / "Pick one by
  one" / "Cancel"). On "Yes", iterate and archive them all, printing
  each action. On "Pick one by one", run a single-task `AskUserQuestion`
  for each. On "Cancel", stop.
- If the user regrets it: `flow unarchive <ref>`.

Archive never deletes files on disk — brief.md and updates/ remain. Make
sure the user knows this so they don't worry about losing notes.

**Playbooks:**
- `flow archive <playbook-slug>` hides the playbook from
  `flow list playbooks` but does not affect past runs (they're independent
  task rows). Past runs can be archived independently with
  `flow archive <run-slug>`.
- "Bulk clean up done runs" pattern: `flow list runs --status done`,
  then archive each.

### 4.9 Weekly review

**Triggers:** "weekly review", "week in review", "what did I ship this
week", "friday review".

**Recipe:**

1. `flow list tasks --status done --since monday` — what shipped.
2. `flow list tasks --status in-progress` — what's still in flight. For
   each one, read the newest file in its `updates/` directory (via the
   `Read` tool) to summarize the latest state in 1 line.
3. Call out any `⚠` stale tasks and any `waiting_on` tasks explicitly.
4. `flow list tasks --status backlog --priority high` — what's queued.
5. `flow workdir list` — surface any workdir that hasn't been used in
   30+ days; mention these as "consider archiving" candidates.
6. `flow list runs --since monday` — group by playbook slug, count runs,
   pull each playbook's most-recent run timestamp.

Produce a digest in this exact shape:

```
## Shipped this week
- <task> — <one-line outcome>

## In flight
- <task> — <latest-update summary>  [⚠ stale if applicable]

## Stalled / waiting
- <task> — waiting on: <who/what>

## Next up
- <task> — <why it's high priority>

## Workdir hygiene
- <path> — untouched since <date>

## Playbook activity
- <playbook-slug> — N runs this week, most recent <date>
```

Do not solve anything during a weekly review — it's a reporting
workflow, not a planning workflow.

### 4.10 Listening for knowledge-base facts (scoop mode)

This is a **passive** workflow — it runs alongside every other workflow
in §5, continuously, without the user asking for it.

**What flow's knowledge base is:**
Five markdown files under `~/.flow/kb/`, seeded by `flow init`:

| File | Holds |
|---|---|
| `user.md` | Durable facts about the user — role, preferences, working style, constraints, availability |
| `org.md` | Company, team, structure, people the user interacts with |
| `products.md` | What the org ships — product lines, modules, features, releases |
| `processes.md` | How the org works — tools, conventions, rituals, review rules |
| `business.md` | Customers, business model, revenue, deals, market positioning |

These files are surfaced in `flow show task` and `flow show project`
output under a `kb:` section, so execution sessions can read them.

**How to decide whether a user statement belongs in the KB:**

Listen for statements that are **durable facts**, not transient state.
Bucket them by these signals:

| User says something like… | Goes to |
|---|---|
| "I'm the / my role is / I prefer / I hate / I always / I never" | `user.md` |
| "our team / my manager is / we have N people / <name> is / reports to" | `org.md` |
| "our product / we ship / feature X / module Y / next release" | `products.md` |
| "we use X for / our process / every Friday / we deploy on / review rule" | `processes.md` |
| "our customers / <customer> asked / revenue / contract / margin / market" | `business.md` |

**The scoop rule: append without asking.** If you hear a durable fact,
use Read to load the matching file, check it's not already there, then
Write an appended entry. Never pause to ask "should I record this?".
Just do it, then announce quietly in one line:

> noted in kb/org.md: "<short paraphrase>"

**Entry format — copy this exactly:**
```
- YYYY-MM-DD — <short quote or paraphrase of what the user said>
```

One line per entry. Keep it terse. Quote the user's actual words where
possible. If the fact is a list (e.g. "our products are A, B, C"),
write one entry per item rather than cramming them into a single line.

**Guardrails (non-negotiable):**

1. **Only durable facts.** "I'm tired today" is not durable. "I prefer
   async communication" is. When in doubt, don't record.
2. **Deduplicate.** Read the file first. If the same fact (even
   paraphrased) is already there, don't append a duplicate.
3. **Never invent.** Only record what the user literally said or clearly
   implied. Do not embellish, extrapolate, or guess.
4. **Never edit existing entries.** Append only. If a fact changes, add
   a new dated entry noting the change — the file is an append-only log
   so readers can see evolution.
5. **One bucket per fact.** If a fact plausibly fits two categories, pick
   the more specific one. Do not cross-post.
6. **Privacy.** KB files may contain personal or org-sensitive info. If
   the user initializes a git repo inside `~/.flow/`, remind them to add
   `kb/` to `.gitignore`.
7. **When helping across many tasks**, read the KB files once per session
   and re-read only if you wrote new entries. They're not load-bearing
   for every turn — but they're load-bearing for "who is this user,
   what is their context" decisions.

**When to read the KB — lazy-load only:**

The KB files are **not** loaded at session start. The SessionStart hook
and the §9 bootstrap contract both explicitly skip them. Read a kb file
only when the current question in front of you actually needs that
category of fact. Signals that it's time to Read one:

- The user mentions a person, product, tool, or customer name and you
  don't know who/what they are → read `org.md` / `products.md` /
  `business.md` as appropriate.
- You're composing a task brief / project brief / progress note and
  want to reflect the user's working style accurately → read `user.md`.
- The user asks "how do we usually do X?" or "what's our convention
  for Y?" → read `processes.md`.
- A brief or CLAUDE.md uses terminology you don't recognize (e.g.
  an internal codename, a product term, a legacy component name) →
  read the relevant kb file for definitions.
- You're generating cross-cutting advice ("how should I approach
  this?") that would benefit from context about the user's role,
  organization, or product suite.

Signals it's NOT time to read the KB:

- The user ran a one-shot mutation command (`flow done`, `flow archive`,
  `flow priority`, `flow waiting`) and you're just relaying the result.
- The current task is purely mechanical and self-contained ("run the
  tests", "fix the obvious typo").
- You already read the relevant file earlier this session and nothing
  new has been written to it since.

**Read at most the specific file you need, not all 5.** If you need
`org.md` to identify someone, don't also preemptively Read the other
four. Load on demand, one file at a time.

**Writing (scoop mode) is still eager.** The lazy rule is for reading.
When you hear a durable fact, append it to the matching kb file
immediately — that doesn't require the file to have been loaded first.
Read-Write just means "load, check for duplicates, write" as a single
sequence at the moment the fact is heard.

**Auxiliary files in entity directories** (any `.md` files in
`tasks/<slug>/`, `projects/<slug>/`, or `playbooks/<slug>/` other than
`brief.md` and the contents of `updates/`) are surfaced by `flow show`
under an `other:` section. Apply the same lazy-load discipline as KB
files: load them on demand when relevant to the work, not preemptively.

### 4.11 Scope-creep detection (passive — surface via AskUserQuestion)

This is a **passive** workflow like §5.10 — you watch the session as it
unfolds and intervene only when the evidence is strong. When you
intervene, the surfacing mechanism is `AskUserQuestion` (never a
prose "want me to...?" question). Its purpose is to keep a task's
transcript and update log focused, instead of letting unrelated work
pile up under whichever task happens to own the current iTerm tab.

**When to consider firing:**

Fire when the *work itself* (not a single question) has clearly moved
off the bootstrapped task. Concretely, any of these is sufficient
evidence:

- You've made Edit/Write calls to files in a directory tree that isn't
  under `$FLOW_TASK`'s `work_dir` and isn't covered by its brief.
- You've spent two or more turns debugging a product, service, or
  repo that the brief doesn't mention.
- The user has introduced a new, named line of investigation ("while
  we're here, can you also look at <unrelated thing>") and begun
  giving it more than a single turn's attention.

**When NOT to fire (false positives to avoid):**

- A one-off tangential question answered in a single turn ("btw what
  does X mean?") — not drift, just curiosity.
- Reading/Read-tool usage outside the work_dir — reading is research,
  not work-migration. The trigger is write-side evidence.
- Natural debugging that requires touching nearby infrastructure the
  brief reasonably implies (e.g. a test helper in a sibling dir).
- The very first turn after session start — you don't yet know what
  "normal" looks like for this task.

**Recipe:**

1. When you notice drift per the signals above, pause your current
   work and use `AskUserQuestion`:

   ```
   AskUserQuestion({
     questions: [{
       question: "This looks unrelated to `<current-slug>` (<one-line drift description>). Want me to create a new flow task for it?",
       header: "New task?",
       options: [
         { label: "Yes, new task",  description: "Pause this work and run the §5.2 intake interview for <derived-name>" },
         { label: "No, stay here",  description: "Keep the work under <current-slug> — I understand this is still in scope" },
         { label: "Later",          description: "Note it in an update on <current-slug> and carry on for now" }
       ],
       multiSelect: false
     }]
   }))
   ```

2. **On "Yes, new task":** enter the §5.2 task-intake interview.
   Derive a task name from what you just observed (e.g. "Fix
   rate-limiter bug I stumbled on while reviewing PRs").
   Use the same project as `$FLOW_PROJECT` only if the new work
   genuinely belongs there; otherwise leave it floating or attach to
   a different project per the user's answer during intake. After
   the new task is saved, use `AskUserQuestion` (header:
   "Open it now?", options: "Yes, open it" / "No, keep in backlog")
   to offer `flow do <new-slug>` so the follow-on work gets its own
   transcript.

3. **On "No, stay here":** accept the user's judgement and continue.
   Consider this a signal to update your mental model of what the
   bootstrapped task includes — don't re-ask on the same thread of
   work in the same session.

4. **On "Later":** use `AskUserQuestion` (header: "Drop a note?",
   options: "Yes, save a drift note" / "No, just continue") to offer
   writing a short progress note on the current task capturing the
   drift observation ("noticed X while doing Y; may need its own
   task"), then continue with the original work.

**Why this lives in the skill, not the hook:** the hook's only
guaranteed side-effect is injecting text at session start. Detection
requires inspecting what you've done this session (edits, debugging
topics) — that state only exists inside the running conversation. The
hook's job is to make sure the skill is loaded; the skill is what
runs the check.

**Note:** "the bootstrapped task" includes playbook-run tasks. The
triggers and recipe are identical for playbook-run sessions —
edits/debugging that drift outside the playbook's scope warrant the
same prompt.

### 4.12 Add a playbook

**Triggers:** "add a playbook", "create a playbook for X",
"track this as a playbook", "this is something I'll re-run".

**The interview is the whole point** (same philosophy as §4.2 task intake — you interview, then write down what the user said; you do NOT solution during intake).

**Sections to ask, ONE AT A TIME, in this order:**

1. **What?** One sentence describing what each run does.
2. **Why?** Why this playbook exists and what value it produces.
3. **Where?** Work_dir for runs (use §6 recipe).
4. **Each run does** — concrete steps every invocation performs. Bullet
   form. Replaces "Done when" from task intake.
5. **Out of scope?** Non-goals. Optional.
6. **Signals to watch for** — observable conditions that should change
   the run's behavior or trigger an escalation. Replaces "Open
   questions" — playbooks have long lifespans so prospective signals
   matter more than open questions.

**Then before calling `flow add playbook`:**

- Suggest 2-3 slug candidates via `AskUserQuestion` (header:
  "Pick a slug", one option per candidate plus "Other" for custom).
- Project attachment via `AskUserQuestion` (header: "Attach to?",
  one option per existing project plus "None (floating playbook)").
  Skip the question if there are no projects.
- `--mkdir` if work_dir doesn't exist — use `AskUserQuestion`
  (header: "Create dir?", options: "Yes, create it" / "No, fix the
  path") same as §6 step 6.

**Draft the brief, show to the user**, then use `AskUserQuestion`
(header: "Brief", options: "Save it" / "Revise") to confirm. Do not
run `flow add playbook` until the user picks "Save it". Then run it
and overwrite the stub `brief.md` with the full content. Use the
playbook brief template from §7.

After save, use `AskUserQuestion` (header: "Run it now?", options:
"Run it now" / "Just save the definition") to offer the first run.
On "Run it now", proceed to §4.13. On "Just save the definition",
stop.

### 4.13 Run a playbook

**Triggers — any of these means "run `flow run playbook <slug>`":**
- "run the X playbook" / "trigger X" / "fire the X playbook"
- "fire the X agent" (legacy term users may use — playbook is the canonical name)
- "start a run of X" / "kick off X"
- A bare `flow run playbook X` typed as command

**Recipe:**

1. Ask session-mode (Regular vs Skip permissions) via AskUserQuestion —
   reuses the §4.4 pattern. Skip if the user already specified.
2. Run: `flow run playbook <slug>` (with `--dangerously-skip-permissions`
   if chosen).
3. The command creates a kind=playbook_run task, snapshots the brief,
   and spawns an iTerm tab. The new tab will boot the flow skill via its
   bootstrap prompt and execute against the snapshotted brief.

**Anti-pattern (per §8):** never auto-fire. Manual trigger only. Even if
the user mentions a playbook name in passing, do not run it without an
explicit verb ("run", "trigger", "fire", "start").

### 4.14 Substantive-unrelated-work check (passive, ongoing)

This is a **passive workflow** that runs alongside every other workflow.
It fires when substantive work emerges that doesn't belong to the
current task binding.

**Triggers (any one is enough):**

- In a **dispatch session** (FLOW_TASK unset):
  - You've been in active design / brainstorming / debugging
    discussion for ≥ 2 turns about a concrete topic, OR
  - You've made any Edit/Write tool calls, OR
  - You've invoked a process skill (`superpowers:brainstorming`,
    `superpowers:writing-plans`, `superpowers:executing-plans`,
    `superpowers:systematic-debugging`,
    `superpowers:test-driven-development`) — a process-skill invocation
    is itself a substantive-work signal.
- In a **bound session** (FLOW_TASK set): same triggers as §4.11
  (work moved off the bootstrapped task's scope).

**NOT a trigger:**

- One-off question answered in a single turn.
- Reading files / running queries to inform an answer.
- The very first message after session start (you don't yet know if
  this is one-off or substantive).

**Recipe:**

1. Pause current work.
2. Run `flow list tasks --status in-progress` and
   `flow list tasks --status backlog --priority high` to see candidates.
3. Use AskUserQuestion to offer three options:
   - **Create a new flow task** for this work (run §4.2 minimal intake,
     then optionally `flow do <new-slug>`).
   - **Switch to an existing task** (list candidates as options;
     on selection, spawn `flow do <slug>`).
   - **Proceed ad-hoc** (user accepts no resumability, no context
     accumulation).

**Process-skill ordering:** when a process skill triggers this check,
load the skill first (so the user sees the right tool engage), then
**before** taking the skill's first concrete action, run the check.
If the user picks "create new task" or "switch to existing task," the
process skill resumes inside the new session, not this one.

**Important: this is an ongoing check, not one-shot.** Re-evaluate the
triggers each turn — especially when transitioning into design /
implementation / debugging work. The SessionStart hook gets you the
first check; you are responsible for every subsequent check.
Re-evaluate on every turn.

## 6. The `work_dir` question — rules

When you're about to ask the user "where does this task live?", run
these steps BEFORE asking, so the question is informed:

1. **Run `flow workdir list`.** Fuzzy-match the task name against
   registered nicknames and paths. If you get an obvious match (e.g.
   task "Add OAuth to budgeting-app" and a registered workdir named
   `budgeting-app`), propose that path via `AskUserQuestion` (header:
   "Use this path?", options: "Yes, use `<path>`" / "Pick a different
   path"). On "Pick a different path", continue to step 2.
2. **If no local match, check GitHub via `gh`.** Run `gh repo list
   --limit 50 --json name,owner,description`. If any repo name or
   description plausibly matches the task, present the top 3 via
   `AskUserQuestion` (header: "Which repo?") with one option per
   candidate (label = `<repo-name>`, description = repo description)
   plus a "None of these — use a path instead" option. If the user
   picks a repo, offer (via `AskUserQuestion`, header: "Clone it?",
   options: "Yes, clone to `~/code/<name>`" / "No, I'll handle it")
   to run `gh repo clone <owner>/<repo> ~/code/<name>` and, after
   clone, run `flow workdir add ~/code/<name>` so next time it's a
   local match.
3. **If `gh` isn't authenticated** (command errors with an auth
   message), fall back gracefully via `AskUserQuestion` (header:
   "GitHub unreachable", options: "Give me a path" / "Make it
   floating"). On "Give me a path", prompt the user for an absolute
   path (this single text input is fine — there are no enumerable
   options). On "Make it floating", skip work_dir entirely.
4. **If the user wants a floating task** (no repo), skip the question
   entirely and let `flow add task` auto-create
   `~/.flow/tasks/<slug>/workspace/`.
5. **Never guess a path.** Don't invent `~/code/foo` because the task
   name sounds like "foo". Always confirm via `AskUserQuestion`.
6. **If the path doesn't exist**, use `AskUserQuestion` (header:
   "Create dir?", options: "Yes, create it" / "No, fix the path")
   to ask whether to pass `--mkdir`. On "Yes", append `--mkdir` to
   the `flow add task` invocation. On "No", loop back to ask for a
   corrected path.

## 7. The task brief format

Use this as a literal template when writing `brief.md` files. Section
headings are fixed; content is whatever came out of the interview.

```markdown
# <task name, verbatim>

## What
<one sentence from the interview, no editorializing>

## Why
<short paragraph capturing the user's reason>

## Where
work_dir: <absolute path>

## Done when
- <bullet 1 from acceptance criteria>
- <bullet 2>
- <bullet 3>

## Out of scope
- <non-goal 1>

## Open questions
- <question 1>
- <question 2>

---
*Before you start on this task, read CLAUDE.md in the work_dir and any
nested CLAUDE.md files in the subtree you plan to modify. Then read
every file under `updates/` (if any exist) to catch up on prior
progress.*
```

**Thin task brief (intake-minimal):**

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

A section is "deferred" if its body is the literal string
`*Deferred — fill in at task start.*` or `*Deferred*`. The bootstrap
session detects this and offers the user a deferred-section prompt
(§9).

If a section has no content, leave the heading with an italic "none"
underneath. Don't omit headings — the parallel structure makes the
briefs scannable.

Projects use a shorter template: `What / Why / Where / Scope`. No
"Done when", no "Open questions" (projects are ongoing).

**Playbook brief template:**

```markdown
# <name>

## What
<one sentence describing what each run does>

## Why
<short paragraph>

## Where
work_dir: <absolute path>

## Each run does
- <step 1>
- <step 2>
- <step 3>

## Out of scope
- <non-goal 1>

## Signals to watch for
- <signal 1>

---
*Run with `flow run playbook <slug>`. Each run gets its own session
and a snapshot of this brief at run time. Editing this file does not
retroactively change past runs.*
```

Notes:
- No "Done when" — playbooks are never done.
- "Each run does" replaces "Done when" as the action-oriented section.
- "Signals to watch for" replaces "Open questions" — playbooks are
  long-running, so the relevant prospective concern is signals to
  notice and respond to, not open questions to resolve.

## 8. Anti-patterns — do NOT do these

**Confirmation method:** every confirmation in this section means
`AskUserQuestion`, not a prose question that buries the choice. The
tool produces clickable options; prose questions force the user to
type. Always prefer the tool. If you find yourself typing "Want me
to X?" or "Should I Y?" into chat, stop and use `AskUserQuestion`
instead.

- **Do not invent context.** If the user says "add a task for the
  budgeting thing", ASK what the budgeting thing is (via
  `AskUserQuestion` if you can list candidates from existing tasks /
  workdirs; otherwise a plain prose clarifying question is fine —
  open-ended "what is this thing?" is not an enumerable choice).
  Don't write a brief based on your prior-session memory of
  budgeting apps.
- **Do not propose solutions during intake.** The user is telling you
  what they want to do, not asking for your opinion on how to do it.
  "What" is one sentence, "Why" is the reason. Neither section is a
  design doc. If you start drafting implementation steps during `flow
  add task`, stop.
- **Do not silently switch tasks.** If `$FLOW_TASK` is set and the user
  starts talking about a different one, confirm via `AskUserQuestion`
  (header: "Switch task?", options: "Yes, switch to `<other-task>`" /
  "No, stay on `<current-task>`"). Don't assume.
- **Do not mark tasks done without explicit confirmation.** Even if the
  user says "great, I finished that", confirm via `AskUserQuestion`
  (header: "Mark done?", options: "Yes, `flow done <slug>`" / "No,
  not yet") and wait for the click.
- **Do not hand-edit `session_id` or any other DB field.** Never edit
  `flow.db` directly, never instruct the user to. The only supported
  mutations are `flow` commands.
- **Do not retry a `flow` command that errored.** Read the error, relay
  it to the user, and ask. In particular, do not loop `flow do X` → see
  "multiple matches" → guess one → run again. Ask.
- **Do not bundle multiple saves into one `flow add task` call.** One
  task per interview. If the user mentions three things they want to
  track, run the interview three times (or ask to batch and then do it
  explicitly with user consent).
- **Do not skip the interview on "quick adds".** Even when the user
  says "just add a task for X, nothing fancy", ask at minimum: What?
  Why? Where? You can compress the other sections to "TBD" if they
  push back, but `What/Why/Where` are non-negotiable.
- **Do not overwrite an existing `brief.md` without checking what's
  there.** `flow add task` writes a stub. You overwrite that stub.
  But if a brief.md already has real content (e.g., the user edited it
  between add and your Write call), Read it first, merge thoughtfully,
  and confirm with the user before writing.
- **Do not forget to offer progress notes.** After a long working
  session, the user will forget to log what they did. At natural
  breakpoints, proactively use `AskUserQuestion` (header:
  "Save note?", options: "Yes, save a note" / "No, skip it") to
  prompt — never a prose "want me to save a note?" question.
- **Do not silently continue scope-drifted work under the bootstrapped
  task.** When the work genuinely moves off `$FLOW_TASK` (new repo,
  new product, new line of investigation sustained over multiple
  turns — see §5.11 for signals), surface the drift via
  `AskUserQuestion` and offer to branch into a new task. Letting
  unrelated work accumulate under the wrong task poisons that task's
  transcript and buries decisions the user will later want to find.
- **Do not auto-fire `flow run playbook`.** Playbooks are
  manual-trigger only. Even if a user mentions a playbook by name in
  passing, do NOT run it without an explicit verb ("run", "trigger",
  "fire", "start").
- **Do not edit a run-task's `brief.md` to change the playbook's
  behavior for future runs.** That brief is a frozen snapshot. To
  change behavior, edit the playbook's `brief.md` and start a new
  run.
- **Do not propose scheduling during playbook intake.** Scheduled
  invocation is out of scope for v1; playbooks are manual.

## 9. The execution-session bootstrap contract

When `flow do <task>` spawns a Claude session in a new iTerm tab, it
pre-allocates a UUID, writes it to `tasks.session_id` before spawning,
and passes it to `claude --session-id <uuid>`. This makes the session's
jsonl file appear at the deterministic path
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`. There is no
self-registration step — the DB is authoritative from the moment the
tab opens.

Subsequent `flow do <same-task>` calls read that UUID and spawn
`claude --resume <uuid>` to continue the same conversation.

**If you are the execution session spawned by `flow do`:**

Do ALL of the following in order, before touching any code or
proposing any plan:

1. **Invoke the flow skill via the `Skill` tool.** The `flow hook
   session-start` output already names this step, but the hook is
   belt-and-braces — the Skill tool is the authoritative way to load
   the operating manual that governs workflows, KB discipline, and
   scope-creep detection.

2. **Load the task context:**
   ```
   flow show task
   ```
   From its output, use the `Read` tool on:
   - The file at the `brief:` path (the task brief — the problem
     statement the user captured when creating this task).
   - Every file listed under `updates:` (prior progress notes, in
     chronological order — skim for blockers and decisions).

   **Do NOT read the `kb:` files at bootstrap.** They're lazy-loaded
   on demand — see §5.10 for when to actually Read them.

   **If `flow show task` indicates `kind: playbook_run`:** also run
   `flow show playbook <playbook-slug>` first (for context: the playbook's
   intent and recent runs). Note any files under its `other:` section —
   they're sidecar references you can load on demand. Then read your
   task's `brief.md` — that's the snapshot taken when this run started,
   and it's your authoritative instructions. The playbook's live
   `brief.md` may have evolved since; you don't need to re-read it.

   **Files listed under `other:`** in any `flow show` output (task,
   project, or playbook) are sidecar references — research notes, decision
   trees, design docs, etc. dropped into the entity's directory. Do **not**
   read them eagerly. Read them on demand when something in the brief, in
   user input, or in the work makes them relevant. This matches the
   lazy-load principle for KB files (§5.10 in the skill, §4.10 in the
   section numbering).

3. **Load the parent project context, if any.** If `flow show task`
   printed a `project:` line that isn't `(floating)`, run:
   ```
   flow show project <project-slug>
   ```
   (or just `flow show project` — it defaults to `$FLOW_PROJECT`).
   From its output, use `Read` on:
   - The file at its `brief:` path (the project brief — overarching
     context, goals, scope shared across sibling tasks).
   - Every file listed under its `updates:` (project-level progress
     notes — often capture cross-task decisions and blockers that
     matter for your task even if your task's own updates don't
     mention them).

   Again, skip the project's `kb:` section at bootstrap.

4. **Load repo conventions.** Read `CLAUDE.md` in your `work_dir` (if
   present), plus any nested `CLAUDE.md` files under subdirectories
   you plan to modify. These are authoritative for build commands,
   test commands, style, and gotchas — they override any assumption
   you might make from the brief.

5. **Only then begin work.** If any brief section is blank or
   unclear, ASK the user before inferring. If the user didn't
   specify a "Done when" in the brief, confirm acceptance criteria
   with them before making changes.

**Throughout the session**, watch for new KB-worthy facts per §5.10 and
append them to the matching `kb/*.md` file on the fly — no permission
needed, no interview required. Just write and quietly note what you
recorded. And lazy-read any kb file when you hit a question that
actually needs that context — not before.

### Deferred-section prompt

If any section body in your brief is the literal `*Deferred — fill in at
task start.*` or `*Deferred*`, pause before doing any work and offer the
user (via AskUserQuestion):

- **Fill in now** — run a mini-§4.2 interview for just the missing
  sections (Why, Done when, Out of scope, Open questions). Save the
  filled-in brief by overwriting the existing `brief.md`.
- **Skip — proceed** — accept that scope is implicit. Reasonable for
  small/known tasks.

This shifts the intake burden from intake-time to task-start-time, where
the user has more context.

Applies only to regular tasks (kind=regular). Playbook-run briefs are
snapshots and should not be edited; if the live playbook brief had
deferred sections, those should have been resolved at playbook intake.

### Cross-task context via transcripts

If you need to understand what happened in a sibling task's session
(e.g. a prior task under the same project made decisions that affect
yours), use:

```
flow transcript <sibling-task-slug>
```

This outputs a readable conversation transcript from that task's Claude
session — user messages, assistant messages, tool calls, and results.
Use `--compact` to omit tool results and thinking blocks for a shorter
overview. Pipe through `grep` or `head` if the full transcript is too
long to read at once.

**When to use:** When the brief and updates for a sibling task don't
give you enough context, or when you need to understand specific
implementation decisions made during that task's session.

### Manual repair — `flow update task`

Escape hatch when the DB drifts from reality. Two fields can be
corrected:

```
flow update task <ref> [--session-id <uuid>] [--work-dir <path>] [--mkdir]
```

When to use:

- **`--session-id <uuid>`** — the user spawned a Claude session outside
  `flow do` (e.g. plain `claude` in a terminal) and wants flow to track
  that session going forward. Or: a session jsonl was restored from a
  backup under a different UUID and they want `flow do` to resume it.
  UUID must be v4 format — claude enforces that on `--session-id`.
- **`--work-dir <path>`** — the repo moved on disk (renamed parent,
  moved between drives, cloned to a new path). Pass `--mkdir` if the
  new path doesn't exist yet.

Both flags are optional but at least one must be given. This is a
manual correction tool — do not run it as a workaround for a bug in
`flow do`; surface the bug instead.

## 10. Environment variables flow sets

When `flow do <task>` spawns an iTerm tab, it attaches these env vars
to the `claude` process (inline on the command line — they do NOT
persist in the tab's shell after claude exits):

- `FLOW_TASK=<task-slug>` — the current task
- `FLOW_PROJECT=<project-slug>` — the current project, if the task has one

Use these as defaults:

- `flow show task` with no argument reads `$FLOW_TASK`.
- `flow show project` with no argument reads `$FLOW_PROJECT`.
- When saving a progress note, assume the current task is `$FLOW_TASK`
  unless the user says otherwise.

If you're in a session where these aren't set (e.g. a dispatch session
the user opened manually), always ask which task/project they mean.

## 11. When in doubt

Ask. The worst outcome is writing a bad brief or silently mis-attributing
a progress note. The second-worst outcome is running `flow do` on the
wrong task. Both are avoided by one clarifying question. The user's time
budget for a clarifying question is vastly lower than their budget for
fixing a wrong save after the fact.

In a dispatch session (FLOW_TASK unset), also re-check §4.14
(substantive-unrelated-work) on every turn. The skill is responsible
for ongoing detection; the SessionStart hook is only a one-shot
trigger.
