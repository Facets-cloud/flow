# Peer hubs — messaging another flow home

A **hub** is one flow home (one `FLOW_ROOT`). Local addresses are unchanged:
`user` is this hub's human and `user/<task>` is one of its agent sessions.
A **peer** is ANOTHER hub in this hub's contact book: a local nickname that
maps to `(connector, at)`. `at` is an opaque endpoint that only that
connector interprets. Peers let AGENTS reach beyond one machine, while each
hub's human stays in the loop on their own hub. Flow is not a human↔human
chat (use Slack for that).

```
hub A (alice)                                        hub B (bob)
task t: flow message bob "ship?" --options "Yes|No"
  row: peer=bob, delivery=dispatched
  └► A's peer connector `run send` ──► B: flow peer deliver --at <A> --ref <idA>
                                        └► B's user mail, from=alice (pinged)
                                  B's human: flow human reply <idB> "Yes"
  B's peer connector ◄── row: peer=alice, reply_to=<idB> ◄┘
  └► A: flow peer deliver --ref <idB> --reply-to-ref <idA>
        └► A's user/t inbox, reply_to=<idA>
```

## For agents (the only part you usually need)

- **Ask another hub's human:** `flow message <peer> "<body>"`. The same flags
  work as for `user`: `--options "A|B"`, `--urgent`, `--fyi`. It returns at
  once, so never wait on it. The answer arrives in YOUR task inbox as a
  reply (`reply_to` = your message id), like any human answer.
- **Never address `<peer>/<task>`** (exit 2). Another hub's task names are
  unknowable. You always reach the peer hub's human, who decides who acts.
- **Answer a peer's message** (one that arrived in your inbox or that the
  human handed to you): `flow message <peer> "<answer>" --reply-to <id>`,
  where `<id>` is the LOCAL id of the inbound message. Replying to a
  different peer's message is refused (exit 2).
- `flow peer list` shows the peers. Use the peer names it shows, not
  people's names.
- #118 holds across hubs: an agent can read a peer's message to the human
  but can never pop it or ack it. Only the human makes it read.

## Commands

```
flow peer add <name> --via <connector> --at <endpoint>
flow peer list | show <name> | rm <name> | block <name> | unblock <name>
flow message <peer> "<body>" [--options "A|B"] [--urgent] [--fyi] [--reply-to <local-id>]
flow peer deliver --at <endpoint> --ref <their-id> [--reply-to-ref <our-id>]
                  [--kind ask|fyi] [--options "A|B"] "<body>"     # connectors only
```

- `flow peer add` needs an installed connector with role `"peer"`
  (contract 2, see references/connectors.md). Otherwise it exits 1. Each
  `(connector, at)` can belong to one peer only, and `user`/`self` are
  reserved names.
- `flow peer show <name>` lists recent traffic with each outbound
  message's delivery state.
- `flow peer block <name>` rejects that peer's inbound messages.
  `unblock` accepts them again.

## Outbound

`flow message bob "…"` stores a row with `peer=bob`, `to_assignee=bob`,
`from_task` set to the sender's bound task (empty from a plain terminal),
and `delivery=dispatched`. It then spawns a detached dispatch to bob's
connector `run send`. When that exits 0 the row becomes `delivery=sent`.
A non-zero exit or a timeout makes it `delivery=failed` and stores the
error in `delivery_error`. There is no auto-retry in v0. Human-role
connectors are never involved in sending to a peer.

When `--reply-to <id>` names an inbound peer row, the payload is
`kind: "reply"` with `reply_to_ref` set to that row's `origin_ref` (the
other hub's id).

## Inbound — `flow peer deliver` (the only door)

1. A valid `FLOW_CONNECTOR_TOKEN` identifies the calling connector C.
   Without one it exits 1 and stores nothing.
2. `(C, at)` must name a peer P. An unknown endpoint exits 1 and writes one
   line to `$FLOW_ROOT/connectors/inbound.log`. A blocked peer exits 1
   ("rejected").
3. A repeat of `(P, ref)` is a no-op that exits 0 (dedupe on the unique
   `(peer, origin_ref)`).
4. `--reply-to-ref <our-id>` that names our outbound row to P is stored in
   `user/<that row's from_task>` with `reply_to=<our-id>`. If that task is
   done, archived or empty, it falls back to step 5 with the body labelled
   `[answer to <our-id>]`.
5. Otherwise it goes to the human's mail: `to_assignee=user`, from P,
   pinged per the connector rules (#119 tiers and escalation). `--kind fyi`
   makes it an FYI that never pings. Options are passed through.
6. P's `last_seen_at` is updated.

Threading uses message ids only: `origin_ref` on an inbound row is the other
hub's id, and `reply_to` is always a LOCAL id.

## Answering a peer

A reply to an inbound peer message routes out to that peer with
`reply_to_ref` set to the inbound row's `origin_ref`. It can come from any
channel:

- **`flow human reply <id> "…"`** from a human connector. It keeps the same
  local semantics (seen + answered, idempotent), and the answer is sent to
  the peer instead of being delivered locally.
- **`flow message <peer> "…" --reply-to <id>`** from an agent or a plain
  terminal.

There is no extra "answered" rule. Unread, pings, escalation and retention
all key on `seen_at`, the same as for local mail.

## Samples

- **peer-local** (`flow connector add peer-local`, role peer): links two
  flow homes on ONE machine. `at` is the other home's `FLOW_ROOT`. `send`
  runs the other home's `flow peer deliver` with that home's peer-local
  token, read from `<at>/secrets/peer-local`, so both homes run with
  `FLOW_SECRET_STORE=file`. That setting is for testing only: it keeps
  connector secrets in files instead of the keychain. The sample uses only
  POSIX sh and awk. It is for demos and CI, not a transport.
- **macos** (role human) has an EXAMPLE third button for peer mail, **Hand
  to agent**. It marks the mail seen, then runs
  `flow auto "Answer <peer>: …" --brief -`. The brief carries the question,
  the human's note, and the instruction to reply with
  `flow message <peer> --reply-to <id>` and then `flow done`. This is the
  connector's own logic, not a flow feature.

## Out of scope (v0)

A hosted switchboard, a Slack sample (next slice), auto-answer policies,
grants and labels, peer directory sync, auto-retry.
