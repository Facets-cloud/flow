#!/bin/sh
# Live-hook matrix for the Claude flow plugin, run entirely inside Docker.
#   sandbox/live-hooks.sh [path/to/claude-sandbox.env]
# The env file (CLAUDE_CODE_OAUTH_TOKEN=...) is passed with --env-file and
# never printed. Without it, only the hooks that fire before the first
# model call can be observed.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
envfile=${1:-}
docker build -q -t flow-sandbox "$here/sandbox" >/dev/null
set -- --rm -v "$here":/src:ro -v flow-gomod:/go/pkg/mod -v flow-gocache:/root/.cache/go-build
[ -n "$envfile" ] && [ -f "$envfile" ] && set -- "$@" --env-file "$envfile"
exec docker run "$@" flow-sandbox sh /src/sandbox/live-hooks-inner.sh
