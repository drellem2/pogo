# Tmux Integration

Pogo's tmux plugin adds project-aware features to your tmux session: automatic project registration, a fuzzy project switcher popup, cross-project code search, and a status bar segment showing the current project.

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
| `prefix + S` | Cross-project code search popup (90% width, 80% height) |

**Project switcher** (`prefix + P`): Opens an `fzf` popup listing all known projects. Select one to `cd` into it in the current pane.

**Code search** (`prefix + S`): Prompts for a search query, then runs `pose` across all indexed projects and displays results in `less`.

### Status Bar

The status bar segment shows the current pane's project name with an indexing status indicator:

| Symbol | Meaning |
|--------|---------|
| `✓` | Ready (indexed) |
| `⟳` | Currently indexing |
| `!` | Stale (needs reindexing) |
| `?` | Unindexed |

If the current pane is not in a known project, the segment outputs nothing.

## Requirements

- [fzf](https://github.com/junegunn/fzf) (for project switcher popup)
- `less` (for search results viewing)
- pogo binaries installed and on `PATH`
- pogo server running (`pogo server start`)

## Troubleshooting

- **Popups not appearing**: Ensure your tmux version supports `display-popup` (tmux 3.2+).
- **Status bar empty**: Verify pogo is running with `pogo server start` and that the current directory is inside a git repository.
- **Key bindings conflict**: If `prefix + P` or `prefix + S` conflict with existing bindings, modify the `bind-key` lines in `pogo.tmux`.
