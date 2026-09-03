# Message bus — full reference (§4.18)

flow's bus lets a bound session (a) call for the user's attention,
(b) message another task's session, and (c) broadcast one-liner updates
to subscribers. One SQLite-backed store in flow.db; `flow inbox pop` is
the ONLY consumption API — one atomic verb that answers, delivers, and
clears. Hooks NEVER consume; they only report pending counts. flow
ships no notification UI: the user polls their own queue however they
like.

## Address grammar

`<assignee>[/<task-slug>]`

- `user` — the local human (tasks with no assignee belong to them).
- `rohit`, `shashwat`, … — other assignees (as used in `tasks.assignee`).
  NOTE: no cross-machine transport yet — a message to another assignee
  queues locally with a warning; delivery lands with flow-workspace.
- `<assignee>/<task-slug>` — the SESSION bound to that task.
- A bare task slug is sugar for `user/<slug>`.
- You can NEVER message your own address — a session cannot mail its own
  inbox, and the human cannot mail their own queue (flow rejects both;
  use a task update or note instead). Done/archived tasks are also
  rejected (undeliverable).

## Messaging the human

```
flow message user "coinswitch-gcp-migration: prod release plan ready, needs approval"
flow message user "state bucket perms broken, blocked on your GCP login" --urgent
```

The message stays pending until answered. Discipline:

- Message when blocked on the user's decision, a permission only they
  can grant, a long task finishing that they wait on, or something they
  must know NOW. Never routine progress; never when they are actively
  replying in this session.
- ONE pending message per wait; NEVER re-send.
- Body ≤200 chars; lead with the ask; mention the task slug or an
  update-file path in the body when the recipient needs context.
- Ack is automatic: the user's next prompt in this session acks it and a
  hook injects "answered after <duration>" — factor that elapsed time in
  (long waits mean stale state: re-verify before acting). They can also
  answer via `flow inbox pop --as user`.
- `--urgent` is a data flag for the user's own tooling; flow attaches no
  behavior to it.

## Messaging a peer session

```
flow message user/tekion-hub-network "state file moved, re-read before release"
```

Delivered instantly if the session has a listener parked (below); a
listener-less session is told the pending COUNT at its next prompt /
session start and pops explicitly. Include what the peer should DO with
the information.

## Broadcasting and watching

```
flow broadcast "imports done, 3 drifts left"     # from a bound session
flow watch coinswitch-gcp-migration               # subscribe to one task
flow watch alpha-cp                               # a whole project
flow watch shashwat                               # everything an assignee's tasks broadcast
flow watch --list / --rm <target>                 # inspect / unsubscribe
flow watch <target> --as user                     # subscribe the USER, not this session
```

Semantics: the broadcaster NEVER picks recipients. At broadcast time
the one-liner fans out as a message to every CURRENT watcher of the
task's slug, its project, or its assignee (new watchers don't receive
older broadcasts; your own subscription to your own task is skipped).
Broadcasts never escalate; if someone specific must act, message them
as well.

At turn end a Stop hook nudges you to broadcast when the task has
watchers and your last broadcast is >30 min old — broadcast only if the
turn produced something a watcher would care about; skip freely.
Declined nudges back off exponentially (30m, 1h, 2h.. capped 4h) and a
broadcast resets the cycle, so skipping is cheap. Stop never touches
your inbox.

## Consuming: pop, the one verb

```
flow inbox [--json]            # list pending (identity-aware)
flow inbox pop [--json]        # atomically consume the oldest, exit 1 if empty
flow inbox pop --wait --timeout 3600
flow inbox pop --wait --as user  # a session monitoring the USER's queue
flow inbox pop --as shashwat   # drain another assignee's queue (transport/monitor workers)
```

Identity is implicit: a bound session consumes its own task's mail; an
unbound/human invocation consumes the user's. `--as <assignee>` targets
a human queue directly. Pops are atomic claims, so concurrent consumers
of one queue never double-pop. Popping a human-directed message ACKS it
(popping is answering); broadcasts are marked delivered. Rows are never
deleted by popping — they transition status and roll off later by count.

To be WOKEN by mail instead of discovering it on your next tool call,
arm a listener. PREFERRED — one persistent **Monitor** wrapping pop in
an explicit loop (required: Monitor streams stdout lines as events and
a bare pop would end the watch after one message):

    Monitor(
      command: "while true; do flow inbox pop --wait --timeout 300 --json || true; done",
      description: "flow bus mail for this session",
      persistent: true)

Each popped message is one JSON event that wakes you; `--json` is
silent on timeouts so the loop emits zero noise; it listens for the
whole session with NO re-arming. FALLBACK when Monitor is unavailable
(e.g. non-Claude harnesses): a background Bash command
(`run_in_background: true`) running `flow inbox pop --wait` — that is
single-shot (blocks, pops one, exits 0 and wakes you; exit 1 =
timeout), so RE-ARM it after every wake. Also subscribe to the tasks
you depend on or spawn: `flow watch <slug>`.

## For the user: notification UX is yours

flow never notifies. Poll your queue from cron, a launchd job, a shell
loop, or a dedicated monitor task, and pipe into any notifier
(terminal-notifier, OSC escapes, ntfy, Slack, say):

    flow inbox --as user --json     # non-destructive peek
    flow inbox pop --wait --as user --json   # blocking consumer

Reminder cadence and dedup are your script's business — flow keeps only
the queue.

## Utilities

```
flow inbox stats        # answered/pending counts, avg/median/worst wait
```

Retention is automatic and row-count based: the newest 1000 rollable
rows are kept (consumed rows of any kind, plus broadcasts of any
status). Only pending directed messages never expire. Task close-out
(`flow done` / `flow archive`) removes the task's undeliverable rows,
its watches, and its nudge stamp.
