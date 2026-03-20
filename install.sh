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

# Strip leading 'v' from version for archive name (goreleaser convention)
ARCHIVE_VERSION=$(echo "$VERSION" | sed 's/^v//')
ARCHIVE_NAME="pogo_${ARCHIVE_VERSION}_${OS}_${ARCH}.tar.gz"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE_NAME}"

echo "  Downloading ${ARCHIVE_NAME}..."
tmpdir=$(mktemp -d)
tmpfile="${tmpdir}/pogo.tar.gz"

if curl -fsSL -o "$tmpfile" "$ARCHIVE_URL"; then
  tar xzf "$tmpfile" -C "$tmpdir"
  for bin in $BINARIES; do
    if [ -f "${tmpdir}/${bin}" ]; then
      chmod +x "${tmpdir}/${bin}"
      $SUDO mv "${tmpdir}/${bin}" "${INSTALL_DIR}/${bin}"
      echo "  Installed ${bin}"
    else
      echo "  Warning: ${bin} not found in archive" >&2
    fi
  done
else
  echo "  Error: failed to download ${ARCHIVE_URL}" >&2
  rm -rf "$tmpdir"
  exit 1
fi

rm -rf "$tmpdir"

echo "Done! Installed to ${INSTALL_DIR}"
echo "Run 'pogo install' to set up agent orchestration."

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
# Editor integrations
###############################################################################

EDITOR_SOURCE_URL="https://raw.githubusercontent.com/${REPO}/main"

echo "--- Editor Integrations ---"

# Emacs
if command -v emacs >/dev/null 2>&1; then
  emacs_dir="$HOME/.emacs.d/site-lisp"

  if [ -f "${emacs_dir}/pogo.el" ]; then
    echo "  emacs: pogo.el already installed in ${emacs_dir}, skipping."
  elif ask_yn "  Install Emacs integration (pogo.el) into ${emacs_dir}?"; then
    mkdir -p "$emacs_dir"
    if curl -fsSL -o "${emacs_dir}/pogo.el" "${EDITOR_SOURCE_URL}/emacs/pogo.el"; then
      echo "  emacs: pogo.el installed to ${emacs_dir}"
      echo ""
      echo "  Add to your init.el:"
      echo "    (add-to-list 'load-path \"${emacs_dir}\")"
      echo "    (require 'pogo)"
    else
      echo "  emacs: failed to download pogo.el" >&2
    fi
  else
    echo "  emacs: skipped."
  fi
  echo ""
fi

# Neovim
if command -v nvim >/dev/null 2>&1; then
  nvim_data_dir="${XDG_DATA_HOME:-$HOME/.local/share}/nvim/site/pack/pogo/start/pogo"

  if [ -d "$nvim_data_dir" ]; then
    echo "  neovim: pogo plugin already installed in ${nvim_data_dir}, skipping."
  elif ask_yn "  Install Neovim integration into ${nvim_data_dir}?"; then
    mkdir -p "${nvim_data_dir}/lua/pogo"
    mkdir -p "${nvim_data_dir}/plugin"
    nvim_ok=true
    for f in init.lua client.lua telescope.lua; do
      if ! curl -fsSL -o "${nvim_data_dir}/lua/pogo/${f}" "${EDITOR_SOURCE_URL}/nvim/lua/pogo/${f}"; then
        echo "  neovim: failed to download ${f}" >&2
        nvim_ok=false
      fi
    done
    if ! curl -fsSL -o "${nvim_data_dir}/plugin/pogo.lua" "${EDITOR_SOURCE_URL}/nvim/plugin/pogo.lua"; then
      echo "  neovim: failed to download plugin/pogo.lua" >&2
      nvim_ok=false
    fi
    if [ "$nvim_ok" = true ]; then
      echo "  neovim: plugin installed to ${nvim_data_dir}"
      echo ""
      echo "  The plugin loads automatically. Configure in your init.lua:"
      echo "    require('pogo').setup({})"
    fi
  else
    echo "  neovim: skipped."
  fi
  echo ""
fi

# VS Code
if command -v code >/dev/null 2>&1; then
  if code --list-extensions 2>/dev/null | grep -qi pogo; then
    echo "  vscode: pogo extension already installed, skipping."
  elif ask_yn "  Install VS Code extension from source?"; then
    vscode_tmpdir=$(mktemp -d)
    echo "  Downloading VS Code extension source..."
    vscode_ok=true
    for f in package.json tsconfig.json src/extension.ts src/projects.ts src/client.ts; do
      target_dir=$(dirname "${vscode_tmpdir}/${f}")
      mkdir -p "$target_dir"
      if ! curl -fsSL -o "${vscode_tmpdir}/${f}" "${EDITOR_SOURCE_URL}/vscode/${f}"; then
        echo "  vscode: failed to download ${f}" >&2
        vscode_ok=false
      fi
    done
    if [ "$vscode_ok" = true ] && command -v npm >/dev/null 2>&1; then
      echo "  Building and installing extension..."
      (cd "$vscode_tmpdir" && npm install --ignore-scripts 2>/dev/null && npx vsce package -o pogo.vsix 2>/dev/null && code --install-extension pogo.vsix 2>/dev/null)
      if [ $? -eq 0 ]; then
        echo "  vscode: extension installed successfully."
      else
        echo "  vscode: automated install failed."
        echo ""
        echo "  To install manually:"
        echo "    cd ${vscode_tmpdir} && npm install && npx vsce package && code --install-extension pogo.vsix"
      fi
    elif [ "$vscode_ok" = true ]; then
      echo "  vscode: npm not found. To install manually:"
      echo "    git clone https://github.com/${REPO}.git && cd pogo/vscode"
      echo "    npm install && npx vsce package && code --install-extension pogo.vsix"
    fi
    rm -rf "$vscode_tmpdir"
  else
    echo "  vscode: skipped."
  fi
  echo ""
fi

if ! command -v emacs >/dev/null 2>&1 && ! command -v nvim >/dev/null 2>&1 && ! command -v code >/dev/null 2>&1; then
  echo "No supported editors detected (emacs, neovim, vscode)."
fi

echo ""

###############################################################################
# Daemon service (auto-start + crash recovery)
###############################################################################

echo "--- Daemon Service ---"
echo "Install pogo as a system service so it starts on login and restarts on crash."
echo ""

if ask_yn "  Install pogo daemon service?"; then
  if "${INSTALL_DIR}/pogo" service install; then
    echo "  Service installed successfully."
  else
    echo "  Service installation failed. You can install manually later with:"
    echo "    pogo service install"
  fi
else
  echo "  Skipped. You can install later with: pogo service install"
fi

echo ""
echo "=== Setup Complete ==="
echo "Restart your shell or source your rc file to activate integrations."
