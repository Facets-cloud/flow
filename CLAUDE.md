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

## Harness architecture (pluggable coding agents)

flow drives a coding agent through `harness.Harness`
(`internal/harness/harness.go`). Adding an agent should be a **TOML
manifest**, not a code change.

- **Capability accessors return nil when unsupported.** 8 methods are
  required (Name, Binary, SessionIDEnvVar, NewSessionID,
  ValidateSessionID, ValidateSession, LaunchCmd, LiveSessionIDs);
  `Resume() Headless() Transcript() Skills() Hooks() Background()`
  return nil if the harness lacks them, and `Vocab()` always returns a
  populated `Vocabulary`. **Always nil-check and report the gap** —
  never assume claude-level capability.
- **Only `internal/harness/registry` imports a concrete adapter.**
  `internal/app` uses `harness` + `registry` and never names a harness.
  `TestAppLayerNamesNoHarnessInternals` enforces this by AST-scanning
  every non-test file in `internal/app` for string literals containing
  any registered harness's `SessionIDEnvVar()` or `Binary()`. The
  forbidden set comes from the registry, so it covers new harnesses
  automatically. It exempts only `stats.go`, pending the analytics phase.
- **Manifests** live in `$FLOW_ROOT/harnesses/*.toml`, load via
  `internal/harness/spec`, and SHADOW a built-in of the same name (the
  shadowing is reported by `flow harness list`). A bad manifest disables
  only itself. Debug with `flow harness list|show <name>|validate <file>`.
- **Skills reach every harness two ways.** `discovery = "native"` writes
  the tree where the harness already scans; `discovery = "pointer"`
  also injects a marker-delimited block (`internal/harness/managedblock`)
  into the harness's instructions file. `owns_dir = false` means
  uninstall must NOT delete the tree, for a manifest deliberately
  pointed at a directory another harness owns.
- **Point a manifest at the harness's OWN skill dir.** Agents that also
  scan a sibling's directory (praxis reads `~/.claude/skills` and
  `~/.codex/skills` as fallbacks) rank their own first and load
  first-name-wins, so installing into the sibling's tree is silently
  shadowed by anything already in the native one. Praxis's is
  `$PRAXIS_CODING_AGENT_DIR`/`~/.praxis/agent` + `/skills`.
- **Hooks are a strategy ladder**, not a yes/no: `config-patch` (JSON
  config, deterministic, covers ad-hoc sessions), `prompt-prelude`
  (flow-spawned sessions only), `instruction-directive` (rides in the
  pointer block; best-effort). `flow skill install` performs every
  declared strategy for EVERY registered harness — `--harness <name>`
  narrows it.
- **`$FLOW_TERM=bg` falls back to the `--auto` supervisor** for any
  harness without native background. There is no `[background]` table:
  supervised background is derivable from `[headless]`, so declaring it
  would be redundant.
- **Quoting rule:** an argv element is shell-quoted iff its template text
  mentions a runtime data variable (`.SessionID .Prompt .Inject .WorkDir
  .Cwd .Home`). `session.validate` remains required to reject malformed
  ids captured from environment/process output; matches must cover the
  entire id.
- **`testdata/claude.toml` + `claude_equivalence_test.go`** pin the
  generic engine to byte-identical output with the hand-written claude
  adapter. If you change either, that test is the gate.
- **Authoring reference:** `docs/harness-manifest.md` (written so a
  coding agent can read it and produce a manifest). Working examples in
  `examples/harnesses/` — claude, praxis, plus an annotated
  `TEMPLATE.toml`. `TestShippedExamplesAreValid` fails the build if a
  schema change invalidates a published example.
- **Only ship a manifest whose full lifecycle was verified** against a
  running binary, a file on disk, or source. Flow stores the session id
  before launch, so the agent must accept that id at launch (or expose a
  pre-launch allocation command). Codex and OMP self-mint after launch
  and are intentionally not shipped. An invented flag or identity map
  can pass structural validation and then silently target the wrong
  session.
- Design and phasing:
  `docs/superpowers/specs/2026-08-15-pluggable-harness-architecture-design.md`.

## Things to watch out for

- `hookCommand` (SessionStart) and `userPromptSubmitHookCommand` (UserPromptSubmit) in `internal/app/skill.go` are the exact strings matched in `~/.claude/settings.json`. Changing either orphans existing installations.
- `do.go` uses `openConcurrentDB` with `busy_timeout(30000)` and `_txlock=immediate` for safe concurrent access.
- `internal/stats` still imports `internal/harness/claude` directly — the last remaining harness bypass, scheduled for the analytics phase.
- The registry caches its resolution per process. Tests that write manifests into a temp `$FLOW_ROOT` must call `registry.Reload()`.
- **`SKILL.md` frontmatter must be single-line scalars.** Not every harness parses YAML — praxis reads frontmatter with a line-based subset, so `description: |` arrives as the literal `"|"`. The skill still loads and reports itself invocable, it just has a useless catalog entry, and `require_frontmatter` cannot catch it (the key is present, the value is junk). `TestSkillFrontmatterSurvivesLineBasedParsers` guards it.
- **Skill install prunes.** `harness.SyncTree` makes the installed directory equal the embedded tree plus `VERSION`; anything else is deleted. Without it every corpus rename left an orphan on disk forever — flow manufacturing the exact drift it exists to prevent. Both adapters share it, and the claude differential test compares the resulting trees.
- Tests override `$HOME` — any code that calls `os.UserHomeDir()` will see the test's temp dir, not the real home.
