# Cross-task transcripts & field edits

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### Cross-task context via transcripts

If you need to understand what happened in a sibling task's session
(e.g. a prior task under the same project made decisions that affect
yours), use:

```
flow transcript <sibling-task-slug>
```

This outputs a readable conversation transcript from that task's harness
session — user messages, assistant messages, tool calls, and results.
Use `--compact` to omit tool results and thinking blocks for a shorter
overview. Pipe through `grep` or `head` if the full transcript is too
long to read at once.

**When to use:** When the brief and updates for a sibling task don't
give you enough context, or when you need to understand specific
implementation decisions made during that task's session.

### Parent links: tasks spun off from a session

`flow add task` run from a session bound to a task records that task
as the new task's parent (`tasks.parent_slug`) and prints `(parent:
"<slug>")`. Every task a bound session creates is linked; tasks created
from an unbound (dispatch) session have no parent. `flow show task`
prints `parent:` on the child and `spun_off:` (comma-separated child
slugs) on the parent. Use it to trace where a task came from, or to
find the follow-ups a task fanned out.

### Where a new task's work_dir comes from

`flow add task` picks the work_dir in this order and prints it with its
source:

- `--work-dir X` uses X (`--mkdir` creates X if missing).
- `--mkdir` alone gives the task a fresh `~/.flow/tasks/<slug>/workspace`,
  ignoring the project's dir. Use it for scratch or delegated work that
  must not run inside the parent's repo.
- Neither: the project's work_dir (the project is inherited from the
  parent task in a bound session), else a fresh workspace.

### Field edits — `flow update task` / `flow update project`

`flow update task` is the canonical lane for in-place field edits on
a task. `flow update project` is the same for project rows (priority
only, for now). All field setters live here — there are no per-field
mini-commands like `flow priority` / `flow due` / `flow waiting` /
`flow assignee` (those used to exist; they were folded into update).

```
flow update task <ref>
    [--work-dir <path>] [--mkdir]
    [--status backlog|in-progress|done]
    [--priority high|medium|low]
    [--assignee <name>] [--clear-assignee]
    [--due-date <date>]   [--clear-due]
    [--waiting "<who or what>"] [--clear-waiting]
    [--tag <t> ...] [--remove-tag <t> ...] [--clear-tags]
    [--project <slug>] [--clear-project]   (moves the task; work_dir unchanged)

flow update project <ref>
    [--priority high|medium|low]
```

When to use which flag:

- **`--work-dir <path>`** — the repo moved on disk (renamed parent,
  moved between drives, cloned to a new path). Pass `--mkdir` if the
  new path doesn't exist yet.
- **`--status <s>`** — primary use case is rolling a `done` task back
  to `in-progress` so `flow do` will reopen it (the do-from-done path
  is gated). Also handy for in-progress → backlog to "demote" a task
  you're not actively working on. Setting backlog → in-progress on a
  task with NULL session_id errors with a pointer at `flow do` /
  `flow do --here` — those are the only paths that attach a session,
  and the session-id invariant requires one for any non-backlog
  status. Setting status to a value it already has is a no-op.
- **`--priority <p>`** — change a task or project priority. Same enum
  as creation: high|medium|low.
- **`--assignee <name>` / `--clear-assignee`** — set or clear the task
  assignee. Convention: NULL = "self" (default); any other value =
  "assigned to that name". The list/show output surfaces the assignee
  only when it's non-null.
- **`--due-date <date>` / `--clear-due`** — set or clear the due date.
  Date formats: `YYYY-MM-DD`, `today`, `tomorrow`, weekday names, `Nd`.
- **`--waiting "<X>"` / `--clear-waiting`** — set or clear the
  `waiting_on` freeform note (see §4.6). Status stays in-progress;
  the note is just there to remind the user.

There is **no** `--session-id` flag. The session_id is owned by
`flow do` / `flow do --here`; manual rewriting was a foot-gun
(silent overwrite of an existing binding) and the lane is gone. Use
`flow do --here <slug>` from inside the session you want to bind.

At least one field-changing flag must be given. `--work-dir` is an
escape hatch — do not run it as a workaround for a bug in `flow do`;
surface the bug instead.
