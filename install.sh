#!/bin/sh
# Pogo install script
# Usage: curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
set -e

REPO="drellem2/pogo"
INSTALL_DIR="${POGO_INSTALL_DIR:-/usr/local/bin}"
BINARIES="pogo pogod lsp pose"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux|darwin) ;;
  *)
    echo "Error: unsupported OS: $OS" >&2
    exit 1
    ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    echo "Error: unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

# Get latest release tag
if [ -n "$POGO_VERSION" ]; then
  VERSION="$POGO_VERSION"
else
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')
  if [ -z "$VERSION" ]; then
    echo "Error: could not determine latest version" >&2
    exit 1
  fi
fi

echo "Installing pogo ${VERSION} (${OS}/${ARCH}) to ${INSTALL_DIR}"

# Create install dir if needed
if [ ! -d "$INSTALL_DIR" ]; then
  echo "Creating ${INSTALL_DIR} (may require sudo)"
  sudo mkdir -p "$INSTALL_DIR"
fi

# Check write access
if [ ! -w "$INSTALL_DIR" ]; then
  SUDO="sudo"
else
  SUDO=""
fi

BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

for bin in $BINARIES; do
  url="${BASE_URL}/${bin}-${OS}-${ARCH}"
  echo "  Downloading ${bin}..."
  tmpfile=$(mktemp)
  if curl -fsSL -o "$tmpfile" "$url"; then
    chmod +x "$tmpfile"
    $SUDO mv "$tmpfile" "${INSTALL_DIR}/${bin}"
  else
    rm -f "$tmpfile"
    echo "  Warning: failed to download ${bin} (${url})" >&2
  fi
done

echo "Done! Installed to ${INSTALL_DIR}"
echo "Run 'pogo server start' to start the daemon."
