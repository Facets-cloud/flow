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
against the release, and renames it over the binary. The next flow
command refreshes the skill and the hooks. So the
`flow-version-stale:` signal appears only when this automatic upgrade
cannot run or its last attempt failed. The log is
`~/.flow/self-upgrade.log`; the last result is
`~/.flow/.self-upgrade.json`.

**Recipe:**

1. Run `flow --version` to capture the currently-installed version.
2. Run `flow upgrade --check` to see the latest release.
3. Run `flow upgrade`. It downloads the build for this Mac, checks
   its SHA-256, and replaces the binary.
   - If it says the binary is a **development build**, the user built
     flow from source. Ask before you run `flow upgrade --force`,
     which replaces it with the release.
   - If it says flow **cannot write to the folder** of the binary
     (for example, `/usr/local/bin` that root owns), offer to move
     flow to `~/.local/bin`, or use the manual fallback below.
4. Run `flow skill update` to refresh the embedded skill on disk and
   re-wire the supported hooks in the active harness configuration
   (`~/.claude/settings.json` or `~/.codex/hooks.json`). (The next
   `flow` command runs the same refresh, but explicit is better and
   surfaces any errors immediately.)
5. Run `flow --version` again and confirm the version changed. If it
   did not change, the binary on `$PATH` is not the one that was
   replaced — check `which flow`.

**Why flow did not upgrade itself** — tell the user the cause:

- `FLOW_NO_AUTO_UPGRADE` is set (the user turned it off), or `CI` is set.
- The binary is a source build (`make install`, version like
  `v0.1.0-alpha.24-1-g7ad6e4e` or `dev`).
- flow runs as root, or cannot write to the folder of its binary.
- The last attempt failed; flow waits 24 hours before it tries the
  same release again. Read `~/.flow/self-upgrade.log`.

**Manual fallback** (only when `flow upgrade` cannot run): the
canonical procedure lives in the README at
`https://github.com/Facets-cloud/flow`. Read its **Install** section
for the binary download URL and the architecture (`arm64` for Apple
Silicon, `amd64` for Intel). Replace the binary, then run
`xattr -d com.apple.quarantine <path>` on it — Gatekeeper refuses a
downloaded unsigned binary otherwise.

**Anti-patterns:**

- **Do not invent download URLs.** Read them from the README at
  `https://github.com/Facets-cloud/flow`. Releases are at
  `/releases/latest/download/`; the README has the exact form.
- **Do not run `flow skill install` on an existing install** — it
  errors. Use `flow skill update` for the refresh path.
- **Do not run `flow upgrade --force` without asking** — it replaces
  a binary the user built on purpose.
- **Do not set `FLOW_NO_AUTO_UPGRADE`** unless the user asks to stop
  automatic upgrades.
