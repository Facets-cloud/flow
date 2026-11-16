# Brief templates

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

## 7. The task brief format

Use this as a literal template when writing `brief.md` files. Section
headings are fixed; content is whatever came out of the interview.

```markdown
# <task name, verbatim>

## What
<one sentence from the interview, no editorializing>

## Why
<short paragraph capturing the user's reason>

## Where
work_dir: <absolute path>

## Done when
- <bullet 1 from acceptance criteria>
- <bullet 2>
- <bullet 3>

## Out of scope
- <non-goal 1>

## Open questions
- <question 1>
- <question 2>

---
*Before you start on this task, read CLAUDE.md in the work_dir and any
nested CLAUDE.md files in the subtree you plan to modify. Then read
every file under `updates/` (if any exist) to catch up on prior
progress.*
```

**Thin task brief (intake-minimal):**

```markdown
# <name>

## What
<one sentence from intake>

## Why
*Deferred — fill in at task start.*

## Where
work_dir: <path>

## Done when
*Deferred — fill in at task start.*

## Out of scope
*Deferred*

## Open questions
*Deferred*

---
*This brief is thin. Before you start substantive work, the bootstrap
session will prompt you to fill in the deferred sections.*
```

**Driver task: the Role section.** When a task becomes a driver (`flow
add task --tag driver` or `flow update task --tag driver`), flow appends
this section as the LAST section of `brief.md` if the brief has no `## Role`
section yet; it never overwrites or duplicates one. The SessionStart hook
then just says "follow the ## Role section in your brief.md". When you
overwrite a driver task's stub with the drafted brief, Read it first and
keep this section at the end (edit it per task if the user wants); if it
is lost anyway, flow re-appends it on the next `flow do` or SessionStart.
Removing the driver tag leaves the section in place, and it is then ignored.

```markdown
## Role: driver

This section applies only while this task is tagged `driver`; once the
tag is removed, ignore it.

You own this task's outcome end to end. It is done when "Done when" is met
and you have verified it, not when the work has been handed off.
In any headless auto run (`flow auto` or `flow do --auto`), do the work
yourself; delegation applies only to the interactive driver session.

- Delegate: hand self-contained work (implementation, research, debugging,
  fix + PR + test, scans, audits) to headless `flow auto` children, not the
  harness's built-in subagents. Keep the conversation, decisions, verifying
  results and live/prod applies yourself. How to split the work is your
  call (optional shapes: flow skill references/delegation.md).
- Spawn with `flow auto "<name>" --slug <s> --brief <file>`; announce
  "spawning auto child: <slug>" and continue without asking.
- Children cannot spawn grandchildren.
- Don't idle while children run; use the time on open decisions.
- Every child brief states: the goal; where (repo, path, branch); the
  output file you will read (an update note on its task); Done when; its
  gates; what NOT to do; where questions go; report via `flow message` at
  milestones and when done or blocked; never block waiting for a reply.
- Reuse children: before spawning, check for a child that already knows
  the area and re-ask it with `flow do --auto <slug> --with "<question>"`;
  keep useful ones as experts.
- Verify each child's output yourself before reporting anything done.
- Gates: outward actions (push, merge, deploy, messages to others) only
  with approval of the exact text or action; a newer HOLD beats an older GO.
- Work with the user (or the parent task) the way a dev lead would. If
  this task has a parent, ask through it with `flow message`,
  never AskUserQuestion.
- End any turn that needs the user with a numbered "Needs from you:" list.
- When Done when is met and verified, offer to close the task out.
```

A section is "deferred" if its body is the literal string
`*Deferred — fill in at task start.*` or `*Deferred*`. The bootstrap
session detects this and offers the user a deferred-section prompt
(§9).

If a section has no content, leave the heading with an italic "none"
underneath. Don't omit headings — the parallel structure makes the
briefs scannable.

Projects use a shorter template: `What / Why / Where / Scope`. No
"Done when", no "Open questions" (projects are ongoing).

**Playbook brief template:**

```markdown
# <name>

## What
<one sentence describing what each run does>

## Why
<short paragraph>

## Where
work_dir: <absolute path>

## Each run does
- <step 1>
- <step 2>
- <step 3>

## Out of scope
- <non-goal 1>

## Signals to watch for
- <signal 1>

---
*Run with `flow run playbook <slug>`. Each run gets its own session
and a snapshot of this brief at run time. Editing this file does not
retroactively change past runs.*
```

Notes:
- No "Done when" — playbooks are never done.
- "Each run does" replaces "Done when" as the action-oriented section.
- "Signals to watch for" replaces "Open questions" — playbooks are
  long-running, so the relevant prospective concern is signals to
  notice and respond to, not open questions to resolve.
