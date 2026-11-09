# Your data: local, portable, yours

Everything flow stores lives under `~/.flow/` (override with `$FLOW_ROOT`).
No server, no cloud, no telemetry. Plain markdown beside a SQLite index,
readable in any editor and versionable in git.

```
~/.flow/
  flow.db                          # SQLite: projects, tasks, playbooks index
  kb/
    user.md  org.md  products.md
    processes.md  business.md      # what flow has learned, 5 markdown files
  projects/<slug>/
    brief.md
    updates/YYYY-MM-DD-*.md
  tasks/<slug>/
    brief.md
    updates/YYYY-MM-DD-*.md
  playbooks/<slug>/
    brief.md
    updates/YYYY-MM-DD-*.md
```

Each task's real content is in the markdown next to it: the brief (a short
plan for the task) and dated progress notes. The database indexes them and
keeps the link from each task to its agent conversation.

## How flow learns

```
   task brief + notes ---> agent session ---> flow done
          ^                    |   |              |
          |      live: facts   |   |   close-out: re-read the
          |      you mention   v   v   whole conversation
          '-------------  ~/.flow/kb/*.md  <------'
                   (loaded into every future session)
```

- **While you work**, the flow skill notices durable facts you mention (your
  role, a teammate's name, a product convention) and appends them to the
  matching kb file.
- **When you close a task**, flow runs a headless pass over the whole
  conversation and saves anything the live pass missed. For tasks in a
  project, it also writes a project-level update.
- **Across tasks**, `flow transcript <other-task>` lets a session read what
  was decided in a related one.

## Backup and sync

- **Git.** `cd ~/.flow && git init && git add . && git commit -m initial`,
  then commit now and then. If you push to a shared remote, add `kb/` to
  `.gitignore` first, since kb files often hold personal or company notes.
- **Time Machine** or any system backup works with no setup.
- **iCloud Drive, Dropbox, Google Drive.** Move `~/.flow` into the synced
  folder and symlink it back:
  ```bash
  mv ~/.flow ~/Library/Mobile\ Documents/com~apple~CloudDocs/flow
  ln -s ~/Library/Mobile\ Documents/com~apple~CloudDocs/flow ~/.flow
  ```
  Don't run flow on two machines at the same time through a synced folder.
  SQLite does not tolerate concurrent writes from separate hosts and
  `flow.db` can get corrupted. Use this for backup and occasional
  second-machine access.
- **rsync.** `rsync -a ~/.flow/ /path/to/backup/flow/` on a schedule, with the
  same caveat.

To move flow to a new machine: copy `~/.flow/` over, install the binary, and
run `flow init` once. It picks up the existing data and reinstalls the skill
and hooks.

## `flow stats`

Usage figures from your own flow history: how often flow recalled stored
context for you, tasks done, background runs, and an estimate of time saved.

```bash
flow stats                      # all-time report in the terminal
flow stats --since 30d          # last 30 days
flow stats --project <slug>     # one project
flow stats --card               # shareable HTML card at ~/.flow/stats-card.html
flow stats --card --out card.html
```

Counts are exact. Time and cost savings are estimates driven by
`~/.flow/stats.json` (optional, built-in defaults apply without it).
`~/.flow/stats-cache.json` is a derived cache, safe to delete, and worth
gitignoring if you track `~/.flow` in git.

Back to the [README](../README.md).
