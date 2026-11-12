# Connectors — contract 2 (contract 1 still accepted)

A **connector** is an opt-in executable. With role `"human"` it pings the
human when an agent asks them something (`flow message user "<q>"`), and
relays the answer back to the asking task. With role `"peer"` it carries
messages to other flow hubs (see references/peers.md and "Peer role"
below). Nothing pings unless the user installed one. This page
is the full public contract: it is enough to write a connector from
scratch. A working sample is `examples/connectors/macos` in the flow repo
(a native macOS dialog). It is also built into the flow binary, so
`flow connector add macos` works with no repo checkout. On Linux there is
no built-in desktop-dialog connector yet; hub/webhook-style connectors work
the same on both OSes, and secrets fall back from the macOS keychain to
0600 files under `$FLOW_ROOT/secrets`.

```
agent: flow message user "q" --options "A|B"      (returns at once)
  └► flow stores the row; decides ping at send time
       └► detached `flow connector dispatch <id>` ─► <connector>/run send  (JSON on stdin)
                                                       └► shows the human, then later:
          "$FLOW_BIN" human reply <id> "A — note"  ─► reply in user/<task> inbox (reply_to=<id>)
```

## Folder layout

```
<name>/
  connector.json   the manifest
  run              any executable (bash, python, a Go binary, …)
  …                anything else it needs (copied as-is on install)
```

`connector.json`:

```json
{
  "name": "macos",
  "contract": 2,
  "roles": ["human"],
  "exec": "./run",
  "capabilities": ["send"],
  "events": ["ask", "urgent"]
}
```

- `name`: lowercase letters, digits, `-` or `_`. The installed folder is
  named after it.
- `contract`: `2` (or `1`, see below).
- `roles`: what the connector does. Use `["human"]`, `["peer"]` or
  `["human","peer"]`. A contract-1 manifest (`"contract": 1`, no
  `roles`) means `["human"]`, so v1 connectors keep working unchanged. Human-ping
  dispatch (tiers, escalation) only ever goes to role `"human"`. Sends to
  a peer only go to that peer's own connector, which needs role `"peer"`.
- `exec`: a path relative to the folder. It must stay inside the folder
  and be executable (`chmod +x`).
- `capabilities`: must include `"send"`.
- `events`: one or both of `"ask"` (a normal ask) and `"urgent"` (an
  `--urgent` ask). A connector receives only the events it lists.
  Required for role `"human"`; a peer-only connector may omit it.

## Verbs flow invokes

flow runs `<exec> <verb>` with the connector folder as cwd and one JSON
object on stdin.

| Verb | When | Contract |
|---|---|---|
| `run send` | a human ask was marked to ping | Notify the human. Exit 0 = notified, meaning a human can actually see it now; non-zero = failed (logged, and the next priority tier is tried). |
| `run test` | `flow connector add` and `flow connector test` | Send a sample notification. It gets the same JSON shape with `"id": "test"` and `"kind": "test"`, and takes no reply. |

stdin for `send`:

```json
{"id":"7983227a","from_task":"deploy-v2","body":"Ship to prod?",
 "options":["Yes","No"],"urgent":false,"kind":"ask",
 "created_at":"2026-11-06T10:00:00+05:30",
 "reply_hint":"\"$FLOW_BIN\" human reply 7983227a \"<answer>\"  (or: \"$FLOW_BIN\" human seen 7983227a)",
 "from_peer":"","from_connector":""}
```

`options` is always an array (`[]` when the ask has none). `from_task` is
`""` for an ask that came from no task. `from_peer` names the peer hub a
message came from, and is `""` for mail from this hub's own tasks. Answering
peer mail with `human reply` sends the answer back to that peer.
`from_connector` names the connector that sent the notice (see below), and is
`""` otherwise — label the dialog with it.

Environment (the connector's whole environment is the caller's, minus the
harness session variables, plus these):

- `FLOW_CONNECTOR_TOKEN`: this connector's secret. It is generated on
  install and kept in the macOS keychain (service `flow-connector-<name>`,
  account `flow-<12 hex of sha256 of the resolved FLOW_ROOT>`, so each
  flow home on a Mac has its own) or a 0600 file under
  `$FLOW_ROOT/secrets/`. It is never in flow.db or
  the folder. Never log it.
- `FLOW_BIN`: the absolute path of the flow binary. Call back with
  `"$FLOW_BIN"`, not `flow`.
- `FLOW_ROOT`: flow's data root.

Rules:

- **Return fast.** Each invocation is killed (with its whole process
  group) after 30s. Anything that waits on the human, such as a dialog or
  a chat thread, must be forked into its own session with stdio
  redirected, and then `exit 0`.
- One failing or hanging connector never blocks the others; connectors
  in the same priority tier run in parallel (see Priority and fallback).
- Results go one line per connector to `$FLOW_ROOT/connectors/dispatch.log`.
  `notified_at` is stamped on the row if any connector succeeded.

## Calling flow back (needs `FLOW_CONNECTOR_TOKEN`)

| Command | Effect |
|---|---|
| `"$FLOW_BIN" human reply <id> "<text>"` | Records the human's answer (seen + answered) and delivers `<text>` to `user/<from_task>` with `reply_to=<id>`. Idempotent: a repeat does nothing. If the asking task is gone or closed, the answer is still recorded and a warning printed (exit 0). Text can be up to 4000 chars. |
| `"$FLOW_BIN" human seen <id>` | Marks the mail seen without answering. |
| `"$FLOW_BIN" message user [--fyi\|--urgent] "<text>"` | Notifies the human (e.g. "alice wants to pair: run flow-p2p accept alice"). The row is human mail from `connector:<name>` (`flow inbox` shows "connector <name>: …"); normal ping rules apply (`--fyi` never pings, default pings, `--urgent` allowed). Only `user` is addressable this way — tasks, peers and other names exit 2 — and `--options` is refused (a connector notifies, it cannot ask). |

Without a token that matches an installed connector, both exit 1 ("not a
connector"); `message user` without one is the usual "your own queue" refusal
(exit 2). That is why an agent can never answer for the human. If the
human dismisses ("Later"), call nothing: the mail stays unread in `flow
inbox`.

## When flow pings

- Only directed asks to the local human ping. Broadcasts, task-to-task
  mail and `--fyi` messages never do.
- `--urgent` always pings.
- Otherwise there is one ping per sender task until the human has seen it.
  Further asks from that task are stored and listed but not re-pinged, and
  nothing is ever re-popped.

## Install and manage

```
flow connector add <dir>       validate, copy to $FLOW_ROOT/connectors/<name>/, store a secret, run `run test`
flow connector add macos       same, from the copy built into the binary (no checkout needed)
flow connector list            installed connectors, their events and where the secret lives
flow connector list --available  built-in sample names
flow connector test [<name>]   send a test notification (all if no name)
flow connector remove <name>   delete the folder, its secret and its priority
flow connector priority <name> <n>       ping order (see below)
flow connector priority <name> --clear
flow connector escalate <dur>|off        global, default off
```

`add` treats its argument as a path when that directory exists, otherwise
as a built-in sample name; an unknown name exits 1 and lists the samples.
If `add`'s test fails, the install is rolled back. To change an installed
connector, `remove` it and `add` it again; the secret is regenerated.

## Priority and fallback

With several connectors installed, every ask would otherwise ping all of
them at once (answer in one, the others stay open). The user can order
them instead. The order is the user's choice, not the connector author's:
it lives in a flow-owned file, `$FLOW_ROOT/connectors/config.json`, never
in `connector.json`.

```
flow connector priority flowhub 1     lower n is tried first
flow connector priority macos 2       same n = one tier, run in parallel
flow connector escalate 10m           or off (default); max 24h
```

```json
{"priority": {"flowhub": 1, "macos": 2}, "escalate_after": "10m"}
```

```
dispatch <id>   (detached; never blocks the sender)
  urgent ──────► every tier at once (as with no priorities)
  ask ─────────► tier 1 ─ any connector exit 0 ─► delivered, stop
                   └ all fail / time out ─► tier 2 … none ok ─► stays unread
     escalate set & a tier delivered:
        sleep <dur>; still unseen and unanswered? ─► next tier (same walk)
        repeat until seen or the tiers run out — one chain per ask
```

- No priorities set: one tier holding every connector, exactly the
  all-in-parallel contract v1 behavior.
- Connectors without a priority form one last tier, after every
  prioritized one.
- This is why exit 0 must mean a human can actually see it: a connector
  that exits 0 for a ping nobody will see (a hub that is down but queues
  silently) stops the fallback.
- `dispatch.log` records the path, e.g. `bc93f2bf flowhub fail → macos ok`
  or `bc93f2bf escalate → macos ok`, plus one line per connector.
- Each escalation wait is logged before it starts:
  `bc93f2bf escalate armed 10m0s → macos`. It is normally followed by
  `escalate → …` or `escalate stop: seen`. An `escalate armed` line with
  neither after it means the dispatcher died during the wait (reboot,
  sleep, a flow upgrade) and that escalation was dropped. The wait lives
  only in that process; nothing resumes it.
- A broken `config.json` never silences pings: dispatch logs it and falls
  back to all at once. `flow connector list` shows each priority, the
  resulting order, and the escalation setting. `remove` drops the
  connector's priority.
- The one-ping-per-task rule and `--fyi` are unchanged; escalation only
  re-pings the same ask through the next tier.

## Peer role (contract 2)

A connector with role `"peer"` carries one message to another flow hub.
flow runs `run send` (same rules: 30s, fork anything slow) with this on
stdin:

```json
{"id":"7983227a","peer":"bob","at":"<opaque endpoint>","kind":"ask",
 "body":"Ship to prod?","options":["Yes","No"],"urgent":false,
 "reply_to_ref":"","created_at":"2026-11-09T10:00:00+05:30",
 "attachments":[{"name":"plan.md","size":412,"sha256":"<hex>","path":"/abs/.flow/attachments/7983227a/plan.md"}]}
```

- `attachments` is `[]` when there are none. `path` is the sending hub's
  stored copy; carry the bytes, verify `sha256` on arrival, then pass the
  received file to `peer deliver --attach <path>` (repeat per file). Bodies
  may be up to 4000 chars.

- `kind` is `ask`, `fyi` or `reply`. A `reply` has `reply_to_ref` set to the
  other hub's id of the message it answers.
- Exit 0 means delivered, and the row becomes `delivery=sent`. A non-zero
  exit or a timeout makes it `delivery=failed` and records the error. There
  is no retry. Optional stdout `{"remote_ref":"…"}` is logged to
  `dispatch.log` for debugging only.
- On the receiving hub, its own peer connector hands the message in with
  `"$FLOW_BIN" peer deliver --at <sender's endpoint> --ref <id>
  [--reply-to-ref <reply_to_ref>] [--to <task>] [--kind fyi] [--options "A|B"]
  [--attach <path> ...] -- "<body>"`
  using its own `FLOW_CONNECTOR_TOKEN`. `--to <task>` lets the receiving
  connector pick the local task (a desk) the mail lands in instead of the
  human's mail; it must be an open task there. That command (`flow peer deliver`)
  is the only inbound door; references/peers.md has its rules.
- `run test` gets the human test payload (`"id": "test"`). Validate your
  setup and call nothing back.

The `peer-local` sample (`flow connector add peer-local`) links two flow
homes on one machine. It reads the other home's token from
`<at>/secrets/peer-local`, so both homes run with `FLOW_SECRET_STORE=file`.
That setting is for testing only: it keeps every connector secret in a 0600
file under `$FLOW_ROOT/secrets/` and never touches the keychain.

## Writing one: a checklist

1. Write `connector.json` with your events, and an executable `run`.
2. `run send`: parse stdin, start the notification in the background,
   `exit 0` within a second or two.
3. When the human answers: `"$FLOW_BIN" human reply "$id" "<choice> — <note>"`
   (the note is optional). When they only acknowledge:
   `"$FLOW_BIN" human seen "$id"`.
4. `run test`: show a harmless sample and do not call back.
5. Document your connector's own CLI (pairing, status, …) in its README;
   humans and agents find it via `flow connector list`.
6. `flow connector add ./<name>`, then `flow message user "hi?" --options "Yes|No"`
   from a task session to try it end to end.
