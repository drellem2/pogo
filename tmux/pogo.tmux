#!/usr/bin/env bash
# pogo.tmux — tmux plugin for pogo project integration
#
# Features:
#   - Auto-registers projects when switching panes/windows (via pogo visit)
#   - Shows current project name + indexing status in the status bar
#   - prefix + P: fuzzy project switcher (lsp | fzf in a popup)
#   - prefix + S: cross-project code search (pose in a popup)
#
# Installation:
#   Option A (TPM):
#     set -g @plugin 'drellem2/pogo'
#     # In .tmux.conf, add to status-right:
#     set -g status-right '#(~/.tmux/plugins/pogo/tmux/pogo-status.sh #{pane_current_path})'
#
#   Option B (manual):
#     run-shell /path/to/pogo/tmux/pogo.tmux
#     # In .tmux.conf, add to status-right:
#     set -g status-right '#(/path/to/pogo/tmux/pogo-status.sh #{pane_current_path})'

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Auto-register projects on pane focus
# Uses the focus-in hook so switching between panes/windows triggers discovery
tmux set-hook -g pane-focus-in \
    "run-shell 'pogo visit \"#{pane_current_path}\" >/dev/null 2>&1 &'"

# prefix + P — fuzzy project switcher popup
tmux bind-key P display-popup -E -w 80% -h 60% \
    "lsp | fzf --prompt='project> ' | xargs -I{} tmux send-keys 'cd {}' Enter"

# prefix + S — cross-project search popup
tmux bind-key S display-popup -E -w 90% -h 80% \
    "read -p 'search> ' q && pose \"\$q\" | less -R"
