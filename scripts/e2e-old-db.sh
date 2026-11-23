#!/usr/bin/env bash
# End-to-end upgrade check: a realistic flow.db written by an old release
# is opened by this tree's binary. Host half extracts the old tag; every
# build and run happens in Docker (sandbox/run.sh).
#   scripts/e2e-old-db.sh [tag]     (default: newest v* tag)
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tag="${1:-$(git -C "$root" tag --list 'v*' | sort -V | tail -1)}"
# One extraction dir per invocation, so concurrent runs never delete each
# other's source tree.
mkdir -p "$root/.fixture-build-e2e"
run="$(mktemp -d "$root/.fixture-build-e2e/run.XXXXXX")"
trap 'rm -rf "$run"; rmdir "$root/.fixture-build-e2e" 2>/dev/null || true' EXIT
git -C "$root" archive "$tag" | tar -x -C "$run"
"$root/sandbox/run.sh" bash scripts/e2e-old-db-inner.sh "$tag" ".fixture-build-e2e/$(basename "$run")"
