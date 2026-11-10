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
daemon** — it only provides `flow owner tick-due` (scan due owners, dispatch
detached ticks). Firing it on an interval is the OS timer's job, and
**`flow schedule` installs that timer**; never hand-write a plist, unit or
crontab line. When the user creates/starts their first owner — or asks "are
my owners running?" — check `flow schedule list` and, if `owner-scheduler`
is missing or not loaded/active, offer (AskUserQuestion, never silently):

```
flow schedule install owners            # every 60s; --every <dur> to change
flow schedule list                      # NAME BACKEND WHEN STATUS SOURCE COMMAND
flow schedule remove owners             # stop it
```

Per OS it writes, prints (file + content + commands run) and activates:
- **macOS (launchd):** `~/Library/LaunchAgents/cloud.facets.flow.owner-scheduler.plist`
  with `StartInterval`, `RunAtLoad`, the user's full `PATH` (launchd's own is
  minimal — without it ticks fail `exec: "claude": executable file not
  found`), `HOME`, `FLOW_ROOT` when set; `launchctl load -w`.
- **Linux (systemd --user):** `~/.config/systemd/user/flow-owner-scheduler.{service,timer}`
  (`OnUnitActiveSec`), `systemctl --user daemon-reload` + `enable --now`.
  Picked when a user manager is reachable (`systemctl --user` works —
  needs a login session; on a headless box `loginctl enable-linger $USER`
  keeps it running after logout).
- **Linux (cron):** one tagged crontab line (`# flow-schedule:owner-scheduler`)
  that runs a short wrapper `$FLOW_ROOT/schedules/owner-scheduler.sh` (env +
  command + log redirects live there, so a long PATH or a `%` in a path never
  breaks the crontab), when no systemd user session is reachable, or on
  `--backend cron`. cron speaks whole minutes/hours only.
- Logs: `$FLOW_ROOT/schedules/owner-scheduler.{log,err.log}`. `--dry-run`
  shows the plan without touching anything. A file flow did not write is
  never replaced or removed without `--force` (so a hand-written
  `cloud.facets.flow.owner-scheduler.plist` from before this command needs
  `--force` once, then it is flow-managed). Windows: not supported yet.

It's **opt-in** (owners then run unattended until paused/removed) — offer
via AskUserQuestion; never install silently. Re-verify with `flow schedule
list` whenever the user touches owners. **Stop all:** `flow schedule remove
owners`; **stop one:** `flow owner pause`.

**Anti-patterns:**
- **Don't auto-create owners** — explicit request only (they run unattended).
- **Don't let a tick execute work inline** — sessionless, no sweep/transcript;
  orchestrate via playbook/task runs that self-close.
- **Don't `--auto` a `question`-tagged task** — it's for the human.
- **Don't invoke `flow __owner-tick` / `flow owner tick-due` by hand** —
  scheduler internals.
