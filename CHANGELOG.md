# Changelog

All notable changes to flow will be documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
(`0.x.y` until the API stabilises).

## [Unreleased]

### Fixed

- **Interactive Codex sessions can write again.** `flow do` minted the
  Codex thread with a read-only, approval=never probe, and `codex resume`
  kept those first-turn settings, so `flow show task` failed with
  "attempt to write a readonly database" and the agent could neither edit
  the repo nor ask. Interactive Codex launches and resumes now pass
  `-s workspace-write -a on-request --add-dir <flow root>` (honoring
  `$FLOW_ROOT`). Skip-permissions launches are unchanged.
- **flow refuses to launch harness runs from inside the Codex sandbox.**
  Under Codex's seatbelt sandbox (`CODEX_SANDBOX` set) a child had no
  network or keychain: Codex children could not mint a thread, Claude
  children were "Not logged in", and `flow done`'s close-out sweep failed
  after the status had already flipped, so the sweep was lost. `flow
  auto`, `flow do` (including `--auto`), `flow run playbook`, `flow owner
  tick` and `flow done` (and `update task --status done`) now refuse
  before any side effect, telling the agent to re-run escalated. `flow do
  --here` and `--no-sweep` launch nothing and stay allowed.
- **`--help` never creates anything.** `flow add task --help` used to
  create a task named "--help". Every command that takes a name or ref
  before its flags (`add task|project|playbook|owner`, `auto`, `done`,
  `update`, `run playbook`, `owner …`, `message`, `broadcast`,
  `connector …`, …) now prints usage and exits 0 on `-h`/`--help`, and
  a leading dash-argument is never taken as a name.
- **Parallel `flow auto` launches no longer die with SQLITE_BUSY.** `flow
  do` minted the Codex thread (a model round-trip) inside its write
  transaction, holding the DB lock for the whole probe, while every DB
  open ran write migrations. The session id is now minted before the
  transaction. The DB records a schema fingerprint in `PRAGMA
  user_version` and skips DDL and migrations (no write lock) when it is
  current, and real migrations are serialized across processes with a
  lock file (`flow.db.migrate.lock`).
- **`flow update task --status done` runs the close-out sweep.** It now
  goes through the same close path as `flow done` (after the other
  fields apply), so a session that closes via `update` no longer skips
  the KB and project distillation. `--no-sweep` keeps the old
  status-only flip for scripted bulk edits, with a note on stderr.
- **Codex sessions are told how to ask and close.** Codex has no
  AskUserQuestion tool. Its SessionStart context, and the skill, now say:
  ask in plain chat and wait for the reply, close only with `flow done`
  after the user says yes, and run flow's launch commands escalated.

## [0.1.0-alpha.36] — 2026-11-10

### Added

- **Pick a child's harness by preference.** When both Claude Code and
  Codex are installed, the SessionStart driver and delegation blocks add
  one line: `harnesses available for children: claude, codex (flow auto …
  --harness <name>; follow the user's preference)`. With one harness the
  blocks are unchanged. The skill's delegation reference now says how to
  choose: use the user's stated preference, else ask once and save it to
  `kb/user.md`, else (headless) the caller's harness. When a child dies
  on a Claude or Codex usage limit, the driver reports the limit and its
  reset time and offers to re-run the same brief on the other harness as
  a new child. No usage reading, no automatic switching, no re-pinning.
- `dispatch.log` now records when a connector escalation is armed:
  `<id> escalate armed <dur> → <next tier>`, written before the wait. An
  armed line with no later `escalate → …` or `escalate stop` line means
  the detached dispatcher died mid-wait (reboot, sleep, upgrade) and the
  escalation was dropped, which used to leave no trace.

### Fixed

- **A dead parentless auto run no longer fails silently.** When a
  `flow do --auto` / `flow auto` run (or a playbook run) with no parent task
  ends dead — exited without `flow done`, or its supervisor was killed and
  the run is settled dead on the next read — flow now sends the human a
  normal, pinging message naming the task, its slug, the first line of its
  brief and the auto-run log path, with how to reopen it. Sent once per
  dead transition. Parented runs keep the existing parent notice; tasks
  tagged `owner:<slug>` are left to their owner's next tick.
- **A `flow done` that lands while an auto run is winding down is no
  longer lost.** The auto-run supervisor read the task status once after
  the harness exited, but `flow done` keeps deferring its close-out sweep
  to the supervisor for as long as that pid is alive. A done that landed
  after the read (a human closing the task, or a late backgrounded `flow
  done`) was neither swept nor counted: the run finalized `dead` and the
  parent got a dead-child notice for a finished task. The supervisor now
  finalizes `dead` only while the task is still not done (one conditional
  UPDATE) and otherwise takes the done path (sweep, `completed`); `flow
  done` decides whether to defer from the row as it was at the status
  flip (`UPDATE … RETURNING`), not from the copy it loaded earlier, so a
  supervisor that already finalized makes it sweep inline. (#120
  follow-up.)

## [0.1.0-alpha.35] — 2026-11-09

### Added

- **Connector priority, fallback and escalation.** With more than one
  human connector installed, every ask used to ping all of them at once.
  `flow connector priority <name> <n>` now orders them: the lowest tier is
  tried first (same number = one tier, in parallel), and the next tier
  only if every connector in it failed. `flow connector escalate <dur>|off`
  (default off, max 24h) pings the next tier when an ask is still unseen
  that long after delivery. `--urgent` asks still go to all at once, and
  with no priorities set nothing changes. The settings live in
  `$FLOW_ROOT/connectors/config.json`; `flow connector list` shows the
  order and `dispatch.log` records the path taken (e.g. `flowhub fail →
  macos ok`).

### Fixed

- `flow init` now installs the PreToolUse hook too. It used to install
  only three of the four hooks that `flow skill install` installs.
- **Codex harness.** The live-session guard now sees Codex auto runs
  (`codex exec resume <flags> <id>`), so `flow do` no longer opens a second
  writer on a thread a headless run is driving. `flow transcript` honours
  `$CODEX_HOME`. The thread-minting probe runs in the task's work_dir instead
  of the caller's cwd.
- **Codex auto runs no longer log "failed to record rollout items: thread
  … not found".** A Codex auto run's own `flow done` now defers the
  close-out sweep, and the `flow __auto-exec` supervisor runs it once the
  harness process has exited. Before, the sweep ran as a nested `codex exec`
  inside the live run. Claude and interactive `flow done` still sweep inline.
  Root cause, confirmed live: Codex's shell tool backgrounds `flow done`
  after ~10s, the agent ends its turn, and the exiting run kills the
  sweep. An inline sweep (plain or `--ephemeral`) cannot survive that.
- **Race fixes around the deferred close-out sweep.** The supervisor sweeps
  after the harness exits and before it records the run's outcome. During
  that window the task is `done`, the auto run is `running` and the
  supervisor pid is alive. Three races in that window are fixed:
  - If the supervisor dies mid-sweep, a done task is reconciled to
    `completed`, not `dead`. A done child that exits non-zero is also
    `completed`, so its parent never gets a crash notice.
  - Only one sweep runs. `flow done` on a task that is already done exits
    0 and skips the sweep. The status flip is conditional, so of two
    concurrent `flow done` calls only one sweeps. A `flow done` while the
    task's Codex auto run is alive (for example a human closing it mid-run)
    leaves the sweep to the supervisor.
  - A stale supervisor no longer overwrites a newer run. It finalizes only
    while the row still carries its own pid. If `flow do --auto --force`
    started a newer run, the old supervisor logs that the newer run took
    over. It writes no status and sends no dead-child notice.
- **Codex Stop hook output.** In Codex sessions the watcher broadcast
  nudge is emitted as `{"decision":"block","reason":…}`. Codex's Stop
  schema rejects `hookSpecificOutput`.
- **Codex `$CODEX_HOME`.** The flow skill and `hooks.json` install under
  `$CODEX_HOME` when it is set (default `~/.codex`), where Codex reads them.
- **Auto runs no longer lose mail to a background inbox listener.** An
  auto run is bound, so it was told to arm an inbox listener. Codex has no
  Monitor and took the fallback: a background `flow inbox pop --wait` loop.
  Each message sent to the child was acked into output nobody read and lost
  when the run exited. Auto runs now get the opposite rule, in the
  SessionStart hook and in the auto prompt: no Monitor, no `pop --wait`, no
  `flow watch`. When the PreToolUse notice says mail landed, and before each
  report, the child runs a one-shot `flow inbox pop` in the foreground and
  acts on it. `flow inbox pop --wait` refuses under `FLOW_AUTO_RUN` (exit 3,
  so a stray listener loop stops) and leaves the message unread.
- **Interactive Codex sessions no longer lose mail to a background inbox
  listener.** With no Monitor tool, a bound Codex session took the
  "background Bash `flow inbox pop --wait`" fallback. Measured live on
  codex-cli 0.166: Codex kills a backgrounded child under 0.5s after the
  shell command returns, so the listener never listened. Mail already
  pending when it was armed was acked at once into a log file the model
  never read, and was lost. A Codex session now gets a no-listener rule at
  SessionStart: when a flow-bus notice says mail landed or is pending, run a
  one-shot `flow inbox pop` in the foreground. `flow watch` still works.
  The pending-count and PreToolUse notices say "pop in the foreground"
  instead of pointing at a Monitor. `flow inbox pop --wait` refuses in a
  Codex shell (`$CODEX_THREAD_ID`, exit 3) and leaves the message unread.
  `flow do --auto` from a Codex parent no longer prints a Monitor recipe.
  The driver blocks and the delegation child template no longer promise an
  inbox listener. Claude's Monitor recipe is unchanged.
- **Harness-neutral SessionStart text.** The hook no longer says "This
  Claude session", "resumes Claude sessions" or "invoke the flow skill via
  the Skill tool". It uses the same wording as the prompts below, names
  `CLAUDE.md / AGENTS.md`, and asks to use AskUserQuestion only if the
  harness has it.
- **Harness-neutral session prompts.** The bootstrap, auto-run, close-out
  sweep and owner-tick prompts no longer assume Claude. They say to use the
  Skill tool if the harness has one, or else read the flow `SKILL.md` (the
  real Codex path is spelled out), and they name `CLAUDE.md / AGENTS.md`.
- **`FLOW_TERM=bg` spawns no longer fail on newer `claude --bg` output.**
  Newer Claude Code prints a `Starting background service…` line before
  the `backgrounded · <id> · <name>` banner. flow only read the first
  line, so it failed with "no background banner in output" even though
  the session had started. flow now checks every line for the banner,
  with or without ANSI color, and still reports a clear error when no
  banner line appears.

### Added

- **Install for every present harness.** `flow init`, `flow skill
  install` and `flow skill update` now install the skill and all four
  hooks (SessionStart, UserPromptSubmit, PreToolUse, Stop) for every
  harness on the machine, not just the ambient one. A harness is present
  when its binary is on `PATH` or its config home exists (`~/.claude`;
  `$CODEX_HOME`, else `~/.codex`); with none found, Claude is the
  fallback. `flow skill uninstall` removes the skill from every harness
  that has it. All four take `--harness claude|codex` to act on one
  harness; an unknown name is a usage error that lists the valid names,
  and a named harness that isn't detected is installed anyway. Output is
  prefixed per harness (`claude: …`, `codex: …`).
- The post-upgrade skill refresh now checks every harness, each against
  its own `VERSION` sidecar, and refreshes only harnesses that already
  have the skill, so a harness you removed it from stays opted out.
- Codex auto runs write the session's final message (`codex exec -o`) to
  `auto-runs/<run>.last.txt` next to the run log; `flow show task` lists it
  as `auto_run_last:` once it exists.
- **Codex hooks.** `flow skill install` now installs the UserPromptSubmit,
  PreToolUse and Stop hooks for Codex too (Codex 0.166 fires them; before,
  only SessionStart was installed). The entries have no matcher, because
  Codex tool names differ from Claude's.
- **Hooks fire in headless Codex runs.** Auto runs, the close-out sweep and
  owner ticks pass `--dangerously-bypass-hook-trust`, so hooks fire without
  a one-time interactive trust approval, as they do under Claude. Interactive
  launches keep Codex's trust prompt.

## [0.1.0-alpha.34] — 2026-11-06

### Added

- **Human connectors (opt-in).** An agent asks with `flow message user
  "<q>" --options "A|B"` and returns at once. If a connector is
  installed, flow pings the human through it, and the answer comes back
  to the asking task's inbox as a reply (`reply_to=<id>`). Nothing pings
  unless a connector is installed.
  - `flow message user` gains `--options "A|B|C"` (stored and shown in
    `flow inbox`) and `--fyi` (stored and listed, never pinged). Both are
    only for mail to the human. Broadcasts never ping.
  - The ping decision is made at send time: one ping per sender task
    while a pinged ask is still unseen, `--urgent` always pings, and a
    detached `flow connector dispatch` runs each matching connector's
    `send` (30s timeout each, in parallel, logged to
    `connectors/dispatch.log`, `notified_at` stamped).
  - `flow connector add|list|remove|test`. Each connector gets a random
    secret in the macOS keychain (service `flow-connector-<name>`,
    account `flow-<hash of the resolved FLOW_ROOT>`, so two flow homes on
    one Mac never share or overwrite a connector's secret), or a 0600
    file under `$FLOW_ROOT/secrets/` where there is no keychain.
  - `flow human reply <id> "<text>"` / `flow human seen <id>` work only
    with a connector's `FLOW_CONNECTOR_TOKEN`, so agents can't answer for
    the human. A reply records `answered_at` and delivers the text to the
    asking task. Repeats are idempotent.
  - Contract v1 is documented in the skill's `references/connectors.md`.
    `examples/connectors/macos` is a stock-macOS dialog connector. It is
    built into the binary, so `flow connector add macos` works without a
    repo checkout (`flow connector list --available` lists the built-in
    samples; an existing directory argument still installs from that path).
  - flow's own FYIs to the human (the closed-parent report in the auto
    prompt, the dead-child notice) are sent as `--fyi`.

## [0.1.0-alpha.33] — 2026-11-06

### Fixed

- **Agents no longer swallow the human's mail.** `flow inbox pop` never
  claims, acks or returns mail addressed to `user` (asks and broadcasts),
  in any mode (`--keep-unread`, `--wait`, `--as user` included), and an
  agent's `flow inbox read <id>` on it is display-only. Only bound
  sessions arm an inbox Monitor (the skill said every session should, so
  dispatch sessions drained the human's queue as `user`).
- **Human mail stays unread until a human has seen it.** A new
  `seen_at` drives one unread predicate for `flow inbox --as user`, the
  counts and the read/unread label, so mail a reader left `delivered` no
  longer disappears. Notices read "N ask(s) await you · M FYI" and list
  the top items by id. Existing human mail is marked seen on upgrade.
- **Only a prompt the human types counts.** Human mail a SessionStart
  hook surfaced is marked seen on the next human-typed prompt in that
  session, and an ask is "answered" only by one. `--with` injections,
  auto runs, owner ticks, `/loop` and wakeup firings, and flow's own
  bootstrap prompts never count.
- **Who consumed a message is recorded.** Every claim, ack and seen
  stores the consumer's bus identity and session id; `flow inbox --all`
  shows "by <identity> (<session>)" and `--json` a `consumed_by` object.
- **`user` and `self` are reserved.** `flow add task --slug user|self`
  exits 2; a bare address is always an assignee (sessions are
  `user/<slug>` only), so a task slugged after a person can't capture
  their mail; `self` is an alias for `user` (`--as self` now reads the
  human's queue). Messages no longer tell agents to `flow message self`.
- **`flow inbox read <id>` respects the recipient.** Only the message's
  addressee changes its state; anyone else gets a read-only display
  (`--as <assignee>` reads as a person's queue).
- **One child-reporting rule.** A parented auto child reports to
  `user/<parent>`; if the parent is closed it writes its update note and
  sends the human an `FYI:` message (the dead-child notice falls back the
  same way); a parentless child messages the human only when done or
  blocked. Auto runs get no Stop broadcast nudge (progress no longer
  reaches the parent twice).
- **The inbox Monitor loop backs off and stops.** `pop` exits 3 when
  the caller's bound task is done or archived, and the recipe sleeps 5s
  after a failed pop and breaks on 3:
  `while true; do flow inbox pop --wait --timeout 300 --json; rc=$?; [ $rc -eq 3 ] && break; [ $rc -ne 0 ] && sleep 5; done`.
- **`pop --json` reports the real status** (`acked`/`delivered`) of the
  row it just consumed instead of `pending`/`unread`.

## [0.1.0-alpha.32] — 2026-11-06

### Added

- **Driver tasks.** Tag a task `driver` (offered as an intake question,
  or `flow update task <slug> --tag driver` mid-session) and it owns its
  outcome end to end: its session gets a stronger `flow DRIVER task`
  block that hands any self-contained work (implementation, research,
  debugging, fix + PR, scans) to `flow auto` children instead of harness
  subagents, keeps decisions with the user, never idles, and ends turns
  with a numbered "Needs from you" list. Regular bound tasks keep the
  light delegation block. Drivers show as `◆driver` in `flow list`
  (`"driver": true` in json), `role: driver` in `flow show task`, and
  `[driver]` in the per-prompt anchor.
- **`flow do --here` prints the role block**, so a session bound
  mid-flight (missed SessionStart) gets the driver or delegation context
  immediately. Tagging/untagging the bound task as driver does the same.
- **Dead children notify their driver.** When a parented auto run ends
  `dead`, flow posts a crash notice to the parent's inbox; previously a
  crashed child went silent.

## [0.1.0-alpha.31] — 2026-11-02

### Added

- **Driver sessions delegate deterministic subtasks to auto children.**
  The SessionStart hook now gives a bound, human-driven session a short
  `flow driver delegation` block: carve scans, audits, fact-finding,
  repro debugging, bulk passes and fix + PR work off to headless children
  without asking, keep design and decisions inline, and leave live/prod
  applies to the driver. Auto runs (marked by a new `FLOW_AUTO_RUN` env
  var or a live auto run on the bound task) never get it. Skill: new
  section 4.4a plus `references/delegation.md`.
- **`flow auto "<name>" --brief <file|->`.** One shot for add task +
  write brief + `flow do --auto`, with the same parent, project and
  auto-watch as `flow add task`. Refuses without a brief, and inside an
  auto run (no grandchildren).
- **Auto child result contract.** A child with a parent writes its
  findings as an update note on its own task and names it in its final
  `flow message user/<parent>`; it is told not to spawn children itself.
- **Parents auto-watch the tasks they spawn.** `flow add task` from a
  bound session now also subscribes that session (`user/<parent>`) to the
  new task and prints `(user/<parent> now watches <child>)`.
- **Spawned auto runs report to their parent.** The `flow do --auto`
  prompt for a task with a parent authorizes and asks the child to `flow
  message user/<parent>` at milestones and when done or blocked.
  Parentless tasks keep the old "message no one unless the brief says so"
  rule.
- **`flow do --auto` parent reminder.** Launching a child with a parent
  prints whether the parent watches it and the inbox Monitor recipe.

### Fixed

- **`flow add task --mkdir` without `--work-dir` now gives the task a
  fresh workspace.** It used to be silently ignored, so the task fell back
  to the project's work_dir, including a project inherited from the parent
  task, and ran inside that repo. `--mkdir` alone now creates
  `~/.flow/tasks/<slug>/workspace` and uses it (the project link is kept).
  `flow add task` also prints the chosen `work_dir` and its source (`from
  project "<slug>"`, `new workspace`, or the explicit path). `flow update
  task --mkdir` without `--work-dir` is a usage error.
- **Monitor listener recipe.** The SessionStart hook, SKILL.md and
  `references/messaging.md` told agents to arm `Monitor(...,
  persistent: true)` with "no re-arming ever". Claude Code's Monitor has
  no `persistent` param and requires `timeout_ms` (max 1800000). The
  recipe now passes `description` + `timeout_ms: 1800000`, says to re-arm
  on the expiry notice, and notes Monitor may need loading via ToolSearch.
- **SessionStart hook re-fires after compaction.** The Claude hook matcher
  is now `startup|resume|compact`, so a compacted session gets its task
  context and listener discipline back. Re-running `flow init` (or `flow
  skill install`) migrates an existing `startup|resume` entry in place
  without duplicating it. Codex has no compact event and is unchanged.

## [0.1.0-alpha.30] — 2026-11-02

### Added

- **Rex terminal backend.** `flow do` run inside
  [Rex](https://www.superlogical.com/rex) (`$REX_SESSION` set or
  `TERM_PROGRAM=rex`, or `FLOW_TERM=rex`) opens the task in a new Rex tab.
  Previously Rex users fell through to the iTerm backend. An already-open
  task's tab is found in any Rex session and focused; switching the Rex
  app to another session needs **Remote Control** on in Rex settings, and
  without it flow prints which session the tab is in. Commands over 512
  bytes (the bootstrap prompt) go through a self-deleting temp script the
  tab sources, because a just-started shell on macOS drops typed lines
  over 1024 bytes, Enter included.
- **Project terminal groups.** `flow update project <ref> --terminal-group
  <name>` / `--clear-terminal-group` sets where `flow do` opens the
  project's task tabs; `flow show project` shows it. For Rex the name is a
  session label, created if missing. Ungrouped projects and floating tasks
  open where `flow do` ran; other terminals ignore the group. Existing DBs
  gain the nullable `projects.terminal_group` column on open.

### Fixed

- **Rex: `flow do` now actually shows the task's tab.** Focusing an open
  task's tab, or opening a new one, in the current Rex session reported
  success but left the app on the previous tab: `rex focus` and `rex
  window new` only set the server's active window, and the Rex app keeps
  showing its own selection. flow now always asks the app to select the
  tab (as it already did for other sessions); this needs Remote Control
  on in Rex settings.

## [0.1.0-alpha.29] — 2026-10-28

### Added

- **Spawned tasks inherit their parent's project.** `flow add task` run
  without `--project` from a session bound to a task now places the new
  task in that task's project (and inherits the project work_dir, same as
  `--project`). An explicit `--project` still wins. Previously, tasks an
  agent fanned out landed with no project and vanished from project views.
- **Spawned tasks record their parent.** `flow add task` run from a
  session bound to a task stores that task as `parent_slug` on the new
  task and prints `(parent: "<slug>")`. `flow show task` shows `parent:`
  on the child and `spun_off:` with the child slugs on the parent. Tasks
  created from an unbound session have no parent. DBs that already carry
  a legacy `parent_slug` column are reused as is.
- **`flow update task <ref> --project <slug>` / `--clear-project`** to move
  a task between projects or make it floating. work_dir is not changed, so
  an attached session keeps working.

### Fixed

- **`flow do --auto` re-runs resume instead of dying.** Re-running auto on a
  task whose session already ran failed with "Session ID already in use";
  it now continues the session (`claude --resume`), so a paused auto keeps
  its context.
- **Codex hooks see the bound session.** Codex passes its thread id to hooks
  only in the stdin payload, not `$CODEX_THREAD_ID`, so every hook in a bound
  Codex session treated it as unbound. Hooks now read `session_id` from the
  payload, falling back to the env var.

## [0.1.0-alpha.28] — 2026-09-07

### Added

- **Mail-model inbox.** The message bus adopts unread/read ("mail")
  nomenclature and gains read-side reach: `flow inbox --all` lists everything
  still retained (read + unread) with per-message status; `flow inbox read
  <id>` shows and acks one specific message out of `pop`'s oldest-first order;
  and `flow inbox pop --wait --keep-unread` is the reader/relay wake primitive
  — it returns newly arrived mail without acking it (claims it `delivered` so
  it neither re-loops nor can be swept), so a forwarder is woken without
  consuming a message it only relays. `flow message … --reply-to <id>` stamps
  a parent message id so a routed reply carries lineage (validated at send
  time), and the receiver is shown which message it answers. Skill §4.18 and
  `references/messaging.md` are rewritten around the two real purposes
  (reach-out-to-user vs peer-collab) with two Monitor recipes (consumer
  `pop --wait` vs reader `pop --wait --keep-unread`).

### Changed

- **`flow message` body cap raised 200 → 500 chars** (non-breaking; short
  messages unaffected).
- **`flow message` argument parsing is now order-independent and typo-safe**
  ([#95](https://github.com/Facets-cloud/flow/pull/95) by
  [@anshulsao](https://github.com/anshulsao)). The body may be positional or
  `--body "<text>"`, and `--urgent` / `--body` / `--reply-to` may appear in any
  order; an unknown `--flag` is now rejected with an error instead of being
  silently stored as the message body.
- **Headless `flow do --auto` guidance:** agents should post regular progress
  via `flow message user` / `flow broadcast` as milestones land — a silent
  unwatched run looks stuck.

## [0.1.0-alpha.27] — 2026-09-02

### Added

- **Message bus: `flow message` / `flow broadcast` / `flow watch` / `flow inbox`.**
  Sessions call for the user's attention, message each other's tasks, and
  broadcast one-liners to watchers — queued in flow.db, CLI-only (no
  notification UI; script your own on `flow inbox due`). Directed messages to
  a human stay pending on an escalating notify schedule (1m→30m cap) until
  answered; the user's reply in the sending session acks automatically and the
  agent is told how long it waited (`flow inbox stats` aggregates). Broadcasts
  fan out on write to current watchers of the task, its project, or its
  assignee and never interrupt. `flow inbox pop` atomically claims the oldest
  message (exactly-once under concurrent consumers); `pop --wait` blocks until
  mail arrives — the canonical agent listener is one persistent Monitor loop
  (`while true; do flow inbox pop --wait --timeout 300 --json || true; done`).
  `--as <assignee>` consumes any human queue (`--as self` from inside a bound
  session; other assignees for monitor/transport workers). Hooks are strictly
  inform-only (pending counts; they never consume mail): SessionStart also
  teaches the listener discipline, UserPromptSubmit carries the ack contract,
  and a new Stop hook nudges watched tasks to broadcast (30m→4h decline
  backoff, `stop_hook_active` loop guard; needs Claude Code ≥ v2.1.163).
  Retention is row-count based (newest 1000 rollable rows; pending directed
  messages never expire) and task close-out cleans the task's bus footprint.
  Skill: new §4.18 plus `references/messaging.md`.

## [0.1.0-alpha.26] — 2026-08-17

### Added

- **Codex harness support.** Tasks can now launch and resume Codex threads,
  persist their harness and transcript path, install the flow skill and
  SessionStart hook under `~/.codex`, and use Codex for transcript rendering
  and close-out sweeps. `flow do <task> --harness codex` explicitly chooses
  Codex for a task's first session; a task with an existing session remains
  pinned to its original harness and cannot be transferred implicitly.
  This implementation builds on the Codex-session groundwork from
  [#22](https://github.com/Facets-cloud/flow/pull/22) by
  [@sillanaresh](https://github.com/sillanaresh).

### Changed

- **Skill token footprint cut ~55% via progressive disclosure.** The flow
  skill's `SKILL.md` is loaded resident by the Skill tool and re-billed as
  cached tokens on *every* turn, making it the dominant contributor to
  flow's per-session cost (~28K tokens, and multiplied across a long
  session). The skill is now a **lean resident core** (~50 KB, down from
  ~111 KB / ~27.7K → ~12.6K tokens) plus twelve on-demand
  `references/*.md` files that hold rarely-needed workflows (playbooks,
  owners, upgrade, weekly review/archive, tagging, session-binding
  detail, project intake, first-run setup, brief templates, the work_dir
  recipe, cross-task transcripts & field-edit semantics). The core keeps a
  one-line trigger pointing at each reference, so the model loads a
  workflow's full text only when it's actually needed — paid once, not
  every turn. No workflow content was removed; sections were relocated and
  the resident prose tightened. The embed changed from a single
  `//go:embed skill/SKILL.md` to the whole `skill/` directory, and
  `Harness.InstallSkill` now writes the tree (`SKILL.md` + `references/`)
  under `~/.claude/skills/flow/`. New guards: `TestSkillCoreIsLean` caps
  the resident core, `TestSkillReferencesAreReachable` forbids orphan
  references, and content assertions run against the full corpus so a
  future relocation can't silently drop a section.

## [0.1.0-alpha.24] — 2026-07-06

### Added

- **UserPromptSubmit hook — bound-session drift/close-out anchor.** In a
  Claude session bound to a flow task, `flow hook user-prompt-submit`
  injects a tiny (~45-token) per-prompt anchor naming the task and
  re-running the skill's lifecycle-transition checks: if the prompt is
  unrelated work it offers a new task (§4.11), and if the prompt signals
  the task is finished it offers to close it out (§4.7). Unbound sessions
  are a pure no-op. This is distinct from — and not a revival of — the
  per-prompt *unbound* skill nudge retired in v0.1.0-alpha.7: that one
  duplicated the SessionStart hint, whereas drift and close-out are
  per-prompt signals SessionStart structurally cannot catch. Both
  `flow skill install` and the auto-upgrade path now install the hook
  (leaving any unrelated user-defined hooks in the same event untouched).

## [0.1.0-alpha.22] — 2026-06-24

### Added

- **`flow stats` — local usage & ROI analytics.** A new read-only command
  that mines your own flow history (harness session transcripts, the task
  DB, and the on-disk KB/updates) to show how much context flow has
  re-established for you without re-typing: context recalls broken down by
  kind (resume · reference · cross-task · kb), tokens processed, tasks
  shipped, KB facts, automation runs, and estimated time/$ saved. Ground-
  truth counts are exact; time/token figures are estimates.
  - `flow stats` (all-time terminal report); `--since all|<N>d|RFC3339`
    (window); `--project <slug>` (scope to one project).
  - **Shareable cards.** `flow stats --card` writes a flow-branded HTML
    card; `flow stats --png` renders the same card to a self-contained PNG
    with no browser or runtime dependency (pure-Go drawing via
    `fogleman/gg` + embedded fonts; CGO stays off — costs ~+1.27MB binary).
    The PNG card is flow-branded — embedded wordmark logo, gradient hero
    number, dark palette. `--out <path>` overrides the default
    `<flow-root>/stats-card.{html,png}`.
  - **Estimates are tunable and grounded.** Savings figures derive from an
    optional `~/.flow/stats.json` (built-in defaults apply when absent, so
    create it only to override); context-token savings are file-size-
    grounded. A derived `~/.flow/stats-cache.json` (keyed by mtime+size)
    keeps re-scans cheap — safe to delete; gitignore it if you track
    `~/.flow` in git.

## [0.1.0-alpha.21] — 2026-06-11

### Added

- **Owners — autonomous, self-prompting controllers (`flow owner`).** A new
  durable primitive that takes ongoing responsibility for an outcome:
  re-wakes on its own, re-evaluates, and acts until done — vs. the one-shot
  `flow do --auto`. An owner is **not** a long-running session; it is state
  (a `charter.md` operating manual + a journal + a clock). Each tick is a
  *fresh headless run* that reads its charter and journal, **orchestrates**
  (routes work through tasks/playbook-runs that self-close with the
  `flow done` KB sweep — it never executes work inline), parks human
  decisions as `question`-tagged tasks, **self-paces** its next wake, and
  journals. Cheaper and more durable than keeping a mind alive between
  events.
  - Commands: `flow add owner "<name>" --work-dir <p> [--every <dur>]
    [--slug] [--project] [--mkdir]`; `flow owner list | show <slug> |
    start | pause | tick [--auto] | next (--in|--at) | retire [--delete]`.
    `list`/`show` also as verb-first aliases `flow list owners` /
    `flow show owner <slug>`.
  - **Tags, not new tables** — owner↔task linkage and the human-question
    marker reuse the tag system (`owner:<slug>`, `question`);
    `flow add task --tag` and `flow list tasks --tag` are now repeatable
    (intersection).
  - **Self-paced scheduling** — each tick sets its own next wake; `--every`
    is a fallback heartbeat floor (default 24h). Overdue ticks (e.g. after
    the laptop sleeps) fire once on wake, never stack.
  - **Event-driven owners (advanced)** — for a bounded reactive window a
    tick can dispatch a watcher task (Monitor tool) that fires a focused
    `flow owner tick --auto` on an event, then exits; the manual `--auto`
    tick is overlap-guarded.
  - **No daemon / no OS-specific scheduler code** — flow ships only
    `flow owner tick-due`; the recurring heartbeat is a launchd (or
    systemd/cron) agent the flow skill sets up once per host.
  - Live tick indicator (`tick_pid`/`tick_started`) with overlap guard,
    dead-/stale-pid reconciliation, and targeted-column writes that commute
    across the scheduler and the detached tick (no lost updates).

## [0.1.0-alpha.20] — 2026-06-10

### Fixed

- **`$FLOW_TERM=bg` spawn could fail to capture the session id.** The
  `claude agents --json --all` lookup that resolves a freshly-spawned bg
  session's id had a 5s timeout, but immediately after `claude --bg` the
  daemon is busy registering the new agent and the query routinely takes
  ~10s — so the spawn errored (`signal: killed`) and rolled back, leaving
  an orphaned agent. The backstop timeout is now 30s (it only guards
  against a genuinely stalled daemon, not normal post-spawn latency).
  Fixes bg spawn introduced in alpha.19.

## [0.1.0-alpha.19] — 2026-06-10

### Added

- **Terminal-free background-agent spawn backend (`$FLOW_TERM=bg`).** Set
  `FLOW_TERM=bg` and `flow do <task>` launches the session as a Claude Code
  **background agent** (Agent View, `claude agents`) instead of opening a
  terminal tab — for users who live in the Agent View. flow spawns
  `claude --bg --name "<project>/<task>" <prompt>` (in the task's
  `work_dir`), parses the launch banner, and resolves the **real**
  session id via a single `claude agents --json --all` lookup — so a
  `--bg`-injecting `claude` alias no longer defeats flow's session-id
  binding (flow captures the harness-minted id instead of recording a
  phantom one). Re-running is idempotent: a still-live session is reported
  as open in the Agent View; a not-running or removed one is brought back
  via `claude --bg --resume` (which inherits the prior conversation under
  a new id, so flow re-records it). `flow show` / `flow list` surface live
  bg status (and `flow transcript` resolves a bg session's jsonl by
  globbing the session id, handling the git-worktree relocation case).
  Background mode is Claude-only; pointing `FLOW_TERM=bg` at a task pinned
  to another harness fails cleanly rather than silently opening a tab.

## [0.1.0-alpha.18] — 2026-06-08

### Added

- **`flow do --auto` / `flow run playbook --auto` — autonomous, headless
  background runs.** Instead of opening a terminal tab for a human to
  drive, `--auto` launches a detached supervisor (`flow __auto-exec`,
  hidden) that runs the task's harness headlessly — pinned to the
  pre-allocated session id, cwd at `work_dir`, stdout/stderr captured to
  `tasks/<slug>/auto-runs/<timestamp>.log` — and returns immediately. The
  session works end to end on best judgment (no `AskUserQuestion`,
  persisting toward a closeable state rather than giving up early) and
  calls `flow done` on **itself** when the brief's *Done when* is met,
  which still fires the close-out KB/project sweep. `--auto` implies
  `--dangerously-skip-permissions` (no human to approve tool calls) and
  accepts `--with` / `--with-file` for a one-off directive layered on the
  brief; `--auto` + `--here` is rejected. The headless-with-session-id
  invocation is a third harness execution shape, added to the harness
  interface as `AutoRunArgv` — claude implements it; codex/gemini inherit
  auto mode by implementing the one method. A per-run status —
  `running → completed | dead`, with read-time pid reconciliation so a
  crashed supervisor surfaces as `dead` — shows in `flow show task` and a
  dedicated `AUTO` column in `flow list tasks` (a running row shows the
  pid). New nullable `tasks` columns: `auto_run_status`, `auto_run_pid`,
  `auto_run_started`, `auto_run_finished`, `auto_run_log` (additive
  migration). Non-blocking hardening follow-ups are tracked in
  [#68](https://github.com/Facets-cloud/flow/issues/68).
  ([#67](https://github.com/Facets-cloud/flow/pull/67) by
  [@anshulsao](https://github.com/anshulsao))

### Changed

- **Hardened iTerm2 tab spawning.** Raises the file-descriptor `ulimit`
  before spawning and uses more robust AppleScript quoting, preventing
  tab-spawn failures under constrained descriptor limits or when the
  command contains characters that tripped the previous quoting.
  ([#64](https://github.com/Facets-cloud/flow/pull/64) by
  [@ishaankalra](https://github.com/ishaankalra))

## [0.1.0-alpha.17] — 2026-05-29

### Fixed

- **`flow transcript` elided pre-bind work on retrospective `--here`
  binds, silently starving the close-out KB sweep.** `flow transcript`
  filtered out every jsonl entry before `tasks.session_started`, on the
  assumption that `session_started` ≈ the conversation's start. That
  holds for `flow do` spawns (the UUID is pre-allocated, so the first
  message lands just after `session_started`) but breaks for a
  retrospective `flow do --here` bind: there `session_started` is
  stamped at *bind time*, after all the real work. The cutoff then
  dropped the entire conversation, so the `flow done` close-out sweep
  mined an empty tail and wrote nothing to the KB — silent knowledge
  loss. Hit live on 2026-05-29 (task `cp-mgmt-skill`), whose whole
  session had to be hand-distilled into the KB. The fix removes the
  time cutoff entirely: `flow transcript` (and therefore the sweep)
  now always renders the full session. An over-inclusive sweep was
  already the documented preference over silent data loss; the
  dispatch-chatter-stripping the cutoff bought for early binds was
  deemed not worth the failure mode. `RenderTranscript`'s `cutoff`
  parameter is gone from the harness interface.
  ([#65](https://github.com/Facets-cloud/flow/pull/65) by
  [@rr0hit](https://github.com/rr0hit))

## [0.1.0-alpha.16] — 2026-05-28

### Fixed

- **Migration silently drops `tasks.harness` on older DBs.** The
  session-invariant table rebuild in `migrateTasksSessionInvariant`
  hardcoded `tasks_new`'s DDL and omitted the `harness` column added
  one statement earlier in the same `runMigrations` pass. DBs
  upgrading from a version that predated the rebuild (e.g.
  `v0.1.0-alpha.4` → `v0.1.0-alpha.15`) had `harness` added by
  `ALTER TABLE` and then silently dropped by the rebuild that
  recreates `tasks` without listing it. Symptom: every subsequent
  `SELECT` using `TaskCols` errored with `no such column: harness`,
  making `flow list tasks` and most other commands unusable after
  upgrading to alpha.15. Fix adds `harness TEXT` to `tasks_new`'s
  DDL and to both column lists in `INSERT INTO tasks_new (...)
  SELECT ... FROM tasks`. Users who already hit the bug recover
  automatically on next upgrade: `columnExists` re-runs the
  `ALTER`, and the rebuild's idempotency guard
  (`strings.Contains(ddl, "session_id IS NOT NULL")`) short-circuits
  because the broken table already carries the CHECK. No data loss
  in either direction — `harness` is nullable with `NULL` = "claude"
  back-compat.
  ([#63](https://github.com/Facets-cloud/flow/pull/63) by
  [@rr0hit](https://github.com/rr0hit))

## [0.1.0-alpha.15] — 2026-05-27

### Added

- **GitHub Pages site.** A static one-page site at
  `https://facets-cloud.github.io/flow/` introducing flow at a glance:
  dark terminal-aesthetic hero, brief-styled (What / Why / Done-when)
  feature sections, four-act demo embedded as MP4 video with poster
  + reduced-motion fallback, inline-SVG diagrams from the README, and
  a one-shot install block. Pure HTML/CSS/vanilla JS, zero build step,
  ~50 KB above the fold. CI workflow (`.github/workflows/pages.yml`)
  force-pushes `site/**` + `docs/demo/**` to a `gh-pages` orphan
  branch on every push to main. **One-time setup after upgrade:**
  Repo Settings → Pages → Source = "Deploy from a branch", Branch =
  `gh-pages` / `/(root)`.
  ([#60](https://github.com/Facets-cloud/flow/pull/60) by
  [@pramodh-ayyappan](https://github.com/pramodh-ayyappan))
- **Pluggable agent harnesses.** Internal refactor that moves
  Claude-specific code behind a 14-method `internal/harness`
  interface, with codex/gemini adapters ready to drop in as one
  line each in `allHarnesses()`. Adds `tasks.harness` (per-task
  adapter pinning; NULL = claude for back-compat) and
  `tasks.session_cwd` (fixes a pre-existing `flow transcript`
  not-found for `--here`-bound sessions started in a directory ≠
  `task.work_dir`; session identity is now keyed on
  `(cwd, session_id)` to match claude's on-disk layout). Ambient
  harness detection probes `$CLAUDE_CODE_SESSION_ID` /
  `$CODEX_THREAD_ID` / `$GEMINI_SESSION_ID`; defaults to claude.
  `flow do --here --force` on a task pinned to a different harness
  switches the pinning alongside the session rebind, with a
  warning that the prior transcript is orphaned. Schema changes
  are additive and nullable — no backfill, no behavior change for
  existing rows.
  ([#58](https://github.com/Facets-cloud/flow/pull/58) by
  [@rr0hit](https://github.com/rr0hit))
- **Focus existing tab on `flow do`.** When the task's `session_id`
  is already running, `flow do <task>` switches to the existing tab
  instead of erroring with the "use `--force`" message. Implemented
  via `spawner.FocusSession` with per-backend implementations across
  iTerm2, Terminal.app, and zellij — each maps the running claude
  PID back to a tab/pane and selects it. Exits 0 with `Already open:
  <slug> — switched to existing tab` on focus; preserves the old
  error on focus miss so `--force` semantics are unchanged. Warns on
  stderr when more than one claude process is detected for the same
  UUID (prior `--force`, or a manual `claude --resume`).
  ([#28](https://github.com/Facets-cloud/flow/pull/28) by
  [@pa](https://github.com/pa))

### Changed

- **README — Claude shell aliases.** New "Optional: Claude shell
  aliases" subsection in Install with two aliases: `claude` →
  `claude --dangerously-skip-permissions --bg` (skip per-tool prompts,
  run in background) and `ca` → `command claude agents` (Claude
  agents-mode shortcut; `command` bypasses the first alias so the
  `agents` subcommand sees a clean argv). Source two lines to enable
  agents mode without typing the flag each time.
  ([#57](https://github.com/Facets-cloud/flow/pull/57) by
  [@rr0hit](https://github.com/rr0hit))

## [0.1.0-alpha.14] — 2026-05-18

### Added

- **`flow do --with` / `--with-file`.** Inject a one-shot instruction
  as the resumed/started session's first user message (prefixed with
  `[via flow do --with]` so the model can distinguish injected from
  typed input). `--with-file <path>` points the session at a file
  (`read instructions at <abs-path>`) instead of embedding contents —
  no size limits. `--with` on a `done` task auto-rolls it back to
  in-progress. Rejected in combination with `--here` (no spawned
  session to inject into). `flow run playbook <slug>` accepts the same
  flags. The lane for nudging parked tasks and feeding ad-hoc
  instructions to scheduled playbook runs without opening the tab.
  ([#50](https://github.com/Facets-cloud/flow/pull/50) by
  [@anshulsao](https://github.com/anshulsao))
- **kitty as a first-class spawn backend.** `flow do` opens new tabs
  in kitty via `kitty @ launch --type tab` when invoked from a kitty
  shell. Requires `allow_remote_control yes` in kitty config.
  ([#37](https://github.com/Facets-cloud/flow/pull/37) by
  [@unni-facets](https://github.com/unni-facets))
- **Warp as a first-class spawn backend.** `flow do` opens new tabs
  in Warp when invoked from a Warp shell (`TERM_PROGRAM=WarpTerminal`).
  Uses `warp://action/new_tab` to open the tab and osascript to
  keystroke a self-deleting bootstrap script, since Warp has no
  AppleScript dictionary or command-running CLI. Requires macOS
  Accessibility for Warp.
  ([#46](https://github.com/Facets-cloud/flow/pull/46) by
  [@swapnildahiphale](https://github.com/swapnildahiphale))
- **Ghostty as a first-class spawn backend.** `flow do` opens new
  tabs in Ghostty when invoked from a Ghostty shell.
  ([#53](https://github.com/Facets-cloud/flow/pull/53) by
  [@cyphernext](https://github.com/cyphernext))
- **`FLOW_TERM` env override.** Set
  `FLOW_TERM=warp|iterm|terminal|zellij|kitty|ghostty` to force a
  specific spawn backend regardless of `$TERM_PROGRAM`. `$ZELLIJ`
  still wins; unrecognized values fall through to `$TERM_PROGRAM`
  detection.
  ([#46](https://github.com/Facets-cloud/flow/pull/46))
- **`flow run playbook --here`.** Bind THIS Claude session to a
  playbook-run task without spawning a new tab — mirrors `flow do
  --here` for run-tasks. Includes a close-out sweep refactor.
  ([#48](https://github.com/Facets-cloud/flow/pull/48) by
  [@vishnukv-facets](https://github.com/vishnukv-facets))

### Changed

- **`flow list` rendering.** Tabwriter-aligned columns,
  `--format json|tsv` for machine-readable output, ANSI color when
  stdout is a TTY.
  ([#44](https://github.com/Facets-cloud/flow/pull/44) by
  [@unni-facets](https://github.com/unni-facets))
- **README wordmark logo.** Theme-aware SVG logo at the top of
  the README.
  ([#35](https://github.com/Facets-cloud/flow/pull/35) by
  [@pa](https://github.com/pa))

### Fixed

- **flowdb concurrent-open race.** `busy_timeout` is now applied at
  `OpenDB` time so concurrent opens don't race the pragma.
  ([#36](https://github.com/Facets-cloud/flow/pull/36) by
  [@pa](https://github.com/pa))
- **e2e spawner override leak.** Pin `spawner.Override` in the e2e
  test so a real kitty tab isn't spawned during CI.
  ([#42](https://github.com/Facets-cloud/flow/pull/42) by
  [@unni-facets](https://github.com/unni-facets))

## [0.1.0-alpha.8] — 2026-05-09

### Added

- **zellij as a first-class spawn backend.** `flow do` now opens new
  tabs inside the current zellij session when `$ZELLIJ` is set, via
  `zellij action new-tab` + `zellij action write-chars`. Behavior is
  unchanged for non-zellij users — selection priority is `$ZELLIJ` →
  `Apple_Terminal` → `iTerm.app` → iTerm-default. Requires zellij ≥
  0.40. Embedded skill (`SKILL.md`) wording neutralized from
  "iTerm tab" to "terminal tab" so it reads correctly across all
  backends; the Terminal.app Accessibility section stays backend-specific.
  ([#21](https://github.com/Facets-cloud/flow/pull/21) by
  [@pa](https://github.com/pa))

## [0.1.0-alpha.7] — 2026-05-09

### Removed

- **UserPromptSubmit hook.** Per-prompt skill nudge in ad-hoc Claude
  sessions retired — the ~200 words of `additionalContext` injected
  on every user prompt cost more in tokens than it returned in
  marginal §4.14 reliability over the SessionStart hook alone. The
  command itself (`flow hook user-prompt-submit`) is now a permanent
  no-op so any stale entry left behind in older `~/.claude/settings.json`
  files doesn't error; both `flow skill install` and the auto-upgrade
  path actively remove the entry, leaving any unrelated user-defined
  hooks in the same event untouched.

## [0.1.0-alpha.6] — 2026-05-08

### Added

- **Free-form tags on tasks.** New `task_tags(task_slug, tag,
  created_at)` table; values normalized lowercase + trimmed. Add via
  `flow update task <ref> --tag <t>` (repeatable, idempotent), remove
  via `--remove-tag`, wipe via `--clear-tags`. Filter task listings via
  `flow list tasks --tag <t>`. Aggregate listing via `flow list tags`
  (distinct tags + per-tag task counts) so tag vocabulary stays
  consistent over time.
- **Assignee on tasks.** `tasks.assignee TEXT`. Set at create time via
  `flow add task --assignee <name>`; post-creation via `flow update
  task --assignee` / `--clear-assignee`. NULL = self (default);
  non-null renders as `[@name]` in list/show output.
- **Live-session detection.** `flow list tasks` and `flow show task`
  mark `[live]` next to tasks whose `session_id` matches a running
  Claude process (parsed from `ps`). `flow do <ref>` refuses to spawn
  a duplicate when the task's session is already running elsewhere;
  `--force` overrides.
- **`flow find-session <marker>`.** Scans
  `~/.claude/projects/*/*.jsonl` for a marker and prints the matching
  session UUID — the reliable in-flight session-ID capture path.
  Errors deterministically on zero or multiple matches.
- **`flow update project <ref> --priority`.** Project priority is now
  editable after creation.
- **Reverse status transitions.** `flow update task <ref> --status
  in-progress` works on `done` tasks, letting `flow do` reopen them.

### Changed

- **`flow update task` is now the canonical lane for all in-place
  field edits.** New flags: `--status`, `--priority`, `--assignee` /
  `--clear-assignee`, `--due-date` / `--clear-due`, `--waiting` /
  `--clear-waiting`, repeatable `--tag` / `--remove-tag`,
  `--clear-tags`. The existing `--session-id` and `--work-dir`
  escape hatches stay.
- **Close-out sweep prompt rewritten with two-tier discipline.** KB
  step is strict — default = write nothing; three bars (durable /
  surprising / future-relevant); distill the essence rather than
  quote-dump (deliberate departure from §4.10 real-time scoop). The
  project-log step is more permissive — narrative is fine when the
  session moved the project forward. Floating-task prompts omit
  project-update concepts entirely.
- **Skill (`SKILL.md`).** New §4.16 (binding an in-flight session to
  a task via marker-grep). New §4.16a (tagging — vocabulary
  discipline rule that says read `flow list tags` before inventing).
  §4.2 intake gets an optional tag step at the end (offers existing
  tags via multi-select AskUserQuestion) and a subtle
  retrospective-capture hint pointing at §4.16. §4.6 waiting
  workflow rewired through `flow update task --waiting`. Cheat sheet
  rewritten.

### Removed

- **Legacy field-setter mini-commands consolidated into
  `flow update task`:** `flow priority`, `flow due`, `flow waiting`,
  `flow assignee`, `flow tag`, `flow tags`. The aggregate listing
  for tags moved to `flow list tags`. One canonical verb for
  in-place edits — fewer commands to learn, no parallel paths for
  the same field.

## [0.1.0-alpha.1] — 2026-05-04

Initial public release.

### Added

- **Tasks and projects.** `flow add task` / `flow add project` with
  interview-driven intake; SQLite metadata at `~/.flow/flow.db`.
- **Knowledge base.** Five markdown files
  (`user`, `org`, `products`, `processes`, `business`) under
  `~/.flow/kb/`, surfaced in every task/project context.
- **Sessions.** `flow do <task>` pre-allocates a session UUID and spawns
  a Claude Code session in a dedicated iTerm tab. Resume with the same
  command.
- **Progress notes.** Append-only markdown logs under each task and
  project (`updates/YYYY-MM-DD-*.md`).
- **Playbooks.** `flow add playbook` + `flow run playbook <slug>` for
  reusable, snapshotted run definitions.
- **Transcripts.** `flow transcript <task>` produces a readable
  conversation transcript from a task's Claude session jsonl.
- **Manual repair.** `flow update task --session-id … --work-dir …` for
  cases when the DB drifts from reality.
- **Embedded skill.** `~/.claude/skills/flow/SKILL.md` — natural-language
  interface to flow commands, installed by `flow init`.
- **SessionStart hook.** Re-injects task brief, updates, and CLAUDE.md
  context on every session resume.
- **`flow --version`.** Build-time `-ldflags '-X main.version=…'`
  populated from `git describe`.
- **Auto skill upgrade.** Released binaries detect a version bump and
  refresh the skill + hook on next invocation; `dev` builds opt out.
- **Prebuilt binaries.** Darwin arm64 + amd64 published on the GitHub
  Releases page.
- **CI.** `.github/workflows/ci.yml` runs `go vet` + `go test ./...`
  against `macos-latest` and `ubuntu-latest`.
- **License.** MIT.

[Unreleased]: https://github.com/Facets-cloud/flow/compare/v0.1.0-alpha.24...HEAD
[0.1.0-alpha.24]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.24
[0.1.0-alpha.20]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.20
[0.1.0-alpha.19]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.19
[0.1.0-alpha.18]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.18
[0.1.0-alpha.17]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.17
[0.1.0-alpha.16]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.16
[0.1.0-alpha.15]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.15
[0.1.0-alpha.14]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.14
[0.1.0-alpha.8]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.8
[0.1.0-alpha.7]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.7
[0.1.0-alpha.6]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.6
[0.1.0-alpha.1]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.1
