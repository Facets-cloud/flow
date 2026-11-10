# Human connectors — contract v1

A **connector** is an opt-in executable that pings the human when an agent
asks them something (`flow message user "<q>"`), and relays the answer back
to the asking task. Nothing pings unless the user installed one. This page
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
  "contract": 1,
  "exec": "./run",
  "capabilities": ["send"],
  "events": ["ask", "urgent"]
}
```

- `name`: lowercase letters, digits, `-` or `_`. The installed folder is
  named after it.
- `contract`: must be `1`.
- `exec`: a path relative to the folder. It must stay inside the folder
  and be executable (`chmod +x`).
- `capabilities`: must include `"send"`.
- `events`: one or both of `"ask"` (a normal ask) and `"urgent"` (an
  `--urgent` ask). A connector receives only the events it lists.

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
 "reply_hint":"\"$FLOW_BIN\" human reply 7983227a \"<answer>\"  (or: \"$FLOW_BIN\" human seen 7983227a)"}
```

`options` is always an array (`[]` when the ask has none). `from_task` is
`""` for an ask that came from no task.

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

Without a token that matches an installed connector, both exit 1 ("not a
connector"). That is why an agent can never answer for the human. If the
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

## Writing one: a checklist

1. Write `connector.json` with your events, and an executable `run`.
2. `run send`: parse stdin, start the notification in the background,
   `exit 0` within a second or two.
3. When the human answers: `"$FLOW_BIN" human reply "$id" "<choice> — <note>"`
   (the note is optional). When they only acknowledge:
   `"$FLOW_BIN" human seen "$id"`.
4. `run test`: show a harmless sample and do not call back.
5. `flow connector add ./<name>`, then `flow message user "hi?" --options "Yes|No"`
   from a task session to try it end to end.
