# Zsh Integration

Pogo's zsh integration automatically registers projects as you navigate directories and provides quick project switching.

## Installation

### Automated (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh -s -- --interactive
```

The interactive installer detects zsh and offers to append the integration snippet to your `~/.zshrc`.

### Manual

Add the following to `${ZDOTDIR:-$HOME}/.zshrc`:

```zsh
# Resolve symlinks in paths
dir_resolve() {
    cd "$1" 2>/dev/null || return $?
    echo "$(pwd -P)"
}

# Configure paths — adjust POGO_BINARY_PATH to your install location
export POGO_BINARY_PATH=$(dir_resolve ~/dev/pogo/bin)
export POGO_HOME=$(dir_resolve ~)
export POGO_PLUGIN_PATH="$POGO_BINARY_PATH/plugin"
export PATH="$POGO_BINARY_PATH:$PATH"

# Auto-register projects on directory change
chpwd() {
    pogo visit $(pwd | xargs realpath) > "$POGO_HOME/.pogo-cli-log.txt" 2>&1
}

# Project switcher (requires fzf)
alias sp="cd \"\$(lsp | fzf)\""
```

## Features

| Feature | Description |
|---------|-------------|
| Auto-discovery | Projects are registered with pogo as you `cd` into directories |
| `sp` | Fuzzy project switcher — lists known projects via `fzf` and `cd`s to your selection |
| `lsp` | List all known projects |
| `pose QUERY` | Search code across all indexed projects |

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `POGO_BINARY_PATH` | Directory containing pogo binaries | `~/dev/pogo/bin` |
| `POGO_HOME` | Directory for pogo data files | `~` |
| `POGO_PLUGIN_PATH` | Plugin discovery directory | `$POGO_BINARY_PATH/plugin` |

## Requirements

- [fzf](https://github.com/junegunn/fzf) (for the `sp` project switcher)
- pogo binaries installed and accessible

## How It Works

The integration uses zsh's native `chpwd` hook, which fires every time the working directory changes. This calls `pogo visit` to register the directory's git repository with the pogo daemon for indexing.

## Troubleshooting

- **`sp` does nothing**: Ensure `fzf` is installed and on your `PATH`.
- **Projects not appearing in `lsp`**: Check that `POGO_BINARY_PATH` points to the correct directory and that the pogo server is running (`pogo server start`).
- **Errors in terminal**: Check `$POGO_HOME/.pogo-cli-log.txt` for pogo visit output.
- **Conflicts with other `chpwd` hooks**: The pogo integration defines `chpwd()` directly. If you use frameworks like oh-my-zsh that also define `chpwd`, you may need to wrap the pogo visit call in an `add-zsh-hook chpwd` instead.
