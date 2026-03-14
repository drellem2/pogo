# Fish Integration

Pogo's fish integration automatically registers projects as you navigate directories and provides quick project switching.

## Installation

### Automated (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/drellem2/pogo/main/install.sh | sh -s -- --interactive
```

The interactive installer detects fish and offers to install the integration.

### Manual

Copy `shell/pogo.fish` to your fish config directory:

```sh
cp shell/pogo.fish ${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/pogo.fish
```

Or add the following to your `config.fish`:

```fish
# Configure paths — adjust POGO_BINARY_PATH to your install location
set -gx POGO_BINARY_PATH (realpath ~/dev/pogo/bin 2>/dev/null; or echo ~/dev/pogo/bin)
set -gx POGO_HOME (realpath ~ 2>/dev/null; or echo ~)
set -gx POGO_PLUGIN_PATH "$POGO_BINARY_PATH/plugin"
fish_add_path --prepend $POGO_BINARY_PATH

# Auto-register projects on directory change
function __pogo_on_pwd --on-variable PWD
    pogo visit (realpath $PWD) > "$POGO_HOME/.pogo-cli-log.txt" 2>&1
end

# Project switcher (requires fzf)
abbr -a sp 'cd (lsp | fzf)'
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

The integration uses fish's `--on-variable PWD` event handler to detect directory changes. When `$PWD` changes, `pogo visit` is called to register the directory's git repository with the pogo daemon for indexing.

## Troubleshooting

- **`sp` does nothing**: Ensure `fzf` is installed and on your `PATH`.
- **Projects not appearing in `lsp`**: Check that `POGO_BINARY_PATH` points to the correct directory and that the pogo server is running (`pogo server start`).
- **Errors in terminal**: Check `$POGO_HOME/.pogo-cli-log.txt` for pogo visit output.
