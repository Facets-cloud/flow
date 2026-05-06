# Changelog

All notable changes to flow will be documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
(`0.x.y` until the API stabilises).

## [Unreleased]

### Added

- `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `CHANGELOG.md`,
  GitHub issue + PR templates.
- README polish: tagline, ICP framing, Claude Code link, "flow is /
  isn't" section, persona examples, asciinema placeholder.

### Changed

- `make install` now copies the binary to `~/.local/bin/flow` instead of
  adding the repo dir to PATH; `make uninstall` is symmetric.
- `make install` prompts before modifying your shell rc file.
- README `Build from source` instructions use HTTPS, not SSH.

### Removed

- Internal planning docs (`docs/plans/`, `docs/specs/`).

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

[Unreleased]: https://github.com/Facets-cloud/flow/compare/v0.1.0-alpha.1...HEAD
[0.1.0-alpha.1]: https://github.com/Facets-cloud/flow/releases/tag/v0.1.0-alpha.1
