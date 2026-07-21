package providers

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveClaude(t *testing.T) {
	p, ok := Resolve("claude")
	if !ok {
		t.Fatal("Resolve(\"claude\") returned ok=false")
	}
	if p == nil || p.ID != "claude" {
		t.Fatalf("Resolve(\"claude\") = %+v, want provider with ID=claude", p)
	}
	if p.Binary != "claude" {
		t.Errorf("claude provider Binary = %q, want %q", p.Binary, "claude")
	}
}

func TestResolveCodex(t *testing.T) {
	p, ok := Resolve("codex")
	if !ok {
		t.Fatal("Resolve(\"codex\") returned ok=false")
	}
	if p == nil || p.ID != "codex" {
		t.Fatalf("Resolve(\"codex\") = %+v, want provider with ID=codex", p)
	}
	if p.Binary != "codex" {
		t.Errorf("codex provider Binary = %q, want %q", p.Binary, "codex")
	}
}

func TestResolvePi(t *testing.T) {
	p, ok := Resolve("pi")
	if !ok {
		t.Fatal("Resolve(\"pi\") returned ok=false")
	}
	if p == nil || p.ID != "pi" {
		t.Fatalf("Resolve(\"pi\") = %+v, want provider with ID=pi", p)
	}
	if p.Binary != "pi" {
		t.Errorf("pi provider Binary = %q, want %q", p.Binary, "pi")
	}
}

// TestResolveCursor pins the Cursor id -> descriptor mapping. Note the id and
// the binary differ: the config key is "cursor", but the CLI command is
// "agent" (renamed from cursor-agent in 2026).
func TestResolveCursor(t *testing.T) {
	p, ok := Resolve("cursor")
	if !ok {
		t.Fatal("Resolve(\"cursor\") returned ok=false")
	}
	if p == nil || p.ID != "cursor" {
		t.Fatalf("Resolve(\"cursor\") = %+v, want provider with ID=cursor", p)
	}
	if p.Binary != "agent" {
		t.Errorf("cursor provider Binary = %q, want %q", p.Binary, "agent")
	}
}

// TestAllProvidersHaveDistinctIDs guards the Resolve switch: two providers
// sharing an ID would make one of them unreachable by config.
func TestAllProvidersHaveDistinctIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All() {
		if p == nil {
			t.Fatal("All() contains a nil provider")
		}
		if seen[p.ID] {
			t.Errorf("duplicate provider ID %q in All()", p.ID)
		}
		seen[p.ID] = true
	}
}

// TestAllProvidersResolveByID keeps All() and Resolve() in lockstep: every
// descriptor All() advertises must be reachable through the config id, and
// Resolve must hand back that exact descriptor pointer.
func TestAllProvidersResolveByID(t *testing.T) {
	for _, p := range All() {
		got, ok := Resolve(p.ID)
		if !ok {
			t.Errorf("Resolve(%q) returned ok=false, but %q is in All()", p.ID, p.ID)
			continue
		}
		if got != p {
			t.Errorf("Resolve(%q) = %p, want the All() descriptor %p", p.ID, got, p)
		}
	}
}

func TestAllContainsEveryProvider(t *testing.T) {
	all := All()
	want := []string{"claude", "codex", "pi", "cursor"}
	if len(all) != len(want) {
		t.Fatalf("All() returned %d providers, want %d", len(all), len(want))
	}
	for i, id := range want {
		if all[i] == nil || all[i].ID != id {
			t.Errorf("All()[%d].ID = %v, want %q", i, all[i], id)
		}
	}
}

func TestResolveEmptyDefaultsToClaude(t *testing.T) {
	p, ok := Resolve("")
	if !ok {
		t.Fatal("Resolve(\"\") returned ok=false; empty id should default to Claude")
	}
	if p == nil || p.ID != "claude" {
		t.Fatalf("Resolve(\"\") = %+v, want the Claude default", p)
	}
}

func TestResolveUnknownFallsBackToClaude(t *testing.T) {
	p, ok := Resolve("nonesuch")
	if ok {
		t.Error("Resolve(\"nonesuch\") returned ok=true for an unknown provider")
	}
	if p == nil || p.ID != "claude" {
		t.Fatalf("Resolve(\"nonesuch\") = %+v, want the Claude fallback so startup never wedges", p)
	}
}

// TestMemoryIndexGlobsCarriesClaudeRoot: the Claude auto-memory root must still
// be checked after being moved out of internal/memcheck — a refactor that
// silently dropped coverage would look identical to one that preserved it.
func TestMemoryIndexGlobsCarriesClaudeRoot(t *testing.T) {
	globs := MemoryIndexGlobs()
	if len(globs) == 0 {
		t.Fatal("MemoryIndexGlobs() is empty — no harness declares a memory root, so doctor checks none")
	}
	want := filepath.Join(".claude", "projects", "*", "memory", "MEMORY.md")
	for _, g := range globs {
		if g == want {
			return
		}
	}
	t.Fatalf("Claude's auto-memory root %q is no longer declared by any provider; got %v", want, globs)
}

// TestMemoryIndexGlobsAreHomeRelative pins the contract memcheck.Locate relies
// on: globs join UNDER home. An absolute glob would escape the caller's home
// and, in tests, escape t.TempDir() to hit the real user's files.
func TestMemoryIndexGlobsAreHomeRelative(t *testing.T) {
	for _, g := range MemoryIndexGlobs() {
		if filepath.IsAbs(g) {
			t.Errorf("glob %q is absolute; MemoryIndexGlobs must be home-relative", g)
		}
		if strings.HasPrefix(g, "~") {
			t.Errorf("glob %q starts with ~; expansion is the caller's job, so this would never match", g)
		}
	}
}

// TestEveryProviderDeclaresMemoryIntent: a provider with no memory root must
// say so by construction rather than by omission. All four are enumerated here
// so ADDING a provider forces a decision instead of silently defaulting to
// "not checked" — the failure mode this whole change exists to remove.
func TestEveryProviderDeclaresMemoryIntent(t *testing.T) {
	// id -> whether it is expected to declare at least one memory root.
	want := map[string]bool{
		"claude": true,  // per-project auto-memory index
		"codex":  false, // no MEMORY.md index (measured 2026-07-21)
		"pi":     false, // no MEMORY.md index (measured 2026-07-21)
		"cursor": false, // no MEMORY.md index (measured 2026-07-21)
	}
	all := All()
	if len(all) != len(want) {
		t.Fatalf("All() has %d providers but this test enumerates %d — a new provider must declare whether it ships an auto-memory index", len(all), len(want))
	}
	for _, p := range all {
		expect, known := want[p.ID]
		if !known {
			t.Errorf("provider %q is not enumerated here; declare whether it ships an auto-memory index", p.ID)
			continue
		}
		if got := len(p.MemoryIndexGlobs) > 0; got != expect {
			t.Errorf("provider %q declares memory globs = %v (%v), want %v", p.ID, p.MemoryIndexGlobs, got, expect)
		}
	}
}
