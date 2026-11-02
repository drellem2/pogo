#!/usr/bin/env bash
# pogo-status.sh — tmux status bar segment showing the current pane's project
# Usage in .tmux.conf:
#   set -g status-right '#(/path/to/pogo/tmux/pogo-status.sh #{pane_current_path})'
#
# Output: project_name, or empty string if not in a project. (It also showed
# an indexing-status icon until the search indexer was deleted, mg-14647.)
# Dependencies: pogo

pane_path="${1:-}"
[ -z "$pane_path" ] && exit 0

# Use pogo visit to discover/register the project and get its path
project_path="$(pogo visit "$pane_path" 2>/dev/null)" || exit 0
[ -z "$project_path" ] && exit 0

name="$(basename "$project_path")"

echo "$name"
