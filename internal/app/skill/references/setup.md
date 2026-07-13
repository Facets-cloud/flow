# flow first-run detection & setup

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

## 3. First-run detection (once per session)

The **first time in a session** you're about to run a `flow` command,
run `flow list tasks` or `flow list projects` as a probe:

- If the command **succeeds** (even with zero results): flow is
  initialized. Proceed normally. **Do not check again this session.**
- If the command **errors** with a message about a missing database:
  the user hasn't initialized flow yet. Use `AskUserQuestion` (header:
  "Set up flow?", options: "Yes, set it up" / "No, not now") with
  question text describing flow as a personal task and session
  manager that will store its data in `$FLOW_ROOT` (or `~/.flow` if
  unset). On "Yes", run `flow init` yourself and then enter the
  **first-run coaching** below. On "No", stop.

### First-run coaching

After `flow init` succeeds for a brand-new user, walk them through the
basics in this order:

1. **Explain what just happened.** "`flow init` created `~/.flow/` with
   an empty database and 5 knowledge-base files."

2. **Create their first project.** "Let's set up a project — what's the
   main thing you're working on right now?" Then enter the §5.3
   add-project interview. This gets them a project and at least one task
   immediately.

3. **Show how to start work.** After the first task exists, use
   `AskUserQuestion` (header: "Open it now?", options:
   "Open it now" / "Later, just save") to ask whether to run
   `flow do <slug>`. Briefly explain in the question: a dedicated
   Claude session gets the brief, updates, and repo conventions
   automatically. If "Open it now", proceed to §4.4. If "Later",
   stop here.

4. **Mention the knowledge base.** "As we work together, I'll
   automatically note durable facts about you and your org in
   `~/.flow/kb/`. These notes carry across sessions so future Claude
   conversations have context without you repeating yourself."

5. **Point to daily use.** "From any session, just say 'what should I
   work on' or 'start my day' and I'll pull up your task list. Say
   'add a task' to capture new work."

Keep the coaching conversational and brief — don't dump all five points
in one wall of text. Let the user respond between steps. If they want
to skip ahead ("I know, just set it up"), respect that and stop
coaching.
