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
        |     child: headless -> flow message user/<driver> at milestones
        |            -> writes findings as an update note -> flow done
        |- inbox event wakes the driver -> verify the result -> report
        `- live/prod change: child plans + gates, driver applies with the user
```

The SessionStart hook prints a `flow driver delegation` block in bound,
human-driven sessions only. Unbound sessions and auto children never get
it.

## Driver tasks (tag `driver`)

A **driver task** is the explicit, stronger form of the above. It owns its
outcome end to end and is the single place the user talks to; ANY
self-contained unit of work goes to a `flow auto` child, not just scans
or "grunt work" (implementation, research, debugging, fix + PR + test
all qualify). Never use the harness's built-in Agent/subagent tool for
it: a flow child has its own task, brief, transcript and close-out sweep.

```
 user ◀──▶ DRIVER (tag driver)            regular bound task
           ├─ talk, design, decisions      ├─ same, plus light delegation
           ├─ verify every child result    │  (scans, audits, fix+PR)
           ├─ live/prod applies            └─ no "own it end to end" prompt
           └─ everything else → flow auto children
                  child dead → driver inbox gets a crash notice
```

Flow marks it everywhere: `flow list` shows `◆driver` (json
`"driver": true`), `flow show task` prints `role: driver`, the per-prompt
anchor says `[driver]`, and SessionStart / `flow do --here` print the
`flow DRIVER task` block. Auto children never get it, even if tagged.

### Intake question (§4.2 step 5)

After Priority, AskUserQuestion (header "Driver?"):

> "How should this task run?"
> - **Driver (owns it end to end)**: "I keep design and decisions with
>   you and hand self-contained work to background flow auto tasks."
> - **Regular (just this session)**

Mark **Driver (Recommended)** when the brief has several workstreams or
the user describes a big goal; else Regular first. On Driver, add
`--tag driver` to `flow add task`. Skip if the user already said.

### Mid-session switch

"make this a driver", "drive this", "you own this end to end" → run
`flow update task <bound-slug> --tag driver`. Its output prints the
driver block: follow it from that turn on. To step back, `--remove-tag
driver` (output says it is no longer a driver). Tell the user in one
line; no confirmation needed when they asked.

### Driver habits

- **Never idle.** While children run, work the user's open decisions or
  the next piece of design. Do not sit waiting for a child.
- **Lead with what changed.** When child mail arrives or the user comes
  back ("status", "what happened"), open with done / blocked / dead
  children before anything else. A dead child arrives as a crash notice
  from flow: check its log and re-run or take it over.
- **Needs from you.** End any turn that needs the user with a numbered
  list they can answer in one line:
  ```
  Needs from you:
  1. Keep the 9 unread keys in app_secrets? (yes/no)
  2. PVC copy or fresh volume?
  ```
  Route each answer to the child that asked (`flow message`, or re-run
  it with `--with`).

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
4. The child reports with `flow message user/<driver>`; harness wake
   brings each report to you (§4.18): pop in the foreground when a
   flow-bus notice says mail landed. No listener to arm. If your session
   has exited, a child's report revives it headlessly (messaging.md,
   "Driver revive").
5. When the child says it is done, open the update note it names
   (`flow show task <slug>` lists it), verify the result yourself, then
   report to the user. Do not relay a child's claim unverified.

Prefer this over the harness's built-in Agent/subagent tool for multi-step
work: a flow child has its own task, transcript, brief and close-out
sweep, and survives this session.

## Choosing the child's harness

When more than one harness is installed, the driver block adds a
`harnesses available for children: claude, codex` line. Pick each
child's harness by the **user's stated preference**. No automatic
switching, and never read usage numbers.

```
 preference in kb/user.md / said this session? ── yes ─▶ use it
        │ no
 can ask the user? ── yes ─▶ ask ONCE "Run children on Claude or Codex?"
        │ no (headless)          └─▶ save to kb/user.md, use it
        ▼
 caller's harness (flow auto with no --harness, as today)
```

- **Ask once.** Use the harness's question tool if it has one. Save the
  answer to `kb/user.md` as a KB scoop entry (§4.10 format), e.g.
  "prefers Codex for auto children". Reuse it after that without asking
  again.
- The user can override per child ("run this one on Claude"): pass
  `--harness <name>` for that child only; don't rewrite the KB unless
  they say the preference changed.
- No preference and no one to ask: omit `--harness`, so the child runs
  on the caller's harness.

### Limit hit → offer the other harness

A child that ends `dead` may have hit a usage limit. Check its auto-run
log (`flow show task <slug>` → `auto_run_log:`) and `flow transcript
<slug>`:

- **Claude**: `You've hit your … limit` in the log; in the transcript
  `quotaLimits.status:"rejected"` or `error:"rate_limit"`.
- **Codex**: `rate_limit_reached_type` set, or a usage-limit error in
  the log.

Then tell the user which limit was hit and when it resets (quote the
reset time from the message), and offer to re-run on the other harness.
On yes, spawn a NEW child with the same brief and `--harness <other>`
under a new slug (e.g. `<slug>-codex`): the old task is pinned to its
harness. No DB edits, no re-pinning. The same applies to the driver
itself: if its own harness hits its limit, the user can open
the work in the other harness (references/harness-transfer.md).

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
- never block waiting for a reply: keep working. Arm no inbox listener;
  a message (for example from the user via the hub) surfaces as a
  flow-bus notice before your next tool call: pop it in the foreground.
  If a decision truly needs the user, set waiting_on, message the driver, and exit.
- When Done when is met: flow done <slug>, then exit.
```

## Reporting rule (one rule)

- Parent live: the child messages `user/<parent>` at milestones, done,
  and blocked. No Stop broadcast nudge in auto runs, so it arrives once.
- Parent done/archived: the child still writes its update note and sends
  its done/blocked report to the human as `flow message user "FYI: …" --fyi`.
  flow's own dead-child notice falls back to the human the same way.
- No parent: update note; the human hears only at done/blocked. If the run
  ends dead, flow itself pings the human (normal, not FYI) with the slug,
  brief first line and log path. Owner-tagged tasks are left to the owner.

## Result contract

Every child with a parent is told, in its auto prompt, to:

- write its findings or result as an update note on its own task
  (`tasks/<slug>/updates/YYYY-MM-DD-<kebab>.md`) before `flow done`;
- make its final `flow message user/<parent>` name the slug and the
  update file (body <=200 chars);
- never spawn its own children.

## PR trailer (every bound session)

A pull request description opened from a flow-bound session (tab or
headless child) ends with this exact line, last in the body, after any
harness footer:

```
Shipped from a [flow](https://github.com/Facets-cloud/flow) task.
```

Add nothing to it (no task slug, no session id). It is an instruction the
SessionStart hook injects, not an enforcement; the user drops it with
`FLOW_PR_TRAILER=off` (also `0`/`false`).

## No grandchildren

Delegation is one level deep. An auto child may not spawn a grandchild:
`flow auto` refuses inside an auto run (the `FLOW_AUTO_RUN` env marker or
the bound task's live auto run), and the child's prompt says so. If the
work needs splitting, the child says so in its result and the driver
decides.
