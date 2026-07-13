# Owners (autonomous ownership)

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.17 Owners (autonomous ownership)

An **owner** takes durable, ongoing responsibility for an outcome and drives
it itself — re-waking, re-evaluating, acting — instead of a one-shot run. It
is **not a single Claude session**: it's a `charter.md` (operating manual) +
a `updates/` journal + a clock. Each interval it runs a **fresh headless
tick** (new session) that reads the charter + journal, reviews what it owns,
orchestrates, self-paces its next wake, and exits.

**Triggers:** "create an owner for X", "keep X true", "automate maintenance
of <repo>", "own <repo>'s bug-fixing", "run this on a loop".

**Creating one — operational interview** (the charter is the *how-to-operate*
manual). Ask one at a time: (1) **what it owns** (one sentence); (2) **where**
(work_dir, §6 recipe); (3) **how to observe & act** — what to watch (PRs, CI,
prod health) and do when off-target, bootstrapping from CLAUDE.md / KB /
workdir registry / `gh`, asking only the gaps; (4) **when to ask vs. act**;
(5) **fallback interval** `--every` (optional, default 24h) — NOT a fixed
schedule, just the heartbeat floor (ticks self-pace). Then `flow add owner
"<name>" --work-dir <p> [--every <dur>] [--project <s>] [--slug <s>]`, write
the manual into `charter.md` (Read stub once, then Write), and offer to `flow
owner start <slug>`.

**The tag contract (how everything an owner touches is tracked):**
- Every task an owner creates/manages is tagged **`owner:<slug>`** → its
  ledger is `flow list tasks --tag owner:<slug>`, and the tag renders on
  `flow show task` (bidirectional). Playbook *runs* it triggers are tasks
  too — tag them likewise.
- A task it parks for a human is **also tagged `question`** (assigned to the
  user) — a normal task, **never `--auto`**, surfacing in the user's queue.

**Orchestrate, never execute inline.** A tick is *sessionless* and never
calls `flow done`, so work done directly is lost to the KB (no sweep, no
transcript). Route EVERY piece of work through a unit that self-closes:
**recurring → a playbook** (`flow run playbook <slug> --auto`), **one-time →
a task** (`flow add task "<what>" --tag owner:<slug>` then `flow do --auto
<task>`), **a human decision → a question task** (`--tag question --tag
owner:<slug>`). The tick *dispatches*; it never does the fix-work itself.

**The tick procedure** (the tick's own prompt enforces this; restated for the
human-side picture). Each tick: read `charter.md`; read recent
`owners/<slug>/updates/` (its **journal** — what it dispatched / is waiting
on); review what it owns via **`flow owner show <slug>`** (NOT `flow list
tasks`, which hides playbook runs); dispatch the needed runs/tasks/questions;
**self-pace** the next wake (`flow owner next <slug> --in <dur> | --at
<when>`); append a journal note (what it saw, dispatched-with-slugs, what to
check next). The next tick starts blank and knows only the journal + task
records. No AskUserQuestion, no blocking; conservative with
irreversible/outward actions unless the charter allows; never re-spawn an
in-progress run.

**Answering an owner's question (human side):** it's a normal task tagged
`question` + `owner:<slug>`. Read it (`flow show task <q>`) or `flow do` it,
capture the answer on the task, **mark it done** — the owner reads it next
tick and won't ask again. "What does <owner> need from me?" → `flow list
tasks --tag owner:<slug> --tag question`.

**Status:** `flow owner show <slug>` (charter, status, next tick, in-flight /
runs / questions); `flow owner list` (all owners + next tick).

**Waking on demand / the first tick.** Scheduled ticks fire automatically,
but `flow owner tick <slug>` runs one **now** — interactive by default
(spawns a tab the user drives; MAY use AskUserQuestion, can refine the
charter live); `--auto` runs it headless. It's an extra tick, doesn't disturb
the schedule. **Strongly prefer an interactive FIRST tick:** when an owner
shows `last tick: (never)`, offer (via AskUserQuestion) to run it
interactively so the user navigates the agent and tunes the charter before it
runs unattended (like playbook first-run capture, §4.13). Then let the
scheduler take over.

**Event-driven owners (advanced; default is poll-based).** By default an
owner is a *poller* — scheduled ticks, self-paced via `flow owner next`. For
a window that needs faster reaction than polling (a deploy in flight, a CI
run, a PR's checks), an owner can become **event-driven** — but a tick is
headless and **exits**, so it cannot hold a Monitor itself. Instead the tick
spins up a **bounded watcher** and goes back to sleep:

- The tick dispatches a one-time TASK (`flow add task "watch <event> for
  <owner>" --tag owner:<slug>`, then `flow do --auto`) whose brief says: use
  the **Monitor tool** to watch `<event>` with a clear stop condition **and**
  a timeout.
- When the event fires, that watcher session (a) appends a focus note to the
  owner's journal (`owners/<slug>/updates/<today>-EVENT.md` — what fired,
  what to check), then (b) runs `flow owner tick <slug> --auto` to fire a
  **focused tick** now, then **exits**.
- The triggered tick reads the journal (its normal step 3), sees the focus
  note, acts on it, and re-sleeps at its normal cadence. (`flow owner tick
  --auto` is overlap-guarded, so an event trigger that races a scheduled tick
  won't double-fire.)

This gives both modes from one primitive: cheap spaced **polling** by
default, an event-driven **focused tick** on demand — without keeping a mind
alive between events. **Bounded only:** the watcher is a living session that
costs tokens while it watches, so use it for windows with a clear end (deploy
/ CI / PR-checks), **never** as a permanent watcher (that's back to the
expensive long-running-session model an owner exists to avoid). Always give
the watcher a timeout so a never-fired event doesn't strand it running.

**Lifecycle:** `start` begins ticking; `pause` stops but keeps state (resume
with `start`); `flow owner retire <slug>` stops it (retired+archived — no
longer ticks, off the default list, but charter/journal/owned-tasks
preserved). Retire is reversible: `flow owner start <slug>` reactivates a
paused OR retired owner (it un-archives and schedules a tick now). For a
truly permanent removal, `--delete` hard-removes the row +
`owners/<slug>/` dir (use instead of editing the DB; owned tasks survive).
Confirm retire/delete via AskUserQuestion (`--delete` is destructive). Edit
the charter directly at `owners/<slug>/charter.md`.

**Ensuring the tick scheduler (host setup, once per machine).** flow has **no
daemon and no OS-specific scheduler code** — it only provides `flow owner
tick-due` (scan due owners, dispatch detached ticks). Firing it on an
interval is **this skill's job, per host**. When the user creates/starts
their first owner — or asks "are my owners running?" — ensure (idempotently:
check → install if missing → reload if dropped) a host scheduler runs `flow
owner tick-due` ~every 60s:
- **macOS (launchd):** if `launchctl list | grep
  cloud.facets.flow.owner-scheduler` is absent, write
  `~/Library/LaunchAgents/cloud.facets.flow.owner-scheduler.plist` —
  `Label`, `ProgramArguments=[<abs flow>, owner, tick-due]`,
  `StartInterval=60`, `RunAtLoad=true`,
  `StandardOut/ErrorPath=~/.flow/owner-scheduler.{log,err.log}`, and —
  **CRITICAL** — `EnvironmentVariables.PATH` = the user's full interactive
  `$PATH` (launchd's default PATH is minimal; without it the tick fails
  `exec: "claude": executable file not found` — claude/gh/git live in
  ~/.local/bin, homebrew). `launchctl load -w <plist>` (or `bootstrap
  gui/$UID`), then verify with `launchctl list`.
- **Linux:** a systemd **user** timer (`OnUnitActiveSec=60s` + a `.service`
  running `flow owner tick-due`), or `* * * * * <flow> owner tick-due` in
  crontab.

It's **opt-in** (owners then run unattended until paused/unloaded) — offer
via AskUserQuestion; never install silently. Re-verify/respawn whenever the
user touches owners. **Stop all:** unload the plist; **stop one:** `flow
owner pause`.

**Anti-patterns:**
- **Don't auto-create owners** — explicit request only (they run unattended).
- **Don't let a tick execute work inline** — sessionless, no sweep/transcript;
  orchestrate via playbook/task runs that self-close.
- **Don't `--auto` a `question`-tagged task** — it's for the human.
- **Don't invoke `flow __owner-tick` / `flow owner tick-due` by hand** —
  scheduler internals.
