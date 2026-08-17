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

**Recipe:**

1. Run `flow --version` to capture the currently-installed version.
2. The canonical install/upgrade procedure lives in the README at
   `https://github.com/Facets-cloud/flow`. Use the `Read` tool /
   `WebFetch` to read the **Install** and **Upgrade** sections —
   they're the source of truth for the binary download URL,
   architecture flag (`arm64` for Apple Silicon, `amd64` for Intel),
   and the `xattr -d com.apple.quarantine` workaround for unsigned
   binaries. Do not invent download URLs; read them from the README.
3. Download the new binary per the README and replace the existing
   one (typically at `/usr/local/bin/flow`; confirm with
   `which flow` if unsure).
4. Run `flow skill update` to refresh the embedded skill on disk and
   re-wire the supported hooks in the active harness configuration
   (`~/.claude/settings.json` or `~/.codex/hooks.json`). (The auto-upgrade path runs the same
   refresh on the next `flow` invocation, but explicit is better and
   surfaces any errors immediately.)
5. Run `flow --version` again and confirm the version changed. If it
   did not change, the binary on `$PATH` is still the old one —
   check `which flow` against the path you wrote to.

**Anti-patterns:**

- **Do not invent download URLs.** Read them from the README at
  `https://github.com/Facets-cloud/flow`. Releases are at
  `/releases/latest/download/`; the README has the exact form.
- **Do not run `flow skill install` on an existing install** — it
  errors. Use `flow skill update` for the refresh path.
- **Do not skip the `xattr -d com.apple.quarantine`** step on a
  freshly-downloaded binary — Gatekeeper will refuse to run it
  otherwise.
