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
- If the command **errors** with a message about a missing database or
  `~/.flow`: the user hasn't run `flow init` yet. Tell them:
  "flow isn't initialized yet — run `flow init` to set up `~/.flow/`
  and the database." Then, once `flow init` succeeds, enter the
  **first-run coaching** below.

Do **not** silently run `flow init` for the user — let them run it so
they see what it creates.

### First-run coaching

After `flow init` succeeds for a brand-new user, walk them through the
basics in this order:

1. **Explain what just happened.** "`flow init` created `~/.flow/` with
   an empty database and 5 knowledge-base files."

2. **Create their first project.** "Let's set up a project — what's the
   main thing you're working on right now?" Then enter the §5.3
   add-project interview. This gets them a project and at least one task
   immediately.

3. **Show how to start work.** After the first task exists, offer:
   "Run `flow do <slug>` to open a dedicated Claude session for this
   task. That session gets the brief, updates, and repo conventions
   automatically."

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

Sessions
  flow do               <ref> [--fresh] [--dangerously-skip-permissions]
  flow done             <ref>
  flow register-session [<slug>] [--force]    (execution session's first action — self-report session_id)

Read
  flow show task    [<ref>]     (defaults to $FLOW_TASK)
  flow show project [<ref>]     (defaults to $FLOW_PROJECT)
  flow list tasks    [--status backlog|in-progress|done] [--project <slug>]
                     [--priority high|medium|low] [--since today|monday|7d|YYYY-MM-DD]
                     [--include-archived]
  flow list projects [--status active|done] [--include-archived]

Edit / mutate
  flow edit      <ref>           opens brief.md in $EDITOR, bumps updated_at
  flow priority  <ref> high|medium|low
  flow waiting   <ref> "<who or what>"
  flow waiting   <ref> --clear
  flow archive   <ref>
  flow unarchive <ref>

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
5. Ask which one the user wants to pick up, or offer to add a new task
   if none of the listed ones match their intent.

Do not auto-run `flow do` after listing. Wait for the user to pick.

### 4.2 Add a task — INTERVIEW MODE (mandatory)

**Triggers:** "add a task", "new task", "track this work", "let me add a
flow task for X".

**The interview is the whole point.** The skill's value vs. "just run
`flow add task`" is that you interview the user before saving. You NEVER
solution during intake. You NEVER fill blanks with guesses. If a section
is unclear, ask. If the user says "I don't know yet", write "Open
question: ..." in the brief and move on.

**Sections to ask, ONE AT A TIME, in this order:**

1. **What?** One sentence describing the work. "Add OAuth login to the
   budgeting app." Not: "Set up Passport.js with Google as the provider
   and switch the login form to..." — that's solutioning. Just capture
   the user's one-sentence framing.
2. **Why?** Why is this worth doing now? Business reason, user pain,
   self-motivated curiosity — whatever the user actually says. If they
   say "just because", write "Because Rohit wants to." Don't editorialize.
3. **Where?** Which codebase or filesystem location. This is where the
   `work_dir` question happens — see §6 for the full recipe.
4. **Done when?** Concrete acceptance criteria. Bullet form. "Users can
   sign in with Google", "session persists across reloads", "docs
   updated". If the user gives a single fuzzy answer, ask for one more
   bullet to sharpen it.
5. **Out of scope?** Explicit non-goals. "Not rebuilding the sign-up
   flow." "Not touching the mobile app." This is optional — if the user
   has nothing here, leave it empty.
6. **Open questions?** Things the user isn't sure about and wants to
   decide later. Write them literally.

**Then, BEFORE calling `flow add task`:**

- **Ask for a short slug.** "What short slug do you want for this task?
  Something you'll type to resume it — e.g. `caas`, `auth-bug`,
  `billing`." Pass it as `--slug <s>`. If the user declines, omit the
  flag and an auto-generated slug will be used (truncated to ~6 words).
- Ask about project attachment. "Is this part of an existing project? I
  see `<project-a>`, `<project-b>` in `flow list projects`..." If yes,
  pass `--project <slug>`. If no, omit (floating task).
- Ask about priority if not obvious. Default is `medium`.
- Ask about `--mkdir` if the `work_dir` doesn't exist yet.

**Draft the brief. Show it to the user. Wait for explicit confirmation**
("save it", "looks good", "yes") before running `flow add task`. If the
user asks for changes, revise the draft inline and show it again.

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

Finally, offer: "Want me to open it now with `flow do <slug>`?" If yes,
run `flow do <slug>`. If no, stop — the task is in backlog and the user
can pick it up later.

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
   declined), offer `flow do <first-task>` if they want to start
   immediately.

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

1. **Ask the user which session mode they want** before running anything:
   - **Regular** — normal Claude session (default, safer)
   - **Skip permissions** — passes `--dangerously-skip-permissions`
     (faster, no tool-approval prompts)
   Present it concisely, e.g.: "Regular or skip-permissions session?"
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
3. **Show the filename and the content to the user. Wait for
   confirmation** ("save it", "yes"). Do not write silently.
4. Determine the task slug from `$FLOW_TASK` (usually set in the current
   iTerm tab's env) or by asking the user, or — if the user named the
   task in the request — by running `flow show task <that-ref>` to get
   the canonical slug.
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
"no longer waiting on X". Action: `flow waiting <task> --clear`.

Do not infer the task slug silently — use `$FLOW_TASK` if set, otherwise
ask "which task is this for?" and pass the user's answer as the exact
alias or slug.

### 4.7 Mark done

**Triggers:** "mark X done", "finish X", "X is done", "close out X".

**Recipe:** run `flow done <ref>`. **Do not close the iTerm tab** and
**do not kill the Claude session** — `flow done` deliberately leaves
both intact. The session_id stays on the task row so a future reopen can
still resume it.

Before running `flow done`, if the user hasn't just saved a progress
note, offer: "want me to save a closing note first?" If yes, run the
§5.5 recipe first, then `flow done`.

### 4.8 Archive / cleanup

**Triggers:** "archive X", "clean up", "clean up my done tasks", "hide
finished work".

**Recipe:**

- Single task/project: `flow archive <ref>`.
- Bulk "archive everything done": run `flow list tasks --status done`.
  Show the list to the user. Confirm each one unless the user said
  "archive all done" explicitly — in that case, iterate and archive
  them all, printing each action as you go.
- If the user regrets it: `flow unarchive <ref>`.

Archive never deletes files on disk — brief.md and updates/ remain. Make
sure the user knows this so they don't worry about losing notes.

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
  "blueprint", "spec field", "output interface") → read the relevant
  kb file for definitions.
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

## 6. The `work_dir` question — rules

When you're about to ask the user "where does this task live?", run
these steps BEFORE asking, so the question is informed:

1. **Run `flow workdir list`.** Fuzzy-match the task name against
   registered nicknames and paths. If you get an obvious match (e.g.
   task "Add OAuth to budgeting-app" and a registered workdir named
   `budgeting-app`), propose that path as the default: "Looks like
   `<path>` — is that right?"
2. **If no local match, check GitHub via `gh`.** Run `gh repo list
   --limit 50 --json name,owner,description`. If any repo name or
   description plausibly matches the task, propose the top 3 as
   candidates: "On GitHub I see `<repo-a>`, `<repo-b>`, `<repo-c>` —
   any of these?" If the user picks one, offer `gh repo clone
   <owner>/<repo> ~/code/<name>` and, after clone, run `flow workdir
   add ~/code/<name>` so next time it's a local match.
3. **If `gh` isn't authenticated** (command errors with an auth
   message), fall back gracefully: "I couldn't reach GitHub — want to
   just give me an absolute path?"
4. **If the user wants a floating task** (no repo), skip the question
   entirely and let `flow add task` auto-create
   `~/.flow/tasks/<slug>/workspace/`.
5. **Never guess a path.** Don't invent `~/code/foo` because the task
   name sounds like "foo". Always confirm with the user.
6. **If the path doesn't exist**, offer `--mkdir`: "That directory
   doesn't exist. Want me to pass `--mkdir` so `flow` creates it?"

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

If a section has no content, leave the heading with an italic "none"
underneath. Don't omit headings — the parallel structure makes the
briefs scannable.

Projects use a shorter template: `What / Why / Where / Scope`. No
"Done when", no "Open questions" (projects are ongoing).

## 8. Anti-patterns — do NOT do these

- **Do not invent context.** If the user says "add a task for the
  budgeting thing", ASK what the budgeting thing is. Don't write a brief
  based on your prior-session memory of budgeting apps.
- **Do not propose solutions during intake.** The user is telling you
  what they want to do, not asking for your opinion on how to do it.
  "What" is one sentence, "Why" is the reason. Neither section is a
  design doc. If you start drafting implementation steps during `flow
  add task`, stop.
- **Do not silently switch tasks.** If `$FLOW_TASK` is set and the user
  starts talking about a different one, confirm: "Are we switching to
  `<other-task>`?" Don't assume.
- **Do not mark tasks done without explicit confirmation.** Even if the
  user says "great, I finished that", confirm: "Want me to `flow done
  <slug>`?" and wait.
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
  session, the user will forget to log what they did. Proactively offer
  "want me to save a note before we stop?" at natural breakpoints.

## 9. The execution-session bootstrap contract

When `flow do <task>` spawns a fresh Claude session in a new iTerm tab,
it does NOT pre-allocate a session_id. The DB row's `session_id` stays
`NULL` until the new session self-reports. Under this contract, the
execution session's **first action**, before anything else, is:

```
flow register-session
```

That command:

1. Reads `$FLOW_TASK` (set by `flow do` when spawning the tab).
2. Looks up the task's `work_dir`.
3. Scans `~/.claude/projects/<encoded-cwd>/*.jsonl` for the newest file —
   the session's own jsonl, currently being written.
4. UPDATEs `tasks.session_id` with that file's UUID.

Subsequent `flow do <same-task>` calls will see the now-populated
`session_id` and spawn `claude --resume <uuid>` instead of a fresh tab.

**If you are the execution session spawned by `flow do`:**

Do ALL of the following in order, before touching any code or
proposing any plan:

1. **Self-register your session_id** (first action, always):
   ```
   flow register-session
   ```
   No arguments — it uses `$FLOW_TASK`. This writes your session UUID
   to the DB so future `flow do` calls can `claude --resume` you.
   If it fails with "no *.jsonl found", run `sleep 1 && flow
   register-session` once and try again. If it still fails, warn the
   user and continue — the session works, it just won't be resumable.
   If it says "session already registered", another `flow do` won the
   race; your tab is a duplicate, ask the user whether to close it.

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

## 10. Environment variables flow sets

When `flow do <task>` spawns an iTerm tab, it exports:

- `FLOW_TASK=<task-slug>` — the current task
- `FLOW_PROJECT=<project-slug>` — the current project, if the task has one

Use these as defaults:

- `flow show task` with no argument reads `$FLOW_TASK`.
- `flow show project` with no argument reads `$FLOW_PROJECT`.
- `flow register-session` with no argument reads `$FLOW_TASK`.
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
