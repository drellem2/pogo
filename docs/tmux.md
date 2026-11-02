# Tmux Integration

Pogo's tmux plugin adds project-aware features to your tmux session: automatic project registration, a fuzzy project switcher popup, and a status bar segment showing the current project.

## Installation

### Option A: TPM (Tmux Plugin Manager)

Add to your `~/.tmux.conf`:

```tmux
set -g @plugin 'drellem2/pogo'
```

Then press `prefix + I` to install.

To add the status bar segment:

```tmux
set -g status-right '#(~/.tmux/plugins/pogo/tmux/pogo-status.sh #{pane_current_path})'
```

### Option B: Manual

1. Clone the repo or copy the `tmux/` directory to a known location.

2. Add to your `~/.tmux.conf`:

```tmux
run-shell /path/to/pogo/tmux/pogo.tmux
set -g status-right '#(/path/to/pogo/tmux/pogo-status.sh #{pane_current_path})'
```

3. Reload tmux: `tmux source-file ~/.tmux.conf`

## Features

### Auto-Discovery

Projects are automatically registered with pogo when you switch between tmux panes or windows. This uses tmux's `pane-focus-in` hook to call `pogo visit` on the current pane's working directory.

### Key Bindings

| Binding | Description |
|---------|-------------|
| `prefix + P` | Fuzzy project switcher popup (80% width, 60% height) |

**Project switcher** (`prefix + P`): Opens an `fzf` popup listing all known projects. Select one to `cd` into it in the current pane.

### Status Bar

The status bar segment shows the current pane's project name.

If the current pane is not in a known project, the segment outputs nothing.

## Requirements

- [fzf](https://github.com/junegunn/fzf) (for project switcher popup)
- pogo binaries installed and on `PATH`
- pogo server running (`pogo server start`)

## Troubleshooting

- **Popups not appearing**: Ensure your tmux version supports `display-popup` (tmux 3.2+).
- **Status bar empty**: Verify pogo is running with `pogo server start` and that the current directory is inside a git repository.
- **Key bindings conflict**: If `prefix + P` conflicts with an existing binding, modify the `bind-key` line in `pogo.tmux`.
