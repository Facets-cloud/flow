# flow

*The working memory between you and [Claude Code](https://claude.ai/claude-code) — for developers who use it daily on macOS.*

<!-- TODO: hero asciinema/GIF — "let's work on auth" → flowde launches new tab → Claude greets with task context. -->

You don't hire a new engineer every day. You hire one, and you work together.

Claude is the most capable coding partner you've ever had — but every
session starts from zero. It doesn't know what you're building, what you
tried yesterday, or why you care. You re-explain yourself constantly.
The more sessions you run, the worse it gets.

flow is the working relationship between you and Claude.

It's not a task tracker. It's not a session manager. It's the layer that
turns isolated Claude conversations into continuous collaboration — where
context compounds instead of evaporating.

## The problem

Think about how you use Claude today:

- You start a session, explain your project, get deep into a problem.
  Next morning: fresh session, start over.
- You have five sessions open. Which one had the auth discussion?
  Which one has your half-finished migration?
- You ask Claude to help prioritize — but it doesn't know what your
  week looks like.
- A colleague who's worked with you for a month knows your org, your
  role, your products, your team's quirks, your deployment process.
  Claude knows none of this. Every session, it's a stranger.

The bottleneck isn't Claude's capability. It's context.

## What flow does

flow sits between you and Claude. You tell flow what you're working on
and why. Claude gets that context automatically — every session, every
time.

**You capture your work once.**
Projects, tasks, priorities, acceptance criteria — structured through a
quick interview, not a form. Flow asks what, why, where, and done-when,
then writes it down.

**Claude shows up informed.**
When you start a session on a task, Claude gets the brief, the progress
notes, the repo conventions, and your knowledge base — before you say a
word.

**Context compounds.**
Progress notes accumulate. Your knowledge base grows. What Claude knows
about you on day 50 is radically different from day 1. You stop
repeating yourself. Claude starts anticipating.

**Sessions persist.**
Pick up where you left off. `flow do auth` resumes the same Claude
conversation — same context, same thread, same momentum.

## How it works

- **Projects and tasks** live in a local SQLite database. Each task gets
  a markdown brief capturing what, why, where, and done-when.
- **`flow do <task>`** spawns a Claude session in a dedicated iTerm tab
  with full context injected — brief, progress notes, repo conventions,
  knowledge base. Resume the same session tomorrow with the same command.
- **Knowledge base** — five markdown files tracking durable facts about
  you, the people you work with, the products you build, your conventions,
  and the customers or markets you care about. Solo? Use whichever buckets
  fit. Claude reads them and learns them over time. You never repeat
  yourself.
- **Progress notes** — append-only logs under each task. Context survives
  across sessions so Claude knows what happened last time.
- **A Claude skill** interprets natural language into flow commands. Say
  "what should I work on" or "add a task" — the skill handles the rest.

## Prerequisites

- macOS (iTerm2 for session spawning)
- [Claude Code](https://claude.ai/claude-code) CLI installed

## Install

Download the latest binary for your Mac, mark it executable, clear the
quarantine flag, and put it on your PATH:

```bash
# Apple Silicon (M1/M2/M3/M4):
ARCH=arm64
# Intel:
# ARCH=amd64

curl -fsSL -o /usr/local/bin/flow \
  "https://github.com/Facets-cloud/flow/releases/latest/download/flow-darwin-${ARCH}"
chmod +x /usr/local/bin/flow
xattr -d com.apple.quarantine /usr/local/bin/flow 2>/dev/null || true

flow init
```

`flow init` creates `~/.flow/`, the database, the knowledge base, and
installs the flow skill into `~/.claude/skills/flow/SKILL.md` plus a
SessionStart hook in `~/.claude/settings.json`.

Then run **`claude`** and say **"let's get to work"**.

> The `xattr` step removes Gatekeeper's quarantine attribute so macOS
> doesn't refuse to run the unsigned binary. If you prefer, the first
> launch will fail and you can right-click → Open in Finder to allow
> it instead.

## Upgrade

Re-download the binary the same way you installed it:

```bash
curl -fsSL -o /usr/local/bin/flow \
  "https://github.com/Facets-cloud/flow/releases/latest/download/flow-darwin-${ARCH}"
chmod +x /usr/local/bin/flow
xattr -d com.apple.quarantine /usr/local/bin/flow 2>/dev/null || true
```

The next time you run any flow command, the binary detects the version
bump and refreshes the skill + SessionStart hook automatically. Check
the running version with:

```bash
flow --version
```

## Build from source

If you want to hack on flow, clone and build with the included
Makefile:

```bash
git clone https://github.com/Facets-cloud/flow.git
cd flow
make install     # builds, copies binary to ~/.local/bin/flow, installs skill + hook
flow init
```

`make install` places the binary in `~/.local/bin/flow` and asks before
appending an `export PATH=…` line to your shell rc file. If you decline,
either add the line yourself or invoke flow as `~/.local/bin/flow`.
`make uninstall` removes the binary and the skill + SessionStart hook.

Local dev builds are tagged `dev` and skip the auto-upgrade check, so
you can iterate on the skill without your changes being clobbered.

## Usage

You don't need to memorize commands. Just talk to Claude:

- **"what should I work on"** — shows your task list
- **"add a task"** — interviews you and saves a structured brief
- **"resume auth"** — opens a dedicated Claude session for that task
- **"save a note"** — logs progress under the current task
- **"mark done"** — closes out the task

For direct CLI use:

```bash
flow add project "My App" --work-dir ~/code/my-app
flow add task "Add auth" --project my-app --slug auth
flow do auth
flow list tasks --status in-progress
flow done auth
```

## Who flow is for

- Indie devs who run multiple Claude sessions across projects and lose
  context between them.
- Senior ICs at small teams who use Claude as their daily pair
  programmer.
- Anyone who's ever opened a fresh Claude tab and thought *"wait, where
  did I leave off"*.

## flow is / flow isn't

**flow is**

- For developers who use Claude Code daily.
- macOS + iTerm2 only.
- One user, one machine — fully local, no server, no telemetry.
- Opinionated about session structure: one task, one session, one tab.

**flow isn't**

- A team task tracker — use Linear, Jira, or Asana for that.
- A drop-in replacement for TaskWarrior or todo.txt outside Claude
  workflows.
- Cross-platform (Linux/Windows) — by design, for now.

## How it works under the hood

`flow do <task>` pre-allocates a session UUID, writes it to the task row,
and spawns a new iTerm tab running `claude --session-id <uuid>` with
`FLOW_TASK` / `FLOW_PROJECT` environment variables inlined. The jsonl
file lands at the deterministic path
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`, so future `flow do`
calls spawn `claude --resume <uuid>` to continue the same conversation.
A SessionStart hook re-injects the task brief, updates, and CLAUDE.md
context on every resume.

Briefs live at `~/.flow/tasks/<slug>/brief.md`. Progress notes accumulate
under `~/.flow/tasks/<slug>/updates/`. The flow skill (installed to
`~/.claude/skills/flow/SKILL.md`) interprets natural language into flow
commands and enforces interview-driven intake.

## Data directory

All runtime state lives under `~/.flow/` (or `$FLOW_ROOT` if set):

```
~/.flow/
  flow.db          # SQLite database
  kb/              # knowledge base (5 markdown files)
  projects/        # per-project briefs and updates
  tasks/           # per-task briefs and updates
```

## Environment variables

| Variable | Purpose |
|---|---|
| `FLOW_ROOT` | Override the default `~/.flow` data directory |
| `FLOW_TASK` | Set by `flow do` — current task slug |
| `FLOW_PROJECT` | Set by `flow do` — current project slug |
| `FLOW_STALE_DAYS` | Staleness threshold in days (default: 3) |
