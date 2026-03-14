# Bash Integration

Pogo's bash integration automatically registers projects as you navigate directories and provides quick project switching.

## Installation

### Automated (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh -s -- --interactive
```

The interactive installer detects bash and offers to append the integration snippet to your `~/.bashrc` (or `~/.bash_profile` on macOS).

### Manual

Add the following to your `~/.bashrc` (Linux) or `~/.bash_profile` (macOS):

```bash
# Resolve symlinks in paths
dir_resolve() {
    cd "$1" 2>/dev/null || return $?
    pwd -P
    cd - > /dev/null 2>&1
}

# Configure paths — adjust POGO_BINARY_PATH to your install location
export POGO_BINARY_PATH=$(dir_resolve ~/dev/pogo/bin)
export POGO_HOME=$(dir_resolve ~)
export POGO_PLUGIN_PATH="$POGO_BINARY_PATH/plugin"
export PATH="$POGO_BINARY_PATH:$PATH"

# Auto-register projects on directory change
__pogo_last_dir=""
__pogo_chpwd() {
    local cur
    cur="$(pwd -P)"
    if [ "$cur" != "$__pogo_last_dir" ]; then
        __pogo_last_dir="$cur"
        pogo visit "$cur" > "$POGO_HOME/.pogo-cli-log.txt" 2>&1
    fi
}

if [ -z "$PROMPT_COMMAND" ]; then
    PROMPT_COMMAND="__pogo_chpwd"
else
    PROMPT_COMMAND="__pogo_chpwd;${PROMPT_COMMAND}"
fi

# Project switcher (requires fzf)
alias sp='cd "$(lsp | fzf)"'
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

The integration hooks into bash's `PROMPT_COMMAND` to call `pogo visit` whenever the working directory changes. This registers the directory (and its git repository, if any) with the pogo daemon for indexing and discovery. The hook is appended to any existing `PROMPT_COMMAND` to avoid conflicts.

## Troubleshooting

- **`sp` does nothing**: Ensure `fzf` is installed and on your `PATH`.
- **Projects not appearing in `lsp`**: Check that `POGO_BINARY_PATH` points to the correct directory and that the pogo server is running (`pogo server start`).
- **Errors in terminal**: Check `$POGO_HOME/.pogo-cli-log.txt` for pogo visit output.
