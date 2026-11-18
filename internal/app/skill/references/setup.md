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

Run this after `flow init` succeeds for a brand-new user, AND whenever
the SessionStart hook emits a `flow-onboarding:` block (fresh install:
no projects, no tasks, empty KB) — in Claude Code, Codex and prx alike.
Offer it first (AskUserQuestion, header "Onboard?", "Yes, set me up" /
"Skip for now"); on Skip, do the user's request and keep scooping
(§4.10). Walk them through the basics in this order:

1. **Explain what just happened.** "`flow init` created `~/.flow/` with
   an empty database and 5 knowledge-base files."

2. **Capture the knowledge base.** Interview one bucket at a time (one
   question per turn, let them skip any):
   - `user.md` — role, what they work on, working style, preferences.
   - `org.md` — company, team, manager, key people they work with.
   - `products.md` — what the org ships; which parts they own.
   - `processes.md` — tools, rituals, review/deploy/on-call rules.
   - `business.md` — customers, business model, key accounts.
   Append every answer immediately per §4.10's entry format (one fact
   per line, their words, no invention), then show a one-line summary
   per file of what got recorded.

3. **Import recent work (optional, token-bounded).** Offer it
   (AskUserQuestion "Import past sessions?": "Yes — small ($20 plan,
   Recommended)" / "Yes — large ($100–200 plan)" / "Skip"). Never read
   transcript files yourself — that is what burns a small plan.
   - Run `flow onboard scan --budget small|large` ONCE. Go reads every
     past Claude/Codex/prx session locally and prints a bounded list
     (~1.5k tokens): one block per directory with sample prompts and
     `harness:id` refs. Bound sessions, flow's own runs and temp dirs are
     excluded already.
   - From it propose task candidates in ONE multi-select AskUserQuestion
     (label = short task name, desc = dir + last-active). Merge sessions
     about the same thing into one candidate; drop one-off questions.
     Cap: 3 picks on small, 10 on large.
   - Per pick: `flow onboard digest <ref> --budget <same>` (≤ ~1.5k
     tokens; user prompts + last reply), then `flow add task` with
     `--work-dir <dir from scan>` — if scan marked it `(dir no longer
     exists)`, never `--mkdir` it: ask (AskUserQuestion "Floating task" /
     "Other path"), default floating (omit `--work-dir`) — a brief built ONLY from the digest
     (thin template; unknowns as "Open question"), and one update note
     `YYYY-MM-DD-imported-from-session.md` stating what was done and what
     looked unfinished. Status stays backlog unless the digest shows it
     finished — then ask "Mark done?" per §4.7.
   - Durable facts seen in digests (people, products, tools) go to kb
     per §4.10. Digest nothing the user didn't pick.

4. **Create their first project.** "Let's set up a project — what's the
   main thing you're working on right now?" Then enter the §5.3
   add-project interview. This gets them a project and at least one task
   immediately.

5. **Show how to start work.** After the first task exists, use
   `AskUserQuestion` (header: "Open it now?", options:
   "Open it now" / "Later, just save") to ask whether to run
   `flow do <slug>`. Briefly explain in the question: a dedicated
   agent session gets the brief, updates, and repo conventions
   automatically. If "Open it now", proceed to §4.4. If "Later",
   stop here.

6. **Mention the knowledge base keeps growing.** "As we work together, I'll
   keep noting durable facts about you and your org in
   `~/.flow/kb/`. These notes carry across sessions so future agent
   conversations have context without you repeating yourself."

7. **Point to daily use.** "From any session, just say 'what should I
   work on' or 'start my day' and I'll pull up your task list. Say
   'add a task' to capture new work."

Keep the coaching conversational and brief — don't dump all five points
in one wall of text. Let the user respond between steps. If they want
to skip ahead ("I know, just set it up"), respect that and stop
coaching.
