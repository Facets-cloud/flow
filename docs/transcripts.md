# Transcript store

Flow keeps its own normalized copy of every task's session transcript, in
one schema for every harness (Claude Code, Codex, prx). Harnesses delete,
compress, or rotate their session transcripts on their own schedule, so
recall — which learns from your own past prompts — otherwise loses most of
its history over time. The flow copy makes `flow transcript` and recall
independent of harness retention.

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

`injected` records (wake pushes, `flow do --with` injections, system
reminders, hook context) are **flagged, never dropped** — readers that want
only genuine conversation (recall) skip them, while the full audit trail is
preserved. `turn` increments on each genuine (non-injected) user prompt;
every record that follows shares that turn number.

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

## Opt out

Capture is **on by default** (you keep the full transcript). Turn it off
globally or per task; nothing is captured and backfill becomes a no-op
while it is off:

```bash
flow config set transcripts.copy off                 # global
flow config set transcripts.tasks.<slug>.copy off    # one task
flow config set transcripts.tasks.<slug>.copy on     # re-enable one task when global is off
```

Delete copies already written:

```bash
flow transcript purge <slug>   # one task
flow transcript purge --all    # every task
```

Purge only removes flow's copies; the harness's own transcripts are never
touched.

## Size

The copy is the **full** transcript by default — no truncation. If a
pathological tool result bloats a copy, cap per-record tool-result text
(the default `0` means unlimited):

```bash
flow config set transcripts.max_tool_result_bytes 65536
```

## Privacy

The copies hold your full conversation. If you init a git repo in
`~/.flow/`, add `tasks/*/transcripts/` (and `kb/`) to `.gitignore`.
Settings live in `~/.flow/settings.json`.
