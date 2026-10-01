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

// TestLineageRunnerDefaults: with no runner_* key the runner reference is
// drellem2/pogo's — the deploy checkout (empty repo, resolved by the caller),
// origin/main, scripts/launchd/pogo-deploy.sh — and it is NOT declared, so no
// install refuses and no audit compares against anything but this build
// (drellem2/pogo#126).
func TestLineageRunnerDefaults(t *testing.T) {
	layeredSandbox(t)

	cfg := Load()

	if cfg.Lineage.RunnerRepo != "" {
		t.Errorf("runner_repo = %q, want empty (the deploy checkout, resolved by the caller)", cfg.Lineage.RunnerRepo)
	}
	if cfg.Lineage.RunnerRef != "origin/main" {
		t.Errorf("runner_ref = %q, want origin/main", cfg.Lineage.RunnerRef)
	}
	if cfg.Lineage.RunnerPath != "scripts/launchd/pogo-deploy.sh" {
		t.Errorf("runner_path = %q, want scripts/launchd/pogo-deploy.sh", cfg.Lineage.RunnerPath)
	}
	if cfg.Lineage.RunnerDeclared || cfg.Lineage.Declared() {
		t.Error("no [lineage] block was written, yet the runner lineage reads as declared")
	}
}

// TestLineageRunnerDeclared: each runner key is read, ~ is expanded, and the
// runner declaration is independent of the prompt one in both directions.
func TestLineageRunnerDeclared(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[lineage]\nrunner_repo = \"~/src/org-dotpogo\"\nrunner_ref = \"origin/trunk\"\nrunner_path = \"bin/pogo-deploy.sh\"\n")

	cfg := Load()

	if want := filepath.Join(homeDirForTest(t), "src", "org-dotpogo"); cfg.Lineage.RunnerRepo != want {
		t.Errorf("runner_repo = %q, want %q (~ expanded)", cfg.Lineage.RunnerRepo, want)
	}
	if cfg.Lineage.RunnerRef != "origin/trunk" {
		t.Errorf("runner_ref = %q, want origin/trunk", cfg.Lineage.RunnerRef)
	}
	if cfg.Lineage.RunnerPath != "bin/pogo-deploy.sh" {
		t.Errorf("runner_path = %q, want bin/pogo-deploy.sh", cfg.Lineage.RunnerPath)
	}
	if !cfg.Lineage.RunnerDeclared || !cfg.Lineage.Declared() {
		t.Errorf("runner keys did not declare: RunnerDeclared=%v Declared()=%v", cfg.Lineage.RunnerDeclared, cfg.Lineage.Declared())
	}
	if cfg.Lineage.PromptDeclared {
		t.Error("runner keys declared the PROMPT lineage; the two artifacts may have different upstreams")
	}
	if cfg.Lineage.PromptRef != "origin/main" {
		t.Errorf("prompt_ref = %q; a runner_ref leaked into the prompt reference", cfg.Lineage.PromptRef)
	}
}

// TestLineagePromptKeysDoNotDeclareTheRunner: a host that names only its prompt
// upstream still runs drellem2/pogo's runner, so install-deploy must not start
// refusing — but Declared() covers both.
func TestLineagePromptKeysDoNotDeclareTheRunner(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[lineage]\nprompt_repo = \"~/src/org-dotpogo\"\nprompt_ref = \"origin/trunk\"\n")

	cfg := Load()
	if cfg.Lineage.RunnerDeclared {
		t.Error("prompt keys declared the runner lineage")
	}
	if cfg.Lineage.RunnerRef != "origin/main" {
		t.Errorf("runner_ref = %q, want origin/main — it must not inherit prompt_ref", cfg.Lineage.RunnerRef)
	}
	if !cfg.Lineage.Declared() {
		t.Error("Declared() = false with prompt keys set")
	}
}

// TestLineageRunnerPathAloneDeclares: naming only where the runner lives is
// still a declaration.
func TestLineageRunnerPathAloneDeclares(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[lineage]\nrunner_path = \"bin/pogo-deploy.sh\"\n")

	cfg := Load()
	if !cfg.Lineage.RunnerDeclared {
		t.Error("runner_path alone did not declare the runner lineage")
	}
	if cfg.Lineage.RunnerRef != "origin/main" || cfg.Lineage.RunnerRepo != "" {
		t.Errorf("repo=%q ref=%q, want the defaults", cfg.Lineage.RunnerRepo, cfg.Lineage.RunnerRef)
	}
}
