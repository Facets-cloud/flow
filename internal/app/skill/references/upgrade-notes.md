# flow upgrade notes — what changed for a running session

> Loaded on demand from the flow skill's resident core (SKILL.md). Read it
> when a `[flow-upgrade]` notice says flow was upgraded while your session
> was running. Newest first; act on every entry newer than the version you
> started on (if that version is unknown, the newest entry). Then re-load
> the flow skill: SKILL.md and its references changed with the binary.
>
> Each section is a release tag (`## Unreleased` until it ships). Only
> changes that alter how an agent should behave belong here.

## Unreleased

- **Progress-note nudge.** In a bound, human-driven session the existing
  `UserPromptSubmit` hook now injects one extra line once you've gone a
  base number of user turns (default 15) since the task's last progress
  note, then again at each compounding step (15, 18, 22, 26, 32, …). When
  you see a `flow:` line saying *N turns since the last progress note*,
  treat it as a §4.5 trigger: offer to save a note via AskUserQuestion —
  never write one silently. Writing a note resets the count. Auto runs and
  unbound sessions never see it. Tunables (`nudge.notes.enabled`,
  `base_turns`, `step`, per-task override) and `flow config get/list` now
  showing documented keys with their defaults: `docs/settings.md`.

- **Flow can keep its own transcript copy (opt-in).** Flow can capture a
  normalized copy of each task's session transcript (one schema for every
  harness) via the existing Stop/UserPromptSubmit hooks, under
  `~/.flow/tasks/<slug>/transcripts/`, so recall and `flow transcript` no
  longer depend on harness retention. It is **off by default** — the user
  turns it on with `flow config set transcripts.copy true` (global) or
  `transcripts.tasks.<slug>.copy true` (per task); enabling it prints a
  one-time notice. When on, copies are private (`0600`/`0700`), oversized
  tool output is capped (`transcripts.max_tool_result_bytes`, 16 KiB
  default), old copies can be pruned (`transcripts.retention_days`), and a
  malformed `settings.json` fails closed (copy off; `flow config set` refuses
  to overwrite it). Seed older history with `flow transcript backfill`,
  delete copies with `flow transcript purge`. When surfacing the privacy note
  (KB §4.10 guardrail 6), include `tasks/*/transcripts/` alongside `kb/`.
  Details: `docs/transcripts.md`.

- **`flow recall` — surface the user's relevant past work.** The lookback
  ranker is now a first-class command: `flow recall` (bound task), `flow
  recall <slug>`, `flow recall "<free text>"`, and `flow recall [<slug>]
  --prompt "<text>"`, each returning a short **strong**/**possible** list
  from the user's own history. Use it at task start, before building
  something that may already exist, and on intake to catch duplicates —
  treat every hit as evidence (open it with `flow show task <slug>`), never
  act blind. New skill workflow: §4.20 → references/recall.md. `flow recall
  train [--calibrate]` rebuilds/retunes the index; `flow recall eval` is
  dev-only. The old experimental `flow match` command has been removed.

## v0.1.0-alpha.38

- **Mail reaches you automatically (harness wake).** flow now pushes a
  `[flow-bus wake]` notice into an idle session, and your own hooks surface
  new mail before your next tool call or at turn end. If you armed an inbox
  Monitor loop (`flow inbox pop --wait`), stop it now (TaskStop) and do not
  re-arm it. When a flow-bus notice says mail landed, run `flow inbox pop`
  in the foreground (one-shot), repeat until empty, and act on each.
  Exception: with `FLOW_NO_WAKE=1` set, keep the Monitor recipe in
  references/messaging.md.
- **Codex: run flow commands bare.** Regular Codex sessions run in the
  workspace-write sandbox; flow's exec-policy allow-list (`flow.rules`)
  only matches a bare top-level command. No `VAR=… flow`, no
  `cd … && flow`, no wrapper scripts — otherwise flow fails with
  "readonly database".
- **Upgrade notices.** When flow changes under a running session, the
  next prompt or tool call carries one `[flow-upgrade]` notice pointing
  here. It does not repeat.
