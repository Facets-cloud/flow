# flow dashboard

A read-only terminal UI that shows everything on your plate at a
glance. Launch it with:

```
flow dashboard
```

The dashboard is a *viewer*, not an editor — it doesn't mutate task
state on its own. The one action it surfaces is `enter`, which calls
`flow do <slug>` (or `flow run playbook <slug>`) and spawns the task
or playbook run in a new terminal tab. Quit with `q`.

---

## Status view (default)

Six panes, top to bottom:

| Pane | Contents |
|---|---|
| `working` | in-progress tasks that have been touched recently |
| `awaiting` | in-progress tasks with `waiting_on` set |
| `stale` | in-progress tasks untouched for more than 7 days |
| `backlog` | tasks not yet started, sorted high → low priority |
| `playbooks` | playbook definitions, sorted by most-recent run |
| `recent activity` | git-log-style stream of recent notes, starts, dones, project creations |

Empty panes are hidden so the layout stays compact. Each visible
pane gets adaptive height — a 1-row stale pane takes only one row,
while a 30-row backlog pane gets the rest of the vertical budget.

The right side hosts a `details` pane that mirrors the currently
selected row: priority, status, updated/created times, due date,
assignee, waiting-on, workdir, tags, and the headline of the latest
update file. When the active pane is `playbooks`, the right side
splits vertically — top is the playbook's metadata, bottom is its
runs sub-pane.

### Key bindings

| Key | Action |
|---|---|
| `↑` / `↓` / `k` / `j` | Move the cursor within the active pane |
| `←` / `→` / `h` / `l` | Cycle to the previous / next non-empty pane |
| `tab` / `shift+tab` | Same as ←→ but force a pane switch (skipping any sub-pane drill-in) |
| `enter` | Open the selected task via `flow do <slug>` |
| `pgup` / `pgdn` / `ctrl+u` / `ctrl+d` | Half-page scroll inside the active pane |
| `g` / `G` / `home` / `end` | Top / bottom of the active pane |
| `r` | Reload the snapshot from `flow.db` and `~/.flow/...` |
| `q` / `ctrl+c` | Quit |

---

## Project view

Toggle with `v`. The layout pivots: projects become the top-level
list on the left, and the selected project's tasks fan out on the
right grouped by status.

```
projects  (5)                              cost-management
╭──────────────────────────────────────╮  ╭─────────────────────────────────────────╮
│ › cost-management        3 tasks     │  │  working  (1)                            │
│   developer-experience   4 tasks     │  │    ●  channel-reserve  @sarah  me  5m   │
│   infrastructure-…       3 tasks     │  │                                          │
│   observability          4 tasks     │  │  backlog  (2)                            │
│   security-compliance    3 tasks     │  │    ○  stage-cost-opt           hi  4d   │
╰──────────────────────────────────────╯  │    ○  budget-alerts            me  2d   │
                                           ╰─────────────────────────────────────────╯
```

| Key | Action |
|---|---|
| `↑` / `↓` | Move the project cursor; right pane updates instantly |
| `→` / `l` | Drill into the right task pane (border highlights blue) |
| `↑` / `↓` (focused on right) | Move the task cursor across all status sections |
| `enter` (focused on right) | Open the selected task via `flow do <slug>` |
| `←` / `h` / `esc` | Return focus to the project list |
| `v` | Toggle back to status view |

---

## Playbooks pane (status view)

Each row shows a playbook definition with its run count and the
relative time of the most-recent run.

| Key | Action |
|---|---|
| `↑` / `↓` | Move across playbooks |
| `enter` | Spawn a **new** run — `flow run playbook <slug>` (a new task with `kind=playbook_run` and its own session) |
| `→` / `l` | Drill into the runs sub-pane (right side, below the details box) |
| `↑` / `↓` (runs focus) | Move across past runs of the selected playbook |
| `enter` (runs focus) | Open the selected existing run via `flow do <run-slug>` |
| `←` / `h` / `esc` | Return focus to the playbook list |

So: `enter` on a playbook starts new work; `→ enter` on a run
reopens past work.

---

## Search & filters

The dashboard hides a one-line filter bar at the top — it only
materializes when you start typing or when a facet filter is active.

### Free-text search

| Key | Action |
|---|---|
| `/` | Focus the search bar |
| (type) | Filter task panes in real time |
| `enter` (in search) | Keep the filter, unfocus the input so navigation keys work again |
| `esc` (in search) | Clear the search text and unfocus |

The search uses **fuzzy match** (fzf-style subsequence) against task
slug, project slug, tag, and assignee — so `btmig` matches
`budget-migration`, and `omndr` matches assignee `@Omendra`. It uses
a stricter **substring match** against the human-readable task name
to avoid spurious hits in long sentences.

Matched characters render in bright yellow inside the row labels so
you can see which positions matched.

### Facet hotkeys

Each of these cycles through values that are *actually present* in
the data — you'll never get a filter that hides everything by
accident.

| Key | Cycles |
|---|---|
| `p` | priority: `none → high → medium → low → none` |
| `P` | project: `none → <first project> → ... → none` |
| `a` | assignee: `none → <first assignee> → ... → none` |
| `t` | tag: `none → <first tag> → ... → none` |

Active filters render as chips between the counts pill and the
panes: `[/btmig]  [hi]  [@karl]  [#frontend]  [proj:flow]`. Press
`esc` (outside of search) to clear all of them in one shot.

Header counts always reflect **raw** totals so you keep sight of the
actual workload while narrowing the view.

---

## Layout rules

- Terminals wider than ~100 columns get the two-column layout
  (panes on the left, detail on the right). Narrower terminals fall
  back to a single column.
- Left-column width is adaptive — it tracks the widest task label
  in your data, capped at 70% of the screen.
- Labels that exceed the available column truncate with `…`;
  priority, assignee, and reltime always render at full width.
- Reltime is right-aligned so `1d ago` and `12d ago` line up on
  the right edge.

---

## What's intentionally out of scope (for now)

- **Auto-refresh** — manual `r` only. Add a ticker if you want it.
- **A real events table** — the activity feed is derived live from
  update file mtimes, `status_changed_at`, and project creation
  dates. No event storage; nothing to migrate.
- **Smart-search query syntax** (`#tag @user p:hi` parsed from one
  line) — could be a nice power-user add later.
- **Saved filter presets** — same.
