# flow — repo conventions

## What this is

A Go CLI (`flow`) that manages personal tasks and bootstraps per-task Claude Code sessions. SQLite via `modernc.org/sqlite` (pure Go, no CGO).

Runs on **macOS, Windows, and Linux**. Platform-specific behavior is isolated behind `//go:build` seams (see "Platform support" below); the rest of the code is OS-neutral. Because there's no CGO, every target cross-compiles from any host (`GOOS=windows go build ./...`).

## Build and test

```bash
# Build (produces ./flow in the repo dir, which is on PATH)
make build
# or: go build -o flow .

# Full install (build + PATH + init + skill + hook)
make install

# Run all tests (fast — no network, no real iTerm/Claude)
make test
# or: go test ./...

# Run a single test
go test -run TestE2EFullRoundtrip -v ./internal/app/
```

Tests use `$FLOW_ROOT` pointed at a temp directory and override `$HOME` so nothing touches real `~/.flow/` or `~/.claude/`. External dependencies (osascript, claude CLI) are mocked via package-level function vars.

## Project structure

```
flow/
├── main.go                          # thin entry point — calls app.Run()
├── internal/
│   ├── app/                         # CLI commands and dispatch
│   │   ├── app.go                   # Run(), printUsage()
│   │   ├── helpers.go               # flagSet()
│   │   ├── add.go                   # flow add project|task
│   │   ├── archive.go               # flow archive|unarchive
│   │   ├── do.go                    # flow do — session spawner
│   │   ├── done.go                  # flow done
│   │   ├── due.go                   # flow due
│   │   ├── edit.go                  # flow edit
│   │   ├── hook.go                  # flow hook session-start
│   │   ├── init.go                  # flow init, flowRoot(), kbSeeds()
│   │   ├── list.go                  # flow list tasks|projects
│   │   ├── priority.go              # flow priority
│   │   ├── show.go                  # flow show task|project
│   │   ├── skill.go                 # flow skill install|uninstall|update
│   │   ├── transcript.go            # flow transcript — session jsonl reader
│   │   ├── waiting.go               # flow waiting
│   │   ├── workdir.go               # flow workdir
│   │   ├── bootstrap.go             # UUID gen, session file scanning
│   │   ├── resolve.go               # task/project slug resolution
│   │   ├── slug.go                  # name-to-slug conversion
│   │   ├── skill/SKILL.md           # embedded skill (//go:embed)
│   │   └── *_test.go
│   ├── flowdb/                      # SQLite data layer
│   │   ├── db.go                    # schema, models, CRUD queries
│   │   └── db_test.go
│   ├── iterm/                       # iTerm2 tab spawning
│   │   └── iterm.go
│   ├── terminal/                    # macOS Terminal.app tab spawning
│   │   └── terminal.go
│   ├── warp/                        # Warp tab spawning (warp:// URI + osascript keystroke)
│   │   └── warp.go
│   ├── zellij/                      # zellij tab spawning
│   │   └── zellij.go
│   └── spawner/                     # backend selection + dispatch
│       └── spawner.go
├── Makefile
├── README.md
├── CLAUDE.md
├── .gitignore
├── go.mod
└── go.sum
```

## Package responsibilities

- **`internal/app`** — all CLI command handlers, dispatch, shared helpers. One file per subcommand. Imports `flowdb` and `spawner`.
- **`internal/flowdb`** — schema DDL, model structs (`Project`, `Task`, `Workdir`), scan helpers, CRUD queries, migrations. All DB access via `database/sql` + `modernc.org/sqlite`.
- **`internal/spawner`** — picks a terminal backend at runtime (`$ZELLIJ` > `$FLOW_TERM` > `$TERM_PROGRAM` > historical iTerm default) and forwards `SpawnTab` to it. Exposes `Override` for test pinning.
- **`internal/iterm`** — osascript-based iTerm2 tab spawning. Exposes `iterm.Runner` for test mocking.
- **`internal/terminal`** — osascript-based macOS Terminal.app tab spawning. Requires Accessibility for the cmd-T keystroke via System Events.
- **`internal/warp`** — Warp tab spawning via `warp://action/new_tab` URI + osascript keystroke of a self-deleting per-spawn shell script. Exposes `warp.Runner`, `warp.OpenURL`, `warp.WriteScript` for test mocking. Requires Accessibility (same gate as Terminal.app).
- **`internal/zellij`** — zellij CLI–based tab spawning. Active when `$ZELLIJ` is set in the environment.

## Platform support

flow runs on macOS, Windows, and Linux. OS-specific behavior is isolated behind `//go:build` constraints so the bulk of the code stays platform-neutral and every target cross-compiles (`GOOS=windows go build ./...`, gated in CI). The seams:

- **`internal/app/proc_{unix,windows}.go`** — `setDetached(cmd)` (detach a child for `--auto` / owner ticks: `Setsid` on Unix, `CREATE_NEW_PROCESS_GROUP|DETACHED_PROCESS` on Windows) and `processAliveImpl(pid)` (signal-0 vs. `OpenProcess`+`GetExitCodeProcess`). `processAlive` stays a package var so tests override it.
- **`internal/harness/claude/ps_{unix,windows}.go`** — `runPS()` feeds `LiveSessionIDs`: `ps -axo` on Unix, a `Get-CimInstance Win32_Process` PowerShell query on Windows.
- **`internal/spawner/shellquote_{unix,windows}.go`** — `ShellQuote` for the shell the spawned tab runs: POSIX single-quote on Unix, PowerShell single-quote on Windows.
- **`internal/winterm`** — the Windows Terminal (`wt.exe`) backend. Passes the launch command via PowerShell `-EncodedCommand` (base64/UTF-16LE) to avoid `wt`/PowerShell quoting pitfalls. `spawner.Detect()` defaults to it on Windows (never iTerm).
- `EncodeCwd` (`harness/claude/claude.go`) maps Windows path chars (`\`, `:`) — **pending verification** against Claude Code's real Windows encoding (see `docs/windows-support-plan.md`).
- Windows-only dep: `golang.org/x/sys/windows` (used only in `proc_windows.go`).

The macOS-only backends (iterm, terminal, warp, ghostty) and the macOS Accessibility error path are never selected on Windows; they compile everywhere but only run on macOS.

## Conventions

- **No CGO.** Pure Go SQLite driver (`modernc.org/sqlite`).
- **Platform seams via build tags**, not `runtime.GOOS` scattered through logic. New OS-specific behavior goes in a `*_unix.go` / `*_windows.go` pair behind a neutral function, mirroring the seams above. Keep the overridable test `var` (e.g. `processAlive`, `PSRunner`) in the shared file so tests don't need per-OS stubs.
- **Flag parsing:** `flag.FlagSet` with `ContinueOnError`, not `flag.Parse()`. Created via `flagSet()` helper in `internal/app/helpers.go`.
- **Exit codes:** 0 = success, 1 = runtime error, 2 = usage error.
- **Timestamps:** RFC3339 strings everywhere (never Unix timestamps).
- **Tests:** Table-driven where possible. Command tests live alongside source in `internal/app/`. `e2e_test.go` exercises the full command surface in sequence.
- **No mocks for DB.** Tests use real SQLite in a temp directory. Only osascript is mocked (via `iterm.Runner` function var).
- **Skill file is the source of truth** for how Claude sessions interact with flow. If the skill says something, the code must support it.
- **Skill embed path:** `internal/app/skill/SKILL.md` is embedded at compile time via `//go:embed` in `internal/app/skill.go`. After editing, rebuild for `flow skill update` to pick up changes.

## Data directory layout

```
~/.flow/
  flow.db
  kb/{user,org,products,processes,business}.md
  projects/<slug>/brief.md
  projects/<slug>/updates/*.md
  tasks/<slug>/brief.md
  tasks/<slug>/updates/*.md
```

## Things to watch out for

- `hookCommand` in `internal/app/skill.go` is the exact string matched in `~/.claude/settings.json`. Changing it orphans existing installations.
- `do.go` uses `openConcurrentDB` with `busy_timeout(30000)` and `_txlock=immediate` for safe concurrent access.
- Tests override `$HOME` — any code that calls `os.UserHomeDir()` will see the test's temp dir, not the real home.
