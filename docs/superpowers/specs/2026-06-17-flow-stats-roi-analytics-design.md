# flow stats — usage & ROI analytics

**Status:** Design approved 2026-06-17. Ready for implementation plan.
**Task:** `flow-roi-analytics` (project `flow`).

## Summary

A user-facing `flow stats` command (plus `flow stats --card`) that shows a
flow user how they use the tool, whether it is paying off, and a
braggable ROI number. Everything is **derived from data flow already
keeps** — flow.db, the Claude session jsonl transcripts, and the
auto-runs / owner / kb directories on disk. No new instrumentation, no
schema change, no writes added to hot commands. A derived cache keeps
repeat runs fast.

## Why

Earlier flow updates establish that flow's value is durable context and
automation — resumable per-task sessions, a knowledge base, playbooks,
and autonomous owners. None of that value is currently *visible* to the
user. `flow stats` makes it visible and shareable: it answers "is flow
working for me?", surfaces productivity/token gains, and produces a
flaunt-worthy card for the OSS launch. It is local-only and personal —
the audience is the individual flow user inspecting their own usage.

## Audience and framing

- **Audience:** the individual flow user, locally. Not a server-side or
  multi-user analytics product.
- **Three jobs:** (1) observe your own usage; (2) judge whether flow is
  working for you; (3) flaunt — produce a shareable artifact.
- **ROI framing:** counterfactual — "flow saved you X" — but built so the
  headline rests on **ground-truth counts**, with counterfactual
  *savings* shown as clearly-labeled estimates. Credible beats inflated.

## Core metric: lookups

The spine of the analytics is the **lookup** — any moment flow served the
user stored context they would otherwise have to recall or hunt for. The
chosen definition is **every retrieval counts** (a lookup need not be
proven to have "led to work"). The headline reads:

> **"flow served you stored context N times."**

Lookup kinds:

| Kind         | What it is                                                        |
|--------------|-------------------------------------------------------------------|
| `resume`     | `flow do <slug>` resumes a task's full prior session + injects brief/updates/KB/CLAUDE.md |
| `reference`  | `flow show <slug>` — addressed something by slug and got its state |
| `cross_task` | `flow transcript <sibling>` or reading another task's dir — reaching into a different task's context |
| `kb`         | a session reads `~/.flow/kb/*.md` for durable org/user/product facts |

This metric is grounded in real history: a scan of the user's 1342
session jsonl files (993 MB) found **~2,600+ minable lookup events
already on disk** — 747 brief reads, 372 updates reads, 291 project
reads, 210 KB reads, 777 `flow show`, 237 `flow transcript`, 137
`flow do`. The metric is therefore retroactively rich on day one.

## Build shape: derive-only + cache (decision A′)

The data shows lookups are already minable from history, so **no forward
instrumentation is added** (no `lookups` table, no inserts in
`do`/`show`/`transcript`). A general events table (full event-sourcing)
is rejected as YAGNI. The engine **derives** all metrics from existing
sources and **caches** the per-file rollup to stay fast.

### Why a forward log was rejected

An earlier design iteration proposed an append-only `lookups` table
written by `flow do` / `flow show` / `flow transcript`. The empirical
scan undercut it: reference lookups are already visible as `flow show`
Bash records (777), KB/brief/cross-task lookups as `Read` tool calls
(1,620), and **resume count is recoverable** because the SessionStart
hook re-fires `flow show task` + brief reads on every resume — so resume
count ≈ the number of bootstrap-sequence repetitions inside a task's
jsonl. A forward log would only additionally capture flow commands run in
a *bare terminal outside a Claude session*, which do not represent
context-fed-to-work. Marginal value; not worth the instrumentation debt.

## Data sources and what each yields

| Source                                   | Scan                                                              | Yields                                                      |
|------------------------------------------|------------------------------------------------------------------|------------------------------------------------------------|
| `flow.db` (tasks/projects/playbooks/tags)| `SELECT` counts + timestamps                                     | #tasks, #done, time-to-done (approx), #playbook runs, per-project rollup, tags |
| each task's session `.jsonl`             | reuse `sessionJSONLPath()`; per record:                          |                                                            |
|                                          | • sum `message.usage{}` (input/output/cache_creation/cache_read) | **actual tokens** (ground truth)                           |
|                                          | • `Read` tool_use on `~/.flow/kb/*`                              | `kb` lookup + timestamp                                    |
|                                          | • `Read`/`Bash` on `~/.flow/tasks/<other>` or `flow show`/`flow transcript` Bash | `cross_task` / `reference` lookup                |
|                                          | • repeated bootstrap sequence (`flow show task` + brief read)    | **resume count** (per task)                                |
|                                          | • first/last record timestamps                                   | session span                                               |
| `~/.flow/tasks/<slug>/auto-runs/*.log`   | file count + mtime                                               | #unattended runs, rough duration                           |
| `~/.flow/owners/<slug>/updates/*.md`     | file count + mtime                                               | #owner ticks, cadence                                      |
| `~/.flow/kb/*.md`                        | entry/byte count                                                 | facts captured, injected-context size                      |

Notes / known approximations (must be reflected honestly in output):

- **time-to-done** uses `created_at → status_changed_at` and is only
  correct while the task's current status is `done`; it breaks if a task
  was reopened (flow.db keeps only the last transition).
- **resume count** is a heuristic (bootstrap-sequence repetition); label
  it as derived, not exact.
- **KB usage attribution** (which session used which fact) is not tracked
  beyond the `Read` event; only the lookup occurrence is counted.

## Metrics produced

1. **Lookups (spine):** total + breakdown by kind. *"flow served you
   stored context N times."*
2. **Ground truth:** actual tokens processed; #tasks done; #auto runs;
   #owner ticks; #playbook runs; #KB facts; time-to-done.
3. **Counterfactual savings (labeled estimates):**
   - Automation hours = `(#auto runs + #owner ticks) × minutes_per_unattended_run`
   - Context-switch recovery = `(#resume + #reference lookups) × minutes_per_context_switch`
   - KB reuse = `(#kb lookups) × tokens_per_kb_lookup`
   - Addressable memory = `#cross_task + #reference` — "addressed by slug,
     never hunted a UUID"
   - Dollars (optional) = `hours × dollar_per_hour`
4. **Trends:** windows all-time / last 30d / last 7d, plus a weekly
   sparkline of lookups and tokens.

### Honesty rule

Ground-truth numbers are shown plainly. Every counterfactual savings
figure is labeled `est.` and prints the assumption behind it (e.g.
"≈ 9.3 hrs saved · assumes 20 min/unattended run"). The card leads with
ground-truth numbers; savings are secondary, labeled.

**Avoiding double-counting.** The savings levers are shown as distinct
rows, not naively summed. Units differ on purpose: *automation hours* and
*context-switch recovery* are time (and roll up into the optional dollar
total); *KB reuse* is tokens; *addressable memory* is a count
(`#cross_task + #reference`), a "you never hunted a UUID" headline, not
time. A single "total time/$ saved" figure (if shown) sums **only**
automation hours + context-switch recovery (+ KB tokens converted to time
only if a token→time rate is ever defined — not in v1). `#reference`
contributes minutes to context-switch recovery and separately appears in
the addressable-memory count; because the units differ these are not an
arithmetic double-count, but the implementation must not add the
addressable-memory count into any time/$ total.

## Counterfactual constants

Stored in user-editable `~/.flow/stats.toml` so the flaunt number is the
user's own:

```toml
[savings]
minutes_per_unattended_run = 20
minutes_per_context_switch = 5
tokens_per_kb_lookup       = 1500
dollar_per_hour            = 100
```

Defaults apply when the file is absent. Out-of-range / malformed values
fall back to defaults with a stderr notice (do not crash `flow stats`).

## Surfaces

- **`flow stats [--since 7d|30d|all] [--project <slug>]`** — prints a
  polished terminal report: headline lookup count, lookup breakdown,
  ground-truth block, savings block (labeled `est.`), weekly sparkline.
  Defaults to all-time, all projects. Screenshot the terminal to flaunt.
- **`flow stats --card [--out <path>]`** — renders a self-contained
  **HTML** card (flow wordmark, headline number, key stats) intended for
  posting to X/LinkedIn. HTML rather than PNG keeps the CGO-free binary
  free of image libraries; the user screenshots the rendered card.
  Default output path under `~/.flow/` (e.g. `~/.flow/stats-card.html`),
  overridable with `--out`. PNG / headless render is **deferred**.

## Architecture

```
internal/stats/
  scan.go    — walk sources → raw events (lookups, tokens, automation, structural)
  cache.go   — ~/.flow/stats-cache.json keyed by {jsonl path, mtime, size}; incremental rescan
  model.go   — counterfactual constants (load stats.toml) + savings math
  report.go  — fold raw events → Stats struct over windows (all-time / 30d / 7d)
internal/app/
  stats.go   — `flow stats` command: flag parsing + terminal renderer; dispatched from app.go
  card.go    — `flow stats --card` → HTML card renderer
~/.flow/stats.toml        — user-editable savings constants
~/.flow/stats-cache.json  — derived cache (add to ~/.flow .gitignore guidance)
```

Reuses `sessionJSONLPath()` and the jsonl parsing types in
`internal/app/transcript.go`, extended to extract `message.usage`. If
the usage extraction is shared between `transcript.go` and the stats
scanner, lift the shared jsonl-record types to a small shared location
rather than duplicating them.

Unit boundaries (each independently testable):

- `scan` — pure: given a jsonl path (or `io.Reader`) → typed raw events.
  No DB, no clock dependence beyond record timestamps.
- `cache` — pure given a filesystem: maps file identity (path+mtime+size)
  to cached per-file rollup; decides hit/miss; folds new results in.
- `model` — pure: constants + counts → savings figures.
- `report` — pure: raw events + windows → `Stats` struct.
- `stats.go` / `card.go` — IO/rendering only; no business logic.

## Cache

`~/.flow/stats-cache.json` stores a per-jsonl rollup keyed by
`{path, mtime, size}`. On each `flow stats`:

1. Load cache (empty if absent/corrupt — never fatal).
2. For each task's jsonl: if `{path, mtime, size}` matches cache, reuse
   the cached per-file rollup; else rescan that file and update the entry.
3. Drop cache entries whose file no longer exists.
4. Fold all per-file rollups + flow.db + dir scans → `Stats`.
5. Rewrite the cache.

This bounds repeat-run cost to "rescan only changed files" without
touching the hot `do`/`show`/`transcript` paths.

## Testing (TDD)

Per repo conventions: real SQLite in a temp dir, `$HOME` / `$FLOW_ROOT`
overridden to a temp dir with fixture jsonl files. No DB mocks. Table-
driven where possible.

- **scan:** fixture jsonl → correct token sum; correct lookup counts by
  kind; resume count from a fixture with N bootstrap repetitions.
- **cache:** hit when mtime/size unchanged; miss + rescan on change;
  incremental fold correctness; corrupt/missing cache is non-fatal.
- **model:** savings math given constants; malformed `stats.toml` falls
  back to defaults.
- **report:** window filtering (all-time / 30d / 7d); per-project rollup.
- **render:** terminal output contains headline + each section; HTML card
  contains headline number + wordmark (golden-ish structural assertion).
- **e2e:** extend `e2e_test.go` to exercise `flow stats` end-to-end against
  a seeded DB + fixture transcripts.

## Out of scope (v1)

- PNG / headless-browser card rendering.
- Interactive HTML dashboard / `flow stats --serve`.
- Slack or social-share automation.
- Forward instrumentation / events table / any schema change.
- Cross-machine or multi-user aggregation.
- "Led-to-work" usefulness proxy (rejected in favor of every-retrieval-counts).

## Resolved task brief sections

This design fills the task brief's deferred sections:

- **Why:** make flow's durable-context/automation value visible and
  shareable to the individual user (observe, judge, flaunt).
- **Done when:** `flow stats` and `flow stats --card` ship, computing the
  lookup spine + ground-truth + labeled counterfactual savings purely from
  derived data, cached for speed, with tests green.
- **Out of scope:** as listed above.
- **Open questions:** card visual layout details (HTML structure/branding)
  and whether dollars are shown by default or opt-in — to settle during
  implementation.
