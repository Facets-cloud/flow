# Changelog

All notable changes to flow will be documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
(`0.x.y` until the API stabilises).

## [Unreleased]

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

[Unreleased]: https://github.com/Facets-cloud/flow/compare/v0.1.0-alpha.8...HEAD
[0.1.0-alpha.8]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.8
[0.1.0-alpha.7]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.7
[0.1.0-alpha.6]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.6
[0.1.0-alpha.1]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.1
