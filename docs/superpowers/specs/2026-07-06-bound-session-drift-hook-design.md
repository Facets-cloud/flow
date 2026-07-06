# Reintroduce the UserPromptSubmit hook as a bound-session drift/close-out anchor

**Date:** 2026-07-06
**Status:** Implemented (branch `feat/bound-session-drift-hook`)

## Summary

Reintroduce the `flow hook user-prompt-submit` handler (retired in PR #24)
as a **bound-session** anchor. In sessions bound to a flow task, it injects
a ~50-token `additionalContext` payload on each prompt that re-runs two
existing skill checks against that prompt:

- **§4.11 scope-creep** — if the prompt is unrelated work, offer a new task.
- **§4.7 close-out** — if the prompt signals the task is finished, offer to
  mark it done.

Unbound sessions are a pure no-op (exit 0, no output).

## Why this is not reviving #24's mistake

PR #24 retired the hook because it fired in **unbound** sessions, injecting
~200 words nudging Claude to *bind* a task — redundant with the SessionStart
hook (which already fires on startup + resume), so pure token cost.

This is the mirror image:

- Fires **only when bound** — the branch #24 killed was the unbound one.
- Payload is ~50 tokens, not ~200 words.
- Non-redundant with SessionStart: drift and done-signals are **per-prompt**
  phenomena that a session-start-only hook structurally cannot catch.

The binding is discovered deterministically and for free via
`lookupBoundTaskSlug()` (reverse-lookup on `$CLAUDE_CODE_SESSION_ID` against
`tasks.session_id`) — no LLM cost. The semantic "is this drift / is this
done?" judgment stays with Claude via §4.11 / §4.7; the hook only supplies
the anchor.

## Behavior

`flow hook user-prompt-submit`:

| Session state | Action |
|---|---|
| Unbound (`lookupBoundTaskSlug()` == "") | No-op: exit 0, emit nothing. |
| Bound | Emit the anchor payload below as `UserPromptSubmit` `additionalContext`. |

**Anchor payload (bound only):**

> flow session → task **{name}** (`{slug}`). Per §4.11/§4.7: if this prompt
> is unrelated work, offer a new task; if it signals the task is finished,
> offer to close it out. Else proceed silently.

The trailing "Else proceed silently" is load-bearing: it stops the hook from
turning every prompt into a nagging offer.

All errors in the handler are swallowed and treated as "unbound" — a hook
must never fail loud, since a hook error blocks the user's session.

## Code changes

1. **`internal/app/hook.go`** — replace the no-op `cmdHookUserPromptSubmit`
   with the bound-branch logic. Add `lookupBoundTask() *flowdb.Task` (returns
   the task or nil) so the payload can name the task; refactor
   `lookupBoundTaskSlug` to call it.
2. **`internal/harness/harness.go`** — add
   `InstallUserPromptSubmitHook(command string) (added bool, err error)` to
   the `Harness` interface (sibling of the existing `Uninstall`).
3. **`internal/harness/claude/claude.go`** — implement it via
   `installHook("UserPromptSubmit", "", command)` (empty matcher, as the
   pre-#24 code used).
4. **`internal/app/skill.go`** — reverse the #24 plumbing: `skillInstall` and
   `maybeAutoUpgradeSkill` call `InstallUserPromptSubmitHook` instead of
   `Uninstall`; restore the "installed UserPromptSubmit hook" console
   messaging. `flow skill uninstall` keeps calling `Uninstall`. Update the
   stale doc comment on `userPromptSubmitHookCommand` (no longer "legacy").
   The command string `flow hook user-prompt-submit` is unchanged — stable, so
   any surviving stale entry becomes live again rather than orphaned.
5. **`internal/app/skill/SKILL.md`** — short note in/near §4.11 that a
   per-prompt hook reinforces the drift + close-out checks in bound sessions
   (skill is source of truth). Rebuild so `flow skill update` ships it.
6. **`CLAUDE.md`** — the "Things to watch out for" note flags `hookCommand`;
   add the `user-prompt-submit` command string alongside it.
7. **`CHANGELOG.md`** — "Added" entry framed as the bound-session drift/
   close-out anchor, explicitly distinct from the retired unbound nudge.

## Tests (reverse #24's pins)

- **`hook_test.go`** — replace `TestHookUserPromptSubmitIsNoOp` with:
  - bound session emits an anchor containing the task name, slug, `§4.11`,
    `§4.7`;
  - unbound session is a clean no-op (empty stdout, rc 0).
  Bound-session tests seed a task via the real temp SQLite DB and set
  `CLAUDE_CODE_SESSION_ID` to its `session_id` (same pattern as existing
  SessionStart bound tests).
- **`skill_test.go`** —
  - flip `TestSkillInstallWritesSessionStartHook` back to asserting **both**
    hooks install;
  - restore both-hook idempotency (`install --force` twice → one entry each);
  - `TestSkillInstallRemovesStaleUserPromptSubmit` is now obsolete (we want
    the entry) — remove it;
  - repurpose `TestSkillInstallPreservesUnrelatedHooks` to prove install
    **adds** flow's UPS entry while leaving a user's own UPS hook + a
    PreToolUse hook + a top-level key untouched;
  - flip the uninstall test back to asserting both hooks are stripped.

## Out of scope (YAGNI)

- No hook-side keyword/heuristic drift detection — semantic judgment stays
  with Claude.
- No throttle/cadence state — every bound prompt fires; the ~50-token payload
  makes that cheap enough.
- No codex/gemini harness work — only `claude` exists on `main`.
