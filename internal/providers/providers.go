// Package providers is the registry of agent harness providers: it maps a
// config provider id ("claude", "codex", "pi", "cursor", and in future
// "gemini") to its agent.Provider descriptor.
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
	"github.com/drellem2/pogo/internal/cursor"
	"github.com/drellem2/pogo/internal/pi"
)

// All returns every known harness provider descriptor, in a stable order
// (claude, codex, pi, cursor). pogod registers the whole set into the
// agent registry at startup so a provider can be resolved per-spawn — the
// mixed-fleet capability from mg-b31b — instead of once globally. Use Resolve
// when mapping a single id; use All when you need the complete set.
func All() []*agent.Provider {
	return []*agent.Provider{&claude.Provider, &codex.Provider, &pi.Provider, &cursor.Provider}
}

// MemoryIndexGlobs returns the home-relative auto-memory index globs declared
// by every known provider, in All's stable order. It is the composition point
// that keeps shared packages free of any one harness's dotdir: memcheck takes
// these as data instead of naming ~/.claude itself.
//
// It spans All rather than the configured provider deliberately. `pogo doctor`
// checks the MACHINE, not one agent, and pogo resolves a provider per-spawn
// (the mixed-fleet capability from mg-b31b) — so a machine can be running
// several harnesses at once, and narrowing to the configured default would
// under-report exactly the indexes most likely to be missed. Globbing a root
// that does not exist costs nothing: it contributes no matches.
func MemoryIndexGlobs() []string {
	var globs []string
	for _, p := range All() {
		globs = append(globs, p.MemoryIndexGlobs...)
	}
	return globs
}

// Resolve maps a config provider id to its agent.Provider descriptor.
//
// "" and "claude" resolve to Claude (the default); "codex" resolves to Codex;
// "pi" resolves to pi; "cursor" resolves to the Cursor CLI.
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
	case pi.Provider.ID:
		return &pi.Provider, true
	case cursor.Provider.ID:
		return &cursor.Provider, true
	default:
		return &claude.Provider, false
	}
}
