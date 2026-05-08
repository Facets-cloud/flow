#!/bin/bash
set -e

# Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "${ARCH}" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: ${ARCH}"; exit 1 ;;
esac

case "${OS}" in
    linux|darwin) BIN_EXT="" ;;
    msys*|mingw*|cygwin*) OS="windows"; BIN_EXT=".exe" ;;
    *) echo "Unsupported operating system: ${OS}"; exit 1 ;;
esac

echo "Detecting latest version..."
LATEST_VERSION=$(curl -s https://api.github.com/repos/Facets-cloud/flow/releases/latest | grep tag_name | cut -d '"' -f 4)
VERSION_NUMBER="${LATEST_VERSION#v}"

FILENAME="flow_${VERSION_NUMBER}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/Facets-cloud/flow/releases/latest/download/${FILENAME}"

echo "Downloading ${FILENAME}..."
curl -L "${URL}" -o flow.tar.gz

echo "Extracting..."
tar -xzf flow.tar.gz

echo "Installing to /usr/local/bin/flow (may require sudo)..."
if [ "${OS}" = "windows" ]; then
    # For Git Bash on Windows
    mkdir -p /usr/bin
    mv "flow${BIN_EXT}" /usr/bin/
else
    if [ -w /usr/local/bin ]; then
        mv flow /usr/local/bin/
    else
        sudo mv flow /usr/local/bin/
    fi
fi

chmod +x /usr/local/bin/flow${BIN_EXT}

# Remove macOS quarantine flag for unsigned binaries
if [ "${OS}" = "darwin" ]; then
    xattr -d com.apple.quarantine /usr/local/bin/flow 2>/dev/null || true
fi

# Clean up
rm flow.tar.gz

echo "Done! To complete the setup, run:"
echo "  flow init"
