# README Narrative Redesign — Design Spec

## Goal

Rewrite the flow README to lead with a relatable, problem-first narrative
that makes the value proposition immediately clear to two audiences:

1. **Claude Code power users** who already feel the pain of fragmented sessions
2. **Casual Claude Code users** who haven't articulated the problem yet

## Core Narrative

Flow is not a task tracker. It's not a session manager. It's the
operating layer between you and Claude — the thing that turns isolated
conversations into a continuous working relationship where context
compounds.

**Central analogy:** You don't hire a new engineer every day. You hire
one, and you work together. Right now, every Claude session is a
brilliant engineer on their first day — capable of anything, but knows
nothing about you. Flow fixes that.

**Key insight:** The bottleneck isn't Claude's capability. It's context.
A real colleague knows your org, your role, your products, your team's
quirks, your deployment process. Claude knows none of this — unless you
give it a persistent layer to learn from.

## Tone

Sharp and punchy. Short sentences. Pain point, consequence, solution.
Every line earns its place. No filler, no marketing fluff.

## README Structure

### Section 1: The hook

```markdown
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
```

### Section 2: The problem

Make the pain visceral. Four bullet points covering: session amnesia,
session sprawl, lack of prioritization context, and lack of personal/org
context.

```markdown
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
```

### Section 3: What flow does

Solution framed as four value props, not feature descriptions. Focus on
the *experience change*, not the mechanics.

```markdown
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
```

### Section 4: How it works

Concrete mechanics, kept brief. Five bullet points covering: data model,
session spawning, knowledge base, progress notes, natural language skill.

```markdown
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
```

### Sections 5-8: Retained from current README (lightly edited)

- **Prerequisites** — kept as-is (macOS, Go 1.25+, Claude Code CLI)
- **Install** — kept as-is (`make install` instructions)
- **Quick start** — kept as-is (add project, add task, flow do, etc.)
- **Data directory** — moved to bottom, kept as reference
- **Environment variables** — moved to bottom, kept as reference

## What changes from current README

| Current | New |
|---|---|
| Leads with "Personal task and Claude session manager" | Leads with the engineer analogy and relationship framing |
| Feature list at top | Problem statement at top, features reframed as value props |
| "What it does" is a bullet list of capabilities | "What flow does" is four narrative blocks about experience change |
| No emotional hook | Pain points section makes the problem visceral |
| Mechanics-first | Narrative-first, mechanics second |
| Data directory and env vars are mid-page | Moved to bottom as reference material |

## What stays the same

- Prerequisites, Install, Quick start sections (functional, already good)
- Data directory layout (reference material, just repositioned)
- Environment variables table (reference material, just repositioned)
