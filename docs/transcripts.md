# Transcript store

Flow keeps its own normalized copy of every task's session transcript, in
one schema for every harness (Claude Code, Codex, prx). Harnesses delete,
compress, or rotate their session transcripts on their own schedule, so
recall — which learns from your own past prompts — otherwise loses most of
its history over time. The flow copy makes `flow transcript` and recall
independent of harness retention.

> **Copying is OFF by default — it is opt-in.** A second copy of every
> session is sensitive, so flow keeps none until you turn it on with
> `flow config set transcripts.copy true` (see [Enabling](#enabling-opt-in)).
> When enabled, copies are private (0600 files in 0700 dirs), cap oversized
> tool output, and can be auto-pruned by age.

## Where copies live

```
~/.flow/tasks/<slug>/transcripts/
  <harness>-<session-id>.jsonl         # the normalized copy (append-only)
  <harness>-<session-id>.offset.json   # capture sidecar (byte offset, turn, counts)
```

(`~/.flow` is the default; substitute `$FLOW_ROOT` if set.)

## The schema

Each line of the `.jsonl` copy is one record:

```json
{
  "ts": "2026-11-17T10:00:00Z",   // RFC3339; the source's timestamp, or capture time
  "harness": "claude",            // claude | codex | prx
  "session_id": "…",              // the harness session this copy belongs to
  "turn": 3,                      // 1-based user-prompt counter
  "role": "assistant",            // user | assistant | tool_call | tool_result | system
  "text": "…",                    // message text, tool input, or tool output
  "tool": "Bash",                 // tool name (tool_call / tool_result); omitted otherwise
  "injected": true                // hook/skill/system-reminder payload — flagged, not dropped
}
```

`injected` records are **flagged, never dropped** — readers that want only
genuine conversation (recall) skip them, while the full audit trail is
preserved. A record is injected when it is a flow wake push or
`flow do --with` injection, a harness bookkeeping or sub-agent record
(Claude's `isMeta` / `isSidechain`), or a user turn that is really a
hook-context block, a skill body, a `<task-notification>` or another
injected payload. That last set is decided by the **same** boilerplate
rules recall uses to clean prompts (match's `dropPrefixes`), so the copy and
recall agree on what a real user turn is — a single source of truth. `turn`
increments on each genuine (non-injected) user prompt; every record that
follows shares that turn number, so injected noise never skews turn numbers
or the note nudge.

## How capture works

Capture rides flow's **existing** hooks — no new hook, no changed hook
command string:

- The **Stop** hook (end of every turn) resolves the bound task's harness
  transcript, reads only the bytes that arrived since the saved offset,
  normalizes the new complete lines, and appends them to the copy.
- The **UserPromptSubmit** hook may append the just-typed prompt
  immediately, so the live turn's prompt is in the copy before the turn
  ends. The next Stop capture deduplicates it against the source.

Capture is append-only and **never fails or blocks the harness**: errors
are swallowed and logged to `~/.flow/transcripts.log`. It handles source
**truncation or rotation** (offset past end-of-file, or a new inode) by
rebuilding the copy from scratch. Typical incremental cost is well under a
millisecond; a cold rebuild of a very large transcript is a few
milliseconds.

## Backfill

Seed copies from transcripts that still exist (idempotent):

```bash
flow transcript backfill            # every task with a surviving transcript
flow transcript backfill <slug>     # one task
flow transcript backfill --dry-run  # report what would be written, write nothing
```

Run this once after upgrading, while the harness transcripts still exist,
so recall recovers as much history as possible.

## Reading

`flow transcript <slug>` prefers the harness's own transcript when it is
still on disk (it is authoritative and may hold the current, not-yet-
captured turn) and falls back to the flow copy when the harness transcript
is gone — the gap this store exists to close.

## Enabling (opt-in)

Capture is **off by default**. Turn it on globally or per task:

```bash
flow config set transcripts.copy true                # global
flow config set transcripts.tasks.<slug>.copy true   # one task only
flow config set transcripts.tasks.<slug>.copy false  # disable one task when global is on
```

The first time you enable it, flow prints a one-line notice of what it now
stores and how to remove it. A per-task override wins over the global
setting.

Keys are validated: an unknown key is refused with a "did you mean" hint
(e.g. `transcript.copy` → `transcripts.copy`), and a value of the wrong type
is rejected.

Delete copies already written:

```bash
flow transcript purge <slug>   # one task
flow transcript purge --all    # every task
```

Purge only removes flow's copies; the harness's own transcripts are never
touched.

## Permissions

Copies can hold a full session, so they are kept private: the `.jsonl`
copies and their sidecars are written `0600` inside `0700` directories. A
copy written world-readable by an older flow is tightened to `0600` on its
next write (or on `flow transcript backfill`).

## Tool-result cap

To keep a pathological tool dump (a multi-MB log, a huge file read) from
bloating a copy, tool-result text is capped per record. The default is
**16 KiB** (`16384`); the overflow is replaced with `…[truncated]`. Raise,
lower, or disable it (`0` = unlimited):

```bash
flow config set transcripts.max_tool_result_bytes 65536   # 64 KiB
flow config set transcripts.max_tool_result_bytes 0       # unlimited (full output)
```

## Retention

By default copies are kept forever. Set an age limit to have `flow
transcript backfill` and `flow transcript purge` prune copies older than N
days (by the copy's modification time):

```bash
flow config set transcripts.retention_days 30   # drop copies older than 30 days
flow config set transcripts.retention_days 0    # keep forever (default)
```

## Fail-closed

Capture is privacy-sensitive, so it **fails closed**. If `settings.json` is
unreadable or not valid JSON, copying is treated as OFF (and the reason is
logged to `~/.flow/transcripts.log`) rather than falling back to a
permissive default. For the same reason `flow config set` **refuses** to
write over a malformed `settings.json` — fix or delete the file first.

## Privacy

The copies hold your full conversation. If you init a git repo in
`~/.flow/`, add `tasks/*/transcripts/` (and `kb/`) to `.gitignore`.
Settings live in `~/.flow/settings.json`.
