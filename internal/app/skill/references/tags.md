# Tagging tasks

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.16a Tagging tasks

**Triggers:** "tag this task as X", "add a tag X to <task>", "what
tags does <task> have", "show all tags", "list my tags", "what tags
are in use", "find all tasks tagged X".

**What tags are:** free-form single-string labels attached to tasks
for cross-cutting identification — `#frontend`, `#urgent`,
`#tech-debt`, `#h2-2026`, `#triage`, `#research`. Stored normalized
(lowercase, trimmed). Many-to-many: a task can have any number of
tags, a tag can be on any number of tasks. Tags are *single strings*
— if you want kv-style semantics (`type:bug`, `priority:p0`), use a
`key:value` colon convention inside the string. Don't introduce a
parallel kv schema.

**The vocabulary discipline rule:** before suggesting a new tag for
a task, ALWAYS run `flow list tags` first. That command lists every
tag in use across non-archived tasks with a per-tag task count. Reuse
existing tags whenever they fit — the user's tag vocabulary is more
useful when it stays consistent. Inventing a synonym (e.g.
`#frontend` when `#ui` already has 8 tasks) fragments the tag space
and makes filtering useless.

**Recipe — add tags:**

1. Run `flow list tags` and read the output. Note any existing tag
   that matches the user's intent.
2. If a good match exists, propose it via AskUserQuestion (header:
   "Use existing tag?", options: existing-tag candidates + "Use a
   new tag"). Skip this step if the user already named the exact
   tag.
3. Run `flow update task <ref> --tag <tag1> --tag <tag2> ...`.
   `--tag` is repeatable — pass it once per tag value. Tags are
   normalized to lowercase + trimmed; idempotent on duplicates.

**Recipe — remove or clear:**

- `flow update task <ref> --remove-tag <tag1> --remove-tag <tag2>`
  removes specific tags (also repeatable).
- `flow update task <ref> --clear-tags` removes all tags from a task.
  Confirm via AskUserQuestion (header: "Clear all tags?", options:
  "Yes, clear all" / "No, name specific tags") before mutating —
  clearing is destructive and per §8 every state mutation deserves
  a click.
- `--clear-tags` and `--remove-tag` are mutually exclusive (clear
  removes everything anyway). `--clear-tags --tag <new>` is allowed
  and means "wipe and replace with `<new>`" — useful for retagging.

**Recipe — find tasks by tag:**

- `flow list tasks --tag <tag>` filters the task list to that tag.
  Combine with `--status`, `--project`, `--priority`, etc.

**Display:** list/show output renders tags as `#tag1 #tag2` tokens
trailing the row. The hashtag prefix is render-only — do not type
`#` into `--tag` values (it would be normalized away, but treat the
rule as "tag values are unprefixed strings").

**Anti-patterns:**

- **Do not invent new tags without checking `flow list tags` first.**
  The vocabulary discipline rule isn't optional.
- **Do not use kv-style alternative storage.** Single strings with
  `key:value` convention are the canonical form.
- **Do not auto-tag.** Always confirm with the user before adding
  tags they didn't explicitly name. The exception is when the user's
  request literally names the tag ("tag this `#frontend`").
