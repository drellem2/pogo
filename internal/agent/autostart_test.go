package agent

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
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
	testsandbox.Isolate(t)

	// Pin a coordinator name that differs from the mayor.md file stem, so the
	// "registered under the display name, not the file stem" assertion below
	// keeps discriminating no matter what the shipped default happens to be.
	// Under the mg-2c17 default ("mayor") the two coincide, and asserting on
	// the default alone would pass even if the code keyed off the stem.
	setCoordinator(t, "ringmaster")

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

	reg, err := NewRegistry(shortSocketDir(t))
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

	// The top-level mayor.md prompt is registered under the coordinator's
	// display name (pinned to "ringmaster" above), not the file stem.
	if got["ringmaster"] != AutoStartStatusStarted {
		t.Errorf("coordinator status = %q, want %q (results=%v)", got["ringmaster"], AutoStartStatusStarted, results)
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
	if reg.Get("ringmaster") == nil {
		t.Error("coordinator not registered after auto-start")
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
	testsandbox.Isolate(t)
	setCoordinator(t, "ringmaster") // display name ≠ file stem; see the test above

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writePrompt(t, PromptDir(), "mayor", "+++\nauto_start = true\n+++\n# mayor\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	first := reg.AutoStartAgents()
	if len(first) != 1 || first[0].Status != AutoStartStatusStarted {
		t.Fatalf("first scan = %+v, want one started entry", first)
	}
	originalPID := reg.Get("ringmaster").PID

	// Second call must not respawn the running agent.
	second := reg.AutoStartAgents()
	if len(second) != 1 || second[0].Status != AutoStartStatusSkippedRunning {
		t.Fatalf("second scan = %+v, want one skipped_running entry", second)
	}
	if got := reg.Get("ringmaster"); got == nil || got.PID != originalPID {
		t.Errorf("coordinator PID changed after second scan: original=%d got=%v", originalPID, got)
	}
}

// TestAutoStartAgents_SkipsParked verifies a park flag wins over
// auto_start = true: parked agents stay dormant across pogod restarts until
// explicitly woken (mg-41e1).
func TestAutoStartAgents_SkipsParked(t *testing.T) {
	testsandbox.Isolate(t)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writePrompt(t, CrewPromptDir(), "napper", "+++\nauto_start = true\n+++\n# napper\n")
	if err := writeParkState(&ParkState{Name: "napper", ParkedAt: time.Now()}); err != nil {
		t.Fatalf("writeParkState: %v", err)
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	results := reg.AutoStartAgents()
	if len(results) != 1 || results[0].Status != AutoStartStatusSkippedParked {
		t.Fatalf("results = %+v, want one skipped_parked entry", results)
	}
	if reg.Get("napper") != nil {
		t.Error("parked agent must not be auto-started")
	}
}

// TestAutoStartAgents_NoPromptDir verifies the scan returns no results (and
// does not panic) when ~/.pogo/agents/ does not exist.
func TestAutoStartAgents_NoPromptDir(t *testing.T) {
	testsandbox.Isolate(t)

	reg, err := NewRegistry(shortSocketDir(t))
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
	testsandbox.Isolate(t)
	setCoordinator(t, "ringmaster") // display name ≠ file stem; see the test above

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	// Write in non-alphabetical order to make sure ListPrompts/sort is what's
	// driving the order rather than insertion order.
	writePrompt(t, CrewPromptDir(), "zeta", "+++\nauto_start = true\n+++\n")
	writePrompt(t, CrewPromptDir(), "alpha", "+++\nauto_start = true\n+++\n")
	writePrompt(t, PromptDir(), "mayor", "+++\nauto_start = true\n+++\n")

	reg, err := NewRegistry(shortSocketDir(t))
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
	want := []string{"alpha", "ringmaster", "zeta"}
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
	testsandbox.Isolate(t)
	setCoordinator(t, "ringmaster") // display name ≠ file stem; see the test above

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writePrompt(t, PromptDir(), "mayor", "# mayor with no frontmatter\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	results := reg.AutoStartAgents()
	if len(results) != 1 || results[0].Status != AutoStartStatusSkippedNoFlag {
		t.Fatalf("results = %+v, want one skipped_no_flag entry", results)
	}
	if reg.Get("ringmaster") != nil {
		t.Error("coordinator should not be running without auto_start = true")
	}
}

// TestAutoStartAgents_ConcurrentSweepsReportSkippedNotFailed covers the race the
// sweep's own comment claimed to handle and did not.
//
// The guard in AutoStartAgents is a check-then-act — r.Get(name), then
// StartCrewAgent — and it was never atomic. That was harmless while the only
// caller was pogod's boot, which runs the sweep exactly once. It stopped being
// harmless in mg-060c: `pogo server start` now sweeps too, and a start issued
// against a daemon that is still booting runs the two sweeps concurrently. The
// loser used to report the agent it found already up as FAILED, and
// AgentsFailed is what decides the CLI's exit code — so recovering the fleet
// would have exited non-zero and named a healthy mayor as a casualty.
func TestAutoStartAgents_ConcurrentSweepsReportSkippedNotFailed(t *testing.T) {
	testsandbox.Isolate(t)
	setCoordinator(t, "ringmaster")

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writePrompt(t, PromptDir(), "mayor", "+++\nauto_start = true\n+++\n# mayor\n")
	writePrompt(t, CrewPromptDir(), "scout", "+++\nauto_start = true\n+++\n# scout\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(5 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	const sweeps = 4
	var wg sync.WaitGroup
	all := make([][]AutoStartResult, sweeps)
	for i := range sweeps {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			all[i] = reg.AutoStartAgents()
		}(i)
	}
	wg.Wait()

	started := map[string]int{}
	for _, results := range all {
		for _, r := range results {
			if r.Status == AutoStartStatusFailed {
				t.Errorf("concurrent sweep reported %s as FAILED (%s); losing the check-then-act "+
					"race means someone else started it, which is the outcome the sweep wanted",
					r.Name, r.Error)
			}
			if r.Status == AutoStartStatusStarted {
				started[r.Name]++
			}
		}
	}

	// Exactly one sweep may claim each agent: the rest must see skipped_running.
	for _, name := range []string{"ringmaster", "scout"} {
		if started[name] != 1 {
			t.Errorf("%s reported started by %d sweeps, want exactly 1", name, started[name])
		}
		if reg.Get(name) == nil {
			t.Errorf("%s is not registered after %d concurrent sweeps", name, sweeps)
		}
	}
}
