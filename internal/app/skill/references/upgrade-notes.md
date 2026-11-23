# flow upgrade notes — what changed for a running session

> Loaded on demand from the flow skill's resident core (SKILL.md). Read it
> when a `[flow-upgrade]` notice says flow was upgraded while your session
> was running. Newest first; act on every entry newer than the version you
> started on (if that version is unknown, the newest entry). Then re-load
> the flow skill: SKILL.md and its references changed with the binary.
>
> Each section is a release tag (`## Unreleased` until it ships). Only
> changes that alter how an agent should behave belong here.

## Unreleased

- **Auto children can run in a visible tab.** `flow auto … --tab` and
  `flow do --auto <slug> --tab` (or `FLOW_AUTO_TAB=1` as the default) run
  the child as an interactive claude/codex/prx session in a terminal tab
  instead of headless. Same contract: it reports to `user/<parent>` and
  self-closes with `flow done`. If you ARE such a tab child, a human may
  type into your session — treat it as instructions. `flow show task`
  reads `auto_run: running (tab, …)`; closing the tab before `flow done`
  marks the run dead.
- **Tabs run the flow that launched them.** Every flow-spawned tab gets
  the launcher's binary dir prepended to its own `PATH` and the binary as `$FLOW_BIN`, so another
  `flow` on your login-shell PATH no longer handles your hooks or your
  `flow done`. If `flow` in your shell still resolves elsewhere, call
  `"$FLOW_BIN"`. A tab run marked dead still completes on its own
  `flow done`, and `flow auto --mkdir` trusts the dir it just created
  unless that dir is inside a git repo.
- **Pick a model per task.** `flow do`, `flow do --auto`, `flow auto` and
  `flow run playbook` take `--model <m>` and `--effort <e>`, passed to the
  harness as given. The task keeps them, so resumes reuse them; `flow show
  task` prints `model:`/`effort:` when set. `--here` refuses both (exit 2).
  Drivers: see "Choosing the child's model" in references/delegation.md
  before putting a routine child on a cheaper model. The task's close-out
  sweep runs on its stored model too. Owners take the same flags on `flow
  add owner` and `flow owner start`; every tick runs on them, and the tasks
  an owner dispatches keep their own (references/owners.md).
- Restart running Codex sessions after upgrading, then review the pending hooks (`/hooks`) and trust flow's hooks.
- **Every interactive task is a driver.** Rules: references/driver.md
  (flow's session line and a brief's `Role: driver` line point there).
  New work goes through §4.2 "Start a driver": goal + ONE project question
  (never "no project"), create, bind, announce "Created driver task …".
  Never ask "driver or not?". While flow prints `INTAKE TURN`, end the turn
  at the goal + proposed Done when + "Needs from you:".
- **A headless child that stops on a blocker is `blocked`, not `dead`.** Its
  final message's last nonblank line starts with `BLOCKED:`. A `waiting_on`
  set during the run does not count: it may be a human's note. `flow show
  task` and `flow list` report `blocked`, and no dead-child alarm is sent. A
  stuck child should end with that line.
- **`flow do --here` needs the directory the session STARTED in.** If you
  `cd` into a worktree, `$PWD` is not that directory. When the bind is
  refused, run the `flow update task … --work-dir` command only if it shows
  a real path. If it shows `<real-cwd>`, or says the task is still bound to
  another session, open the task in a new tab with `flow do <slug>`.

## v0.1.0-alpha.38

- **Mail reaches you automatically (harness wake).** flow now pushes a
  `[flow-bus wake]` notice into an idle session, and your own hooks surface
  new mail before your next tool call or at turn end. If you armed an inbox
  Monitor loop (`flow inbox pop --wait`), stop it now (TaskStop) and do not
  re-arm it. When a flow-bus notice says mail landed, run `flow inbox pop`
  in the foreground (one-shot), repeat until empty, and act on each.
  Exception: with `FLOW_NO_WAKE=1` set, keep the Monitor recipe in
  references/messaging.md.
- **Codex: run flow commands bare.** Regular Codex sessions run in the
  workspace-write sandbox; flow's exec-policy allow-list (`flow.rules`)
  only matches a bare top-level command. No `VAR=… flow`, no
  `cd … && flow`, no wrapper scripts — otherwise flow fails with
  "readonly database".
- **Upgrade notices.** When flow changes under a running session, the
  next prompt or tool call carries one `[flow-upgrade]` notice pointing
  here. It does not repeat.
