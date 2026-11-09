# What `flow init` sets up

`flow init` is the step that wires flow into your coding agents. Without it,
your agent can't talk to flow. It:

- Creates `~/.flow/` (database, knowledge base, projects, tasks, playbooks).
  Set `$FLOW_ROOT` to put it somewhere else.
- Installs for **every agent it finds**, so someone who uses Claude Code and
  also runs Codex gets both. An agent counts as present when its binary is on
  `PATH` or its config home exists (`~/.claude`; `$CODEX_HOME`, else
  `~/.codex`). If none is found, it installs for Claude Code.
- Writes the flow skill to `~/.claude/skills/flow/` and/or
  `$CODEX_HOME/skills/flow/`. The skill is what lets you talk to flow in plain
  English ("what should I work on", "resume auth", "save a note").
- Adds the SessionStart, UserPromptSubmit, PreToolUse and Stop hooks to each
  agent's config (`~/.claude/settings.json`, `$CODEX_HOME/hooks.json`), so
  every new session loads the skill and the task's context on its own.

`flow init` and `flow skill install|update|uninstall` all take
`--harness claude|codex` to act on one agent only. A named agent is installed
even if it isn't detected yet (you asked for it). Uninstall without
`--harness` removes the skill from every agent that has it. The automatic
skill refresh after a binary upgrade only touches agents that already have
the skill, so removing it from one keeps it removed.

## About the unsigned binary

Release binaries are not code-signed yet, so macOS Gatekeeper quarantines a
downloaded copy and refuses to run it. The install steps remove the
quarantine attribute with `xattr -d com.apple.quarantine`. Each release
publishes a `SHA256SUMS` file you can check the download against:

```bash
curl -fsSL -O https://github.com/Facets-cloud/flow/releases/latest/download/SHA256SUMS
shasum -a 256 /usr/local/bin/flow     # compare with the flow-darwin-<arch> line
```

Signing is on the roadmap. You can also build from source (`make build`, see
[CONTRIBUTING.md](../CONTRIBUTING.md)).

Back to the [README](../README.md).
