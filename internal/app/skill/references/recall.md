# Recall — surfacing the user's relevant past work

## Why it matters

When a piece of work has been done or attempted before, the past task's
decisions, notes and dead-ends are worth their weight: reusing them saves
rediscovering what was already figured out, avoids repeating a mistake the
user already hit, and keeps new work consistent with what came before. That
is why it pays to check *before* starting anything substantial — the cost of
one recall is tiny next to redoing work or contradicting a past decision.

`flow recall` ranks the user's PAST tasks against what's in front of you now
and returns a short list, each labelled **strong** or **possible**. It learns
only from the user's own flow history (briefs, update notes, session
transcripts). Use it as *evidence to investigate*, never as an answer to act
on. Like every flow command, YOU run it under the hood — never tell the user
to type a `flow recall ...` command (skill §8).

## When to reach for it

Run recall at the moments where repeating or contradicting past work is the
real risk:

- **At task start.** When you bootstrap a task (or the user starts designing /
  implementing), check whether this has been done or attempted before. One
  `flow recall` on the bound task is cheap and often surfaces a sibling task
  whose brief or update notes save hours.
- **Before designing or building something.** When the user proposes an
  approach or a piece of work — set up CI caching, rotate the API keys, write
  the quarterly report — before you write a plan or code, recall past work on
  the same thing. If a strong match exists, read it first.
- **On task intake (catching duplicates).** During §4.2 intake, after you have
  the one-line "what", recall it as free text: `flow recall "<the what>"`. A
  strong match means the user may already have a task for this — surface it via
  AskUserQuestion ("this looks related to `<slug>` — same work, or new?")
  before creating a duplicate.
- **Mid-session, on a concrete prompt.** When the user asks something specific
  and self-contained ("have we set up CI caching before?"), use the prompt
  form so the ranker weighs what they just said plus the task's brief as
  context.

Don't run it on every turn, on pure chit-chat, or on a one-word ack — the
ranker abstains on content-free input anyway, but calling it there is noise.

## The forms (you choose by intent)

- Bound task: run recall with no argument — it reverse-looks-up the current
  task and ranks its related past work.
- A specific task: pass its exact slug.
- Free text: pass a quoted phrase that is not a slug (multi-word is always
  text). Use `--text` to force text mode on a one-word phrase that happens to
  collide with a slug.
- In-session prompt: pass `--prompt "<what the user just said>"` (optionally
  with the task slug as context).
- `--json` when you need to branch on the result programmatically; `--k`,
  `--min`, `--project` to widen/narrow.

The first call ever builds the index (a one-time "building index…" on stderr);
after that calls are fast and auto-refresh only what changed. There is also
`flow recall train` (force a full rebuild) and `flow recall train --calibrate`
(refit the confidence model on the user's own links) — offer `--calibrate`
only if the user asks for better-tuned confidence and has a long history; it
refuses gracefully on thin data.

## Reading strong vs. possible

- **strong** (calibrated confidence ≥ 0.60) — on the eval these are right most
  of the time. Treat as "very likely relevant; read it before proceeding".
- **possible** (≥ 0.20) — a real lead, but noisier. Worth a glance,
  especially if the slug/name obviously fits.
- **"no confident matches"** — the ranker found nothing distinctive enough.
  That is a genuine signal ("probably new ground"), not a failure — report it
  as such; don't lower `--min` and fish unless the user asks.

If the results look thin and you suspect the user's wording differs from how
past work was named, you may try one rephrasing or a synonym you know (e.g. a
product or vendor's other name) once — but treat a second empty result as the
real "new ground" signal, not an invitation to keep fishing.

Confidence orders your attention; it is not a verdict.

## Treat results as evidence — never act blind

A recall hit is a pointer, not a conclusion. Before you rely on a past task:

1. Open it — `flow show task <slug>` — and READ its brief and the files under
   `updates:`. The one-line summary in the recall output is a teaser, not the
   content.
2. Confirm it actually matches the situation (same system, same goal), not
   just shared vocabulary.
3. THEN use what you learned — cite the past decision, reuse the approach, warn
   the user about a prior dead-end, or flag the duplicate.

Never restate a past task's conclusion, copy its approach, or tell the user
"we already did X" from the recall line alone — a wrong-but-plausible match
that you act on is worse than not running recall at all. When you do surface a
match to the user, describe the finding ("there's prior work on this in a task
about <topic> — it decided <X>"), not the command.
