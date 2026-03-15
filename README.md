# pogo
code intelligence daemon

*Like a language server for project navigation.*

Open a git repository in your terminal, then navigate the project in your editor — no extra effort required.

See [VISION.md](VISION.md) for the design principles and long-term direction.

## Installation

### Quick Install

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
```

This detects your OS and architecture, downloads the latest release binaries, and installs them to `/usr/local/bin`. Override the install directory with `POGO_INSTALL_DIR` or pin a version with `POGO_VERSION`:

```sh
POGO_INSTALL_DIR=~/.local/bin curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh
```

### Build from Source

Requires [Go](https://go.dev/dl/) 1.21+.

```sh
git clone https://github.com/drellem2/pogo.git && cd pogo
./build.sh                    # fmt, test, build
export PATH="$PWD/bin:$PATH"  # or copy bin/* to ~/.local/bin
```

Then run the interactive installer to set up shell/editor integrations:

```sh
./install.sh --interactive
```

## Integrations

Pogo integrates with shells, terminal multiplexers, and editors. Run the interactive installer to set up integrations automatically:

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh -s -- --interactive
```

### Shells

Auto-discover projects as you `cd` into directories. All shell integrations provide `sp` (fuzzy project switcher via fzf) and automatic project registration.

| Shell | Status | Docs |
|-------|--------|------|
| [Bash](docs/bash.md) | Supported | Append snippet to `~/.bashrc` |
| [Zsh](docs/zsh.md) | Supported | Append snippet to `~/.zshrc` |
| [Fish](docs/fish.md) | Supported | Copy `pogo.fish` to `conf.d/` |

### Terminal Multiplexers

| Tool | Status | Docs |
|------|--------|------|
| [tmux](docs/tmux.md) | Supported | Plugin with project switcher popup, code search popup, and status bar segment |

### Editors

| Editor | Status | Docs |
|--------|--------|------|
| [Emacs](docs/emacs.md) | Supported | Full minor mode with project navigation, code search, and buffer management |
| [Neovim](docs/neovim.md) | Supported | Lua plugin with Telescope/fzf-lua integration |
| [VS Code](docs/vscode.md) | In development | Extension with command palette and search panel |

> **Emacs**: Install with [straight.el](https://github.com/radian-software/straight.el) or manually from the `.el` file. See [docs/emacs.md](docs/emacs.md) for details.

> **Neovim plugin manager configuration required**: Once available, the Neovim plugin will need to be added to your plugin manager (lazy.nvim, packer, etc.) and configured in your `init.lua`. See [docs/neovim.md](docs/neovim.md) for details.

## Shell Usage

1. Pogo will autodiscover projects as you visit directories in the shell.
2. Use `lsp` to list projects.
3. Use `sp` to switch projects.
4. Use `pose` to search with zoekt.

Zoekt query examples can be found [here](https://github.com/sourcegraph/zoekt/blob/main/web/templates.go#L158).
e.g. `pose banana` or `pose banana .` will search the current directory for `banana`.

## Environment Variables

- `POGO_HOME`: Folder for pogo to store indexes.
- `POGO_PLUGIN_PATH`: Folder to discover plugins.

## Plugins

You can write/install plugins to provide IDE-like features for all editors. See the included `search` plugin for an example.
