# Security

flow is a fully local CLI: no network calls, no telemetry, no remote
state. The blast radius of a flow bug is bounded by what flow already
does on your machine — spawn iTerm tabs, write under `~/.flow/`, modify
`~/.claude/settings.json` (the SessionStart hook), shell out to `claude`
and `osascript`.

If you find something that looks like a security issue (path traversal,
unintended privilege use, supply-chain concern, etc.), please open a
[GitHub issue](https://github.com/Facets-cloud/flow/issues/new) with as
much detail as you're comfortable sharing publicly. flow has no
private-disclosure channel today; the project is small enough and the
attack surface narrow enough that public triage is acceptable for now.

If you'd prefer not to file publicly, mention that in the issue and a
maintainer will follow up.

## Supported versions

flow follows SemVer (`0.x.y` until the API stabilises). Only the latest
release receives security fixes. Binaries are published on the
[GitHub Releases](https://github.com/Facets-cloud/flow/releases) page;
released binaries auto-detect version bumps and prompt to refresh the
embedded skill on next run.
