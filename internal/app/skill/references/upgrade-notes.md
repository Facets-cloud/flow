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

- **Drivers are revived when a child writes and the session has exited.**
  If you are a driver resumed with a `[flow-bus wake]` prompt that says no
  human is watching, pop the inbox in the foreground, act on each message,
  send the human at most one `flow message user` for a decision, and end
  the turn. You may dispatch auto children from a revive. Opt out with
  `FLOW_NO_REVIVE=1`. Details: references/messaging.md, "Driver revive".
  When a revive is refused (rate limit, session may still be running, no
  transcript) or fails, the human gets one FYI naming the driver and the
  reason; relay it and reopen the driver if its children are waiting.

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
