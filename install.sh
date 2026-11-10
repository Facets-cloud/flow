#!/bin/sh
# Install flow — https://github.com/Facets-cloud/flow
#
#   curl -fsSL https://raw.githubusercontent.com/Facets-cloud/flow/main/install.sh | sh
#
# Options (environment variables):
#   FLOW_VERSION=v0.1.0-alpha.35   install a specific release tag (default: latest)
#   FLOW_INSTALL_DIR=/usr/local/bin install somewhere else (default: ~/.local/bin)
#   FLOW_BASE_URL=https://...       download from a mirror instead of GitHub releases
#
# What it does: picks the release binary for your OS/arch, verifies it against
# the release's SHA256SUMS, and copies it into the install dir. Nothing else.
set -eu

REPO="Facets-cloud/flow"
VERSION="${FLOW_VERSION:-latest}"
INSTALL_DIR="${FLOW_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *)      die "unsupported OS: $(uname -s) (flow ships macOS and Linux builds)" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64)  arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *)               die "unsupported architecture: $(uname -m)" ;;
esac

asset="flow-$os-$arch"
if [ -n "${FLOW_BASE_URL:-}" ]; then
  base="$FLOW_BASE_URL"
elif [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "need sha256sum or shasum to verify the download"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset ($VERSION)..."
fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: $base/SHA256SUMS"

want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || die "$asset is not listed in SHA256SUMS"
got="$(sha256 "$tmp/$asset")"
[ "$want" = "$got" ] || die "checksum mismatch for $asset (want $want, got $got)"
say "Checksum OK."

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR (try FLOW_INSTALL_DIR=...)"
chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$INSTALL_DIR/flow" || die "cannot write $INSTALL_DIR/flow (try FLOW_INSTALL_DIR=...)"
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$INSTALL_DIR/flow" 2>/dev/null || true
fi
say "Installed flow -> $INSTALL_DIR/flow"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    say ""
    say "$INSTALL_DIR is not on your PATH. Add this to your shell rc file:"
    say "  export PATH=\"$INSTALL_DIR:\$PATH\""
    ;;
esac

say ""
say "Next: run 'flow init' to create ~/.flow and install the agent skill."
