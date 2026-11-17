# Settings

Flow keeps user-tunable settings in a single JSON file at
`$FLOW_ROOT/settings.json` (`~/.flow/settings.json` by default). It is a
nested object addressed by dotted keys; a missing file or key means that
key's documented default. Edit it with the CLI:

```bash
flow config get <key>          # print one value
flow config set <key> <value>  # set one value (on/off/yes/no, ints and floats coerced)
flow config list               # print the whole file
```

Keys and values are **validated**: an unknown key is refused with a "did you
mean" hint (e.g. `transcript.copy` → `transcripts.copy`), and a value of the
wrong type (a string where a bool is wanted, say) is rejected. A per-task key
`X.tasks.<slug>.Y` is accepted whenever its global `X.Y` is a known setting.

For read-only display (`get`/`list`) a malformed file is treated as
all-defaults so a hand-edit typo can never wedge the hooks. But `flow config
set` **refuses** to overwrite a malformed `settings.json` (which would drop
every other key) — fix or delete the file first. Privacy-sensitive readers
(transcript copy) **fail closed**: a malformed file disables copying rather
than assuming a permissive default.

## Transcript copy

| Key | Default | Meaning |
|-----|---------|---------|
| `transcripts.copy` | `false` | Keep flow's own copy of each task transcript (opt-in). |
| `transcripts.tasks.<slug>.copy` | — | Per-task override of the above. |
| `transcripts.max_tool_result_bytes` | `16384` | Per-record cap on tool-result text (`0` = unlimited). |
| `transcripts.retention_days` | `0` | Prune copies older than N days on backfill/purge (`0` = keep forever). |

Copying is **off by default**; enabling it prints a one-time notice and
writes private `0600` copies in `0700` dirs. See
[transcripts.md](transcripts.md) for the full store.

## Progress-note nudge

Once a bound, human-driven session has gone a base number of user turns
since the task's last progress note (the newest `updates/*.md` mtime, or
the session start if there is none), flow injects one extra line of
`UserPromptSubmit` context suggesting the agent offer to save a note. It
fires again at each further compounding step and resets to zero when a new
note is written. With the defaults the nudge turns fall at 15, 18, 22, 26,
32, … — `ceil(base·(1+step)^k)`.

| Key | Default | Meaning |
|-----|---------|---------|
| `nudge.notes.enabled` | `true` | Whether the progress-note nudge fires at all. |
| `nudge.notes.tasks.<slug>.enabled` | — | Per-task override of the above. |
| `nudge.notes.base_turns` | `15` | User turns before the first nudge. |
| `nudge.notes.step` | `0.2` | Compounding growth per further nudge (0 = nudge once at base only). |

The nudge is delivered only through flow's existing `UserPromptSubmit` hook
(no new hook), fires once per threshold crossing, and is skipped entirely in
headless auto runs and unbound sessions. Turn counting prefers the
transcript copy's genuine (non-injected) user records; when the copy is off
it falls back to a tiny per-session counter. It never blocks or fails the
hook.

## Privacy

The transcript copies and `kb/` hold your full conversation and durable
facts. If you init a git repo in `~/.flow/`, add `tasks/*/transcripts/` and
`kb/` to `.gitignore`.
