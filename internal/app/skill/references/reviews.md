# Archive / cleanup & weekly review

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

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
