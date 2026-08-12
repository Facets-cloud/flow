# Bind an in-flight session to a task

> Loaded on demand from the flow skill's resident core (SKILL.md).

### 4.16 Bind an in-flight harness session to a task

**Triggers:** "bind this session to <task>", "track this session under
<task>", "attach this conversation to <task>", or "this session is for
<task>".

Run:

```
flow do --here <slug>
```

Flow detects the active harness automatically: Claude Code exposes
`$CLAUDE_CODE_SESSION_ID`; Codex exposes `$CODEX_THREAD_ID`. It validates the
identifier, writes it to `tasks.session_id`, pins `tasks.harness`, and moves
the task to `in-progress`. No terminal tab is spawned.

**Safety properties:**

- Refuses when no supported harness session is active, when both harness ids
  are present, or when the active id is malformed.
- Refuses if this session is already bound to another task. `--force` does
  not override that invariant.
- Refuses if the target has another session unless the user explicitly passes
  `--force`; doing so replaces flow's tracked transcript association.
- Refuses a done task; reopen it first. A same-session bind is idempotent.
- Claude Code validates that its cwd-keyed transcript belongs to the task's
  `work_dir`; a `cd` inside a tool command cannot bypass that check. Codex
  transcripts are keyed only by thread id, so no cwd validation is needed.

Never invent or guess a session id. `--here` always binds the current tab's
session; to bind a different tab, switch to that tab first.
