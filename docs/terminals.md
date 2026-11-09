# Terminals and how sessions start

`flow do <task>` opens the task's agent conversation in a new tab of the
terminal you ran it from. Tomorrow's `flow do <task>` resumes the same
conversation.

## Supported terminals

| Terminal | Detected by | Setup |
|---|---|---|
| zellij (0.40+) | `$ZELLIJ` | none |
| [Rex](https://www.superlogical.com/rex) | `$REX_SESSION` or `$TERM_PROGRAM=rex` | turn on Remote Control (see below) |
| kitty | `$KITTY_WINDOW_ID` or `$TERM=xterm-kitty` | `allow_remote_control yes` (or `socket-only`) in `kitty.conf` |
| Warp, iTerm2, Ghostty, Terminal.app | `$TERM_PROGRAM` | Terminal.app and Warp need Accessibility (see below) |

flow checks zellij, Rex and kitty first, then `$FLOW_TERM`, then
`$TERM_PROGRAM`. Set `FLOW_TERM=warp|iterm|terminal|ghostty|zellij|kitty|rex`
to pick a backend on a host flow can't detect. iTerm2 is the fallback when
nothing is detected.

zellij and kitty also work on Linux, since neither backend uses macOS APIs.
Release binaries are macOS-only today.

### macOS Accessibility (Terminal.app and Warp)

Terminal.app's AppleScript dictionary has no "make new tab" verb, so flow
sends cmd-T through System Events. System Events checks Accessibility against
the **app hosting your shell**, not the `flow` binary. Until that is granted,
`flow do` errors with a message pointing at System Settings > Privacy &
Security > Accessibility. Enable the toggle for "Terminal" if you launched
flow from Terminal.app, "iTerm" from iTerm2, "Claude" if Claude Code is the
host, and so on (add it with the + button if it is not listed). After the
grant the spawn is silent. iTerm2 does not need this, it has a native
`create tab` verb.

### Rex

Rex needs no setup beyond one toggle. flow drives the `rex` CLI that Rex puts
on `PATH`, and opens the new tab in the Rex session you ran `flow do` from. If
the task's project has a terminal group, the tab opens in that Rex session
(created if missing):

```bash
flow update project billing --terminal-group billing
```

`flow do` on an already-open task finds its tab in any Rex session. Bringing
a tab into view goes through the Rex app, so turn on **Remote Control** in
Rex Settings > Rex Server. Without it flow still opens or finds the tab,
prints a reminder, and leaves the app on its current tab.

## What happens on `flow do`

`flow do <task>` picks the task's harness (Claude Code or Codex), records its
session id, and spawns the tab. Claude sessions use a UUID flow allocates.
Codex sessions mint a real thread with `codex exec --json` before flow opens
`codex resume <thread-id>`. A SessionStart hook loads the task brief, progress
notes, project context and repo conventions on every start and resume.

- **Pick the harness for a new task:** `flow do <task> --harness codex` (or
  `--harness claude`). Once a task has a session its harness stays pinned, so
  flow can always resume the same transcript.
- **Already open?** If the task's session is live in another tab, flow focuses
  that tab instead of opening a duplicate.
- **Adopt the current conversation:** `flow do --here <task>` binds the
  session you are in to the task, without a new tab.
- **Run it with nobody watching:** `flow do --auto <task>`. See
  [running-without-you.md](running-without-you.md).

## One-off instructions with `--with`

`flow do <task> --with "<instruction>"` resumes (or starts) the task's
session and sends the instruction as the first message, prefixed with
`[via flow do --with]` so the agent can tell it apart from typed input.
`--with-file <path>` does the same for longer instructions: flow sends `read
instructions at <absolute path>` and the agent reads the file. The two flags
are mutually exclusive and cannot be combined with `--here`.

```bash
# Nudge a parked task without opening the tab yourself.
flow do auth --with "check if the upstream PR merged and update the brief if so"

# --with on a done task moves it back to in-progress.
flow do auth --with "are we still blocked on the security review?"

# Hand the session a longer checklist.
flow do auth --with-file ~/checklists/triage.md
```

`flow run playbook <slug>` takes the same flags.

## Want another terminal?

Session spawning is one small package per terminal (`internal/iterm`,
`internal/warp`, `internal/zellij`, ...). Linux with tmux or wezterm, and
Windows Terminal, need contributors who use them daily.
[A PR is very welcome](../CONTRIBUTING.md).

Back to the [README](../README.md).
