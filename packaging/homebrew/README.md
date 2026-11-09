# Homebrew tap for flow

flow is distributed through a tap: `Facets-cloud/homebrew-tap`, file `Formula/flow.rb`.

```sh
brew install facets-cloud/tap/flow
flow init
```

Always use the fully-qualified name: homebrew-core already has an unrelated
`flow` formula (Facebook's JS type checker), so a bare `brew install flow`
installs the wrong tool.

## Files here

| File | Purpose |
|---|---|
| `flow.rb.tmpl` | Formula template (macOS + Linux, arm64 + amd64) with `@TAG@`, `@VERSION@`, `@SHA_*@` placeholders |
| `render-formula.sh` | `sh render-formula.sh <tag> <SHA256SUMS>` prints the filled-in formula |

The release workflow (`.github/workflows/release.yml`, job `homebrew`) runs on
every `v*` tag: it downloads the release's `SHA256SUMS`, renders the formula and
pushes it to the tap. The job **skips** (does not fail) until the token below exists.

## One-time setup (maintainer)

1. Create the public repo `Facets-cloud/homebrew-tap` with an empty `Formula/` dir.
2. Create a fine-grained PAT (or GitHub App token) with **Contents: read & write**
   on `Facets-cloud/homebrew-tap` only.
3. Add it to `Facets-cloud/flow` as the Actions secret `HOMEBREW_TAP_TOKEN`.
4. Seed the tap from the latest release that has Linux assets:
   ```sh
   gh release download <tag> -R Facets-cloud/flow -p SHA256SUMS
   sh packaging/homebrew/render-formula.sh <tag> SHA256SUMS > ../homebrew-tap/Formula/flow.rb
   ```
   (or just re-run the release workflow for that tag).
5. Verify: `brew install facets-cloud/tap/flow && brew test facets-cloud/tap/flow`.
