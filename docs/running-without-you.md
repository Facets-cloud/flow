# Running without you

Most people use flow interactively: one task, one agent conversation, picked
up by name whenever you come back. Every task can also run with nobody
watching. This page covers the ways to do that, from a single background run
to an agent that looks after something for weeks.

```
  you, in a tab          background run        routine             owner
  flow do <task>   -->   flow do --auto   -->  flow run playbook   flow add owner
  (you drive)            (runs to "done when", (same brief, run     (checks in on its own
                          closes itself)        again and again)     schedule, keeps notes)
                                  \                   |                  /
                                   '---- agents message you when they need you ----'
```

Everything here works with Claude Code and Codex, on the subscription you
already have. No API keys, no extra service.

## Background runs

```bash
flow do --auto <task>
```

The task's agent starts headless: no tab, no human. It reads the brief, works
toward the brief's "Done when", writes a progress note, and runs `flow done`
on itself. Closing still teaches flow: the close-out pass reads the
conversation and saves what it learned, same as when you close a task by hand.

`flow show task <task>` shows whether a run is `running`, `completed`, or
`dead` (exited without closing), with a log under
`~/.flow/tasks/<task>/auto-runs/`. `flow list tasks` has an `AUTO` column.

To create a task, write its brief and start it in one step:

```bash
flow auto "Upgrade the Go toolchain to 1.24" --brief brief.md --slug go-124
```

Background runs skip tool-approval prompts. Write the brief so it says what
the agent may and may not do (for example "open a PR, do not merge").

## Routines (playbooks)

Some work repeats: a morning triage, a weekly review, a monthly cost check.
A **playbook** is a brief you write once and run as often as you like.

```bash
flow add playbook "Daily triage" --work-dir ~/work --slug daily-triage
flow run playbook daily-triage            # in a new tab
flow run playbook daily-triage --auto     # in the background
```

Each run is its own task with a frozen copy of the playbook brief, so editing
the playbook later does not rewrite what past runs did. Runs close like any
other task, so the knowledge base keeps learning from them.

flow has no scheduler of its own. To run a playbook every morning, point
launchd or cron at `flow run playbook <slug> --auto`.

## Handing work to background agents (drivers)

A long conversation tends to collect side questions. Each one derails the
main thread. Tag a task `driver` and its agent hands self-contained pieces
(a bit of research, a fix with a PR, a scan) to background runs, while you
keep talking:

```bash
flow update task launch-plan --tag driver
```

```
   you <--> driver task (your tab)
               |-- flow auto "competitor pricing"   --> note when done
               |-- flow auto "billing rules in IN"  --> note when done
               '-- flow auto "sample app analysis"  --> note when done
```

Each child is a normal task linked to its parent. It reports back on the
message bus at milestones and when done, and leaves its result in a progress
note you can read later. Children do not spawn children of their own.

## Owners

An **owner** keeps something true over weeks: "this service stays healthy",
"tell me when the customer confirms the DNS change", "keep this repo's
dependencies current". It has a charter (what it is responsible for), a
journal, and a clock.

```bash
flow add owner "Keep staging healthy" --work-dir ~/work/infra --every 1h
flow owner start keep-staging-healthy
flow owner tick  keep-staging-healthy     # wake it now, in a tab, to watch the first run
```

Each check-in is a fresh headless session. It reads the charter and its
recent journal notes, looks at the tasks it owns, starts new background runs
if something needs doing, asks you about decisions it should not make, writes
a journal note, sets when it wants to wake next, and exits. Between check-ins
nothing is running, so there is no long-lived process or growing chat to look
after.

Owners need a host timer that calls `flow owner tick-due` (launchd or cron,
every minute is fine). `flow owner list` and `flow owner show <slug>` show
what each owner is doing and when it wakes next. `pause`, `start` and
`retire` do what they say.

## Agents messaging you

Background agents need a way to reach you. flow has a small message bus:

```bash
flow message user "deploy-v2: ship to prod now?" --options "Yes|No"
flow message user "imports 3/5 done" --fyi
flow inbox
```

A question waits in your inbox until you answer. Your answer goes back to the
task that asked. Agents can also message each other's tasks
(`flow message <assignee>/<task> "..."`), and `flow watch <task>` subscribes
you to a task's updates.

If you have stepped away, a **connector** can ping you, for example with a
native macOS dialog. See [connectors.md](connectors.md).

## A few honest notes

- Background runs are only as good as the brief. A clear "Done when" matters
  more here than anywhere else.
- Some runs die before finishing (a crash, a timeout, a harness change). They
  show up as `dead` in `flow show task`; rerun or open the task in a tab.
- macOS is the tested platform today.

Back to the [README](../README.md).
