# Upgrade flow itself

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.15 Upgrade flow itself

**Triggers:** "update flow", "upgrade flow", "is there a new version
of flow", "new flow version", "what version am I on", "what's the
latest flow", "flow is stale", a bare `flow --version` typed as
command-like input. Also fires when the SessionStart hook reports
`flow-version-stale: <new-version>` in its additionalContext — when
you see that signal, proactively offer the upgrade via
`AskUserQuestion`.

**How flow upgrades itself.** The SessionStart hook checks GitHub
releases at most once a day. When a newer release exists, flow
downloads it in the background after a command, checks its SHA-256
against the release, runs it with `--version` (it must print the
release tag), keeps the old binary beside it as `flow.prev`, and
renames the new one over the binary. The next flow
command refreshes the skill and the hooks. So the
`flow-version-stale:` signal appears only when this automatic upgrade
cannot run or its last attempt failed. The log is
`~/.flow/self-upgrade.log`; the last result is
`~/.flow/.self-upgrade.json`.

**Recipe (same on macOS and Linux):**

1. Run `flow --version` to capture the currently-installed version,
   and `command -v flow` to see where the binary lives.
2. Run `flow upgrade --check` to see the latest release, then
   `flow upgrade`. It downloads the build for this OS/arch, checks its
   SHA-256, and replaces the binary.
   - If it says the binary is a **development build**, the user built
     flow from source. Ask before you run `flow upgrade --force`,
     which replaces it with the release (or `git pull && make install`
     in their checkout, if they want to stay on source).
   - If it says flow **cannot write to the folder** of the binary
     (for example, `/usr/local/bin` that root owns), use the
     install.sh fallback below.
3. **install.sh fallback.** The canonical install/upgrade procedure is
   `INSTALL.md` in the repo
   (`https://raw.githubusercontent.com/Facets-cloud/flow/main/INSTALL.md`).
   Read it with `WebFetch`/`Read`; do not invent download URLs. The
   short version: re-running the one-line installer upgrades in place:
   ```
   curl -fsSL https://raw.githubusercontent.com/Facets-cloud/flow/main/install.sh | sh
   ```
   It picks the OS/arch, verifies SHA256, installs to `~/.local/bin`
   (override with `FLOW_INSTALL_DIR`; pin with `FLOW_VERSION=vX.Y.Z`).
   For `/usr/local/bin/flow` or another root-owned path, re-run it
   with `FLOW_INSTALL_DIR=<that dir>`; it needs `sudo`, so say so and
   let the user run it (or move to `~/.local/bin` and fix PATH, which
   also lets flow upgrade itself from then on).
4. Run `flow skill update` to refresh the embedded skill on disk and
   re-wire the supported hooks for every present harness
   (`~/.claude/settings.json` and/or `$CODEX_HOME/hooks.json`, default
   `~/.codex`; `--harness claude|codex` limits it to one). (The auto-upgrade path runs the same
   refresh on the next `flow` invocation, but explicit is better and
   surfaces any errors immediately.)
5. Run `flow --version` again and confirm the version changed. If it
   did not change, the binary on `$PATH` is still the old one —
   check `command -v flow` against the path you wrote to.

**Why flow did not upgrade itself** — tell the user the cause:

- `FLOW_NO_AUTO_UPGRADE` is set (the user turned it off), or `CI` is set.
- The binary is a source build (`make install`, version like
  `v0.1.0-alpha.24-1-g7ad6e4e` or `dev`).
- flow runs as root, or cannot write to the folder of its binary.
- It runs inside the Codex sandbox (`CODEX_SANDBOX` is set) or inside a
  headless auto run (`FLOW_AUTO_RUN` is set); the next ordinary command
  upgrades.
- The last attempt failed (no network, a GitHub rate limit, a bad
  checksum, or a download that does not run or reports another
  version); flow waits 24 hours before it tries the same release
  again. Read `~/.flow/self-upgrade.log`.

**Roll back an upgrade.** Every upgrade (automatic or `flow upgrade`)
keeps the binary it replaced as `flow.prev` in the same folder. If the
new version misbehaves, put the old one back and stop automatic
upgrades until a fixed release exists:
```
mv "$(dirname "$(command -v flow)")/flow.prev" "$(command -v flow)"
export FLOW_NO_AUTO_UPGRADE=1   # in the shell profile, until the fix
flow --version && flow skill update
```
Ask before you roll back; tell the user to remove
`FLOW_NO_AUTO_UPGRADE` once a newer release is out.

**Trust model.** The SHA-256 comes from the same GitHub release as the
binary, so it proves the download is intact, not who built it. That is
the same trust install.sh has.

**Platform notes:**

- macOS: `flow upgrade` and install.sh do not leave a quarantine
  attribute. Only a hand-downloaded binary needs
  `xattr -d com.apple.quarantine <path>` before it will run.
- Linux: there is no quarantine step. Nothing else differs.

**Anti-patterns:**

- **Do not invent download URLs.** Read INSTALL.md (or the README) at
  `https://github.com/Facets-cloud/flow`. Releases are at
  `/releases/latest/download/`; install.sh already knows the exact form.
- **Do not run `flow skill install` on an existing install** — it
  errors. Use `flow skill update` for the refresh path.
- **Do not run `flow upgrade --force` without asking** — it replaces
  a binary the user built on purpose.
- **Do not set `FLOW_NO_AUTO_UPGRADE`** unless the user asks to stop
  automatic upgrades.
- **Do not reach for `sudo` by default.** The default install dir is
  user-writable; sudo is only for a root-owned `FLOW_INSTALL_DIR`.
- **Do not run `xattr` on Linux** (it is not there) and do not skip it
  on a hand-downloaded macOS binary.
