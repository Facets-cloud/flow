# Paging bus — full reference (§4.18)

The paging bus lets a flow-bound Claude session (a) call for the user's
attention, (b) message another task's session, and (c) broadcast
one-liner updates to subscribers. One SQLite-backed inbox in flow.db,
one delivery path; aggressive hooks drain it on every LLM turn.

## Address grammar

`<assignee>[/<task-slug>]`

- `self` — the local user (tasks with no assignee belong to self).
- `rohit`, `shashwat`, … — other assignees (as used in `tasks.assignee`).
  NOTE: no cross-machine transport yet — a page to another assignee
  queues locally with a warning; delivery lands with flow-workspace.
- `<assignee>/<task-slug>` — the SESSION bound to that task.
- A bare task slug is sugar for `self/<slug>`.

## Paging the human

```
flow page self "prod release plan ready, needs approval" --re coinswitch-gcp-migration
flow page self "state bucket perms broken, blocked on your GCP login" --urgent
```

What happens: a native iTerm notification fires from THIS session's tab
(clicking it focuses this tab), the Dock requests attention, the tab is
badged, and the page re-notifies with exponential backoff (1m, 2m, 4m …
capped at 30m) until answered. `--urgent` bounces the Dock until iTerm
is focused.

Discipline:
- Page when blocked on the user's decision, a permission only they can
  grant, a long task finishing that they wait on, or something they must
  know NOW. Never for routine progress; never when they are actively
  replying in this session.
- ONE pending page per wait. The bus escalates it — re-sending is noise.
- Body ≤200 chars; lead with the ask; context goes in `--re <task-slug>`.
- Ack is automatic: the user's next prompt in this session acks the page
  and a hook injects "answered after <duration>" — factor that elapsed
  time in (long waits mean stale state: re-verify before acting).

## Messaging a peer session

```
flow page self/tekion-hub-network "state file moved, re-read before release"
```

Delivered into that session's context via hooks: mid-turn on its next
tool call, or at its next prompt / session start — or instantly if it
runs a listener. Include what the peer should DO with the information.

## Broadcasting (posts) and watching

```
flow post "imports done, 3 drifts left"          # from a bound session
flow watch coinswitch-gcp-migration               # subscribe to one task
flow watch alpha-cp                               # a whole project
flow watch self --list / --rm <target>            # inspect / unsubscribe
```

Semantics: the poster NEVER picks recipients. At post time the one-liner
fans out as a message to every CURRENT watcher of the task's slug, its
project, or its assignee (new watchers don't receive older posts). Posts
never interrupt: sessions get them as context, the human sees them in
`flow page` and `flow watch --follow`. If someone specific must act on
the content of a post, page them as well — posts never escalate.

A Stop hook nudges you to post when the task has watchers and your last
post is >30 min old — post only if the turn produced something a watcher
would care about; skip freely.

## Listening (be woken instead of polling)

```
flow page listen --timeout 3600
```

Run it as a BACKGROUND Bash command (`run_in_background: true`). It
blocks until a page/post arrives for this session, prints it, and exits
— the exit wakes you. Re-start it after each wake while you still expect
mail. One listener per session; a hook nudges you if mail arrived with
no listener running. Humans can run `flow page listen` (unbound) or
`flow watch --follow` as a terminal feed.

## Utilities

```
flow page              # pending pages/posts for the user, with age + notify count
flow page ack [<id>]   # manual ack (no-arg acks everything pending)
flow page stats        # answered/pending counts, avg/median/worst wait
```

Retention is automatic: answered/delivered rows are swept after 90 days
(pending pages never expire); stale endpoints prune after 30 days.
