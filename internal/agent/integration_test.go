package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMayorPromptResolution verifies that the mayor prompt resolves correctly
// when installed to ~/.pogo/agents/mayor.md.
func TestMayorPromptResolution(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Before install, should fail
	_, err := ResolveMayorPrompt()
	if err == nil {
		t.Error("expected error before install")
	}

	// Install prompts
	result, err := InstallPrompts(InstallOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// Should have installed mayor.md and templates/polecat.md
	var hasMayor, hasPolecat bool
	for _, f := range result.Installed {
		if f == "mayor.md" {
			hasMayor = true
		}
		if f == filepath.Join("templates", "polecat.md") {
			hasPolecat = true
		}
	}
	if !hasMayor {
		t.Error("mayor.md not installed")
	}
	if !hasPolecat {
		t.Error("templates/polecat.md not installed")
	}

	// Now resolution should succeed
	path, err := ResolveMayorPrompt()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The raw installed file carries the coordinator placeholder; the
	// synthesis pass resolves it to the configured name at spawn time.
	if !strings.Contains(string(data), "You are the {{.Coordinator}}") {
		t.Error("mayor prompt content missing expected text")
	}
}

// TestInstallPromptsIdempotent verifies that install skips existing files.
func TestInstallPromptsIdempotent(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// First install
	r1, err := InstallPrompts(InstallOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Installed) == 0 {
		t.Fatal("first install should install files")
	}

	// Second install should skip
	r2, err := InstallPrompts(InstallOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Installed) != 0 {
		t.Errorf("second install should not install files, got %v", r2.Installed)
	}
	if len(r2.Skipped) == 0 {
		t.Error("second install should report skipped files")
	}
}

// TestInstallPromptsForce verifies --force overwrites existing files.
func TestInstallPromptsForce(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// First install
	InstallPrompts(InstallOpts{})

	// Modify a file
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	os.WriteFile(mayorPath, []byte("custom mayor"), 0644)

	// Force install should overwrite
	r, err := InstallPrompts(InstallOpts{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Installed) == 0 {
		t.Error("force install should install files")
	}

	// Verify content was restored
	data, _ := os.ReadFile(mayorPath)
	if strings.Contains(string(data), "custom mayor") {
		t.Error("force install should have overwritten custom content")
	}
}

// TestPolecatTemplateExpansion verifies the shipped polecat template
// expands correctly with work item variables.
func TestPolecatTemplateExpansion(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	InstallPrompts(InstallOpts{})

	tmplPath, err := ResolveTemplate("polecat")
	if err != nil {
		t.Fatal(err)
	}

	vars := TemplateVars{
		Task: "Fix auth token expiry",
		Body: "OAuth tokens expire after 5 minutes instead of 1 hour.",
		Id:   "gt-a3f",
		Repo: "/home/user/projects/myapp",
	}

	expanded, err := ExpandTemplate(tmplPath, vars)
	if err != nil {
		t.Fatal(err)
	}

	checks := []string{
		"Fix auth token expiry",
		"gt-a3f",
		"/home/user/projects/myapp",
		"OAuth tokens expire after 5 minutes",
		"mg claim gt-a3f",
		"mg done gt-a3f",
		"--target=main",
	}
	for _, check := range checks {
		if !strings.Contains(expanded, check) {
			t.Errorf("expanded template missing %q", check)
		}
	}

	// The template must NOT name the branch. This assertion used to demand
	// "polecat-gt-a3f" — pinning the mg-d39e defect in place: pogod names the
	// branch after the agent name, not the work item id, so a template built
	// from Id was wrong on every dispatch. The template now tells the polecat
	// to read its branch instead. See TestShippedTemplatesNeverNameTheBranch.
	if strings.Contains(expanded, "polecat-gt-a3f") {
		t.Errorf("expanded template names a branch built from Id (%q); "+
			"it must tell the polecat to read its branch instead", "polecat-gt-a3f")
	}
	if !strings.Contains(expanded, "git rev-parse --abbrev-ref HEAD") {
		t.Errorf("expanded template must teach the polecat to read its branch")
	}

	// Verify the mail-check schedule instruction is present. Polecats are not
	// on pogod's nudge cycle, so they need a mail-check schedule to proactively
	// check mail. Post-mg-2f79 this uses pogod's sleep-resilient `pogo schedule`
	// rather than Claude's in-process `CronCreate`. Post-mg-e633 spawn-polecat
	// auto-registers it under the polecat's bare registry name, so the template
	// instruction addresses `$POGO_AGENT_NAME` (matching the spawn-registered
	// entry, keeping it idempotent) rather than the event identity.
	scheduleChecks := []string{
		"pogo schedule $POGO_AGENT_NAME", // pogod scheduler CLI, bare agent name
		"--cron \"*/10 * * * *\"",        // 10-minute cadence
		"--id mail-check-gt-a3f",         // idempotent registration key (work item id)
		"mg mail list gt-a3f",            // expanded message body
	}
	for _, check := range scheduleChecks {
		if !strings.Contains(expanded, check) {
			t.Errorf("expanded template missing mail-check schedule instruction %q", check)
		}
	}

	// Verify guidance against additional schedules is still present — only the
	// one mail-check schedule is allowed.
	if !strings.Contains(expanded, "One mail-check schedule only") {
		t.Errorf("expanded template missing guidance limiting background triggers to the mail-check schedule")
	}

	// `CronCreate` should still be mentioned, but only as the documented
	// ephemeral-in-session option that is explicitly NOT for the mail-check
	// loop. Catch a regression where someone re-introduces it as the primary
	// mechanism by checking for the ephemeral guidance heading.
	if !strings.Contains(expanded, "ephemeral") {
		t.Errorf("expanded template missing CronCreate-as-ephemeral guidance")
	}
}

// TestMayorStartSpawnPolecat is an end-to-end test that verifies:
// 1. Mayor can be started as a crew agent
// 2. A polecat can be spawned with template expansion
// 3. Both agents run with correct env vars and prompt files
// 4. Polecat can be stopped and cleaned up
//
// This tests the spawn/lifecycle path without requiring macguffin or the refinery.
func TestMayorStartSpawnPolecat(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Install prompts
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatal(err)
	}

	// Use /tmp for socket dir to keep paths short
	socketDir, err := os.MkdirTemp("/tmp", "pogo-e2e-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(3 * time.Second)

	// 1. Start "mayor" as a crew agent using its prompt file
	mayorPrompt, err := ResolveMayorPrompt()
	if err != nil {
		t.Fatal(err)
	}
	// Use 'cat' as a stand-in for 'claude' since we're testing the lifecycle, not the LLM
	mayor, err := reg.Spawn(SpawnRequest{
		Name:       "mayor",
		Type:       TypeCrew,
		Command:    []string{"cat"},
		PromptFile: mayorPrompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mayor.Type != TypeCrew {
		t.Errorf("mayor type = %q, want crew", mayor.Type)
	}
	if mayor.PromptFile != mayorPrompt {
		t.Errorf("mayor prompt = %q, want %q", mayor.PromptFile, mayorPrompt)
	}
	if ProcessName(mayor.Type, mayor.Name) != "pogo-crew-mayor" {
		t.Errorf("mayor process name = %q", ProcessName(mayor.Type, mayor.Name))
	}

	// 2. Spawn a polecat from template (simulating what the mayor would do)
	tmplPath, err := ResolveTemplate("polecat")
	if err != nil {
		t.Fatal(err)
	}
	vars := TemplateVars{
		Task: "Fix auth bug",
		Body: "Tokens expire too early",
		Id:   "gt-abc",
		Repo: "/tmp/fakerepo",
	}
	polecatPrompt, err := ExpandTemplateToFile(tmplPath, vars)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(polecatPrompt)

	cat, err := reg.Spawn(SpawnRequest{
		Name:       "abc",
		Type:       TypePolecat,
		Command:    []string{"cat"},
		PromptFile: polecatPrompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Type != TypePolecat {
		t.Errorf("polecat type = %q, want polecat", cat.Type)
	}
	if ProcessName(cat.Type, cat.Name) != "pogo-cat-abc" {
		t.Errorf("polecat process name = %q", ProcessName(cat.Type, cat.Name))
	}

	// Verify the expanded prompt file contains the task
	data, err := os.ReadFile(polecatPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Fix auth bug") {
		t.Error("expanded polecat prompt missing task")
	}

	// 3. Both agents should be listed
	agents := reg.List()
	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}

	// 4. Stop polecat, verify mayor still running
	if err := reg.Stop("abc", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if reg.Get("abc") != nil {
		t.Error("polecat should be removed after stop")
	}
	if reg.Get("mayor") == nil {
		t.Error("mayor should still be running")
	}
}

// TestCrewRestartOnCrash verifies that crew agents (like the mayor) are
// restarted when they exit unexpectedly.
func TestCrewRestartOnCrash(t *testing.T) {
	// A restart contract asserted against the host's live park flags is not a
	// contract: a stray ~/.pogo/agents/crasher/.parked makes Respawn refuse and
	// this test reports a broken supervisor that is working fine (mg-e8e7).
	isolateParkState(t)
	socketDir, err := os.MkdirTemp("/tmp", "pogo-restart-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(3 * time.Second)

	restartCh := make(chan struct{}, 1)
	reg.SetOnExit(func(a *Agent, exitErr error) {
		if a.Type == TypeCrew {
			go func() {
				time.Sleep(100 * time.Millisecond)
				if _, rerr := reg.Respawn(a.Name); rerr == nil {
					restartCh <- struct{}{}
				}
			}()
		}
	})

	// Spawn a crew agent that exits immediately (simulating a crash)
	_, err = reg.Spawn(SpawnRequest{
		Name:    "crasher",
		Type:    TypeCrew,
		Command: []string{"true"}, // exits immediately
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the restart
	select {
	case <-restartCh:
		// Verify the respawned agent
		a := reg.Get("crasher")
		if a == nil {
			t.Fatal("agent should exist after respawn")
		}
		if a.RestartCount != 1 {
			t.Errorf("RestartCount = %d, want 1", a.RestartCount)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("crew agent was not restarted after crash")
	}
}

// TestPolecatCleanupOnExit verifies that polecat agents are cleaned up
// (not restarted) when they exit.
func TestPolecatCleanupOnExit(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "pogo-cleanup-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	cleanupCh := make(chan string, 1)
	reg.SetOnExit(func(a *Agent, exitErr error) {
		if a.Type == TypePolecat {
			a.Cleanup()
			reg.Remove(a.Name)
			cleanupCh <- a.Name
		}
	})

	_, err = reg.Spawn(SpawnRequest{
		Name:    "task-123",
		Type:    TypePolecat,
		Command: []string{"true"}, // exits immediately
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case name := <-cleanupCh:
		if name != "task-123" {
			t.Errorf("cleaned up %q, want task-123", name)
		}
		if reg.Get("task-123") != nil {
			t.Error("polecat should be removed from registry after cleanup")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("polecat was not cleaned up after exit")
	}
}

// TestRestartOnCrashFlagDrivesBranching verifies the lifecycle callback
// behavior switches on a.RestartOnCrash rather than a.Type. With the flag
// set, an exited agent is respawned regardless of type; without it, an
// exited agent is cleaned up regardless of type. This mirrors the production
// onExit logic in cmd/pogod/main.go.
func TestRestartOnCrashFlagDrivesBranching(t *testing.T) {
	// agent is spelled out per case rather than derived from name: a name over
	// config.MaxAgentNameLen is refused at spawn (mg-ef80), and these subtest
	// names are far longer than that.
	cases := []struct {
		name           string
		agent          string
		typ            AgentType
		restartOnCrash bool
		wantRestart    bool
	}{
		{"crew with restart=true is respawned", "roc-crew-on", TypeCrew, true, true},
		{"crew with restart=false stays down", "roc-crew-off", TypeCrew, false, false},
		{"polecat with restart=true is respawned", "roc-cat-on", TypePolecat, true, true},
		{"polecat with restart=false stays down (default)", "roc-cat-off", TypePolecat, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Both directions of this table need a clean park tree, and the
			// "stays down" rows need it most: a park flag also makes an agent
			// stay down, so without isolation those two rows can pass while the
			// RestartOnCrash branch they exist to pin is broken (mg-e8e7).
			isolateParkState(t)
			socketDir, err := os.MkdirTemp("/tmp", "pogo-roc-sock-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(socketDir)

			reg, err := NewRegistry(socketDir)
			if err != nil {
				t.Fatal(err)
			}
			defer reg.StopAll(2 * time.Second)

			restartCh := make(chan struct{}, 1)
			cleanupCh := make(chan struct{}, 1)
			reg.SetOnExit(func(a *Agent, exitErr error) {
				if a.RestartOnCrash {
					go func() {
						time.Sleep(50 * time.Millisecond)
						if _, rerr := reg.Respawn(a.Name); rerr == nil {
							restartCh <- struct{}{}
						}
					}()
				} else {
					a.Cleanup()
					reg.Remove(a.Name)
					cleanupCh <- struct{}{}
				}
			})

			a, err := reg.Spawn(SpawnRequest{
				Name:           tc.agent,
				Type:           tc.typ,
				Command:        []string{"true"},
				RestartOnCrash: tc.restartOnCrash,
			})
			if err != nil {
				t.Fatal(err)
			}
			if a.RestartOnCrash != tc.restartOnCrash {
				t.Errorf("Spawn did not propagate RestartOnCrash to agent: got %v want %v",
					a.RestartOnCrash, tc.restartOnCrash)
			}

			if tc.wantRestart {
				select {
				case <-restartCh:
					respawned := reg.Get(a.Name)
					if respawned == nil {
						t.Fatal("respawned agent missing from registry")
					}
					if !respawned.RestartOnCrash {
						t.Error("Respawn did not preserve RestartOnCrash")
					}
				case <-cleanupCh:
					t.Fatal("agent was cleaned up but should have been restarted")
				case <-time.After(2 * time.Second):
					t.Fatal("agent was not restarted within timeout")
				}
			} else {
				select {
				case <-cleanupCh:
					if reg.Get(a.Name) != nil {
						t.Error("agent should be removed from registry after cleanup")
					}
				case <-restartCh:
					t.Fatal("agent was restarted but should have stayed down")
				case <-time.After(2 * time.Second):
					t.Fatal("agent was not cleaned up within timeout")
				}
			}
		})
	}
}

// TestStopRespawnsRestartOnCrashAgent reproduces the architect bug from
// mg-dbf4: when an agent with RestartOnCrash=true is stopped (via Stop()
// or DELETE /agents/<name>), the OnExit hook scheduled a Respawn() but
// Stop() removed the agent from the registry first, so the respawn lost
// the race and the agent stayed dead. After the fix, Stop() leaves
// restart_on_crash agents in the registry so the supervisor can bring
// them back per the always-on contract.
func TestStopRespawnsRestartOnCrashAgent(t *testing.T) {
	// Stop's own branch is `RestartOnCrash && !IsParked(name)`, so the host's
	// park state decides which side of mg-dbf4's fix this test exercises.
	isolateParkState(t)
	socketDir, err := os.MkdirTemp("/tmp", "pogo-stop-restart-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	restartCh := make(chan int, 4)
	// Mirror the production OnExit hook from cmd/pogod/main.go: respawn
	// on any exit (clean, crash, or stop) when RestartOnCrash=true.
	reg.SetOnExit(func(a *Agent, exitErr error) {
		if !a.RestartOnCrash {
			a.Cleanup()
			reg.Remove(a.Name)
			return
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			respawned, rerr := reg.Respawn(a.Name)
			if rerr != nil {
				return
			}
			restartCh <- respawned.PID
		}()
	})

	// Use `cat` so the process stays alive until we send SIGTERM via Stop —
	// this isolates the Stop()/Respawn() race from a fast-exiting command.
	a, err := reg.Spawn(SpawnRequest{
		Name:           "stop-respawn",
		Type:           TypeCrew,
		Command:        []string{"cat"},
		RestartOnCrash: true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	originalPID := a.PID

	if err := reg.Stop("stop-respawn", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case newPID := <-restartCh:
		if newPID == originalPID {
			t.Errorf("respawn returned same PID %d as original — process not actually restarted", newPID)
		}
		respawned := reg.Get("stop-respawn")
		if respawned == nil {
			t.Fatal("respawned agent missing from registry")
		}
		if respawned.RestartCount != 1 {
			t.Errorf("RestartCount = %d, want 1", respawned.RestartCount)
		}
		if !respawned.RestartOnCrash {
			t.Error("Respawn did not preserve RestartOnCrash=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent was not respawned after Stop — supervisor failed restart_on_crash=true contract")
	}
}

// TestStopAllShutsDownRestartOnCrashAgent verifies that StopAll prevents
// in-flight respawn goroutines from resurrecting agents during teardown.
// Without the registry shutdown signal, a restart_on_crash agent stopped
// via StopAll would come back 2s later — bad on pogod shutdown and
// disastrous in test cleanup (infinite respawn loop with `true` commands).
func TestStopAllShutsDownRestartOnCrashAgent(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "pogo-stopall-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatal(err)
	}

	respawnAttempted := make(chan error, 4)
	reg.SetOnExit(func(a *Agent, exitErr error) {
		if !a.RestartOnCrash {
			a.Cleanup()
			reg.Remove(a.Name)
			return
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			_, rerr := reg.Respawn(a.Name)
			respawnAttempted <- rerr
		}()
	})

	if _, err := reg.Spawn(SpawnRequest{
		Name:           "stopall-restart",
		Type:           TypeCrew,
		Command:        []string{"cat"},
		RestartOnCrash: true,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	reg.StopAll(2 * time.Second)

	if a := reg.Get("stopall-restart"); a != nil {
		t.Errorf("agent still in registry after StopAll: %+v", a)
	}

	select {
	case rerr := <-respawnAttempted:
		if rerr == nil {
			t.Error("Respawn succeeded after StopAll — registry shutdown not honored")
		}
	case <-time.After(500 * time.Millisecond):
		// Goroutine may not have fired yet; that's fine — the important
		// invariant is that the agent is gone from the registry.
	}
}

func TestPolecatWorktreeDirRemovedOnExit(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "pogo-wtdir-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)

	// Create a fake worktree directory to simulate polecat isolation dir.
	fakeWtDir, err := os.MkdirTemp("/tmp", "pogo-fake-wt-")
	if err != nil {
		t.Fatal(err)
	}
	// Write a file so we can verify the dir is non-empty before removal.
	if err := os.WriteFile(filepath.Join(fakeWtDir, "dummy.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	cleanupCh := make(chan struct{}, 1)
	reg.SetOnExit(func(a *Agent, exitErr error) {
		if a.Type == TypePolecat && a.WorktreeDir != "" {
			// Mirror the pogod cleanup: os.RemoveAll as fallback.
			os.RemoveAll(a.WorktreeDir)
			a.Cleanup()
			reg.Remove(a.Name)
			cleanupCh <- struct{}{}
		}
	})

	_, err = reg.Spawn(SpawnRequest{
		Name:        "wt-cleanup-test",
		Type:        TypePolecat,
		Command:     []string{"true"}, // exits immediately
		WorktreeDir: fakeWtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-cleanupCh:
		if _, err := os.Stat(fakeWtDir); !os.IsNotExist(err) {
			t.Errorf("worktree dir %s should have been removed from disk", fakeWtDir)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("polecat was not cleaned up after exit")
	}
}
