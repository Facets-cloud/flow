#!/usr/bin/env bash
# Regenerate the historical schema fixtures in internal/flowdb/testdata/schemas/.
#
# For every release tag: build that tag's flow binary, `flow init` into a
# throwaway FLOW_ROOT, create a few rows of each kind the tag supports, and
# dump the resulting flow.db as SQL. Tags whose schema is identical to an
# earlier tag's are skipped, so each committed fixture is a distinct shape.
#
# Run on the host from the repo (or a worktree):  scripts/gen-schema-fixtures.sh
# FIXTURE_TAGS="v0.1.0-alpha.40" limits the run to those tags (keeps the rest).
# Drift fixtures (scripts/schema-drift/*.sql) are rebuilt on top afterwards.
# The host step only extracts tag sources (git archive); every build and
# every run of an old binary happens inside Docker via sandbox/run.sh.
# Slow (one build per tag); run once and commit the output, not in CI.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
build="$root/.fixture-build"
rm -rf "$build" && mkdir -p "$build"
tags=$(git -C "$root" tag --list 'v*' | sort -V)
for t in $tags; do
  mkdir -p "$build/$t"
  git -C "$root" archive "$t" | tar -x -C "$build/$t"
done
printf '%s\n' $tags > "$build/TAGS"
"$root/sandbox/run.sh" -e FIXTURE_TAGS="${FIXTURE_TAGS:-}" bash scripts/gen-schema-fixtures-inner.sh
"$root/sandbox/run.sh" bash scripts/gen-drift-fixtures-inner.sh
rm -rf "$build"
