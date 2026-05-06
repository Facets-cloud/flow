<!--
Thanks for sending a PR. A few quick prompts to keep reviews tight.
-->

## What

<!-- One-line summary of what changes. -->

## Why

<!--
Why this change? Link an issue if there is one. If you're changing
behaviour the embedded skill (`internal/app/skill/SKILL.md`) describes,
mention it explicitly — the skill is part of the contract.
-->

## Test plan

- [ ] `make test` passes
- [ ] CI green (`go vet`, `go test ./...`, build on macOS + Ubuntu)
- [ ] Manually exercised the changed command(s) where applicable
- [ ] If the skill changed: rebuilt and ran `flow skill update`
- [ ] If `hookCommand` in `internal/app/skill.go` changed: called out
      in the description (this is a breaking change for existing
      installs)

## Notes

<!-- Anything reviewers should know — alternatives considered, things
deliberately left out, follow-ups planned. -->
