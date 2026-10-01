# Claude Desktop sessions

> Loaded on demand from the flow skill's resident core (SKILL.md). Read it when
> this session runs in Claude Desktop's Code tab: `$CLAUDE_CODE_ENTRYPOINT` is
> `claude-desktop`.

## How `flow do` behaves here

Inside a Desktop session `flow do` does not open a terminal tab. It opens the
task in Claude Desktop instead:

- **Task with a session** → re-opens it with `claude://resume`. The transcript
  carries over, so the task keeps its session id. If Desktop already has it
  open, `flow do` just switches to it ("Already open … switched to it in
  Claude Desktop").
- **Task without a session, or `--fresh`** → opens a new Desktop Code session
  in the task's work_dir with a pre-filled prompt. Nothing starts until the
  user sends that prompt. The new session then runs `flow do --here <slug>`
  itself (Desktop picks its session id, so binding happens from inside), and
  that is what flips the task to in-progress.

After `flow do` succeeds, report what it printed and stop, exactly as in the
terminal case. For a new session, tell the user to switch to it and send the
pre-filled prompt.

Errors to relay, not work around:

- **"open in a terminal; close it there…"** — a terminal already runs this
  session. Opening it in Desktop too would put two writers on one
  transcript. Ask the user to close the terminal one; offer `--force` only if
  they insist.
- **`--with` can't be delivered through the resume link** — send the
  instruction to the task's session with `flow message` instead.
- **`--dangerously-skip-permissions` is ignored** — Desktop sets the
  permission mode per session.

`flow owner tick` (interactive) cannot open in Desktop; it needs a terminal.
`--auto` runs headless as usual.

## Sidebar grouping (the `ccd_sidebar` tools)

Desktop gives every Code session `mcp__ccd_sidebar__*` tools (load them with
ToolSearch). Use them so the sidebar mirrors flow's structure:

1. **After this session binds to a task** (bootstrap or `flow do --here`): if
   the task has a project, call `list_groups`; if no group is named after the
   project slug, `create_group` with that name. Then `move_sessions` with
   `["self"]` into that group. Floating tasks stay ungrouped.
2. **After `flow done` on this session's task**: call `mark_completed` for
   this session.
3. **When an inbox Monitor wakes you with new mail for this session**: call
   `set_unread` on this session so the sidebar shows a dot.

Do these quietly; one short line after is enough ("filed under side-quests").
The tools are unavailable in unattended runs and can be switched off remotely.
If a call fails, mention it once and carry on; never retry in a loop. Desktop
has no tool to rename a session, so titles stay whatever Desktop chose.
