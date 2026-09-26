package main

import (
	"fmt"

	"github.com/drellem2/pogo/internal/agent"
)

// spawnBaseLine renders spawn-polecat's base report as one line for the human
// output, or "" when there is nothing to report (no worktree was created and
// no target was named). It exists so the dispatcher sees where the worker
// actually starts at the moment it can still act on it (drellem2/pogo#176).
func spawnBaseLine(b *agent.SpawnBase, id string) string {
	if b == nil || (b.BaseRef == "" && b.Target == "") {
		return ""
	}
	base := b.BaseRef
	if base == "" {
		base = "local HEAD (no usable origin)"
	}
	switch {
	case b.Target == "":
		return fmt.Sprintf("  base: %s (no target branch; the repo default applies)", base)
	case b.TargetFrom == agent.TargetFromWorkItem:
		return fmt.Sprintf("  base: %s (target %s, from work item %s's branch: field)", base, b.Target, id)
	default:
		return fmt.Sprintf("  base: %s (target %s)", base, b.Target)
	}
}
