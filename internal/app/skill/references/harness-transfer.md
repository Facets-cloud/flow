# Transfer a task between harnesses

> Loaded on demand from the flow skill's resident core (SKILL.md). This file
> holds the full workflow; the core keeps a one-line trigger pointing here.

## When this applies

A task's harness is pinned by its **first** session: `flow do --harness`
is a first-session choice only, and once `tasks.session_id` is set the
binary rejects `--harness` (even with `--fresh`) to protect the
transcript and resume path. So "move this task from Claude to Codex"
(or the reverse) has no single CLI command. This recipe is the reliable
manual transfer. Triggers: "move X to codex", "switch X to claude",
"transfer this task to the other harness", "reopen X in codex" when X
already has a session.

**Before starting, confirm via AskUserQuestion** (header "Transfer?",
"Yes, transfer to <harness>" / "No, wait") — the old session becomes
unreachable through flow afterwards, and step 3 is a direct DB edit.

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

3. **Un-pin the harness** — the one non-CLI step. Clear the binding in
   `~/.flow/flow.db` (substitute `$FLOW_ROOT` if set):

   ```
   sqlite3 ~/.flow/flow.db \
     "UPDATE tasks SET session_id=NULL, harness=NULL, status='backlog' WHERE slug='<slug>';"
   ```

   The `status='backlog'` flip is required — a CHECK constraint
   (`status = 'backlog' OR session_id IS NOT NULL`) forbids a
   non-backlog task without a session. Backlog is also semantically
   right: the task is "unbootstrapped" again.

4. **Respawn in the target harness with the context injected:**

   ```
   flow do <slug> --harness <claude|codex> --with-file ~/.flow/tasks/<slug>/handoff-YYYY-MM-DD.md
   ```

   The new session bootstraps normally (brief + updates), then gets
   "read instructions at <path>" as its first user message, so it
   starts having read the prior conversation. Add
   `--dangerously-skip-permissions` per the user's session-mode choice
   (§4.4 step 1 still applies).

## Caveats (tell the user)

- The old session becomes unreachable through flow: its transcript file
  survives on disk, but `flow do` will only ever resume the new
  binding. It also never gets a close-out sweep — the handoff file and
  progress note are what carry its knowledge forward.
- The transcript is text, not state. The new session re-reads what
  happened but inherits no tool results or working memory beyond what
  the transcript captures.
- This is an exception to the "never hand-edit flow.db" rule, scoped to
  exactly the step-3 UPDATE. Do not generalize it to other fields, and
  prefer a future `flow move <slug> --harness <h>` if the binary grows
  one — check `flow do --help` / `flow --help` for it before using this
  recipe.
