package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpawnAndNudge(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "sockets")

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Spawn a simple cat process that echoes input
	agent, err := reg.Spawn(SpawnRequest{
		Name:    "test-agent",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if agent.PID == 0 {
		t.Error("expected non-zero PID")
	}
	if agent.Name != "test-agent" {
		t.Errorf("Name = %q, want %q", agent.Name, "test-agent")
	}
	if agent.Type != TypePolecat {
		t.Errorf("Type = %q, want %q", agent.Type, TypePolecat)
	}

	// Nudge: write to the agent's PTY
	err = agent.Nudge("hello")
	if err != nil {
		t.Fatalf("Nudge: %v", err)
	}

	// Give cat time to echo back through the PTY
	time.Sleep(200 * time.Millisecond)

	output := string(agent.RecentOutput(1024))
	if !strings.Contains(output, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", output)
	}
}

// TestNudgeSplitsBodyAndSubmit verifies that Nudge writes the message body
// and the trailing submit (\r) as two separate writes with the provider's
// SubmitDelay between them. Required so the receiver's input loop reads them in distinct
// read() calls — otherwise Claude Code's React/Ink input box treats the
// combined chunk as a paste and the \r becomes a literal newline inside the
// input field rather than a submit. Regression test for the bug where crew
// nudges sat unsent in architect's input box.
func TestNudgeSplitsBodyAndSubmit(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "split-nudge",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	start := time.Now()
	if err := a.Nudge("body"); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < DefaultNudgeProfile.SubmitDelay {
		t.Errorf("Nudge returned in %v; expected at least %v so body and submit land in separate read() calls",
			elapsed, DefaultNudgeProfile.SubmitDelay)
	}

	time.Sleep(200 * time.Millisecond)
	if got := string(a.RecentOutput(1024)); !strings.Contains(got, "body") {
		t.Errorf("expected output to contain 'body', got %q", got)
	}
}

// TestNudgeEmptyMessageNoDelay verifies that a submit-only nudge (empty body)
// skips the delay — there's nothing for the receiver to read separately, and
// the delay would just slow down the submit.
func TestNudgeEmptyMessageNoDelay(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "empty-nudge",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	start := time.Now()
	if err := a.Nudge(""); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed >= DefaultNudgeProfile.SubmitDelay {
		t.Errorf("Empty Nudge took %v; expected < %v (no body, no delay needed)",
			elapsed, DefaultNudgeProfile.SubmitDelay)
	}
}

// TestInitialNudgeAutoDelivers verifies that when SpawnRequest.InitialNudge
// is set, pogod auto-delivers the message once the agent's PTY goes idle —
// without any external 'pogo nudge' call. Regression test for the bug where
// a fixed 10s sleep made auto-nudge fire before Claude's TUI was ready,
// leaving polecats stuck until a manual nudge.
func TestInitialNudgeAutoDelivers(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// bash -c "echo ready; cat" prints "ready" (so PTY has lastWrite set,
	// allowing IsIdle to return true after quiescence) then echoes input.
	a, err := reg.Spawn(SpawnRequest{
		Name:         "auto-nudge",
		Type:         TypePolecat,
		Command:      []string{"bash", "-c", "echo ready; cat"},
		InitialNudge: "begin task",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// InitialNudge is stored on the agent so Respawn can re-send it.
	if a.InitialNudge != "begin task" {
		t.Errorf("InitialNudge = %q, want %q", a.InitialNudge, "begin task")
	}

	// Wait long enough for: PTY quiescence (DefaultNudgeProfile.IdleThreshold=2s) + delivery.
	// No external nudge — the auto-delivery in Spawn must fire on its own.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(a.RecentOutput(4096)), "begin task") {
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("initial nudge did not auto-deliver within 8s; output: %q", string(a.RecentOutput(4096)))
}

func TestSpawnDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	_, err = reg.Spawn(SpawnRequest{
		Name:    "dup",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("first Spawn: %v", err)
	}

	_, err = reg.Spawn(SpawnRequest{
		Name:    "dup",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err == nil {
		t.Error("expected error spawning duplicate agent")
	}
}

func TestListAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	_, err = reg.Spawn(SpawnRequest{Name: "a1", Type: TypeCrew, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.Spawn(SpawnRequest{Name: "a2", Type: TypePolecat, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}

	agents := reg.List()
	if len(agents) != 2 {
		t.Errorf("List() returned %d agents, want 2", len(agents))
	}

	if reg.Get("a1") == nil {
		t.Error("Get(a1) returned nil")
	}
	if reg.Get("nonexistent") != nil {
		t.Error("Get(nonexistent) should return nil")
	}
}

func TestStopAgent(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	_, err = reg.Spawn(SpawnRequest{Name: "stopper", Type: TypePolecat, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}

	err = reg.Stop("stopper", 2*time.Second)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if reg.Get("stopper") != nil {
		t.Error("agent should be removed after stop")
	}
}

func TestSocketPath(t *testing.T) {
	// Use /tmp directly to keep unix socket path under 108-char limit
	socketDir, err := os.MkdirTemp("/tmp", "pogo-test-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	agent, err := reg.Spawn(SpawnRequest{Name: "s1", Type: TypeCrew, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(socketDir, "s1.sock")
	if agent.SocketPath() != expected {
		t.Errorf("SocketPath() = %q, want %q", agent.SocketPath(), expected)
	}

	// Verify socket file exists
	if _, err := os.Stat(agent.SocketPath()); os.IsNotExist(err) {
		t.Error("socket file does not exist")
	}
}

func TestProcessExit(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Spawn a process that exits immediately
	agent, err := reg.Spawn(SpawnRequest{
		Name:    "short-lived",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for it to exit
	select {
	case <-agent.Done():
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit within 2 seconds")
	}
}

func TestProcessName(t *testing.T) {
	if got := ProcessName(TypeCrew, "arch"); got != "pogo-crew-arch" {
		t.Errorf("ProcessName(crew, arch) = %q, want %q", got, "pogo-crew-arch")
	}
	if got := ProcessName(TypePolecat, "abc123"); got != "pogo-cat-abc123" {
		t.Errorf("ProcessName(polecat, abc123) = %q, want %q", got, "pogo-cat-abc123")
	}
}

func TestEnvInjection(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Spawn a process that prints env vars and exits
	a, err := reg.Spawn(SpawnRequest{
		Name:       "env-test",
		Type:       TypeCrew,
		Command:    []string{"env"},
		PromptFile: "/tmp/test-prompt.md",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for it to exit and produce output
	select {
	case <-a.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit")
	}

	output := string(a.RecentOutput(8192))

	checks := []string{
		"POGO_AGENT_NAME=env-test",
		"POGO_AGENT_TYPE=crew",
		"POGO_PROCESS_NAME=pogo-crew-env-test",
		"POGO_AGENT_PROMPT=/tmp/test-prompt.md",
	}
	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("expected output to contain %q", check)
		}
	}
}

// TestSpawnContextFileInjection verifies that Spawn delivers the persona
// prompt as a context file when the active provider uses the InjectContextFile
// strategy (Codex's AGENTS.override.md). The file must land in the agent's
// working directory before the process starts.
func TestSpawnContextFileInjection(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// A context-file provider, shaped like codex.Provider (agent cannot import
	// internal/codex — that would be an import cycle). Passed per-spawn via
	// SpawnRequest.Provider, the path handleSpawnPolecat uses in production.
	codexProvider := &Provider{
		ID:     "codex-test",
		Binary: "codex",
		PromptInjection: PromptInjection{
			Kind:        InjectContextFile,
			ContextFile: "AGENTS.override.md",
		},
		Nudge: DefaultNudgeProfile,
	}

	const persona = "# pogo persona\noperating instructions for the agent\n"
	promptFile := filepath.Join(tmpDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	workDir := filepath.Join(tmpDir, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	_, err = reg.Spawn(SpawnRequest{
		Name:       "codex-inject",
		Type:       TypePolecat,
		Command:    []string{"cat"},
		PromptFile: promptFile,
		Dir:        workDir,
		Provider:   codexProvider,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(workDir, "AGENTS.override.md"))
	if err != nil {
		t.Fatalf("AGENTS.override.md not written into the worktree: %v", err)
	}
	if string(got) != persona {
		t.Errorf("AGENTS.override.md = %q, want %q", got, persona)
	}
}

// TestSpawnContextFileInjectionCrew verifies that ContextFile persona injection
// also fires for a crew agent — not just polecats. writeContextFilePrompt does
// not branch on agent type, so the Codex persona is delivered to crew agents
// the same way (AGENTS.override.md in the working directory). This is the crew
// half of Phase 3D (mg-6599) acceptance bar 2.
//
// Note: the multi-provider survey (§3 Phase 3B, §4 risk 4) anticipated a
// CODEX_HOME-based variant for crew so the persona never lands inside a
// checked-out repo's working tree. That variant was not implemented in 3B;
// crew injection currently uses the same in-directory AGENTS.override.md path
// as polecats. See docs/investigations/codex-e2e-validation.md.
func TestSpawnContextFileInjectionCrew(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	codexProvider := &Provider{
		ID:     "codex-test",
		Binary: "codex",
		PromptInjection: PromptInjection{
			Kind:        InjectContextFile,
			ContextFile: "AGENTS.override.md",
		},
		Nudge: DefaultNudgeProfile,
	}

	const persona = "# pogo crew persona\noperating instructions for the crew agent\n"
	promptFile := filepath.Join(tmpDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	workDir := filepath.Join(tmpDir, "crewdir")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	_, err = reg.Spawn(SpawnRequest{
		Name:       "codex-crew",
		Type:       TypeCrew,
		Command:    []string{"cat"},
		PromptFile: promptFile,
		Dir:        workDir,
		Provider:   codexProvider,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(workDir, "AGENTS.override.md"))
	if err != nil {
		t.Fatalf("AGENTS.override.md not written for a crew agent: %v", err)
	}
	if string(got) != persona {
		t.Errorf("AGENTS.override.md = %q, want %q", got, persona)
	}
}

// TestSpawnNoContextFileForClaude confirms the InjectContextFile path is inert
// for a flag-injection provider: no stray AGENTS.override.md is written.
func TestSpawnNoContextFileForClaude(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	claudeProvider := &Provider{
		ID:              "claude-test",
		Binary:          "claude",
		PromptInjection: PromptInjection{Kind: InjectAppendFlag, Flag: "--append-system-prompt-file"},
		Nudge:           DefaultNudgeProfile,
	}

	promptFile := filepath.Join(tmpDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("persona"), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	workDir := filepath.Join(tmpDir, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	_, err = reg.Spawn(SpawnRequest{
		Name:       "claude-inject",
		Type:       TypePolecat,
		Command:    []string{"cat"},
		PromptFile: promptFile,
		Dir:        workDir,
		Provider:   claudeProvider,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workDir, "AGENTS.override.md")); !os.IsNotExist(err) {
		t.Error("flag-injection provider should not write AGENTS.override.md")
	}
}

func TestAgentStatus(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Running agent
	a, err := reg.Spawn(SpawnRequest{
		Name:    "status-test",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := a.GetStatus(); s != StatusRunning {
		t.Errorf("Status = %q, want %q", s, StatusRunning)
	}

	// Stop it and check exited status
	if err := reg.Stop("status-test", 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestExitStatus(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Process that exits with code 0
	a, err := reg.Spawn(SpawnRequest{
		Name:    "exit-ok",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	a.mu.Lock()
	if a.Status != StatusExited {
		t.Errorf("Status = %q, want %q", a.Status, StatusExited)
	}
	if a.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", a.ExitCode)
	}
	a.mu.Unlock()

	// Process that exits with non-zero code
	b, err := reg.Spawn(SpawnRequest{
		Name:    "exit-fail",
		Type:    TypePolecat,
		Command: []string{"false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-b.Done()

	b.mu.Lock()
	if b.Status != StatusExited {
		t.Errorf("Status = %q, want %q", b.Status, StatusExited)
	}
	if b.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", b.ExitCode)
	}
	b.mu.Unlock()
}

func TestOnExitCallback(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	exitCh := make(chan string, 1)
	reg.SetOnExit(func(a *Agent, err error) {
		exitCh <- a.Name
	})

	_, err = reg.Spawn(SpawnRequest{
		Name:    "callback-test",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case name := <-exitCh:
		if name != "callback-test" {
			t.Errorf("callback got name %q, want %q", name, "callback-test")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onExit callback was not called")
	}
}

func TestRespawn(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Spawn a process that exits immediately
	a, err := reg.Spawn(SpawnRequest{
		Name:    "respawn-test",
		Type:    TypeCrew,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	// Respawn it with a long-lived command so it's still running when we check
	a.Command = []string{"sleep", "10"}
	b, err := reg.Respawn("respawn-test")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	if b.RestartCount != 1 {
		t.Errorf("RestartCount = %d, want 1", b.RestartCount)
	}
	if s := b.GetStatus(); s != StatusRunning {
		t.Errorf("Status = %q, want %q", s, StatusRunning)
	}
	if b.PID == a.PID {
		t.Error("expected new PID after respawn")
	}
}

func TestWorktreeFieldsSetBeforeSpawn(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:        "wt-test",
		Type:        TypePolecat,
		Command:     []string{"cat"},
		WorktreeDir: "/tmp/fake-worktree",
		SourceRepo:  "/tmp/fake-repo",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fields must be set immediately after Spawn returns
	if a.WorktreeDir != "/tmp/fake-worktree" {
		t.Errorf("WorktreeDir = %q, want %q", a.WorktreeDir, "/tmp/fake-worktree")
	}
	if a.SourceRepo != "/tmp/fake-repo" {
		t.Errorf("SourceRepo = %q, want %q", a.SourceRepo, "/tmp/fake-repo")
	}
}

func TestWorktreeFieldsVisibleInOnExit(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Track what onExit sees
	type exitInfo struct {
		worktreeDir string
		sourceRepo  string
	}
	exitCh := make(chan exitInfo, 1)

	reg.SetOnExit(func(a *Agent, err error) {
		exitCh <- exitInfo{
			worktreeDir: a.WorktreeDir,
			sourceRepo:  a.SourceRepo,
		}
	})

	// Spawn a fast-exiting process — the race condition scenario
	_, err = reg.Spawn(SpawnRequest{
		Name:        "fast-exit",
		Type:        TypePolecat,
		Command:     []string{"true"},
		WorktreeDir: "/tmp/test-worktree",
		SourceRepo:  "/tmp/test-repo",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case info := <-exitCh:
		if info.worktreeDir != "/tmp/test-worktree" {
			t.Errorf("onExit saw WorktreeDir = %q, want %q", info.worktreeDir, "/tmp/test-worktree")
		}
		if info.sourceRepo != "/tmp/test-repo" {
			t.Errorf("onExit saw SourceRepo = %q, want %q", info.sourceRepo, "/tmp/test-repo")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onExit was not called within 5 seconds")
	}
}

func TestDoneBlocksUntilOnExitCompletes(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// onExit sleeps briefly; Done() must not signal until it returns.
	var onExitDone atomic.Bool
	reg.SetOnExit(func(a *Agent, err error) {
		time.Sleep(200 * time.Millisecond)
		onExitDone.Store(true)
	})

	a, err := reg.Spawn(SpawnRequest{
		Name:    "done-blocks",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-a.Done():
		if !onExitDone.Load() {
			t.Fatal("Done() signaled before onExit completed — cleanup race")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Done() never signaled")
	}
}

func TestWorkItemIDSetOnSpawn(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:       "wi-test",
		Type:       TypePolecat,
		Command:    []string{"cat"},
		WorkItemID: "mg-3640",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.WorkItemID != "mg-3640" {
		t.Errorf("WorkItemID = %q, want %q", a.WorkItemID, "mg-3640")
	}
}

func TestWorkItemIDEmptyByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "no-wi",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.WorkItemID != "" {
		t.Errorf("WorkItemID = %q, want empty", a.WorkItemID)
	}
}

// TestWorkItemIDRoundTripsThroughJSON verifies that the Agent struct's
// WorkItemID field round-trips through json.Marshal/Unmarshal — the same
// encoding any future runtime state file would use to persist the registry
// across pogod restarts. Per the spend-tracking design (mg-75c3),
// live-attribution of polecat token cost depends on this field surviving.
func TestWorkItemIDRoundTripsThroughJSON(t *testing.T) {
	a := &Agent{
		Name:       "pc-3640",
		PID:        12345,
		Type:       TypePolecat,
		StartTime:  time.Unix(1700000000, 0).UTC(),
		Command:    []string{"claude"},
		Status:     StatusRunning,
		WorkItemID: "mg-3640",
	}

	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"work_item_id":"mg-3640"`) {
		t.Errorf("marshal output missing work_item_id: %s", data)
	}

	var b Agent
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if b.WorkItemID != "mg-3640" {
		t.Errorf("after round-trip WorkItemID = %q, want %q", b.WorkItemID, "mg-3640")
	}
}

// TestWorkItemIDOmittedWhenEmpty ensures crew agents (which have empty
// WorkItemID) don't bloat the state-file JSON with an empty string field.
func TestWorkItemIDOmittedWhenEmpty(t *testing.T) {
	a := &Agent{Name: "mayor", Type: TypeCrew, Status: StatusRunning}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "work_item_id") {
		t.Errorf("expected work_item_id to be omitted when empty, got: %s", data)
	}
}

// TestWorkItemIDPreservedAcrossRespawn guards the case where a crashed polecat
// is respawned by pogod's onExit handler — the work item link must survive so
// spend tracking and the agent registry stay correct after the restart.
func TestWorkItemIDPreservedAcrossRespawn(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:       "respawn-wi",
		Type:       TypeCrew,
		Command:    []string{"true"},
		WorkItemID: "mg-7777",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	a.Command = []string{"sleep", "10"}
	b, err := reg.Respawn("respawn-wi")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	if b.WorkItemID != "mg-7777" {
		t.Errorf("after respawn WorkItemID = %q, want %q", b.WorkItemID, "mg-7777")
	}
}

// wedgeDeadAgent spawns a RestartOnCrash=true agent whose process exits
// immediately and waits for it to die, leaving a stale registry entry (dead
// pid, status=exited) — the gh #19 scenario where a crew agent exited cleanly
// without re-arming. The registry has no onExit hook, so nothing respawns or
// removes the entry. Returns the wedged agent.
func wedgeDeadAgent(t *testing.T, reg *Registry, name string) *Agent {
	t.Helper()
	a, err := reg.Spawn(SpawnRequest{
		Name:           name,
		Type:           TypeCrew,
		Command:        []string{"true"},
		RestartOnCrash: true,
	})
	if err != nil {
		t.Fatalf("Spawn %s: %v", name, err)
	}
	<-a.Done()
	if a.alive() {
		t.Fatalf("agent %s should be dead after exit", name)
	}
	if reg.Get(name) == nil {
		t.Fatalf("agent %s should still be wedged in registry after clean exit", name)
	}
	return a
}

// gh #19, Fix 1: pogo agent stop clears a dead-process registration even for
// a RestartOnCrash agent that Stop otherwise leaves intact, so a subsequent
// start is not blocked by a dead pid.
func TestStopClearsDeadRestartAgent(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	wedgeDeadAgent(t, reg, "wedged")

	if err := reg.Stop("wedged", 2*time.Second); err != nil {
		t.Fatalf("Stop on dead agent should succeed, got: %v", err)
	}
	if reg.Get("wedged") != nil {
		t.Error("Stop should have cleared the stale registration for the dead agent")
	}
}

// gh #19, Fix 3: pogo agent start overwrites a dead-process registration
// rather than refusing with "already running".
func TestStartOverwritesDeadRegistration(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	old := wedgeDeadAgent(t, reg, "wedged")

	// Start a fresh long-lived process under the same name; Spawn must treat
	// the dead entry as stale and overwrite it.
	b, err := reg.Spawn(SpawnRequest{
		Name:    "wedged",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn over dead registration should succeed, got: %v", err)
	}
	if !b.alive() {
		t.Error("respawned agent should be alive")
	}
	if b.PID == old.PID {
		t.Errorf("expected a fresh pid, got the dead one %d", old.PID)
	}
	if reg.Get("wedged") != b {
		t.Error("registry should now hold the fresh agent")
	}
}

// gh #19 no-regression: a live agent is still protected — Spawn refuses a
// duplicate name whose process is actually running, and Stop tears it down
// normally. Verified on a distinct agent name per acceptance bar 4.
func TestSpawnRefusesLiveDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "live-one",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !a.alive() {
		t.Fatal("live-one should be alive")
	}

	_, err = reg.Spawn(SpawnRequest{
		Name:    "live-one",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected 'already running' error for live duplicate, got: %v", err)
	}
	// The live agent must be untouched by the refused spawn.
	if reg.Get("live-one") != a {
		t.Error("refused duplicate spawn must not disturb the live registration")
	}
}
