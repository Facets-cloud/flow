# flow

Personal task and Claude session manager. Captures context at intake time and delivers it to Claude Code sessions automatically — no re-explaining across conversations.

## Get started

```bash
git clone git@github.com:Facets-cloud/flow.git && cd flow && make install && source ~/.zshrc
```

Then open Claude Code and say **"let's get to work"**. Flow will guide you from there.

## What it does

- **Projects and tasks** tracked in a local SQLite database
- **Markdown briefs** written via an interview-driven intake — every task starts with clear What/Why/Where/Done-when
- **Per-task Claude sessions** spawned in iTerm tabs with full context (brief, progress notes, repo conventions) injected automatically
- **Session resume** via `flow do <task>` — picks up exactly where you left off
- **Knowledge base** — durable facts about you, your org, products, processes, and business that carry across all sessions
- **Progress notes** — append-only markdown logs under each task so context survives across sessions

## Prerequisites

- macOS (iTerm2 for session spawning)
- Go 1.25+ (to build from source)
- [Claude Code](https://claude.ai/claude-code) CLI installed

## What `make install` does

1. Builds the `flow` binary in the repo directory
2. Adds that directory to your PATH in `~/.zshrc`
3. Installs the Claude Code skill and SessionStart hook

That's it — no data directory or database yet. The first time you talk to Claude, the skill detects that flow isn't initialized, offers to run `flow init`, and walks you through creating your first project and task.

## Usage

You don't need to memorize commands. Just talk to Claude:

- **"what should I work on"** — shows your task list
- **"add a task"** — interviews you and saves a structured brief
- **"resume auth"** — opens a dedicated Claude session for that task
- **"save a note"** — logs progress under the current task
- **"mark done"** — closes out the task

For direct CLI use:

```bash
flow list tasks --status in-progress
flow add project "My App" --work-dir ~/code/my-app
flow add task "Add auth" --project my-app --slug auth
flow do auth
flow done auth
```

## How it works

`flow do <task>` spawns a new iTerm tab running `claude` with environment variables (`FLOW_TASK`, `FLOW_PROJECT`) set. A SessionStart hook re-injects context on every resume. The execution session's first action is `flow register-session`, which writes its session UUID back to the database so future `flow do` calls resume the same conversation.

Briefs live at `~/.flow/tasks/<slug>/brief.md`. Progress notes accumulate under `~/.flow/tasks/<slug>/updates/`. The flow skill (installed to `~/.claude/skills/flow/SKILL.md`) interprets natural language into flow commands and enforces interview-driven intake.

## Data directory

All runtime state lives under `~/.flow/` (or `$FLOW_ROOT` if set):

```
~/.flow/
  flow.db          # SQLite database
  kb/              # knowledge base (5 markdown files)
  projects/        # per-project briefs and updates
  tasks/           # per-task briefs and updates
```

The `flow` binary lives wherever you cloned this repo. Source code and binary are the same directory.

## Environment variables

| Variable | Purpose |
|---|---|
| `FLOW_ROOT` | Override the default `~/.flow` data directory |
| `FLOW_TASK` | Set by `flow do` — current task slug |
| `FLOW_PROJECT` | Set by `flow do` — current project slug |
| `FLOW_STALE_DAYS` | Staleness threshold in days (default: 3) |
