# Upgrade flow itself

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.15 Upgrade flow itself

**Triggers:** "update flow", "upgrade flow", "is there a new version
of flow", "new flow version", "what version am I on", "what's the
latest flow", "flow is stale", a bare `flow --version` typed as
command-like input. Also fires when the SessionStart hook reports
`flow-version-stale: <new-version>` in its additionalContext (the
hook does an at-most-once-per-day cached check against GitHub
releases) — when you see that signal, proactively offer the upgrade
via `AskUserQuestion`.

**Recipe (same on macOS and Linux):**

1. Run `flow --version` to capture the currently-installed version,
   and `command -v flow` to see where the binary lives.
2. The canonical install/upgrade procedure is `INSTALL.md` in the repo
   (`https://raw.githubusercontent.com/Facets-cloud/flow/main/INSTALL.md`).
   Read it with `WebFetch`/`Read`; do not invent download URLs. The
   short version: re-running the one-line installer upgrades in place:
   ```
   curl -fsSL https://raw.githubusercontent.com/Facets-cloud/flow/main/install.sh | sh
   ```
   It picks the OS/arch, verifies SHA256, installs to `~/.local/bin`
   (override with `FLOW_INSTALL_DIR`; pin with `FLOW_VERSION=vX.Y.Z`).
3. Pick the lane that matches where the binary lives:
   - `~/.local/bin/flow` (install.sh default) → re-run install.sh.
   - Homebrew (macOS, `brew list flow` works) → `brew upgrade
     facets-cloud/tap/flow`.
   - `/usr/local/bin/flow` or another root-owned path → re-run install.sh
     with `FLOW_INSTALL_DIR=<that dir>`; it needs `sudo`, so say so and
     let the user run it (or move to `~/.local/bin` and fix PATH).
   - Built from source (`flow --version` says `dev`) → `git pull && make
     install` in their checkout.
4. Run `flow skill update` to refresh the embedded skill on disk and
   re-wire the supported hooks for every present harness
   (`~/.claude/settings.json` and/or `$CODEX_HOME/hooks.json`, default
   `~/.codex`; `--harness claude|codex` limits it to one). (The auto-upgrade path runs the same
   refresh on the next `flow` invocation, but explicit is better and
   surfaces any errors immediately.)
5. Run `flow --version` again and confirm the version changed. If it
   did not change, the binary on `$PATH` is still the old one —
   check `command -v flow` against the path you wrote to.

**Platform notes:**

- macOS: install.sh and the Homebrew formula clear Gatekeeper's quarantine
  attribute themselves. Only a hand-downloaded binary needs
  `xattr -d com.apple.quarantine <path>` before it will run.
- Linux: there is no quarantine step. Nothing else differs.

**Anti-patterns:**

- **Do not invent download URLs.** Read INSTALL.md (or the README) at
  `https://github.com/Facets-cloud/flow`. Releases are at
  `/releases/latest/download/`; install.sh already knows the exact form.
- **Do not run `flow skill install` on an existing install** — it
  errors. Use `flow skill update` for the refresh path.
- **Do not reach for `sudo` by default.** The default install dir is
  user-writable; sudo is only for a root-owned `FLOW_INSTALL_DIR`.
- **Do not run `xattr` on Linux** (it is not there) and do not skip it
  on a hand-downloaded macOS binary.
