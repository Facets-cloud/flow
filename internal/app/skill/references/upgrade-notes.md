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

- Restart running Codex sessions after upgrading, then review the pending hooks (`/hooks`) and trust flow's hooks.
- **Every interactive task is a driver.** Rules: references/driver.md
  (flow's session line and a brief's `Role: driver` line point there).
  New work goes through §4.2 "Start a driver": goal + ONE project question
  (never "no project"), create, bind, announce "Created driver task …".
  Never ask "driver or not?". While flow prints `INTAKE TURN`, end the turn
  at the goal + proposed Done when + "Needs from you:".
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
