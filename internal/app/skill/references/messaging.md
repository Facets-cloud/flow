# Message bus — full reference (§4.18)

flow's bus lets a bound session (a) call for the user's attention,
(b) message another task's session, and (c) broadcast one-liner updates
to subscribers. One SQLite-backed inbox in flow.db, one delivery path:
a parked `flow inbox pop --wait` listener (instant), else hooks at
prompt-submit / session-start. The bus is CLI-only by design: flow
stores, addresses, schedules escalation, and measures waits — it never
draws attention itself. Notification UX is the user's own scripting on
top of `flow inbox due`.

## Address grammar

`<assignee>[/<task-slug>]`

- `self` — the local user (tasks with no assignee belong to self).
- `rohit`, `shashwat`, … — other assignees (as used in `tasks.assignee`).
  NOTE: no cross-machine transport yet — a message to another assignee
  queues locally with a warning; delivery lands with flow-workspace.
- `<assignee>/<task-slug>` — the SESSION bound to that task.
- A bare task slug is sugar for `self/<slug>`.

## Messaging the human

```
flow message self "coinswitch-gcp-migration: prod release plan ready, needs approval"
flow message self "state bucket perms broken, blocked on your GCP login" --urgent
```

The message stays pending on an escalating schedule (1m, 2m, 4m … capped
at 30m — surfaced via `flow inbox due` to whatever notifier the user
scripted) until answered. Discipline:

- Message when blocked on the user's decision, a permission only they
  can grant, a long task finishing that they wait on, or something they
  must know NOW. Never routine progress; never when they are actively
  replying in this session.
- ONE pending message per wait. The schedule escalates it — re-sending
  is noise.
- Body ≤200 chars; lead with the ask; mention the task slug or an
  update-file path in the body when the recipient needs context.
- Ack is automatic: the user's next prompt in this session acks it and a
  hook injects "answered after <duration>" — factor that elapsed time in
  (long waits mean stale state: re-verify before acting). They can also
  answer via `flow inbox pop` / `flow inbox ack`.

## Messaging a peer session

```
flow message self/tekion-hub-network "state file moved, re-read before release"
```

Delivered instantly if the session has a listener parked (below), else
into its context at its next prompt / session start. Include what the
peer should DO with the information.

## Broadcasting (posts) and watching

```
flow post "imports done, 3 drifts left"          # from a bound session
flow watch coinswitch-gcp-migration               # subscribe to one task
flow watch alpha-cp                               # a whole project
flow watch shashwat                               # everything an assignee's tasks post
flow watch --list / --rm <target>                 # inspect / unsubscribe
flow watch <target> --as self                     # subscribe the USER, not this session
```

Semantics: the poster NEVER picks recipients. At post time the one-liner
fans out as a message to every CURRENT watcher of the task's slug, its
project, or its assignee (new watchers don't receive older posts). Posts
never escalate; if someone specific must act, message them as well.

At turn end a Stop hook (a) hands you any mail that arrived mid-turn
when no listener was parked — act on it if it changes anything, then
park a listener — and (b) nudges you to post when the task has watchers
and your last post is >30 min old — post only if the turn produced something a watcher
would care about; skip freely. Declined nudges back off exponentially
(30m, 1h, 2h.. capped 4h) and a post resets the cycle, so skipping is
cheap.

## Consuming: inbox and pop

```
flow inbox [--json]            # list pending (identity-aware)
flow inbox pop [--json]        # consume the oldest, exit 1 if empty
flow inbox pop --wait --timeout 3600
flow inbox pop --wait --as self  # a session monitoring the USER's inbox
flow inbox pop --as shashwat   # drain another assignee's queue (transport/monitor workers)
```

Identity is implicit: a bound session consumes its own task's mail; an
unbound/human invocation consumes the user's. `--as <assignee>`
targets a human queue directly: `--as self` forces the user's own
inbox from inside a bound session — e.g. a dedicated flow task whose
job is monitoring the user's inbox — and any other assignee serves
monitor/transport workers draining that queue. Pops are atomic claims, so
concurrent consumers of one inbox (your terminal + a monitor task)
never double-pop. `--json` on inbox/pop/due emits machine-readable
rows for scripting. Popping a human-directed message ACKS it (popping
is answering); posts are marked delivered. Rows are never deleted by
popping — they transition status and age out via the 90d sweep.

To be WOKEN by mail instead of discovering it on your next tool call,
park your **Monitor tool** — or a background Bash command
(`run_in_background: true`) — on `flow inbox pop --wait`. It blocks
until a message exists, pops exactly one, prints it, and exits 0 — the
exit wakes you, and its output reminds you to RE-ARM immediately (run it
again, backgrounded) so the next message also reaches you. Keep one
armed for the whole session. One listener per identity. Exit 1 =
timeout/empty, nothing popped. Also subscribe to the tasks you depend
on or spawn: `flow watch <slug>` for anything you're waiting on,
coordinating with, or any task you create from this session.

## For the user: scripting notification UX

flow ships no UI. `flow inbox due` prints human-directed messages whose
notify deadline passed (tab-separated: id, attempts, age, urgent, from,
body) and advances each row's backoff. Poll it from cron, a launchd
job, or a shell loop and pipe into any notifier (terminal-notifier,
OSC escapes, ntfy, Slack, say). Empty = exit 1, prints nothing.

## Utilities

```
flow inbox ack [<id>]   # answer by hand (no-arg acks all pending human messages)
flow inbox stats        # answered/pending counts, avg/median/worst wait
```

Retention is automatic: answered/delivered rows sweep after 90 days
(pending messages never expire); stale listeners prune after an hour.
