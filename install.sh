#!/bin/sh
# Pogo install script
# Usage: curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
#        curl -fsSL ... | sh -s -- --interactive
set -e

REPO="drellem2/pogo"
INSTALL_DIR="${POGO_INSTALL_DIR:-/usr/local/bin}"
BINARIES="pogo pogod lsp pose"
INTERACTIVE=false
SHELL_SOURCE_URL="https://raw.githubusercontent.com/${REPO}/main/shell"
TMUX_SOURCE_URL="https://raw.githubusercontent.com/${REPO}/main/tmux"

# Parse flags
for arg in "$@"; do
  case "$arg" in
    --interactive|--with-integrations)
      INTERACTIVE=true
      ;;
  esac
done

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

if [ "$INTERACTIVE" = false ]; then
  echo ""
  echo "Tip: Re-run with --interactive to set up shell and editor integrations."
  exit 0
fi

###############################################################################
# Interactive integration setup
###############################################################################

echo ""
echo "=== Integration Setup ==="
echo ""

# Helper: prompt user with y/n, default to yes
ask_yn() {
  printf "%s [Y/n] " "$1"
  read -r answer </dev/tty
  case "$answer" in
    [nN]*) return 1 ;;
    *) return 0 ;;
  esac
}

# Set up POGO_HOME
POGO_HOME="${POGO_HOME:-$HOME}"
echo "POGO_HOME will be set to: ${POGO_HOME}"
echo ""

###############################################################################
# Shell integrations
###############################################################################

install_shell_snippet() {
  shell_name="$1"
  rc_file="$2"
  snippet_file="$3"
  marker="Start Pogo"

  if [ -f "$rc_file" ] && grep -q "$marker" "$rc_file" 2>/dev/null; then
    echo "  ${shell_name}: pogo integration already present in ${rc_file}, skipping."
    return 0
  fi

  if ask_yn "  Install ${shell_name} integration into ${rc_file}?"; then
    tmpsnippet=$(mktemp)
    if curl -fsSL -o "$tmpsnippet" "$snippet_file"; then
      # Replace default POGO_BINARY_PATH with the actual install dir
      echo "" >> "$rc_file"
      cat "$tmpsnippet" >> "$rc_file"
      rm -f "$tmpsnippet"
      echo "  ${shell_name}: integration added to ${rc_file}"
    else
      rm -f "$tmpsnippet"
      echo "  ${shell_name}: failed to download snippet" >&2
    fi
  else
    echo "  ${shell_name}: skipped."
  fi
}

echo "--- Shell Integrations ---"
echo "Pogo hooks into your shell to auto-discover projects when you cd."
echo ""

# Detect and offer zsh
if command -v zsh >/dev/null 2>&1; then
  install_shell_snippet "zsh" "${ZDOTDIR:-$HOME}/.zshrc" "${SHELL_SOURCE_URL}/.zshrc"
fi

# Detect and offer bash
if command -v bash >/dev/null 2>&1; then
  bashrc="$HOME/.bashrc"
  # On macOS, bash sources .bash_profile for login shells
  if [ "$OS" = "darwin" ] && [ -f "$HOME/.bash_profile" ] && [ ! -f "$bashrc" ]; then
    bashrc="$HOME/.bash_profile"
  fi
  install_shell_snippet "bash" "$bashrc" "${SHELL_SOURCE_URL}/.bashrc"
fi

# Detect and offer fish
if command -v fish >/dev/null 2>&1; then
  fish_conf_dir="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d"
  mkdir -p "$fish_conf_dir"
  install_shell_snippet "fish" "${fish_conf_dir}/pogo.fish" "${SHELL_SOURCE_URL}/pogo.fish"
fi

echo ""

###############################################################################
# Tmux integration
###############################################################################

if command -v tmux >/dev/null 2>&1; then
  echo "--- Tmux Integration ---"
  echo "Pogo can show your current project in the tmux status bar"
  echo "and add key bindings for project switching (prefix+P) and search (prefix+S)."
  echo ""

  tmux_plugin_dir="$HOME/.tmux/plugins/pogo/tmux"

  if [ -f "${tmux_plugin_dir}/pogo.tmux" ]; then
    echo "  tmux: pogo plugin already installed in ${tmux_plugin_dir}, skipping."
  elif ask_yn "  Install tmux integration?"; then
    mkdir -p "$tmux_plugin_dir"
    curl -fsSL -o "${tmux_plugin_dir}/pogo.tmux" "${TMUX_SOURCE_URL}/pogo.tmux"
    curl -fsSL -o "${tmux_plugin_dir}/pogo-status.sh" "${TMUX_SOURCE_URL}/pogo-status.sh"
    chmod +x "${tmux_plugin_dir}/pogo.tmux" "${tmux_plugin_dir}/pogo-status.sh"
    echo "  tmux: plugin installed to ${tmux_plugin_dir}"
    echo ""
    echo "  Add to your .tmux.conf:"
    echo "    run-shell ${tmux_plugin_dir}/pogo.tmux"
    echo "    set -g status-right '#(${tmux_plugin_dir}/pogo-status.sh #{pane_current_path})'"
  else
    echo "  tmux: skipped."
  fi
  echo ""
fi

###############################################################################
# Editor integrations (stub — plugins not yet available)
###############################################################################

echo "--- Editor Integrations ---"

has_editor=false
for editor in nvim vim emacs code; do
  if command -v "$editor" >/dev/null 2>&1; then
    has_editor=true
    break
  fi
done

if [ "$has_editor" = true ]; then
  echo "Editor plugins for pogo are not yet available."
  echo "Detected editors:"
  command -v nvim >/dev/null 2>&1 && echo "  - Neovim"
  command -v vim  >/dev/null 2>&1 && echo "  - Vim"
  command -v emacs >/dev/null 2>&1 && echo "  - Emacs"
  command -v code >/dev/null 2>&1 && echo "  - VS Code"
  echo "Watch https://github.com/${REPO} for editor plugin releases."
else
  echo "No supported editors detected."
fi

echo ""
echo "=== Setup Complete ==="
echo "Restart your shell or source your rc file to activate integrations."
