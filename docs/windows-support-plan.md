# Windows support — implementation plan

Tracking issue: https://github.com/Facets-cloud/flow/issues/81

flow was macOS-only by design. This document is the plan for a
full-parity Windows port. It is implemented on the
`feat/windows-support` branch in phases; each phase is independently
verifiable from a non-Windows dev box via a `GOOS=windows` cross-compile
plus the native `go test ./...` suite, with final behavioral
verification on real Windows hardware.

## Why it was macOS-only

- No build tags, no `runtime.GOOS` branching anywhere.
- Two hard compile-blockers for `GOOS=windows`:
  `syscall.SysProcAttr{Setsid: true}` in `internal/app/auto.go` and
  `internal/app/owner_tick.go` (the `Setsid` field is Unix-only).
- Runtime assumptions: process-table scans via `ps -axo`; all
  interactive terminal backends (iTerm2, Terminal.app, Warp, Ghostty)
  drive macOS `osascript`; `spawner.Detect()` falls through to iTerm
  for any unknown environment; `ShellQuote` is POSIX single-quote;
  `EncodeCwd` assumes `/`-separated paths; the owners scheduler doc
  covers launchd/systemd/cron only.

## What was already portable

- Pure-Go SQLite (`modernc.org/sqlite`, no CGO) → trivial cross-compile.
- `filepath.Join` + `os.UserHomeDir()` everywhere (so `~/.flow`,
  `~/.claude` resolve via `%USERPROFILE%`).
- A clean `harness.Harness` interface; the session-spawn layer is one
  small `spawner` package.
- `FLOW_TERM=bg` background-agent mode bypasses terminals entirely
  (no AppleScript) — the natural Windows MVP path.

## Design: how the platform split is done

Standard Go build-tag pattern. Unix-only and Windows-only behavior live
in sibling files selected by `//go:build` constraints; the rest of the
code calls a small, platform-neutral seam:

| Seam | Unix file | Windows file |
|---|---|---|
| Detached child process + liveness | `internal/app/proc_unix.go` (`setDetached` via `Setsid`; `processAliveImpl` via signal-0) | `internal/app/proc_windows.go` (`CREATE_NEW_PROCESS_GROUP\|DETACHED_PROCESS`; liveness via `OpenProcess`+`GetExitCodeProcess`) |
| Process-table scan for live-session detection | `internal/harness/claude/ps_unix.go` (`ps -axo`) | `internal/harness/claude/ps_windows.go` (`Get-CimInstance Win32_Process`) |
| Shell quoting of the launch command | `internal/spawner/shellquote_unix.go` (POSIX single-quote) | `internal/spawner/shellquote_windows.go` (PowerShell single-quote) |
| Interactive terminal backend | iterm/terminal/warp/ghostty (macOS), zellij/kitty (cross-platform) | `internal/winterm` (Windows Terminal via `wt.exe`) |

The overridable test seams (`processAlive`, `claude.PSRunner`, backend
`Runner` vars) are preserved — only their default implementations are
platform-split, so the existing test suite is untouched.

## Phases

### Phase 0 — compile on Windows ✅ unblocker
- `internal/app/proc_unix.go` + `proc_windows.go` behind `setDetached` /
  `processAliveImpl`. `auto.go` / `owner_tick.go` call the seam.
- Gate: `GOOS=windows GOARCH=amd64 go build ./...` succeeds.

### Phase 1 — background mode runs on Windows
- `spawner.Detect()` is OS-aware: on Windows it never defaults to iTerm
  (prefers Windows Terminal, honors `$WT_SESSION` / `$FLOW_TERM`).
- `EncodeCwd` handles Windows path chars (`\`, `:`).
- `$EDITOR` defaults to `notepad` on Windows.
- Result: `FLOW_TERM=bg flow do <task>` (and all non-spawn commands)
  work on Windows with Claude Code background agents.

### Phase 2 — interactive Windows Terminal backend
- `internal/winterm`: `SpawnTab` opens a `wt.exe` tab running PowerShell
  with the launch command passed via `-EncodedCommand` (base64 of
  UTF-16LE) — sidesteps all `wt`/PowerShell quoting and newline pitfalls.
- Windows-aware `ShellQuote` (PowerShell `'...''...'` literal quoting).
- `FocusSession` returns `(false, nil)` for now (no per-tab process
  query in `wt`); the caller surfaces the existing "running elsewhere"
  message. Tracked as future work.

### Phase 3 — process-scan parity
- `claude` harness `LiveSessionIDs` reads the process table via
  PowerShell CIM on Windows so the live-session guard and duplicate
  detection work.

### Phase 4 — distribution + scheduler + docs
- CI: add a `GOOS=windows` cross-compile gate and a `windows-latest`
  test job.
- Release: build and publish `flow-windows-amd64.exe` /
  `flow-windows-arm64.exe`.
- Owners scheduler: document Windows Task Scheduler (`schtasks`) in the
  skill alongside launchd/systemd/cron; the macOS Accessibility
  special-case is a no-op on Windows.
- Update scope statements in `README.md`, `CONTRIBUTING.md`, `CLAUDE.md`.

## Open questions (verify on real Windows hardware)

- **`EncodeCwd`**: confirm exactly how Claude Code encodes a Windows cwd
  into `~/.claude/projects/<dir>`. The current guess uniformly maps
  `/ . _ \ :` → `-`. If Claude Code differs, `flow do --here`
  validation and `flow transcript` resolution need the encoder fixed to
  match byte-for-byte.
- **SessionStart hook**: confirm the `hookCommand` string fires from
  Claude Code on Windows. Must NOT be renamed (orphans installs).
- **Live-session guard**: the PowerShell CIM scan works but is slower
  than `ps`; confirm acceptable latency, else gate it behind a flag.
- **`wt.exe` arg parsing**: titles/cwd containing `;` can confuse `wt`'s
  command splitter; confirm real work_dir paths are unaffected.
