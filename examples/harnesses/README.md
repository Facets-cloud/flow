# Example harness manifests

Drop one of these in `~/.flow/harnesses/` and flow can drive that coding
agent — no rebuild, no code change.

```bash
flow harness validate ./codex.toml     # check before installing
cp codex.toml ~/.flow/harnesses/
flow harness list                      # confirm it loaded
flow harness show codex                # capabilities + rendered commands
flow skill install --harness codex     # install the skill, wire the hooks
```

Schema reference: [`docs/harness-manifest.md`](../../docs/harness-manifest.md).
Starting from scratch: [`TEMPLATE.toml`](TEMPLATE.toml).

## What is here

| Manifest | Verified against | Notes |
|---|---|---|
| [`claude.toml`](claude.toml) | the built-in adapter, by a byte-equality test | Reference example. Claude is built in — you only need this to *override* it. |
| [`praxis.toml`](praxis.toml) | praxis-harness source | Full capability set: resume, transcript, native skills, config-patch hooks. |
| [`codex.toml`](codex.toml) | `codex-cli 0.146.0` help + its real `hooks.json` and session files | Pointer-discovery skills (codex has no skills dir); hooks share Claude Code's entry shape. |
| [`omp.toml`](omp.toml) | `omp v17.2.10` help + its real session files | Native skills; no hook config to patch, so it uses `prompt-prelude`. |

Every field in these was checked against a running binary, a real file
on disk, or source. Where something could **not** be established it is
either left out or carries a comment saying so — see `codex.toml`'s
`[transcript.map]`, which omits tool-call fields because no tool-call
record existed to confirm the discriminator.

## Agents not covered here

Manifests are only included when their fields were actually verified. A
plausible-looking manifest with an invented flag is worse than none: it
passes `flow harness validate` (which checks structure, not whether a
flag exists) and then fails at spawn time — or silently does nothing.

To add one, follow the research recipe at the top of `TEMPLATE.toml` and
in the schema reference. If you get an agent working, the manifest is a
single file and a good pull request.

## Contributing a manifest

1. Establish every field from `--help`, the binary's strings, or files
   on disk. Never guess a flag.
2. `flow harness validate` it, then `flow harness show <name>` and read
   the **rendered** sample commands — that is where quoting mistakes
   become visible.
3. Install it and confirm the agent *resolves* the skill, not merely
   that the file exists. Shadowed skill directories and frontmatter
   parsing sit between those two claims.
4. Comment anything you could not verify, and say how to check it.
