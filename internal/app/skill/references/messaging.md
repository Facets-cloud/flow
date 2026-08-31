# Message bus — full reference (§4.18)

flow's bus lets a bound session (a) call for the user's attention,
(b) message another task's session, and (c) broadcast one-liner updates
to subscribers. One SQLite-backed inbox in flow.db, one delivery path;
hooks drain it on every LLM turn. The bus is CLI-only by design: flow
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
flow message self "prod release plan ready, needs approval" --re coinswitch-gcp-migration
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
- Body ≤200 chars; lead with the ask; context goes in `--re <task-slug>`.
- Ack is automatic: the user's next prompt in this session acks it and a
  hook injects "answered after <duration>" — factor that elapsed time in
  (long waits mean stale state: re-verify before acting). They can also
  answer via `flow inbox pop` / `flow inbox ack`.

## Messaging a peer session

```
flow message self/tekion-hub-network "state file moved, re-read before release"
```

Delivered into that session's context via hooks: mid-turn on its next
tool call, or at its next prompt / session start — or instantly if it
has a listener parked (below). Include what the peer should DO with the
information.

## Broadcasting (posts) and watching

```
flow post "imports done, 3 drifts left"          # from a bound session
flow watch coinswitch-gcp-migration               # subscribe to one task
flow watch alpha-cp                               # a whole project
flow watch shashwat                               # everything an assignee's tasks post
flow watch --list / --rm <target>                 # inspect / unsubscribe
flow watch <target> --me                          # subscribe the USER, not this session
```

Semantics: the poster NEVER picks recipients. At post time the one-liner
fans out as a message to every CURRENT watcher of the task's slug, its
project, or its assignee (new watchers don't receive older posts). Posts
never escalate; if someone specific must act, message them as well.

A Stop hook nudges you to post when the task has watchers and your last
post is >30 min old — post only if the turn produced something a watcher
would care about; skip freely.

## Consuming: inbox and pop

```
flow inbox                     # list pending (identity-aware)
flow inbox pop                 # consume the oldest, exit 1 if empty
flow inbox pop --wait --timeout 3600
```

Identity is implicit: a bound session consumes its own task's mail; an
unbound/human invocation consumes the user's. Popping a human-directed
message ACKS it (popping is answering); posts are marked delivered.

To be WOKEN by mail instead of discovering it on your next tool call,
park your **Monitor tool** — or a background Bash command
(`run_in_background: true`) — on `flow inbox pop --wait`. It blocks
until a message exists, pops exactly one, prints it, and exits 0 — the
exit wakes you. Re-arm it after each wake while you still expect mail.
One listener per identity; a hook nudges you if mail arrived with no
listener parked. Exit 1 = timeout/empty, nothing popped.

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
