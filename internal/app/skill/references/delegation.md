# Driver delegation: spawning auto children

There is always a task the user drives interactively (`flow do <slug>`).
That bound session is the **driver**. Inside it you should carve smaller
deterministic subtasks off to headless `flow auto` children yourself,
instead of waiting for the user to type "flow auto" each time.

```
user -> flow do <task>              (interactive, unchanged)
        driver session (bound)
        |- design, ascii, decisions, quick answers  -> inline with the user
        |- deterministic subtask                    -> flow auto child, announce it
        |     child: headless -> flow message driver at milestones
        |            -> writes findings as an update note -> flow done
        |- inbox event wakes the driver -> verify the result -> report
        `- live/prod change: child plans + gates, driver applies with the user
```

The SessionStart hook prints a `flow driver delegation` block in bound,
human-driven sessions only. Unbound sessions and auto children never get
it.

## What qualifies (delegate without asking)

A subtask qualifies when its outcome is checkable and it needs no
judgment calls from the user mid-way:

- **Scans / inventories**: "scan services deployed in Elastic Beanstalk",
  "list every module still on the old provider".
- **Audits**: "audit blueprint variables for hardcoded secrets",
  "check which envs override X".
- **Fact-finding**: "find which repo owns this queue and who deploys it".
- **Repro debugging**: "reproduce the failing release locally and find
  the first bad commit".
- **Bulk / mechanical passes**: "demote these 40 literals to variables",
  "rename the field across all instances".
- **Fix + PR + verify**: "raptor fix, raise a PR, then test with the PR
  binary".

## What stays inline

- Design, architecture, ASCII diagrams, trade-off discussion.
- Decisions only the user can make; anything ambiguous.
- Quick answers (one or two tool calls; spawning costs more than doing).
- **Live / prod changes**: the child may plan, dry-run and prepare, then
  stop at the gate. The driver applies with the user watching.

## How

1. Write the child's brief to a file (or pipe it on stdin).
2. Run `flow auto "<name>" --slug <s> --brief <file>` (or `--brief -`).
   It adds the task (parent = this task, project inherited, this
   session auto-watches it), writes brief.md, and launches
   `flow do --auto`. Optional: `--priority`, `--work-dir`, `--mkdir`,
   `--harness`. It refuses with no brief, or a blank one.
3. Tell the user in one line: `spawning auto child: <slug>`. Do not ask
   permission first, and keep working on the main thread.
4. Keep your inbox Monitor armed (§4.18): the child reports with
   `flow message user/<driver>`.
5. When the child says it is done, open the update note it names
   (`flow show task <slug>` lists it), verify the result yourself, then
   report to the user. Do not relay a child's claim unverified.

Prefer this over the harness's built-in Agent/subagent tool for multi-step
work: a flow child has its own task, transcript, brief and close-out
sweep, and survives this session.

## Child brief template

```markdown
# <one-line goal>

## Goal
<what to produce and why it matters to the driver task>

## Where
<repo / work_dir, branch or worktree rules, files that matter>

## Done when
<checkable criteria: file written, tests green, PR open with URL>

## Gates
<what it may do on its own vs. stop at: e.g. "plan only, do NOT apply
to prod", "may push a branch and open a PR, do NOT merge">

## Report
- flow message user/<driver-slug> "<=200 chars" at each milestone, when
  done, and when blocked. Body is positional.
- Write findings as an update note on your own task; name it in the
  final message.
- do not wait on your own inbox: nobody will answer it. If a decision
  truly needs the user, set waiting_on, message the driver, and exit.
- When Done when is met: flow done <slug>, then exit.
```

## Result contract

Every child with a parent is told, in its auto prompt, to:

- write its findings or result as an update note on its own task
  (`tasks/<slug>/updates/YYYY-MM-DD-<kebab>.md`) before `flow done`;
- make its final `flow message user/<parent>` name the slug and the
  update file (body <=200 chars);
- never spawn its own children.

## No grandchildren

Delegation is one level deep. An auto child may not spawn a grandchild:
`flow auto` refuses inside an auto run (the `FLOW_AUTO_RUN` env marker or
the bound task's live auto run), and the child's prompt says so. If the
work needs splitting, the child says so in its result and the driver
decides.
