#!/usr/bin/env bash
# pogo-status.sh — tmux status bar segment showing the current pane's project
# Usage in .tmux.conf:
#   set -g status-right '#(/path/to/pogo/tmux/pogo-status.sh #{pane_current_path})'
#
# Output: project_name [✓|⟳|!] or empty string if not in a project
# Dependencies: pogo

pane_path="${1:-}"
[ -z "$pane_path" ] && exit 0

# Use pogo visit to discover/register the project and get its path
project_path="$(pogo visit "$pane_path" 2>/dev/null)" || exit 0
[ -z "$project_path" ] && exit 0

name="$(basename "$project_path")"

# Get indexing status from pogo status CLI
# Output format: "ready        /path/ (42 files)"
status_line="$(pogo status 2>/dev/null | grep -F "$project_path")" || { echo "$name"; exit 0; }
status="${status_line%%[[:space:]]*}"

case "$status" in
    ready)     echo "$name ✓" ;;
    indexing)  echo "$name ⟳" ;;
    stale)     echo "$name !" ;;
    unindexed) echo "$name ?" ;;
    *)         echo "$name" ;;
esac
