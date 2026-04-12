# flow — repo conventions

## What this is

A Go CLI (`flow`) that manages personal tasks and bootstraps per-task Claude Code sessions. SQLite via `modernc.org/sqlite` (pure Go, no CGO).

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
│   │   ├── register.go              # flow register-session
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
│   └── iterm/                       # iTerm2 tab spawning
│       └── iterm.go
├── Makefile
├── README.md
├── CLAUDE.md
├── .gitignore
├── go.mod
└── go.sum
```

## Package responsibilities

- **`internal/app`** — all CLI command handlers, dispatch, shared helpers. One file per subcommand. Imports `flowdb` and `iterm`.
- **`internal/flowdb`** — schema DDL, model structs (`Project`, `Task`, `Workdir`), scan helpers, CRUD queries, migrations. All DB access via `database/sql` + `modernc.org/sqlite`.
- **`internal/iterm`** — osascript-based iTerm2 tab spawning. Exposes `iterm.Runner` var for test mocking.

## Conventions

- **No CGO.** Pure Go SQLite driver (`modernc.org/sqlite`).
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
