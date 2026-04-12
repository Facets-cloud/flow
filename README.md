# flow

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
  you, your org, your products, your processes, and your business. Claude
  reads these and learns them over time. You never repeat yourself.
- **Progress notes** — append-only logs under each task. Context survives
  across sessions so Claude knows what happened last time.
- **A Claude skill** interprets natural language into flow commands. Say
  "what should I work on" or "add a task" — the skill handles the rest.

## Prerequisites

- macOS (iTerm2 for session spawning)
- Go 1.25+ (to build from source)
- [Claude Code](https://claude.ai/claude-code) CLI installed

## Install

```bash
git clone git@github.com:Facets-cloud/flow.git && cd flow && make install && source ~/.zshrc
```

Then open Claude Code and say **"let's get to work"**. Flow will guide you from there.

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

## How it works under the hood

`flow do <task>` spawns a new iTerm tab running `claude` with environment
variables (`FLOW_TASK`, `FLOW_PROJECT`) set. A SessionStart hook
re-injects context on every resume. The execution session's first action
is `flow register-session`, which writes its session UUID back to the
database so future `flow do` calls resume the same conversation.

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
