# Changelog

All notable changes to flow will be documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
(`0.x.y` until the API stabilises).

## [Unreleased]

## [0.1.0-alpha.18] — 2026-06-08

### Added

- **`flow do --auto` / `flow run playbook --auto` — autonomous, headless
  background runs.** Instead of opening a terminal tab for a human to
  drive, `--auto` launches a detached supervisor (`flow __auto-exec`,
  hidden) that runs the task's harness headlessly — pinned to the
  pre-allocated session id, cwd at `work_dir`, stdout/stderr captured to
  `tasks/<slug>/auto-runs/<timestamp>.log` — and returns immediately. The
  session works end to end on best judgment (no `AskUserQuestion`,
  persisting toward a closeable state rather than giving up early) and
  calls `flow done` on **itself** when the brief's *Done when* is met,
  which still fires the close-out KB/project sweep. `--auto` implies
  `--dangerously-skip-permissions` (no human to approve tool calls) and
  accepts `--with` / `--with-file` for a one-off directive layered on the
  brief; `--auto` + `--here` is rejected. The headless-with-session-id
  invocation is a third harness execution shape, added to the harness
  interface as `AutoRunArgv` — claude implements it; codex/gemini inherit
  auto mode by implementing the one method. A per-run status —
  `running → completed | dead`, with read-time pid reconciliation so a
  crashed supervisor surfaces as `dead` — shows in `flow show task` and a
  dedicated `AUTO` column in `flow list tasks` (a running row shows the
  pid). New nullable `tasks` columns: `auto_run_status`, `auto_run_pid`,
  `auto_run_started`, `auto_run_finished`, `auto_run_log` (additive
  migration). Non-blocking hardening follow-ups are tracked in
  [#68](https://github.com/Facets-cloud/flow/issues/68).
  ([#67](https://github.com/Facets-cloud/flow/pull/67) by
  [@anshulsao](https://github.com/anshulsao))

### Changed

- **Hardened iTerm2 tab spawning.** Raises the file-descriptor `ulimit`
  before spawning and uses more robust AppleScript quoting, preventing
  tab-spawn failures under constrained descriptor limits or when the
  command contains characters that tripped the previous quoting.
  ([#64](https://github.com/Facets-cloud/flow/pull/64) by
  [@ishaankalra](https://github.com/ishaankalra))

## [0.1.0-alpha.17] — 2026-05-29

### Fixed

- **`flow transcript` elided pre-bind work on retrospective `--here`
  binds, silently starving the close-out KB sweep.** `flow transcript`
  filtered out every jsonl entry before `tasks.session_started`, on the
  assumption that `session_started` ≈ the conversation's start. That
  holds for `flow do` spawns (the UUID is pre-allocated, so the first
  message lands just after `session_started`) but breaks for a
  retrospective `flow do --here` bind: there `session_started` is
  stamped at *bind time*, after all the real work. The cutoff then
  dropped the entire conversation, so the `flow done` close-out sweep
  mined an empty tail and wrote nothing to the KB — silent knowledge
  loss. Hit live on 2026-05-29 (task `cp-mgmt-skill`), whose whole
  session had to be hand-distilled into the KB. The fix removes the
  time cutoff entirely: `flow transcript` (and therefore the sweep)
  now always renders the full session. An over-inclusive sweep was
  already the documented preference over silent data loss; the
  dispatch-chatter-stripping the cutoff bought for early binds was
  deemed not worth the failure mode. `RenderTranscript`'s `cutoff`
  parameter is gone from the harness interface.
  ([#65](https://github.com/Facets-cloud/flow/pull/65) by
  [@rr0hit](https://github.com/rr0hit))

## [0.1.0-alpha.16] — 2026-05-28

### Fixed

- **Migration silently drops `tasks.harness` on older DBs.** The
  session-invariant table rebuild in `migrateTasksSessionInvariant`
  hardcoded `tasks_new`'s DDL and omitted the `harness` column added
  one statement earlier in the same `runMigrations` pass. DBs
  upgrading from a version that predated the rebuild (e.g.
  `v0.1.0-alpha.4` → `v0.1.0-alpha.15`) had `harness` added by
  `ALTER TABLE` and then silently dropped by the rebuild that
  recreates `tasks` without listing it. Symptom: every subsequent
  `SELECT` using `TaskCols` errored with `no such column: harness`,
  making `flow list tasks` and most other commands unusable after
  upgrading to alpha.15. Fix adds `harness TEXT` to `tasks_new`'s
  DDL and to both column lists in `INSERT INTO tasks_new (...)
  SELECT ... FROM tasks`. Users who already hit the bug recover
  automatically on next upgrade: `columnExists` re-runs the
  `ALTER`, and the rebuild's idempotency guard
  (`strings.Contains(ddl, "session_id IS NOT NULL")`) short-circuits
  because the broken table already carries the CHECK. No data loss
  in either direction — `harness` is nullable with `NULL` = "claude"
  back-compat.
  ([#63](https://github.com/Facets-cloud/flow/pull/63) by
  [@rr0hit](https://github.com/rr0hit))

## [0.1.0-alpha.15] — 2026-05-27

### Added

- **GitHub Pages site.** A static one-page site at
  `https://facets-cloud.github.io/flow/` introducing flow at a glance:
  dark terminal-aesthetic hero, brief-styled (What / Why / Done-when)
  feature sections, four-act demo embedded as MP4 video with poster
  + reduced-motion fallback, inline-SVG diagrams from the README, and
  a one-shot install block. Pure HTML/CSS/vanilla JS, zero build step,
  ~50 KB above the fold. CI workflow (`.github/workflows/pages.yml`)
  force-pushes `site/**` + `docs/demo/**` to a `gh-pages` orphan
  branch on every push to main. **One-time setup after upgrade:**
  Repo Settings → Pages → Source = "Deploy from a branch", Branch =
  `gh-pages` / `/(root)`.
  ([#60](https://github.com/Facets-cloud/flow/pull/60) by
  [@pramodh-ayyappan](https://github.com/pramodh-ayyappan))
- **Pluggable agent harnesses.** Internal refactor that moves
  Claude-specific code behind a 14-method `internal/harness`
  interface, with codex/gemini adapters ready to drop in as one
  line each in `allHarnesses()`. Adds `tasks.harness` (per-task
  adapter pinning; NULL = claude for back-compat) and
  `tasks.session_cwd` (fixes a pre-existing `flow transcript`
  not-found for `--here`-bound sessions started in a directory ≠
  `task.work_dir`; session identity is now keyed on
  `(cwd, session_id)` to match claude's on-disk layout). Ambient
  harness detection probes `$CLAUDE_CODE_SESSION_ID` /
  `$CODEX_THREAD_ID` / `$GEMINI_SESSION_ID`; defaults to claude.
  `flow do --here --force` on a task pinned to a different harness
  switches the pinning alongside the session rebind, with a
  warning that the prior transcript is orphaned. Schema changes
  are additive and nullable — no backfill, no behavior change for
  existing rows.
  ([#58](https://github.com/Facets-cloud/flow/pull/58) by
  [@rr0hit](https://github.com/rr0hit))
- **Focus existing tab on `flow do`.** When the task's `session_id`
  is already running, `flow do <task>` switches to the existing tab
  instead of erroring with the "use `--force`" message. Implemented
  via `spawner.FocusSession` with per-backend implementations across
  iTerm2, Terminal.app, and zellij — each maps the running claude
  PID back to a tab/pane and selects it. Exits 0 with `Already open:
  <slug> — switched to existing tab` on focus; preserves the old
  error on focus miss so `--force` semantics are unchanged. Warns on
  stderr when more than one claude process is detected for the same
  UUID (prior `--force`, or a manual `claude --resume`).
  ([#28](https://github.com/Facets-cloud/flow/pull/28) by
  [@pa](https://github.com/pa))

### Changed

- **README — Claude shell aliases.** New "Optional: Claude shell
  aliases" subsection in Install with two aliases: `claude` →
  `claude --dangerously-skip-permissions --bg` (skip per-tool prompts,
  run in background) and `ca` → `command claude agents` (Claude
  agents-mode shortcut; `command` bypasses the first alias so the
  `agents` subcommand sees a clean argv). Source two lines to enable
  agents mode without typing the flag each time.
  ([#57](https://github.com/Facets-cloud/flow/pull/57) by
  [@rr0hit](https://github.com/rr0hit))

## [0.1.0-alpha.14] — 2026-05-18

### Added

- **`flow do --with` / `--with-file`.** Inject a one-shot instruction
  as the resumed/started session's first user message (prefixed with
  `[via flow do --with]` so the model can distinguish injected from
  typed input). `--with-file <path>` points the session at a file
  (`read instructions at <abs-path>`) instead of embedding contents —
  no size limits. `--with` on a `done` task auto-rolls it back to
  in-progress. Rejected in combination with `--here` (no spawned
  session to inject into). `flow run playbook <slug>` accepts the same
  flags. The lane for nudging parked tasks and feeding ad-hoc
  instructions to scheduled playbook runs without opening the tab.
  ([#50](https://github.com/Facets-cloud/flow/pull/50) by
  [@anshulsao](https://github.com/anshulsao))
- **kitty as a first-class spawn backend.** `flow do` opens new tabs
  in kitty via `kitty @ launch --type tab` when invoked from a kitty
  shell. Requires `allow_remote_control yes` in kitty config.
  ([#37](https://github.com/Facets-cloud/flow/pull/37) by
  [@unni-facets](https://github.com/unni-facets))
- **Warp as a first-class spawn backend.** `flow do` opens new tabs
  in Warp when invoked from a Warp shell (`TERM_PROGRAM=WarpTerminal`).
  Uses `warp://action/new_tab` to open the tab and osascript to
  keystroke a self-deleting bootstrap script, since Warp has no
  AppleScript dictionary or command-running CLI. Requires macOS
  Accessibility for Warp.
  ([#46](https://github.com/Facets-cloud/flow/pull/46) by
  [@swapnildahiphale](https://github.com/swapnildahiphale))
- **Ghostty as a first-class spawn backend.** `flow do` opens new
  tabs in Ghostty when invoked from a Ghostty shell.
  ([#53](https://github.com/Facets-cloud/flow/pull/53) by
  [@cyphernext](https://github.com/cyphernext))
- **`FLOW_TERM` env override.** Set
  `FLOW_TERM=warp|iterm|terminal|zellij|kitty|ghostty` to force a
  specific spawn backend regardless of `$TERM_PROGRAM`. `$ZELLIJ`
  still wins; unrecognized values fall through to `$TERM_PROGRAM`
  detection.
  ([#46](https://github.com/Facets-cloud/flow/pull/46))
- **`flow run playbook --here`.** Bind THIS Claude session to a
  playbook-run task without spawning a new tab — mirrors `flow do
  --here` for run-tasks. Includes a close-out sweep refactor.
  ([#48](https://github.com/Facets-cloud/flow/pull/48) by
  [@vishnukv-facets](https://github.com/vishnukv-facets))

### Changed

- **`flow list` rendering.** Tabwriter-aligned columns,
  `--format json|tsv` for machine-readable output, ANSI color when
  stdout is a TTY.
  ([#44](https://github.com/Facets-cloud/flow/pull/44) by
  [@unni-facets](https://github.com/unni-facets))
- **README wordmark logo.** Theme-aware SVG logo at the top of
  the README.
  ([#35](https://github.com/Facets-cloud/flow/pull/35) by
  [@pa](https://github.com/pa))

### Fixed

- **flowdb concurrent-open race.** `busy_timeout` is now applied at
  `OpenDB` time so concurrent opens don't race the pragma.
  ([#36](https://github.com/Facets-cloud/flow/pull/36) by
  [@pa](https://github.com/pa))
- **e2e spawner override leak.** Pin `spawner.Override` in the e2e
  test so a real kitty tab isn't spawned during CI.
  ([#42](https://github.com/Facets-cloud/flow/pull/42) by
  [@unni-facets](https://github.com/unni-facets))

## [0.1.0-alpha.8] — 2026-05-09

### Added

- **zellij as a first-class spawn backend.** `flow do` now opens new
  tabs inside the current zellij session when `$ZELLIJ` is set, via
  `zellij action new-tab` + `zellij action write-chars`. Behavior is
  unchanged for non-zellij users — selection priority is `$ZELLIJ` →
  `Apple_Terminal` → `iTerm.app` → iTerm-default. Requires zellij ≥
  0.40. Embedded skill (`SKILL.md`) wording neutralized from
  "iTerm tab" to "terminal tab" so it reads correctly across all
  backends; the Terminal.app Accessibility section stays backend-specific.
  ([#21](https://github.com/Facets-cloud/flow/pull/21) by
  [@pa](https://github.com/pa))

## [0.1.0-alpha.7] — 2026-05-09

### Removed

- **UserPromptSubmit hook.** Per-prompt skill nudge in ad-hoc Claude
  sessions retired — the ~200 words of `additionalContext` injected
  on every user prompt cost more in tokens than it returned in
  marginal §4.14 reliability over the SessionStart hook alone. The
  command itself (`flow hook user-prompt-submit`) is now a permanent
  no-op so any stale entry left behind in older `~/.claude/settings.json`
  files doesn't error; both `flow skill install` and the auto-upgrade
  path actively remove the entry, leaving any unrelated user-defined
  hooks in the same event untouched.

## [0.1.0-alpha.6] — 2026-05-08

### Added

- **Free-form tags on tasks.** New `task_tags(task_slug, tag,
  created_at)` table; values normalized lowercase + trimmed. Add via
  `flow update task <ref> --tag <t>` (repeatable, idempotent), remove
  via `--remove-tag`, wipe via `--clear-tags`. Filter task listings via
  `flow list tasks --tag <t>`. Aggregate listing via `flow list tags`
  (distinct tags + per-tag task counts) so tag vocabulary stays
  consistent over time.
- **Assignee on tasks.** `tasks.assignee TEXT`. Set at create time via
  `flow add task --assignee <name>`; post-creation via `flow update
  task --assignee` / `--clear-assignee`. NULL = self (default);
  non-null renders as `[@name]` in list/show output.
- **Live-session detection.** `flow list tasks` and `flow show task`
  mark `[live]` next to tasks whose `session_id` matches a running
  Claude process (parsed from `ps`). `flow do <ref>` refuses to spawn
  a duplicate when the task's session is already running elsewhere;
  `--force` overrides.
- **`flow find-session <marker>`.** Scans
  `~/.claude/projects/*/*.jsonl` for a marker and prints the matching
  session UUID — the reliable in-flight session-ID capture path.
  Errors deterministically on zero or multiple matches.
- **`flow update project <ref> --priority`.** Project priority is now
  editable after creation.
- **Reverse status transitions.** `flow update task <ref> --status
  in-progress` works on `done` tasks, letting `flow do` reopen them.

### Changed

- **`flow update task` is now the canonical lane for all in-place
  field edits.** New flags: `--status`, `--priority`, `--assignee` /
  `--clear-assignee`, `--due-date` / `--clear-due`, `--waiting` /
  `--clear-waiting`, repeatable `--tag` / `--remove-tag`,
  `--clear-tags`. The existing `--session-id` and `--work-dir`
  escape hatches stay.
- **Close-out sweep prompt rewritten with two-tier discipline.** KB
  step is strict — default = write nothing; three bars (durable /
  surprising / future-relevant); distill the essence rather than
  quote-dump (deliberate departure from §4.10 real-time scoop). The
  project-log step is more permissive — narrative is fine when the
  session moved the project forward. Floating-task prompts omit
  project-update concepts entirely.
- **Skill (`SKILL.md`).** New §4.16 (binding an in-flight session to
  a task via marker-grep). New §4.16a (tagging — vocabulary
  discipline rule that says read `flow list tags` before inventing).
  §4.2 intake gets an optional tag step at the end (offers existing
  tags via multi-select AskUserQuestion) and a subtle
  retrospective-capture hint pointing at §4.16. §4.6 waiting
  workflow rewired through `flow update task --waiting`. Cheat sheet
  rewritten.

### Removed

- **Legacy field-setter mini-commands consolidated into
  `flow update task`:** `flow priority`, `flow due`, `flow waiting`,
  `flow assignee`, `flow tag`, `flow tags`. The aggregate listing
  for tags moved to `flow list tags`. One canonical verb for
  in-place edits — fewer commands to learn, no parallel paths for
  the same field.

## [0.1.0-alpha.1] — 2026-05-04

Initial public release.

### Added

- **Tasks and projects.** `flow add task` / `flow add project` with
  interview-driven intake; SQLite metadata at `~/.flow/flow.db`.
- **Knowledge base.** Five markdown files
  (`user`, `org`, `products`, `processes`, `business`) under
  `~/.flow/kb/`, surfaced in every task/project context.
- **Sessions.** `flow do <task>` pre-allocates a session UUID and spawns
  a Claude Code session in a dedicated iTerm tab. Resume with the same
  command.
- **Progress notes.** Append-only markdown logs under each task and
  project (`updates/YYYY-MM-DD-*.md`).
- **Playbooks.** `flow add playbook` + `flow run playbook <slug>` for
  reusable, snapshotted run definitions.
- **Transcripts.** `flow transcript <task>` produces a readable
  conversation transcript from a task's Claude session jsonl.
- **Manual repair.** `flow update task --session-id … --work-dir …` for
  cases when the DB drifts from reality.
- **Embedded skill.** `~/.claude/skills/flow/SKILL.md` — natural-language
  interface to flow commands, installed by `flow init`.
- **SessionStart hook.** Re-injects task brief, updates, and CLAUDE.md
  context on every session resume.
- **`flow --version`.** Build-time `-ldflags '-X main.version=…'`
  populated from `git describe`.
- **Auto skill upgrade.** Released binaries detect a version bump and
  refresh the skill + hook on next invocation; `dev` builds opt out.
- **Prebuilt binaries.** Darwin arm64 + amd64 published on the GitHub
  Releases page.
- **CI.** `.github/workflows/ci.yml` runs `go vet` + `go test ./...`
  against `macos-latest` and `ubuntu-latest`.
- **License.** MIT.

[Unreleased]: https://github.com/Facets-cloud/flow/compare/v0.1.0-alpha.17...HEAD
[0.1.0-alpha.17]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.17
[0.1.0-alpha.16]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.16
[0.1.0-alpha.15]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.15
[0.1.0-alpha.14]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.14
[0.1.0-alpha.8]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.8
[0.1.0-alpha.7]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.7
[0.1.0-alpha.6]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.6
[0.1.0-alpha.1]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.1
