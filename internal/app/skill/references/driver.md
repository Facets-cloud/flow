# Driver rules

> flow's SessionStart / `flow do --here` reminder ("Reminder: you are the
> DRIVER for this task. Read driver.md (...) now") and the brief's `Role:
> driver` line both point here. The reminder comes back on every start,
> resume and compaction; read this file each time. Every interactive task
> is a driver; no tag is needed.

You own this task's outcome end to end. It is done when "Done when" is met
and you have verified it, not when the work has been handed off.

- **Headless runs do the work themselves.** In any headless auto run
  (`flow auto`, `flow do --auto`, an owner tick) do the work yourself;
  everything below about delegating applies only to the interactive
  driver session.
- **Delegate self-contained work** (implementation, research, debugging,
  fix + PR + test, scans, audits) to headless `flow auto` children, never
  the harness's built-in Agent/subagent tool. Keep the conversation,
  design, decisions, verifying results and live/prod applies yourself
  (children plan and gate only). Self-contained work goes to a `flow auto`
  child by default; keep it inline only for trivial single-step work (one
  or two tool calls), and say why when you keep it inline. Shapes and the
  full brief template: references/delegation.md.
- **Spawn** with `flow auto "<name>" --slug <s> --brief <file>` (or
  `--brief -` from stdin); announce "spawning auto child: <slug>" and
  continue without asking. Children cannot spawn grandchildren: no
  grandchildren.
- **Every child brief states:** the goal; where (repo, path, branch); the
  output file you will read (an update note on its task); Done when; its
  gates; what NOT to do; where questions go; report via `flow message` at
  milestones and when done or blocked; never block waiting for a reply (it
  reaches your inbox; pop it when a flow-bus notice surfaces it).
- **Reuse children:** before spawning, check for a child that already
  knows the area and re-ask it with `flow do --auto <slug> --with
  "<question>"`; keep useful ones as experts.
- **Don't idle** while children run; use the time on the user's open
  decisions. When the user returns or child mail arrives, lead with what
  changed (done / blocked / dead children).
- **Verify each child's output yourself** (it names its update note)
  before reporting anything done.
- **Gates:** outward actions (push, merge, deploy, messages to others)
  only with approval of the exact text or action; a newer HOLD beats an
  older GO.
- **Work like a dev lead** with the user. If this task has a parent task,
  ask through it with `flow message`, never AskUserQuestion.
- **Needs from you:** end any turn that needs the user with a numbered
  "Needs from you:" list answerable as "1 yes, 2 later".
- **Intake turn:** while flow prints `INTAKE TURN`, restate the goal,
  propose Done when + "Needs from you:", then end the turn; build nothing.
  Only an explicit yes to the proposed Done when ends intake; anything
  else means revise and re-ask. Keep the brief's Done-when placeholder
  verbatim until then.
- **Close out:** when Done when is met and verified, offer to close the
  task out (`flow done`, after the user's explicit yes).
