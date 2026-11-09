#!/bin/sh
# Render the Homebrew formula for a flow release.
#
#   sh render-formula.sh v0.1.0-alpha.36 SHA256SUMS > Formula/flow.rb
#
# Reads the per-asset checksums from the release's SHA256SUMS and fills them,
# plus the version, into flow.rb.tmpl (next to this script).
set -eu

[ $# -eq 2 ] || { echo "usage: $0 <tag> <SHA256SUMS>" >&2; exit 2; }
tag="$1"
sums="$2"
tmpl="$(dirname "$0")/flow.rb.tmpl"

sum() {
  s="$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$sums")"
  [ -n "$s" ] || { echo "error: $1 missing from $sums" >&2; exit 1; }
  printf '%s' "$s"
}

sed \
  -e "s|@TAG@|$tag|g" \
  -e "s|@VERSION@|${tag#v}|g" \
  -e "s|@SHA_DARWIN_ARM64@|$(sum flow-darwin-arm64)|g" \
  -e "s|@SHA_DARWIN_AMD64@|$(sum flow-darwin-amd64)|g" \
  -e "s|@SHA_LINUX_ARM64@|$(sum flow-linux-arm64)|g" \
  -e "s|@SHA_LINUX_AMD64@|$(sum flow-linux-amd64)|g" \
  "$tmpl"
