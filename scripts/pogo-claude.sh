#!/bin/bash
# pogo-claude — wrapper that pogod uses instead of invoking 'claude' directly.
#
# This centralizes operational plumbing (startup ceremony, env setup, workarounds
# for Claude Code limitations) so agent prompts stay clean and focused on the
# agent's role. When Claude Code adds native lifecycle hooks, this wrapper either
# becomes a passthrough (exec claude "$@") or gets removed entirely.

set -euo pipefail

# Pre-flight: ensure macguffin workspace exists (safe to call repeatedly)
mg init 2>/dev/null || true

# Hand off to Claude Code with all arguments pogod passed us
exec claude "$@"
