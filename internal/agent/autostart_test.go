package agent

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// writePrompt is a small helper for tests that need a prompt file with
// specific content under a given category.
func writePrompt(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write prompt %s: %v", path, err)
	}
	return path
}

// TestAutoStartAgents_StartsOnlyFlaggedPrompts verifies that AutoStartAgents
// spawns agents whose prompt declares auto_start = true and ignores the rest.
func TestAutoStartAgents_StartsOnlyFlaggedPrompts(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}

	// One mayor with auto_start=true, one crew with auto_start=true, one
	// crew without it, and a polecat template that should never trigger
	// auto-start regardless of frontmatter.
	writePrompt(t, PromptDir(), "mayor", "+++\nauto_start = true\n+++\n# mayor\n")
	writePrompt(t, CrewPromptDir(), "scout", "+++\nauto_start = true\n+++\n# scout\n")
	writePrompt(t, CrewPromptDir(), "lurker", "+++\nauto_start = false\n+++\n# lurker\n")
	writePrompt(t, TemplateDir(), "polecat", "+++\nauto_start = true\n+++\n# polecat template\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	results := reg.AutoStartAgents()

	// Index results by name for assertions.
	got := map[string]AutoStartStatus{}
	for _, r := range results {
		got[r.Name] = r.Status
	}

	if got["mayor"] != AutoStartStatusStarted {
		t.Errorf("mayor status = %q, want %q (results=%v)", got["mayor"], AutoStartStatusStarted, results)
	}
	if got["scout"] != AutoStartStatusStarted {
		t.Errorf("scout status = %q, want %q", got["scout"], AutoStartStatusStarted)
	}
	if got["lurker"] != AutoStartStatusSkippedNoFlag {
		t.Errorf("lurker status = %q, want %q", got["lurker"], AutoStartStatusSkippedNoFlag)
	}
	// Templates must never appear in the auto-start scan: they are polecat
	// scaffolds, not crew agents.
	if _, ok := got["polecat"]; ok {
		t.Errorf("polecat template should be skipped entirely; got status %q", got["polecat"])
	}

	// And the registry should reflect both started agents.
	if reg.Get("mayor") == nil {
		t.Error("mayor not registered after auto-start")
	}
	if reg.Get("scout") == nil {
		t.Error("scout not registered after auto-start")
	}
	if reg.Get("lurker") != nil {
		t.Error("lurker should not have been started")
	}
}

// TestAutoStartAgents_Idempotent verifies that running auto-start twice (e.g.
// after a pogod restart-while-running) does not double-start an agent.
func TestAutoStartAgents_Idempotent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writePrompt(t, PromptDir(), "mayor", "+++\nauto_start = true\n+++\n# mayor\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	first := reg.AutoStartAgents()
	if len(first) != 1 || first[0].Status != AutoStartStatusStarted {
		t.Fatalf("first scan = %+v, want one started entry", first)
	}
	originalPID := reg.Get("mayor").PID

	// Second call must not respawn the running agent.
	second := reg.AutoStartAgents()
	if len(second) != 1 || second[0].Status != AutoStartStatusSkippedRunning {
		t.Fatalf("second scan = %+v, want one skipped_running entry", second)
	}
	if got := reg.Get("mayor"); got == nil || got.PID != originalPID {
		t.Errorf("mayor PID changed after second scan: original=%d got=%v", originalPID, got)
	}
}

// TestAutoStartAgents_NoPromptDir verifies the scan returns no results (and
// does not panic) when ~/.pogo/agents/ does not exist.
func TestAutoStartAgents_NoPromptDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	if results := reg.AutoStartAgents(); len(results) != 0 {
		t.Errorf("AutoStartAgents on empty home = %+v, want no entries", results)
	}
}

// TestAutoStartAgents_AlphabeticalOrder verifies that prompts are processed
// in alphabetical order by name. Order isn't load-bearing for correctness,
// but a stable order keeps logs and tests predictable.
func TestAutoStartAgents_AlphabeticalOrder(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	// Write in non-alphabetical order to make sure ListPrompts/sort is what's
	// driving the order rather than insertion order.
	writePrompt(t, CrewPromptDir(), "zeta", "+++\nauto_start = true\n+++\n")
	writePrompt(t, CrewPromptDir(), "alpha", "+++\nauto_start = true\n+++\n")
	writePrompt(t, PromptDir(), "mayor", "+++\nauto_start = true\n+++\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	results := reg.AutoStartAgents()
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(results), results)
	}
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	want := []string{"alpha", "mayor", "zeta"}
	if !sort.StringsAreSorted(names) {
		t.Errorf("results not sorted: got %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("results[%d] = %q, want %q (full: %v)", i, names[i], want[i], names)
		}
	}
}

// TestAutoStartAgents_NoFrontmatterDoesNotStart verifies prompts without any
// frontmatter (i.e. the historical default) are not auto-started — the
// frontmatter must explicitly opt in.
func TestAutoStartAgents_NoFrontmatterDoesNotStart(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writePrompt(t, PromptDir(), "mayor", "# mayor with no frontmatter\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	results := reg.AutoStartAgents()
	if len(results) != 1 || results[0].Status != AutoStartStatusSkippedNoFlag {
		t.Fatalf("results = %+v, want one skipped_no_flag entry", results)
	}
	if reg.Get("mayor") != nil {
		t.Error("mayor should not be running without auto_start = true")
	}
}
