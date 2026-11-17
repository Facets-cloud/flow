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
│   │   ├── show.go                  # flow show task|project
│   │   ├── skill.go                 # flow skill install|uninstall|update
│   │   ├── transcript.go            # flow transcript — session jsonl reader
│   │   ├── recall.go                # flow recall — lookback CLI (bound/slug/text/--prompt, train, eval)
│   │   ├── recallindex.go           # read-only flow.db scan + fingerprint + hydrate for recall
│   │   ├── matchcache.go            # on-disk per-task index cache ($FLOW_ROOT/match/)
│   │   ├── waiting.go               # flow waiting
│   │   ├── workdir.go               # flow workdir
│   │   ├── bootstrap.go             # UUID gen, session file scanning
│   │   ├── resolve.go               # task/project slug resolution
│   │   ├── slug.go                  # name-to-slug conversion
│   │   ├── skill/SKILL.md           # embedded lean skill core (//go:embed skill)
│   │   ├── skill/references/*.md     # on-demand workflow references (embedded)
│   │   └── *_test.go
│   ├── flowdb/                      # SQLite data layer
│   │   ├── db.go                    # schema, models, CRUD queries
│   │   └── db_test.go
│   ├── match/                       # recall ranking engine (see doc.go for the pipeline)
│   │   ├── doc.go                   # package doc — the L0-L4 pipeline + ASCII diagram
│   │   ├── pairs.go                 # QueryRec / Pair / PairSet scoring primitives
│   │   ├── model.go                 # BM25/cosine term index (the shipped index)
│   │   ├── lsa.go / lsa_scorer.go   # LSA latent space + the bm25+0.25lsa fusion scorer
│   │   ├── idcard.go                # identity-card precision signal (fused into L2)
│   │   ├── related.go / prompt.go   # the L0-L4 pipeline: slug, free-text and prompt modes
│   │   ├── confidence.go            # L3 calibrated confidence model (strong/possible cut)
│   │   ├── calibrate.go             # train --calibrate: refit the confidence model on user links
│   │   ├── lookback.go / *_report.go# the held-out eval behind `flow recall eval`
│   │   └── text.go                  # tokenization + prompt cleaning
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
- **`internal/match`** — the `flow recall` ranking engine: a layered lookback pipeline (L0 filters → L1 candidates → L2 `bm25+0.25lsa+card` re-rank → L3 calibrated confidence → L4 one-liners) learned from the user's own flow history. Reads flow state read-only (via `internal/app`'s scan/hydrate); the only writes are the on-disk index cache and the per-user calibration under `$FLOW_ROOT/match/`. Pure Go, no network. The pipeline and the kept/dropped experiment ledger are documented in `internal/match/doc.go` and `docs/recall.md`. Only the shipped path lives here; every experiment that lost stays on the `exp/*` branches.
- **`internal/spawner`** — picks a terminal backend at runtime (`$ZELLIJ` > `$FLOW_TERM` > `$TERM_PROGRAM` > historical iTerm default) and forwards `SpawnTab` to it. Exposes `Override` for test pinning.
- **`internal/iterm`** — osascript-based iTerm2 tab spawning. Exposes `iterm.Runner` for test mocking.
- **`internal/terminal`** — osascript-based macOS Terminal.app tab spawning. Requires Accessibility for the cmd-T keystroke via System Events.
- **`internal/warp`** — Warp tab spawning via `warp://action/new_tab` URI + osascript keystroke of a self-deleting per-spawn shell script. Exposes `warp.Runner`, `warp.OpenURL`, `warp.WriteScript` for test mocking. Requires Accessibility (same gate as Terminal.app).
- **`internal/zellij`** — zellij CLI–based tab spawning. Active when `$ZELLIJ` is set in the environment.

## Conventions

- **No CGO.** Pure Go SQLite driver (`modernc.org/sqlite`).
- **Flag parsing:** `flag.FlagSet` with `ContinueOnError`, not `flag.Parse()`. Created via `flagSet()` helper in `internal/app/helpers.go`.
- **Exit codes:** 0 = success, 1 = runtime error, 2 = usage error.
- **Timestamps:** RFC3339 strings everywhere (never Unix timestamps).
- **Tests:** Table-driven where possible. Command tests live alongside source in `internal/app/`. `e2e_test.go` exercises the full command surface in sequence.
- **No mocks for DB.** Tests use real SQLite in a temp directory. Only osascript is mocked (via `iterm.Runner` function var).
- **Skill file is the source of truth** for how Claude sessions interact with flow. If the skill says something, the code must support it.
- **Skill embed path:** the entire `internal/app/skill/` directory — the lean resident `SKILL.md` plus `references/*.md` — is embedded at compile time via `//go:embed skill` (an `embed.FS`) in `internal/app/skill.go`. `Harness.InstallSkill(fs.FS)` walks the tree and writes it under `~/.claude/skills/flow/`, so `SKILL.md` lands next to `references/`. After editing any of them, rebuild for `flow skill update` to pick up changes.
- **Progressive disclosure / lean core:** `SKILL.md` is loaded resident by the Skill tool and re-billed as cache on every turn, so it's the dominant token cost — keep it lean. Rarely-needed workflows live in `references/*.md` (loaded on demand via Read), each reachable from a one-line trigger in `SKILL.md`. `TestSkillCoreIsLean` caps `SKILL.md` size; `TestSkillReferencesAreReachable` forbids orphan references; content assertions run against the full corpus (`skillCorpus`) so relocating a section never loses it. Move new low-frequency workflows into `references/`, not the core.
- **Upgrade notes:** a PR that changes agent-facing behaviour (hook context, skill workflows, bus etiquette, harness rules) adds a bullet under `## Unreleased` in `internal/app/skill/references/upgrade-notes.md`; rename the heading to the tag at release. Running sessions are pointed there by the one-time `[flow-upgrade]` hook notice (`internal/app/upgradenotice.go`).

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

- `hookCommand` (SessionStart) and `userPromptSubmitHookCommand` (UserPromptSubmit) in `internal/app/skill.go` are the exact strings matched in `~/.claude/settings.json`. Changing either orphans existing installations.
- `do.go` uses `openConcurrentDB` with `busy_timeout(30000)` and `_txlock=immediate` for safe concurrent access.
- Tests override `$HOME` — any code that calls `os.UserHomeDir()` will see the test's temp dir, not the real home.
