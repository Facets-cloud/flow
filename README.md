# flow

Personal task and Claude session manager. Captures context at intake time and delivers it to Claude Code sessions automatically — no re-explaining across conversations.

## What it does

- **Projects and tasks** tracked in a local SQLite database (`~/.flow/flow.db`)
- **Markdown briefs** written via an interview-driven intake workflow so every task starts with clear What/Why/Where/Done-when
- **Per-task Claude sessions** spawned in iTerm tabs with full context (brief, progress notes, repo conventions) injected automatically
- **Session resume** via `flow do <task>` — picks up exactly where you left off
- **Knowledge base** (`~/.flow/kb/`) — durable facts about you, your org, products, processes, and business that carry across all sessions
- **Progress notes** — append-only markdown logs under each task so context survives across sessions

## Prerequisites

- macOS (iTerm2 for session spawning)
- Go 1.25+ (to build from source)
- [Claude Code](https://claude.ai/claude-code) CLI installed

## Install

```bash
# Clone the repo
git clone git@github.com:Facets-cloud/flow.git ~/rohit-gtd/flow
cd ~/rohit-gtd/flow

# Build, add to PATH, initialize data dir, install skill + hook
make install

# Then either source your shell or open a new terminal
source ~/.zshrc
```

`make install` does three things: builds the `flow` binary in the repo directory, adds that directory to your PATH in `~/.zshrc`, and runs `flow init` (which creates `~/.flow/`, seeds the DB and KB files, and installs the Claude Code skill and SessionStart hook).

After install, the flow skill is available in every Claude Code session. Say "add a task" or "what should I work on" and the skill activates.

## Quick start

```bash
# Add a project (the skill will interview you for details)
flow add project "My App" --work-dir ~/code/my-app

# Add a task under it
flow add task "Add auth" --project my-app --slug auth

# Open a dedicated Claude session for the task
flow do auth

# Later: check your work
flow list tasks --status in-progress

# Mark done
flow done auth
```

## How it works

`flow do <task>` spawns a new iTerm tab running `claude` with environment variables (`FLOW_TASK`, `FLOW_PROJECT`) set. A SessionStart hook re-injects context on every resume. The execution session's first action is `flow register-session`, which writes its session UUID back to the database so future `flow do` calls resume the same conversation.

Briefs live at `~/.flow/tasks/<slug>/brief.md`. Progress notes accumulate under `~/.flow/tasks/<slug>/updates/`. The flow skill (installed to `~/.claude/skills/flow/SKILL.md`) interprets natural language into flow commands and enforces interview-driven intake.

## Data directory

All runtime state lives under `~/.flow/`:

```
~/.flow/
  flow.db          # SQLite database
  kb/              # knowledge base (5 markdown files)
  projects/        # per-project briefs and updates
  tasks/           # per-task briefs and updates
```

The `flow` binary lives wherever you installed it (e.g. `/usr/local/bin/flow`). Source code lives wherever you cloned this repo.

## Environment variables

| Variable | Purpose |
|---|---|
| `FLOW_ROOT` | Override the default `~/.flow` data directory |
| `FLOW_TASK` | Set by `flow do` — current task slug |
| `FLOW_PROJECT` | Set by `flow do` — current project slug |
| `FLOW_STALE_DAYS` | Staleness threshold in days (default: 3) |
