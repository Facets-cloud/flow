<p align="center">
  <img src="assets/flow-logo.svg" alt="flow" width="480">
</p>

<p align="center">
  <img src="https://img.shields.io/badge/status-alpha-orange" alt="Status">
  <img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License">
</p>

> A complete task manager for Claude Code — and the working memory
> layer that turns every session from a brilliant new hire into the
> engineer on your team.

## See it in action

A four-act demo of how flow compounds context across days and tasks.
The work is silly on purpose — Star Trek bridge starships — so the
mechanic is what you watch, not the code.

**Act 1 — Capture the work.** Just talk. flow interviews you for
what / why / where / done-when, drafts a structured brief, and opens
a dedicated Claude session for the task in a new tab.

![Act 1 — task intake](docs/demo/01-task-intake.gif)

**Act 2 — Work, then park.** The session has the brief, the project
context, and the knowledge base loaded. You build until you hit a
blocker — here, "Kirk needs to review this" — and tell Claude to
park it. Status flips to `waiting`. Tab can close.

![Act 2 — execution and park](docs/demo/02-task-execution.gif)

**Act 3 — Resume and close.** A day later you say "Kirk signed off."
Same session resumes with full memory of where it left off. `flow
done` flips status and triggers the sweep — Claude re-reads the
whole transcript and writes durable facts (Kirk approved the design,
the ship class, the conventions used) into the knowledge base.

![Act 3 — approval and close](docs/demo/03-approval-and-close.gif)

**Act 4 — Months later, a new captain.** New task: "Picard's the new
boss, he wants the starship as an SVG." Brand new session, but it
already knows the ship, knows the design choices Kirk approved,
knows the project conventions — because the KB carried it. Claude
just gets to work.

![Act 4 — Picard's SVG upgrade](docs/demo/04-picard-svg-upgrade.gif)

That fourth session is what flow is really about. Not the first
session — the fiftieth.

## Why flow

If you use Claude Code daily, you've felt the ceiling: every session
is a new hire. Brilliant, capable, ready to help — but with no memory
of yesterday's decisions, last week's migrations, or the half-finished
threads in your other tabs. You spend the first ten minutes of every
session catching it up.

flow changes the relationship. It's a complete task manager —
projects, tasks, structured briefs, progress notes, playbooks for
recurring work — *and* a working memory layer that injects all of it
into every Claude session automatically. Capture once, work with
Claude on it forever.

The first session feels normal. By session ten, Claude knows your
codebase quirks, your team, the customer you keep mentioning, and
the migration you're three steps into. By session fifty, it's the
engineer on your team — not a new hire you re-explain yourself to
every morning.

Built for power users who want Claude to *work with them*, not just
*help them*.

## How context compounds

Every task feeds the same knowledge base. Every closed task makes
the next one smarter.

```
                                       ┌────────────────────────┐
                                       │   ~/.flow/kb/          │
                                       │   user · org · products│
                                       │   processes · business │
                                       └─────▲──────────▲───────┘
                                             │          │
                  flow do <task>             │ scoop    │ sweep
   ┌────────┐  ─────────────────▶  ┌─────────┴──────────┴─────┐
   │  Task  │                      │      Claude session      │
   │  brief │  ◀──── updates ───── │  loads brief + kb +      │
   │ +notes │                      │  notes + repo conventions│
   └────────┘  ─── flow done ───▶  └──────────────────────────┘
                                       (auto-sweep transcript
                                        into kb on done)
```

- **Scoop (live):** during a session the flow skill listens for
  durable facts you mention — your role, a teammate's name, a
  product convention — and appends them to the matching kb file
  on the fly.
- **Sweep (on `flow done`):** when you close a task, flow spawns
  a headless Claude pass that re-reads the entire transcript and
  pulls anything kb-worthy that the live scoop missed. The status
  flip is the contract; the sweep is best-effort.
- **Cross-reference:** `flow transcript <sibling-task>` lets a
  current session read what was decided in a related one — useful
  when the brief alone doesn't carry enough context.

Net effect: the longer you use flow, the more your knowledge base
grows, the less you re-explain yourself.

## Playbooks for the work you do on cadence

Some work repeats. Weekly reviews. Daily PR triage. On-call rotations.
Customer-meeting prep.

A **playbook** is a reusable run definition — a markdown brief that
describes what a run does. `flow run playbook weekly-review` snapshots
that brief into a fresh task and spawns a new Claude session against
it. Every run is reproducible (it executes against a frozen snapshot,
so editing the playbook later doesn't rewrite history) and contributes
back to the knowledge base on `flow done` like any other task.

```
┌──────────┐  flow run playbook weekly-review
│ Playbook │ ────────▶ snapshot ─────▶ new task ─────▶ new session
│  brief   │           (frozen for                     (executes
└──────────┘            reproducibility)                against snapshot)
```

Same compounding mechanic — your weekly review session two months from
now will know everything every prior weekly review surfaced.

## Install

In any Claude Code session, paste this:

> Install flow from https://github.com/Facets-cloud/flow

Claude reads the repo, downloads the binary, and runs `flow init` —
which installs the flow skill into `~/.claude/skills/flow/SKILL.md`
and registers a SessionStart hook so every future Claude session
loads the skill automatically. Then say **"let's get to work"** and
follow along.

<details>
<summary>Manual install (curl + chmod + flow init)</summary>

```bash
# 1. Download the binary for your Mac.
ARCH=arm64        # Apple Silicon (M1/M2/M3/M4) — use amd64 for Intel.

curl -fsSL -o /usr/local/bin/flow \
  "https://github.com/Facets-cloud/flow/releases/latest/download/flow-darwin-${ARCH}"
chmod +x /usr/local/bin/flow
xattr -d com.apple.quarantine /usr/local/bin/flow 2>/dev/null || true

# 2. Initialize. This is required — it creates ~/.flow/, the SQLite
#    index, the knowledge base, AND installs the Claude skill +
#    SessionStart hook. Without this step, Claude can't talk to flow.
flow init
```

`flow init` is the step that wires flow into Claude Code. It:

- Creates `~/.flow/` (database, kb, projects, tasks, playbooks)
- Writes the flow skill to `~/.claude/skills/flow/SKILL.md`
- Adds a SessionStart hook to `~/.claude/settings.json` so every new
  Claude Code session auto-loads the skill

The `xattr` step removes Gatekeeper's quarantine attribute so macOS
doesn't refuse to run the unsigned binary.

</details>

## Upgrade

In any Claude Code session:

> Upgrade flow from https://github.com/Facets-cloud/flow

Claude fetches the latest release binary and runs `flow skill
update` to refresh the skill and re-wire the SessionStart and
UserPromptSubmit hooks. Check the running version with
`flow --version`.

## Quickstart

Just open Claude and say **"let's get to work"**. The skill
handles the rest.

## What you get

- **One task, one agent session, one tab.** `flow do <task>`
  spawns a dedicated tab in iTerm2, Warp, stock macOS Terminal,
  kitty (requires `allow_remote_control yes` in `kitty.conf`), or
  your current zellij session (requires zellij ≥ 0.40) — flow
  picks whichever you launched it from. Override with
  `FLOW_TERM=warp|iterm|terminal|zellij|kitty` when you're on a
  non-standard host. Tomorrow's `flow do <task>` resumes the same
  conversation.
- **Claude or Codex, your call.** Default is Claude. Pass
  `--agent codex` (or `--codex`) on `flow add task`, `flow do`,
  or `flow run playbook` to bootstrap a Codex session instead.
  Provider is per-task — switching is a per-task decision, not a
  global one. The knowledge base, briefs, and close-out sweep
  work the same way either way.
- **Worktrees by default.** `flow do` creates a per-task git
  worktree at `<repo>/.<agent>/worktrees/<slug>` on branch
  `flow/<slug>`, so two parallel tasks on the same repo never
  step on each other's working tree. `--no-worktree` opts out;
  `flow do --here` binds the current Claude or Codex session and
  never relocates.
- **Auto-PR on done.** `flow done` pushes the worktree branch and
  runs `gh pr create` against the detected base branch with the
  task brief as the PR body. The PR URL is recorded against the
  task. `--no-pr` opts out; push or PR failures warn and
  continue, never block the status flip.
- **Mission Control, in your browser.** `flow ui serve` boots a
  local web UI at `127.0.0.1:8787` that browses every task,
  project, playbook, and run; edits briefs inline; streams live
  status; embeds an xterm.js terminal bridge to attach to any
  session from the browser; and offers a Cmd+K switcher. No
  cloud, no auth — bound to loopback by default.
- **Full-text search over flow memory.** `flow search "<query>"`
  searches brief and update markdown through SQLite FTS5. Add
  `--in transcripts` when you explicitly want session transcript
  search; transcripts stay out of the default scope because they
  are much larger.
- **Interview-driven task capture.** No forms. flow asks
  what / why / where / done-when, then writes a structured brief.
- **A knowledge base that grows.** Five markdown buckets for
  durable facts about you, your team, products, processes, and
  customers. Live-appended during sessions; auto-swept from
  transcripts on `flow done`.
- **Per-task progress notes.** Append-only logs. Pick up where
  you left off, even after a week away.
- **Playbooks for cadence work.** Weekly reviews, daily triage,
  on-call rotations — define once, run on demand.
- **Soft delete, then restore.** `flow delete` hides a task,
  project, or playbook from normal lists and the UI without
  touching its markdown. `flow restore` brings it back. Use
  `--include-deleted` (or `--deleted`) on `flow list` to see
  what's hidden.
- **A skill that speaks plain English.** "What should I work
  on", "resume auth", "save a note" — the bundled Claude skill
  turns intent into flow commands.

## How it works under the hood

`flow do <task>` resolves the task's provider (`claude` by
default, `codex` when the task was created with `--agent codex`),
pre-allocates or captures a session id, writes it to the task
row, and spawns a tab in zellij (when `$ZELLIJ` is set), kitty
(when `$KITTY_WINDOW_ID` is set or `$TERM=xterm-kitty`), the
backend named in `$FLOW_TERM` (when set), or Warp / iTerm2 / stock
Terminal.app (auto-detected from `$TERM_PROGRAM`) — chosen in
that priority order, with iTerm as the historical fallback —
running `claude --session-id <uuid>` (or the equivalent
`codex resume <uuid>`) with `FLOW_PROJECT` inlined. For Claude
the jsonl file lands at the deterministic path
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`; for Codex it's
captured back from Codex's own session store. Either way, future
`flow do` calls resume the same conversation. A SessionStart
hook re-injects the task brief, updates, and CLAUDE.md context on
every resume.

### Worktrees, branches, and the close-out PR

By default `flow do` ensures a per-task git worktree at
`<repo>/.<agent>/worktrees/<slug>` on branch `flow/<slug>`,
forked from `origin/HEAD` (detected at task start). The agent
session is launched inside that worktree, so multiple tasks
against the same repo never collide. `tasks.worktree_path`
remembers the path; `flow show task` surfaces it. `--no-worktree`
spawns inside the task's raw `work_dir` instead.

When you `flow done <task>`, flow snapshots the worktree's diff
against its starting HEAD, runs the close-out sweep, then pushes
the branch and runs `gh pr create --base <detected> --head
flow/<slug>` with the task brief as the PR body. The PR URL is
stored in `task_pr_links`. Pass `--no-pr` to skip; push or PR
failures warn and keep going (the status flip is the contract).

### Focus instead of spawn for live sessions

When `flow do <task>` is run for a task whose session is already
live in another tab, flow focuses that tab instead of spawning a
duplicate. The source tab prints "Already open: `<slug>` — switched
to existing tab" as an audit line.

The first `flow do` from stock Terminal.app needs macOS Accessibility
permission for the **app hosting your shell** — not the `flow` binary
itself. Terminal.app's AppleScript dictionary has no "make new tab"
verb, so flow drives cmd-T through System Events, and System Events
checks Accessibility against the responsible parent app. Until that's
granted, `flow do` errors out with a multi-line explanation pointing at
System Settings → Privacy & Security → Accessibility (enable the
toggle for "Terminal" if you launched flow from Terminal.app, "iTerm"
from iTerm2, "Claude" if Claude Code is the host, etc.; add it via the
+ button if it's not listed). After the grant the spawn is silent.
iTerm2 doesn't need this — it has a native `create tab` verb.

### One-shot instructions with `--with`

`flow do <task> --with "<instruction>"` resumes (or starts) the
task's session and injects the instruction as the first user
message — prefixed with `[via flow do --with]` so the model can
tell injected input from typed input.

`--with-file <path>` is the same idea for longer instructions:
instead of embedding the file contents, flow injects `read
instructions at <absolute path>` and the session uses its Read
tool to load the file. No size limits. The flags are mutually
exclusive, and cannot be combined with `--here` (there's no
spawned session to inject into).

```bash
# Nudge a parked task without opening the tab.
flow do auth --with "check if upstream PR merged and update the brief if so"

# --with on a done task auto-rolls it back to in-progress, so playbooks
# can fire on previously-closed work.
flow do auth --with "are we still blocked on the security review?"

# Hand the session a longer brief to follow.
flow do auth --with-file ~/playbooks/triage-checklist.md
```

This is the lane scheduled playbooks use to fire instructions at
existing tasks without manual intervention. `flow run playbook
<slug>` accepts the same flags for ad-hoc per-run instructions.

### Mission Control web UI

`flow ui serve` runs a small Go HTTP server (default
`127.0.0.1:8787`, override with `--host` / `--port`, background
with `--bg`) that ships a single-page UI from
`internal/server/static/`. It reads the same `~/.flow/flow.db` the
CLI does, so the browser, your terminal sessions, and the skill
all see one consistent view. The server exposes:

- A Mission Control landing page that aggregates active tasks,
  recent runs, attention items, and a notification center.
- Per-entity detail screens for tasks, projects, playbooks, and
  playbook runs — with inline brief editing.
- An xterm.js **terminal bridge** that attaches your browser to a
  running agent session over WebSocket, including native terminal
  snapshot sync so you don't lose scrollback when you reload.
- Live status from a repo-local **agent-hooks** integration
  (`internal/agenthooks/`): when a task's agent is mid-tool-call,
  waiting for input, or idle, the UI shows it. Codex hooks are
  gated to Flow-owned terminals (`FLOW_HOOK_OWNED=1`) so ordinary
  Codex sessions opened in the same repo do not forward Flow events.
- A global Cmd+K switcher across every entity.

It's a *local* tool — no auth, no TLS, loopback by default. Don't
expose it on a public network.

### Slack integration

flow can pull Slack events into its `monitor_events` pipeline, route
them through the same `notif-autospawn` rules engine as GitHub events,
and (when writes are enabled) post back into the originating thread for
four lifecycle moments: task done, `waiting_on` set, agent needs input,
and `notif-autospawn` drafts a task from a Slack message. Inbound Slack
messages and flow's Slack reactions/replies are also persisted in
`external_messages` / `external_message_actions`, so future FTS can index
the conversation trail instead of reconstructing it from Slack.

**Reading from Slack (ingest)** uses Slack Socket Mode, not
`conversations.history` polling. Enable Socket Mode in the Slack app,
then run `flow ui serve` with `FLOW_SLACK_SOCKET_MODE=1`,
`SLACK_APP_TOKEN`, and `SLACK_BOT_TOKEN`. **Writing to Slack** is off
by default — set `FLOW_SLACK_WRITES_ENABLED=1` to opt in. Writes never
go to a public/private channel top-level; they only DM (D-prefix
channel IDs) or thread under a known message.

**Auto-replies** are a second, stricter opt-in. Set both
`FLOW_SLACK_AUTO_REPLY_ENABLED=1` and `FLOW_SLACK_WRITES_ENABLED=1` to
let flow answer questions in Slack. Flow only answers from facts already
stored in flow (for example, a task linked to the same Slack thread). If
it cannot find a flow-backed answer, it reacts on the message and asks a
clarifying question in that same thread instead of guessing from general
model knowledge.

Env vars (precedence top → bottom):

| Env var | Default | Purpose |
|---|---|---|
| `FLOW_SLACK_SOCKET_MODE` | `false` unless `SLACK_APP_TOKEN` is set | Enables Slack Socket Mode ingest. |
| `SLACK_APP_TOKEN` | — | App-level token (`xapp-...`) with `connections:write`; opens Slack's Socket Mode WebSocket. `FLOW_SLACK_APP_TOKEN` is also accepted. |
| `SLACK_BOT_TOKEN` | — | Bot token (`xoxb-...`) used by Socket Mode and Slack write-back. `FLOW_SLACK_TOKEN`, `SLACK_USER_TOKEN`, and `SLACK_TOKEN` remain fallback Web API tokens for writes, but are not app-level Socket Mode tokens. |
| `FLOW_SLACK_WRITE_TOKEN` | — | Explicit token for Slack writes (`chat.postMessage`, `reactions.add`). Use this when Socket Mode uses a bot token but writes should use a different token with `chat:write` and `reactions:write`. `SLACK_WRITE_TOKEN` is also accepted. |
| `FLOW_SLACK_USER_TOKEN` | — | User token (`xoxp-...`) used as a fallback for resolving private-channel names and for user-scoped Events API subscriptions. `SLACK_USER_TOKEN`, `FLOW_SLACK_TOKEN`, and `SLACK_TOKEN` are also accepted. |
| `FLOW_SLACK_MENTION_USER_ID` / `FLOW_SLACK_MENTION_USER_IDS` | inferred from a user token when possible | Slack user ID(s) to treat as personal mentions. When a `message.channels` / `message.groups` / `message.mpim` event contains `<@that-user>`, flow keeps it as `slack.personal_mention` even if the channel is not in `FLOW_SLACK_CHANNELS`. |
| `FLOW_SLACK_WRITES_ENABLED` | `false` | Off-by-default safety gate. Set to `1` / `true` to allow flow to post into Slack at all. |
| `FLOW_SLACK_AUTO_REPLY_ENABLED` | `false` | Enables Flow-backed Slack auto-replies. Requires `FLOW_SLACK_WRITES_ENABLED=1`; never answers from general model knowledge. |
| `FLOW_SLACK_AUTO_REPLY_EMOJI` | `eyes` | Reaction added before flow answers or asks for clarification in the originating thread. Slack short name without colons. |
| `FLOW_SLACK_CHANNELS` | (empty) | Comma-separated channel IDs for optional channel-message events. Channel chatter only ingests when its channel ID is in this list or `FLOW_SLACK_INCLUDE_CHANNEL_MESSAGES=1`. |
| `FLOW_SLACK_INCLUDE_CHANNEL_MESSAGES` | `false` | When true, ingest non-mention channel chatter from every readable conversation. Almost certainly too noisy — prefer `FLOW_SLACK_CHANNELS`. |
| `FLOW_SLACK_DEBUG` | `false` | When true, print Socket Mode client diagnostics. |
| `FLOW_SLACK_API_BASE_URL` | `https://slack.com/api` | Override for tests / mock servers. |
| `FLOW_SLACK_AGENT_DEBOUNCE` | `60s` | Minimum gap between "agent needs your input" DMs for the same session. Duration string. |
| `FLOW_SLACK_DRAFT_EMOJI` | `eyes` | Reaction added on the originating message when `notif-autospawn` drafts a task from it. Slack short name without colons. |
| `FLOW_BASE_URL` | — | Override for the deep-link prefix flow puts into Slack notices. When unset, flow reads `~/.flow/server.url` (auto-written by `flow ui serve`); when both are unset, Slack messages omit the link and fall back to a slug-only mention. |

Slack app setup: enable **Socket Mode**, create an app-level token with
`connections:write`, and subscribe the bot to `app_mention`,
`message.im`, and `message.mpim`. Optional bot channel ingest needs
`message.channels` / `message.groups` plus the matching bot scopes.
Slack write-back needs the token selected for writes to have
`chat:write` and `reactions:write`; if `SLACK_BOT_TOKEN` lacks those,
set `FLOW_SLACK_WRITE_TOKEN` to a token that has them.
Bot events only cover conversations the bot user is part of: DMs sent
to the app/bot, channels the bot is in, and app mentions. A user's
personal self-DM, human-to-human DM, or channel mention of the human user
will not arrive through bot-only events. To catch pings to the human
without inviting the bot everywhere, configure Slack **Workspace Events**
for `message.channels` / `message.groups` / `message.mpim` as needed,
authorize with the corresponding user scopes, set
`FLOW_SLACK_MENTION_USER_ID` to the human Slack user ID (or provide an
explicit user token so flow can infer it), and let flow filter those
user-scoped message events down to personal mentions.

Default routing: DMs, app @-mentions, and personal @-mentions trigger
an attention notification in flow's inbox (`approval` mode); channel
messages are logged without surfacing a notification (`log` mode). Both
defaults are
overridable via the rules UI in Mission Control — no Slack message
ever auto-spawns an agent unless you explicitly set the rule mode
to `auto_agent` for that source/kind. See `notif-autospawn` for the
full safety stance.

The first `flow do` from stock Terminal.app needs macOS Accessibility
permission for the **app hosting your shell** — not the `flow` binary
itself. Terminal.app's AppleScript dictionary has no "make new tab"
verb, so flow drives cmd-T through System Events, and System Events
checks Accessibility against the responsible parent app. Until that's
granted, `flow do` errors out with a multi-line explanation pointing at
System Settings → Privacy & Security → Accessibility (enable the
toggle for "Terminal" if you launched flow from Terminal.app, "iTerm"
from iTerm2, "Claude" if Claude Code is the host, etc.; add it via the
+ button if it's not listed). After the grant the spawn is silent.
iTerm2 doesn't need this — it has a native `create tab` verb.

## Your data — local, portable, yours

Everything flow stores lives under `~/.flow/` (override with
`$FLOW_ROOT`). No server, no cloud, no telemetry. Plain markdown
beside a SQLite index — readable in any editor, versionable in git.

```
~/.flow/
  flow.db                          # SQLite — projects, tasks, playbooks index
  kb/
    user.md  org.md  products.md
    processes.md  business.md      # 5 markdown buckets, append-only
  projects/<slug>/
    brief.md
    updates/YYYY-MM-DD-*.md
  tasks/<slug>/
    brief.md
    updates/YYYY-MM-DD-*.md
  playbooks/<slug>/
    brief.md
    updates/YYYY-MM-DD-*.md
```

The SQLite database is an *index*, not the source of truth — every
task and project has its real content in the markdown files next to
it. You could delete `flow.db` and rebuild it from the markdown if
you had to.

### Backup & sync

Pick whichever fits your workflow:

- **Git (recommended for single-user history).**
  ```bash
  cd ~/.flow && git init && git add . && git commit -m "initial"
  ```
  Commit periodically. The SQLite file is binary, so diffs aren't
  useful, but each commit is a clean snapshot. **If you push to a
  shared remote**, add `kb/` to `.gitignore` first — kb files often
  contain personal or org-sensitive notes you don't want public.

- **Time Machine / system backup.** Just works, no setup.

- **iCloud Drive / Dropbox / Google Drive.** Symlink `~/.flow` into
  the synced folder:
  ```bash
  mv ~/.flow ~/Library/Mobile\ Documents/com~apple~CloudDocs/flow
  ln -s ~/Library/Mobile\ Documents/com~apple~CloudDocs/flow ~/.flow
  ```
  ⚠️ **Don't run flow on two machines simultaneously** through a
  synced folder — SQLite doesn't tolerate concurrent writes from
  separate hosts and you can corrupt `flow.db`. Use this for backup
  + occasional second-machine access, not active multi-machine use.

- **Manual rsync.** `rsync -a ~/.flow/ /path/to/backup/flow/` on a
  schedule. Same caveat about concurrent writes.

To move flow to a new machine: copy `~/.flow/` over, install the
binary, and run `flow init` once — it'll pick up the existing data
and reinstall the skill + hook.

## Where flow runs (and where we'd love help)

Today flow runs on **macOS (iTerm2, Warp, stock Terminal.app,
kitty, or zellij) + Claude Code or Codex**. That's the stack we
use, and that's what the session-spawn layer was built and
tested against. zellij and kitty work on Linux too as a side
effect — both are cross-platform and flow's zellij / kitty
backends don't depend on any macOS APIs. Kitty needs
`allow_remote_control yes` (or `socket-only`) in `kitty.conf` so
flow can drive `kitty @ launch` from inside the running kitty
instance.

The architecture is portable — session spawning is one small
package, agent providers are pluggable via `internal/agents/` —
but other harnesses (Cursor, Aider, plain shell) and other
terminals (Linux + tmux/wezterm, Windows Terminal) need
contributors who run those stacks daily and care enough to wire
them in. If that's you, [a PR is very welcome](CONTRIBUTING.md).

## Where flow came from

flow started as an internal tool at Facets. We use Claude Code every
day, and the context-loss problem was eating into how much of the
tool's capability we could actually use. flow fixed that for us — to
the point where we couldn't imagine working without it. We're
open-sourcing it as-is because it might do the same for you.

This is not a Facets product. There's no signup, no cloud, no upsell.
Just the tool we built for ourselves.

## Docs & contributing

- [Contributing](CONTRIBUTING.md) — bug reports, PRs, dev setup
- [Changelog](CHANGELOG.md)
- [Security](SECURITY.md) — how to report issues
- [Code of Conduct](CODE_OF_CONDUCT.md)

## License

[MIT](LICENSE) — © 2026 Facets Cloud
