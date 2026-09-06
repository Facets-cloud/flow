# Message bus — full reference (§4.18)

flow's bus is mail between sessions and the human. One SQLite-backed
store in flow.db; `flow inbox pop` is the consumption verb. Mail model: a
message is **unread** until consumed, then **read**. Hooks NEVER consume —
they only report pending counts. flow ships no notification UI; the user
polls their own queue however they like.

## The two purposes — pick the verb by intent

Everything on the bus serves one of two goals. Decide which you're in
before choosing a verb.

### 1. Reach out to the user / get instructions

When the user is AFK and you need something only they can give — a
decision, a permission, or a long task they're waiting on is finished —
message the human:

```
flow message user "coinswitch-gcp-migration: prod release plan ready, needs your approval"
flow message user "blocked on your GCP login — state bucket perms broken" --urgent
```

- `user` (the reserved local human) stays **unread** until answered — it
  never expires while unread.
- Use ONLY for the three cases above. NOT for routine progress, and not
  when the user is clearly active in this session (they'll see it live).
- ONE message per wait — NEVER re-send.
- Body ≤500 chars, lead with the ask, name the task slug for context.
- Ack is automatic when the user replies in THIS session (a hook injects
  "answered after <duration>" — factor the elapsed time in; re-verify
  stale state after a long wait). They can also answer via `flow inbox
  pop --as user` or `flow inbox read <id>`.
- `--urgent` is a data flag for the user's own tooling; flow attaches no
  behavior to it.

**Headless auto-mode exception.** A `flow do --auto` agent runs with no
human watching, so a silent run looks stuck. Post regular progress updates
as milestones land — `flow message user` for a checkpoint, or
`flow broadcast` to watchers — not only when you need a decision. This is
the one case where routine progress on the bus is expected.

### 2. Collaborate with peers

Coordinate or hand off between task sessions, and keep subscribers in
the loop:

```
flow message user/tekion-hub-network "state file moved — re-read before release"
flow message user/tekion-hub-network "answered: use us-east-1" --reply-to 80a7ae52
flow broadcast "imports done, 3 drifts left"     # FYI to watchers
```

- `<assignee>/<task-slug>` (a bare task slug is sugar for `user/<slug>`)
  delivers into that session's context — no interruption. Say what the
  peer should DO with it.
- `--reply-to <id>` stamps the parent message id so a routed reply carries
  lineage — the receiver sees which message it answers (the bus doesn't
  thread, so without this a routed reply arrives contextless).
- `flow broadcast "<one-liner>"` fans out to every CURRENT watcher of the
  task's slug, its project, or its assignee — you never pick recipients,
  and new watchers don't get older broadcasts. Broadcasts never escalate;
  if someone specific must act, message them too. A Stop hook nudges you
  to broadcast when the task has watchers and your last one is >30m old;
  skip freely if nothing notable happened.

## Address grammar

`<assignee>[/<task-slug>]`

- `user` — the local human (tasks with no assignee belong to them).
- `<name>` (e.g. `shashwat`) — another assignee. No cross-machine
  transport yet: it queues locally with a warning; delivery lands with
  flow-workspace.
- `<assignee>/<task-slug>` — the SESSION bound to that task.
- A bare task slug is sugar for `user/<slug>`.
- You can NEVER message your own address — a session cannot mail its own
  inbox, and the human cannot mail their own queue (flow rejects both).
  Done/archived tasks are also rejected (undeliverable).

## Watching

```
flow watch <task|project|assignee>   # subscribe to a topic's broadcasts
flow watch <target> --as user        # subscribe the USER, not this session
flow watch --list / --rm <target>    # inspect / unsubscribe
```

Subscribe to whatever you depend on, coordinate with, or spawn from this
session.

## Consuming: unread vs. everything

```
flow inbox                    # list UNREAD mail (identity-aware)
flow inbox --all              # list everything retained (read + unread), each labelled
flow inbox read <id>          # show ONE message by id (any status) and mark it read
flow inbox pop                # consume the oldest unread, exit 1 if empty
flow inbox pop --wait --timeout 300
flow inbox pop --wait --keep-unread   # wake on mail WITHOUT acking it (reader/relay)
flow inbox pop --as user      # drain the human queue from a monitor/transport session
```

Identity is implicit: a bound session consumes its own task's mail; an
unbound/human invocation consumes the user's. `--as <assignee>` targets a
human queue directly. Pops are atomic claims, so concurrent consumers of
one queue never double-pop. `--json` on inbox/pop/read emits
machine-readable rows.

- `pop` on an unread human message ACKS it (popping IS answering) → **read**.
- `pop --keep-unread` returns the message but leaves it un-acked — it's
  been handed to a reader, still awaiting the human's answer, so it stays
  immortal until actually read. This is the primitive a forwarder/relay
  needs: be woken by mail it must pass along WITHOUT consuming the answer.
- `read <id>` targets a specific message out of arrival order (pop is
  oldest-first only) — display an already-read one, or ack the exact
  message the human just answered.

## Listener discipline — two recipes, pick by role

Arm exactly ONE listener at session start so mail WAKES you instead of
waiting for your next tool call. Which recipe depends on what you do with
each message.

**Consumer** — you fully handle each message yourself (act on it, answer
it). This is the default for a normal task session. `pop` acks as it goes:

    Monitor(
      command: "while true; do flow inbox pop --wait --timeout 300 --json || true; done",
      description: "flow bus mail for this session",
      persistent: true)

**Reader / relay** — you forward or merely observe, and must NOT ack (the
message is answered elsewhere — e.g. you relay it to the user and they
answer over that channel). Use `--keep-unread` so watching never silently
consumes someone else's mail:

    Monitor(
      command: "while true; do flow inbox pop --wait --keep-unread --timeout 300 --json || true; done",
      description: "flow bus relay — read without acking",
      persistent: true)

Each popped message is one JSON event that wakes you; `--json` is silent
on timeouts (zero noise); the loop listens all session with NO re-arming.
FALLBACK when Monitor is unavailable (non-Claude harnesses): a background
Bash `flow inbox pop --wait` (single-shot — re-arm after every wake). Keep
one listener per identity. After a relay forwards a message and the human
answers, ack the specific one with `flow inbox read <id>`.

**Safety net (Claude harness):** a PreToolUse hook also surfaces any
just-arrived unread item — directed message OR broadcast — right before a
tool runs, so a mid-turn "stop, don't do X" reaches you before the action
it would cancel — even if your Monitor loop hasn't woken yet. It's
delta-gated (each item is announced at most once, so it never spams across
a turn's tool calls) and inform-only: it does NOT consume the item, so
still `flow inbox pop` it to actually read and ack. This backstops the
listener; it doesn't replace it.

## For the user: notification UX is yours

flow never notifies. Poll your queue from cron, a launchd job, a shell
loop, or a dedicated monitor task, and pipe into any notifier:

    flow inbox --as user --json            # non-destructive peek (unread)
    flow inbox pop --wait --as user --json # blocking consumer

`flow inbox stats` reports answered/pending counts and wait times.
Reminder cadence and dedup are your script's business — flow keeps only
the queue.

Retention is automatic and row-count based: the newest 1000 rollable rows
are kept (read rows, delivered peer mail, and broadcasts of any status).
Unanswered questions to the human never expire — whether still unread or
handed to a relay and awaiting the answer. Task close-out (`flow done` /
`flow archive`) removes the task's undeliverable rows, its watches, and
its nudge stamp.
