#!/usr/bin/env bash
# test_nvim.sh — Run pogo.nvim neovim plugin tests
# Requires: nvim (neovim) in PATH
#
# Usage: ./nvim/test_nvim.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if ! command -v nvim &>/dev/null; then
    echo "SKIP: nvim not found in PATH (required for neovim plugin tests)"
    exit 0
fi

echo "Running pogo.nvim tests..."
nvim --headless --noplugin -u NONE -l "$SCRIPT_DIR/test_nvim.lua"
