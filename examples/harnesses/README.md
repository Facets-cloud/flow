# Example harness manifests

Drop one of these in `~/.flow/harnesses/` and flow can drive that coding
agent — no rebuild, no code change.

```bash
flow harness validate ./praxis.toml     # check before installing
cp praxis.toml ~/.flow/harnesses/
flow harness list                       # confirm it loaded
flow harness show praxis                # capabilities + rendered commands
flow skill install --harness praxis     # install the skill, wire the hooks
```

Schema reference: [`docs/harness-manifest.md`](../../docs/harness-manifest.md).
Starting from scratch: [`TEMPLATE.toml`](TEMPLATE.toml).

## What is here

| Manifest | Verified against | Notes |
|---|---|---|
| [`claude.toml`](claude.toml) | the built-in adapter, by a byte-equality test | Reference example. Claude is built in — you only need this to *override* it. |
| [`praxis.toml`](praxis.toml) | praxis-harness source and a live `prx run` | Full capability set: resume, headless close-out, transcript, native skills, config-patch hooks. |

Every field in these was checked against a running binary, a real file
on disk, or source.

## Agents not covered here

Manifests are only included when their full **session lifecycle** was
actually verified. Codex and OMP are intentionally absent: both mint the
session id internally and do not accept Flow's id on initial interactive
launch. The current manifest contract allocates and stores the id before
spawning, so a UUID4/UUID7 manifest for either agent would bind Flow to a
session that the agent never created. Launch might appear to work, but
resume, transcripts, liveness, and completion would target the wrong id.

That is precisely why **codex ships as a core adapter**
(`internal/harness/codex`) rather than as a manifest: minting its thread
id takes a probe run whose output has to be parsed before launch — Go
code, not argv templates. A harness needs a hand-written adapter exactly
when its lifecycle needs logic; everything else is a manifest.

A plausible-looking manifest with an invented flag or identity mapping is
worse than none: it passes `flow harness validate` (which checks structure,
not whether a binary honors the lifecycle) and then fails at spawn time —
or silently operates on the wrong session.

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
