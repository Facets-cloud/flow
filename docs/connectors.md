# Human connectors

When an agent needs your decision and you have stepped away, it asks you on
the message bus and moves on:

```bash
flow message user "deploy-v2: ship to prod now?" --options "Yes|No"
flow message user "imports 3/5 done" --fyi   # progress: stored and listed, never pings
```

By default an ask just waits in `flow inbox`. Install a **connector** and
flow pings you through it. flow ships a native macOS dialog connector, built
into the binary (the source is [`examples/connectors/macos`](../examples/connectors/macos)):

```bash
flow connector add macos                 # no checkout needed; shows a test dialog
flow connector list --available          # built-in samples
flow connector add ./my-connector        # or install your own folder
flow connector list
flow connector remove macos
```

You click an option, and can add a note. The answer arrives in the asking
task's inbox as a reply (`reply_to=<id>`), and the agent picks it up from
there. flow pings once per task until you have seen it (`--urgent` always
pings), broadcasts and `--fyi` never ping, and nothing pings unless you
installed a connector.

Connectors are any executable that follows a small JSON contract
(`run send` / `run test`, answering with `flow human reply`). Each one gets
its own secret, kept in the macOS keychain (scoped to the flow home, so two
`FLOW_ROOT`s never share one), so an agent can't answer for you.

## Priority, fallback and escalation

With more than one connector, order them instead of pinging all at once.
flow tries the lowest tier first and falls back only if every connector in it
fails. With escalation on, an ask still unseen after the delay goes to the
next tier too (`--urgent` asks still go to all tiers at once):

```bash
flow connector priority flowhub 1        # tried first
flow connector priority macos 2          # fallback (same number = same tier, in parallel)
flow connector escalate 10m              # unseen 10m after delivery: next tier (off by default)
```

## Writing your own

Contract v1, with enough detail to write your own:
[`internal/app/skill/references/connectors.md`](../internal/app/skill/references/connectors.md).

See also [running-without-you.md](running-without-you.md) for the rest of the
message bus. Back to the [README](../README.md).
