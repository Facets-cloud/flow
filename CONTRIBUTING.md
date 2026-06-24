# Contributing to flow

Thanks for your interest in flow. This is a small, opinionated CLI; the
contribution surface is correspondingly small. Bug reports, fixes, and
focused improvements are very welcome.

## Quick start

```bash
git clone https://github.com/Facets-cloud/flow.git
cd flow
make build      # produces ./flow in the repo dir
make test       # runs go test ./...
```

You need Go 1.25+. No CGO toolchain required — flow uses
[`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), a pure-Go
SQLite driver.

For day-to-day hacking:

```bash
make install    # builds, places binary in ~/.local/bin, installs
                # the embedded skill + SessionStart hook
```

After editing the embedded skill (`internal/app/skill/SKILL.md`), rebuild
and re-install so `flow skill update` picks up the new content:

```bash
make build && flow skill update
```

## Repo conventions

Read [`CLAUDE.md`](./CLAUDE.md) — it's the source of truth for repo
structure, package responsibilities, and conventions. Highlights:

- **No CGO.** Stay on `modernc.org/sqlite`.
- **Flag parsing.** Use the `flagSet()` helper in
  `internal/app/helpers.go` (a `flag.FlagSet` with `ContinueOnError`).
  Never call `flag.Parse()` directly.
- **Timestamps.** RFC3339 strings everywhere, never Unix timestamps.
- **Exit codes.** 0 = success, 1 = runtime error, 2 = usage error.
- **Tests.** Real SQLite in a temp directory; don't mock the database.
  Only `iterm.Runner` and the `claude` CLI are mocked (via
  package-level function vars).
- **Skill is load-bearing.** The embedded skill at
  `internal/app/skill/SKILL.md` is the contract between flow and Claude
  sessions. If you change behavior the skill describes, update both.

### Watch out for `hookCommand`

`hookCommand` in `internal/app/skill.go` is the literal string matched
in `~/.claude/settings.json`. Renaming or rewording it orphans every
existing installation — they'll keep firing the old hook string forever
because nothing migrates it. Treat changes here as a breaking change
and call them out explicitly in the PR description.

## Filing issues

Use the GitHub issue templates. For bugs include `flow --version`, your
macOS version, and a minimal reproduction. flow is fully local, so most
bugs reproduce deterministically with a small command sequence.

## Pull requests

- Branch off `main`, keep PRs focused — one logical change per PR.
- Run `make test` before pushing. CI (`go vet`, `go test ./...`, build
  on `macos-latest` and `ubuntu-latest`) must pass.
- If you change the skill's behavior, edit
  `internal/app/skill/SKILL.md` in the same PR — that's where Claude
  sessions look to know what to do.
- Use a clear commit message subject: `feat:`, `fix:`, `chore:`,
  `docs:`, `test:`. Body explains the *why*.

## Scope

flow targets **macOS, Windows, and Linux** with **Claude Code** as the
agent harness:

- macOS: iTerm2, Warp, stock Terminal.app, kitty, Ghostty, zellij.
- Windows: Windows Terminal (`wt.exe`).
- Linux: zellij or kitty.
- Any platform: `FLOW_TERM=bg` (Claude Code background agents, no terminal).

Platform-specific behavior lives behind `//go:build` seams — see
`docs/windows-support-plan.md` for the pattern (`proc_*`, `ps_*`,
`shellquote_*`, and the `internal/winterm` backend). Because flow is
pure Go (no CGO), every target cross-compiles from any host; CI gates
this on every PR.

Non-Claude harnesses (Codex, Cursor, plain shell) and other terminals
(tmux/wezterm, ConEmu) are still out of scope for the current motion.
If you have a use case that would change that, open an issue first to
discuss before sending code.
