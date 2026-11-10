# flow do — edge cases & surgical instructions

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

#### Special case: live-session guard

`flow do` refuses to spawn when the task's `session_id` is already
running in another process of the task's harness — typically because the user has the
task's tab open elsewhere and forgot. The error names the running
session ID and points at `--force`. When you see it:

1. Tell the user, in plain language, that the task already has a
   running session in another tab. Suggest they switch to that tab.
2. Offer via AskUserQuestion (header: "Open another?", options:
   "Switch to the existing tab" / "Open another anyway") whether to
   bypass the guard. On "Open another anyway", retry with `--force`.

Don't auto-retry with `--force`. The guard exists because two live
sessions on the same task fork the conversation and cause confusion;
bypassing it should be an explicit choice.

#### Special case: macOS Accessibility error from the Terminal.app backend

When `flow do` runs from a stock Terminal.app shell and macOS hasn't
granted Accessibility, the binary returns a multi-line error that
explicitly names "Terminal" as the app needing the grant and includes
a `open "x-apple.systempreferences:..."` URL to jump straight to the
right Settings pane. When you see that error:

1. **Trust the error verbatim.** It says "Terminal" because macOS
   attributes Accessibility to the responsible parent app, which is
   Terminal.app — NOT Claude Code or Codex, and NOT the flow binary. Do not advise
   the user to toggle "Claude" or "flow"; that wastes their time.
2. **Open the Accessibility pane for them**: run
   `open "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"`.
3. **Tell the user, in plain language**, what to do next: enable the
   toggle for "Terminal" in the list (or click + and add Terminal.app
   from `/System/Applications/Utilities/` if it isn't shown).
4. **Wait for the user to confirm** they've granted it. Don't poll
   or retry on a timer.
5. When they confirm, retry the original `flow do` invocation with
   the same flags they originally chose (session mode, `--fresh`,
   etc.).

Macros for this: do not invent more candidate apps to toggle, do not
suggest the user reinstall flow, do not attempt to grant Accessibility
yourself. macOS guards Accessibility deliberately — there is no CLI to
self-grant it, and an agent cannot bypass that.

#### Special case: no supported terminal (Linux outside tmux/zellij/kitty)

flow opens tabs through the terminal it is running in. On macOS that is
iTerm2, Terminal.app, Warp, Ghostty, kitty, zellij or tmux; on Linux it is
tmux, zellij or kitty only. When none is detected, `flow do` exits 0 and
prints a handoff instead of a tab:

```
<slug>: no supported terminal detected on this host, so no tab was opened.
The session is reserved for you — run this in any terminal to start it:

  cd '<work_dir>' && claude --session-id <uuid> '<bootstrap prompt>'

Or skip the tab:
  flow do --here <slug>   bind the harness session you are already in
  flow do --auto <slug>   run it headless in the background
```

The task is already in-progress with that session id, so running the
printed command lands in a bound session (the SessionStart hook finds the
task). A later `flow do <slug>` prints the command again — with
`--session-id` until the session has actually been started once, then
`--resume`. When you see it:

1. **Do not run the printed command yourself** — it starts an interactive
   harness and would hang your Bash tool. Relay it to the user verbatim.
2. Offer via AskUserQuestion (header: "No tab here", options:
   "I'll paste the command" / "Bind this session (--here)" /
   "Run it headless (--auto)"). `--here` only makes sense when the user
   is already inside the harness session they want to use for the task.
3. Mention once, not every time: starting their shell inside tmux
   (`tmux` — `apt install tmux` / `dnf install tmux`) or zellij makes
   future tabs open automatically; `FLOW_TERM=tmux|zellij|kitty` forces a
   backend when auto-detection misses.

#### Surgical instructions: `--with` and `--with-file`

**Triggers:** the user wants to *fire a one-off instruction at a task*
without opening the tab to type it themselves. Phrasings:

- "tell <task> to <do X>"
- "nudge <task> to check <Y>"
- "have <task> verify <Z>"
- "fire <instruction> at <task>"
- "ping <task> with <instruction>"
- "ask <task> whether <Q>"

**Recipe:** add `--with "<instruction>"` to the `flow do` invocation.
Quote the instruction as a single shell-safe string. The session
receives it as its first user message, prefixed with
`[via flow do --with]` so the model knows it's an injected instruction
rather than typed input.

**Use `--with-file <path>` when:** the instruction is a longer brief
the user already wrote down (a checklist, a multi-step recipe, a
one-pager). flow does NOT embed the file contents — it injects
`read instructions at <abs-path>` and the session uses its Read tool
to load it. No size limits. Use this whenever the user references a
file ("the brief in ~/notes/X.md", "the checklist at triage.md").

**The flags are mutually exclusive.** If the user mixes them, ask via
AskUserQuestion which one they meant.

**`--with` on a `done` task** auto-rolls it back to in-progress and
proceeds. This is the supported lane for "nudge a parked task" — do
NOT pre-flip status yourself; just pass `--with` and let `flow do`
handle the reopen. The binary prints a stderr notice
(`--with on done task "X": reopening as in-progress`) — relay it
verbatim.

**`--with` is incompatible with `--here`.** `--here` binds the
current session with no spawn, so there's no first message to inject;
the binary rejects the combination with rc=2. If the user wants to
both bind-here AND act on an instruction, they're already in the
session — just do the work directly, no `--with` needed.

**Same flags work on `flow run playbook <slug>`** — use them when the
user wants a one-off instruction layered on top of a fresh playbook
run (e.g. a scheduled run that today should also "double-check the
Acme deal status").

**When NOT to use `--with`:** if the user is opening the tab to work
in it themselves. `--with` is for fire-and-forget nudges, not for
"open the tab with this prompt pre-typed for me". When the user will
be at the keyboard, run plain `flow do <slug>`.
