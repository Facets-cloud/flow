#!/bin/sh
# Run the flow test suite inside Docker. Mounts ONLY this worktree; HOME and
# FLOW_ROOT live inside the container, so nothing touches the host's
# ~/.flow, ~/.claude, ~/.codex or ~/.praxis. Extra args go to `go test`.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
exec docker run --rm \
  -v "$here":/src -w /src \
  -v flow-gomod:/go/pkg/mod -v flow-gocache:/root/.cache/go-build \
  -e HOME=/root \
  golang:1.25 go test "${@:-./...}"
