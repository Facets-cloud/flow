#!/usr/bin/env bash
# Run a command inside a throwaway golang container with only this worktree
# mounted. HOME and FLOW_ROOT point inside the container, so nothing touches
# the host's real ~/.flow or ~/.claude.
#   sandbox/run.sh go test ./...
#   sandbox/run.sh -e NAME=value bash scripts/foo.sh   (leading -e: extra env)
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
envs=()
while [ "${1:-}" = "-e" ]; do envs+=(-e "$2"); shift 2; done
exec docker run --rm -i \
  -v "$here":/src -w /src \
  -v flow-gomod:/go/pkg/mod -v flow-gocache:/root/.cache/go-build \
  -e HOME=/root -e FLOW_ROOT=/root/.flow -e CGO_ENABLED=0 \
  ${envs[@]+"${envs[@]}"} \
  "${FLOW_SANDBOX_IMAGE:-golang:1.25}" "$@"
