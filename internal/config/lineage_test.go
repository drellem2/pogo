package config

import (
	"path/filepath"
	"testing"
)

// TestLineageDefaults: with no [lineage] block the prompt reference is the
// historical one — deploy checkout (empty repo, resolved by the caller),
// origin/main, internal/agent/prompts — and it is NOT declared. Undeclared is
// what lets check-staleness and the sweep hedge (drellem2/pogo#125).
func TestLineageDefaults(t *testing.T) {
	layeredSandbox(t)

	cfg := Load()

	if cfg.Lineage.PromptRepo != "" {
		t.Errorf("prompt_repo = %q, want empty (the deploy checkout, resolved by the caller)", cfg.Lineage.PromptRepo)
	}
	if cfg.Lineage.PromptRef != "origin/main" {
		t.Errorf("prompt_ref = %q, want origin/main", cfg.Lineage.PromptRef)
	}
	if cfg.Lineage.PromptSubtree != "internal/agent/prompts" {
		t.Errorf("prompt_subtree = %q, want internal/agent/prompts", cfg.Lineage.PromptSubtree)
	}
	if cfg.Lineage.PromptDeclared {
		t.Error("no [lineage] block was written, yet the lineage reads as declared — every report would lose its hedge")
	}
}

// TestLineageDeclared: each key is read, ~ is expanded, the block counts as a
// declaration, and pogod's sweep reads the SAME ref (PromptStale.Ref), so the
// command and the daemon cannot judge against different references.
func TestLineageDeclared(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[lineage]\nprompt_repo = \"~/src/org-dotpogo\"\nprompt_ref = \"origin/trunk\"\nprompt_subtree = \"agents\"\n")

	cfg := Load()

	if want := filepath.Join(homeDirForTest(t), "src", "org-dotpogo"); cfg.Lineage.PromptRepo != want {
		t.Errorf("prompt_repo = %q, want %q (~ expanded)", cfg.Lineage.PromptRepo, want)
	}
	if cfg.Lineage.PromptRef != "origin/trunk" {
		t.Errorf("prompt_ref = %q, want origin/trunk", cfg.Lineage.PromptRef)
	}
	if cfg.Lineage.PromptSubtree != "agents" {
		t.Errorf("prompt_subtree = %q, want agents", cfg.Lineage.PromptSubtree)
	}
	if !cfg.Lineage.PromptDeclared {
		t.Error("a [lineage] block did not read as declared")
	}
	if cfg.PromptStale.Ref != "origin/trunk" {
		t.Errorf("[prompt_stale] resolved ref = %q, want the lineage's origin/trunk — the sweep and the command would disagree", cfg.PromptStale.Ref)
	}
}

// TestLineageSubtreeAloneDeclares: naming only where the corpus lives is still
// the operator naming its upstream's layout.
func TestLineageSubtreeAloneDeclares(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[lineage]\nprompt_subtree = \"agents\"\n")

	cfg := Load()
	if !cfg.Lineage.PromptDeclared {
		t.Error("prompt_subtree alone did not declare the lineage")
	}
	if cfg.Lineage.PromptRef != "origin/main" {
		t.Errorf("prompt_ref = %q, want the default origin/main", cfg.Lineage.PromptRef)
	}
}

// TestLineageRefFallsBackToPromptStaleRef: an existing config that set
// [prompt_stale] ref keeps working — the lineage ref inherits it — and setting
// it does NOT declare a lineage, because a ref moves along an upstream without
// naming one.
func TestLineageRefFallsBackToPromptStaleRef(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_stale]\nref = \"origin/release\"\n")

	cfg := Load()
	if cfg.Lineage.PromptRef != "origin/release" {
		t.Errorf("prompt_ref = %q, want origin/release inherited from [prompt_stale] ref", cfg.Lineage.PromptRef)
	}
	if cfg.Lineage.PromptDeclared {
		t.Error("[prompt_stale] ref declared a lineage; it names a ref, not an upstream")
	}
}

// TestLineageRefWinsOverPromptStaleRef: when both are set the lineage is the
// declaration, and the sweep follows it.
func TestLineageRefWinsOverPromptStaleRef(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_stale]\nref = \"origin/release\"\n\n[lineage]\nprompt_ref = \"origin/trunk\"\n")

	cfg := Load()
	if cfg.Lineage.PromptRef != "origin/trunk" || cfg.PromptStale.Ref != "origin/trunk" {
		t.Errorf("lineage=%q prompt_stale=%q, want both origin/trunk", cfg.Lineage.PromptRef, cfg.PromptStale.Ref)
	}
}

func homeDirForTest(t *testing.T) string {
	t.Helper()
	return expandTildePath("~")
}
