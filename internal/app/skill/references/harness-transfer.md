# Transfer a task between harnesses

> Loaded on demand from the flow skill's resident core (SKILL.md). This file
> holds the full workflow; the core keeps a one-line trigger pointing here.

## When this applies

A task's harness is pinned by its **first** session. On a task that
already has a session, a bare `flow do --harness <h>` is refused to
protect the transcript and resume path; **`flow do <slug> --harness <h>
--fresh`** is the explicit re-pin: a NEW session on `<h>`, the pinned
harness switched, and the old (harness, session id) kept in the task's
session history (`previous_sessions` in `flow show task <slug> --json`).
Triggers: "move X to codex", "switch X to claude", "transfer this task to
the other harness", "reopen X in codex" when X already has a session.

**Before starting, confirm via AskUserQuestion** (header "Transfer?",
"Yes, transfer to <harness>" / "No, wait") — the new session starts from
the brief, updates and handoff, not from the old session's memory.

## The recipe (old harness → new harness)

1. **Save a progress note first** (§4.5) under
   `tasks/<slug>/updates/`. Updates are harness-agnostic files and every
   bootstrap reads them — this is flow's native cross-harness memory
   layer. For a task with lots of in-flight state, a "where things
   stand + next step" note is worth more to the new session than the
   raw transcript.

2. **Export the conversation.** `flow transcript <slug>` works for
   either harness. Write it to a handoff file in the task dir, e.g.
   `~/.flow/tasks/<slug>/handoff-YYYY-MM-DD.md` (it will surface under
   `other:` in `flow show task`, so the new session can re-find it).
   Use `--compact` if the full transcript is huge:

   ```
   flow transcript <slug> --compact > ~/.flow/tasks/<slug>/handoff-YYYY-MM-DD.md
   ```

3. **Re-pin into the target harness with the context injected:**

   ```
   flow do <slug> --harness <claude|codex|prx> --fresh --with-file ~/.flow/tasks/<slug>/handoff-YYYY-MM-DD.md
   ```

   flow prints `--fresh: re-pinned <slug> from <old> to <new>; old <old>
   session <id> kept in history`. The new session bootstraps normally
   (brief + updates), then gets "read instructions at <path>" as its first
   user message. Add `--dangerously-skip-permissions` (or use `--auto`)
   per the user's session-mode choice (§4.4 step 1 still applies). Never
   hand-edit `flow.db` for this — the CLI owns the switch.

## Caveats (tell the user)

- `flow do` only ever resumes the new binding. The old session's
  transcript survives on disk and its id stays listed under
  `previous_sessions`, but it never gets a close-out sweep — the handoff
  file and progress note carry its knowledge forward.
- If the old session is still running, flow warns: it is no longer bound
  to the task, so close it once the new one has taken over.
- The transcript is text, not state. The new session re-reads what
  happened but inherits no tool results or working memory beyond what
  the transcript captures.
- `--harness <same> --fresh` is just a fresh session (old one kept in
  history with reason `fresh`), not a transfer.
