# peer-local connector (sample)

An opt-in flow **peer** connector (contract 2, `"roles": ["peer"]`) that links
two flow homes on **one machine**. It exists to demo and test peer hubs
(see the skill reference `references/peers.md`); it is not a transport for
real teams.

```
home A (FLOW_ROOT=/tmp/a)                       home B (FLOW_ROOT=/tmp/b)
flow message bob "ship?"                         peer alice = (peer-local, /tmp/a)
  └─ dispatch ─► peer-local run send
        at=/tmp/b ──► FLOW_ROOT=/tmp/b FLOW_CONNECTOR_TOKEN=<B's token>
                      flow peer deliver --at /tmp/a --ref <idA> -- "ship?"
                                                  └─► B's user mail, from=alice
```

`at` is the other home's `FLOW_ROOT`, spelled exactly as that home sees it.
`send` reads the other home's peer-local token from `<at>/secrets/peer-local`,
so **both homes must run with `FLOW_SECRET_STORE=file`** (a testing-only
switch that keeps connector secrets in files instead of the keychain).

## Setup (two temp homes)

```bash
export FLOW_SECRET_STORE=file
A=$(mktemp -d)/a; B=$(mktemp -d)/b
FLOW_ROOT=$A flow init; FLOW_ROOT=$B flow init
FLOW_ROOT=$A flow connector add peer-local
FLOW_ROOT=$B flow connector add peer-local
FLOW_ROOT=$A flow peer add bob   --via peer-local --at "$B"
FLOW_ROOT=$B flow peer add alice --via peer-local --at "$A"
FLOW_ROOT=$A flow message bob "ship?" --options "Yes|No"
FLOW_ROOT=$B flow inbox            # from alice: ship?  (Yes | No)
```

Attachments (`flow message bob "see" --attach plan.md`) travel too: `send`
checks each file's sha256 (with `sha256sum` or `shasum` when present) and
passes the sending home's copy to the other home's
`flow peer deliver --attach`, which stores its own copy. This sample never
passes `--to`, so new mail lands in the other home's human mail; a real
desk connector would pick a task with `--to <task>`.

Requirements: POSIX `sh` and `awk` only (the JSON payload is parsed in awk
and turned into single-quoted shell assignments, so nothing in a message is
ever executed).
