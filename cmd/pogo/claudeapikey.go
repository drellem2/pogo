package main

// claudeAPIKeyCheckName is the `pogo doctor --check` row for Claude Code's
// "Detected a custom API key" gate (mg-2037). The row's logic lives beside the
// spawn-time detection in internal/claude/apikeygate.go, so the two surfaces
// cannot disagree about what the gate is or how to clear it.
const claudeAPIKeyCheckName = "claude API key approval"
