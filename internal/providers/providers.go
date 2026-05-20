// Package providers is the registry of agent harness providers: it maps a
// config provider id ("claude", "codex", and in future "gemini") to its
// agent.Provider descriptor.
//
// It lives in its own package — rather than in internal/agent — because
// resolving an id requires importing the concrete provider packages
// (internal/claude, internal/codex, …), and those already import
// internal/agent. Putting the resolver in agent would create the cycle
// agent → claude → agent. Both cmd/pogod and cmd/pogo depend on this package
// so provider selection has a single source of truth.
package providers

import (
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/codex"
)

// Resolve maps a config provider id to its agent.Provider descriptor.
//
// "" and "claude" resolve to Claude (the default); "codex" resolves to Codex.
//
// ok is false when id names no known provider. The returned *agent.Provider is
// still safe to use in that case — it is the Claude fallback — so a stale or
// mistyped config never wedges startup. Callers should warn when ok is false.
func Resolve(id string) (provider *agent.Provider, ok bool) {
	switch id {
	case "", claude.Provider.ID:
		return &claude.Provider, true
	case codex.Provider.ID:
		return &codex.Provider, true
	default:
		return &claude.Provider, false
	}
}
