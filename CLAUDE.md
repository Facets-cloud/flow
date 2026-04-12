# flow — repo conventions

## What this is

A Go CLI (`flow`) that manages personal tasks and bootstraps per-task Claude Code sessions. Single-package `main` in the repo root. SQLite via `modernc.org/sqlite` (pure Go, no CGO).

## Build and test

```bash
# Build
go build -o ~/.flow/bin/flow .

# Run all tests (fast — no network, no real iTerm/Claude)
go test ./...

# Run a single test
go test -run TestE2EFullRoundtrip -v
```

Tests use `$FLOW_ROOT` pointed at a temp directory and override `$HOME` so nothing touches real `~/.flow/` or `~/.claude/`. External dependencies (osascript, claude CLI) are mocked via package-level function vars.

## Architecture

- **Single package `main`** — no internal packages. Every `cmd_*.go` file implements one top-level subcommand.
- **`db.go`** — schema DDL, struct definitions (Project, Task, Workdir), scan helpers, CRUD queries. All DB access goes through `database/sql` with `modernc.org/sqlite`.
- **`cmd_init.go`** — `flowRoot()` determines the data directory (`$FLOW_ROOT` or `~/.flow`). `cmdInit` creates the directory tree, seeds KB files, initializes the DB, and installs the skill.
- **`cmd_do.go`** — the session spawner. Flips task to in-progress, decides fresh-bootstrap vs resume, calls `SpawnITermTab`.
- **`cmd_skill.go`** — skill install/uninstall/update + SessionStart hook management in `~/.claude/settings.json`.
- **`skill/SKILL.md`** — embedded into the binary via `//go:embed`. This is the Claude Code skill file that gets installed to `~/.claude/skills/flow/SKILL.md`.
- **`bootstrap.go`** — UUID generation, `EncodeCwdForClaude`, `FindNewestSessionFile`.
- **`iterm.go`** — osascript-based iTerm2 tab spawning.
- **`resolve.go`** — task/project slug resolution (exact match only).
- **`slug.go`** — name-to-slug conversion.

## Conventions

- **No CGO.** Pure Go SQLite driver (`modernc.org/sqlite`).
- **Flag parsing:** `flag.FlagSet` with `ContinueOnError`, not `flag.Parse()`. Created via `flagSet()` helper.
- **Exit codes:** 0 = success, 1 = runtime error, 2 = usage error.
- **Timestamps:** RFC3339 strings everywhere (never Unix timestamps).
- **Tests:** Table-driven where possible. Every `cmd_*.go` has a `cmd_*_test.go`. `e2e_test.go` exercises the full command surface in sequence.
- **No mocks for DB.** Tests use real SQLite in a temp directory. Only osascript and claude CLI are mocked (via function vars `osascriptRunner`).
- **Skill file is the source of truth** for how Claude sessions interact with flow. If the skill says something, the code must support it.

## Data directory layout

```
~/.flow/
  flow.db
  bin/flow
  kb/{user,org,products,processes,business}.md
  projects/<slug>/brief.md
  projects/<slug>/updates/*.md
  tasks/<slug>/brief.md
  tasks/<slug>/updates/*.md
```

## Things to watch out for

- `hookCommand` in `cmd_skill.go` is the exact string matched in `~/.claude/settings.json`. Changing it orphans existing installations.
- `cmd_do.go` uses `openConcurrentDB` with `busy_timeout(30000)` and `_txlock=immediate` for safe concurrent access.
- The skill file (`skill/SKILL.md`) is embedded at compile time. After editing it, you must rebuild for `flow skill update` to pick up changes.
- Tests override `$HOME` — any code that calls `os.UserHomeDir()` will see the test's temp dir, not the real home.
