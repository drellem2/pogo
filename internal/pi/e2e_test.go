package pi_test

// End-to-end verification for the pi provider (mg-9829, extended by mg-3e7c).
// TestPiEndToEnd drives a real `pi` process through pogo's actual
// agent.Registry and the resolved pi.Provider — the exact pipeline a
// `provider = "pi"` polecat takes:
//
//	[agents.polecat] provider = "pi"   (config tier of resolveProvider)
//	  -> ExpandCommand(provider.CommandTemplate, ...)
//	  -> Registry.Spawn
//	  -> initial task appended to argv (InitialPromptViaArgv, gh #26)
//	  -> mid-session nudge (Agent.Nudge into the settled composer)
//
// The registry is wired exactly as cmd/pogod wires it (all providers
// registered, per-type config, claude as the global default), so the spawn
// resolving pi proves the `[agents.polecat] provider = "pi"` config tier —
// not just a hardcoded default. The worktree carries its own AGENTS.md, so
// the run also regression-tests the mg-9829 collision wrinkle: persona via
// --append-system-prompt must not clobber (or shadow) the repo's context
// file. TestPiHeadless covers the same provider flags in pi's non-interactive
// `--print --mode json` mode, without the TUI or the registry.
//
// Unlike the Codex e2e test, this one needs NO API key and makes NO network
// request: pi's models.json supports custom OpenAI-compatible providers, so
// the test points a "mock" provider at a local httptest server that captures
// the completion request and streams back a fixed reply. That capture gives
// byte-level assertions the TUI can't: the persona delivered via
// --append-system-prompt lands in the system message, and the argv-delivered
// task arrives intact as the user message. PI_CODING_AGENT_DIR isolates pi's
// config/auth/session state from the developer's real ~/.pi.
//
// The tests are opt-in only because they need a `pi` binary (Node >= 22.19)
// on PATH:
//
//	POGO_PI_E2E=1 go test ./internal/pi/ -run 'TestPi' -v
//
// GitHub CI runs them in the pi-e2e job (.github/workflows/ci.yml), which
// installs the pinned calibration version of pi. See
// docs/investigations/pi-nudge-calibration.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/providers"
)

// mockOpenAI captures chat-completion requests and streams a fixed SSE reply.
type mockOpenAI struct {
	mu   sync.Mutex
	reqs [][]byte
}

func (m *mockOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 1<<16)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	m.mu.Lock()
	m.reqs = append(m.reqs, body)
	m.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	const reply = `data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","created":1,"model":"mock-1","choices":[{"index":0,"delta":{"role":"assistant","content":"PONG"},"finish_reason":null}]}

data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","created":1,"model":"mock-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}

data: [DONE]

`
	_, _ = w.Write([]byte(reply))
}

func (m *mockOpenAI) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

func (m *mockOpenAI) last() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.reqs) == 0 {
		return nil
	}
	return m.reqs[len(m.reqs)-1]
}

// polecatPiConfig mirrors a config.toml with `[agents] provider = "claude"`
// and `[agents.polecat] provider = "pi"` — the mixed-fleet shape the per-type
// provider tier exists for (it satisfies agent.AgentCommandConfig the same
// way config.AgentsConfig does).
type polecatPiConfig struct{}

func (polecatPiConfig) AgentCommand(string) string { return "" }
func (polecatPiConfig) AgentProvider(agentType string) string {
	if agentType == string(agent.TypePolecat) {
		return "pi"
	}
	return "claude"
}

func TestPiEndToEnd(t *testing.T) {
	if os.Getenv("POGO_PI_E2E") != "1" {
		t.Skip("set POGO_PI_E2E=1 to run the live pi end-to-end test")
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi binary not on PATH")
	}

	mock := &mockOpenAI{}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	base := t.TempDir()
	workDir := filepath.Join(base, "worktree")
	agentDir := filepath.Join(base, "pi-agent")
	for _, d := range []string{workDir, agentDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// Custom pi provider pointing at the mock server — no API key needed.
	modelsJSON := fmt.Sprintf(`{"providers":{"mock":{"baseUrl":%q,"api":"openai-completions","apiKey":"mock","models":[{"id":"mock-1","name":"Mock 1","contextWindow":128000,"maxTokens":16000}]}}}`,
		srv.URL+"/v1")
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	const personaMarker = "POGO-PI-E2E-PERSONA-MARKER"
	persona := "# pogo polecat persona\n\nYou are a pogo polecat agent (" +
		personaMarker + "). Complete the assigned task precisely, then stop.\n"
	promptFile := filepath.Join(base, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	// The worktree carries its own AGENTS.md — the collision case from
	// mg-9829. AppendFlag injection must leave it untouched (byte-identical
	// after the run) while pi still loads it as project context.
	const repoContextMarker = "POGO-PI-E2E-REPO-AGENTS-MD-MARKER"
	repoAgentsMD := []byte("# repo context\n\nThis repo's own AGENTS.md (" +
		repoContextMarker + ") — pogo must not clobber this file.\n")
	agentsMDPath := filepath.Join(workDir, "AGENTS.md")
	if err := os.WriteFile(agentsMDPath, repoAgentsMD, 0644); err != nil {
		t.Fatalf("write repo AGENTS.md: %v", err)
	}

	provider, ok := providers.Resolve("pi")
	if !ok || provider.ID != "pi" {
		t.Fatalf("providers.Resolve(\"pi\") = (%v, %v), want the pi provider", provider, ok)
	}

	// Wire the registry exactly as cmd/pogod does at startup: every known
	// provider registered, the per-type/global provider config installed, and
	// claude as the global default. pi being resolved for the polecat spawn
	// below therefore proves the `[agents.polecat] provider = "pi"` config
	// tier, not a registry-global default.
	reg, err := agent.NewRegistry(filepath.Join(base, "sock"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, p := range providers.All() {
		reg.RegisterProvider(p)
	}
	reg.SetCommandConfig(polecatPiConfig{})
	reg.SetDefaultProvider("claude")
	defer reg.StopAll(3 * time.Second)

	cmd, err := agent.ExpandCommand(provider.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  "e2e",
		AgentType:  string(agent.TypePolecat),
		WorkDir:    workDir,
	})
	if err != nil {
		t.Fatalf("ExpandCommand: %v", err)
	}
	// Pin the mock model and disable session persistence — test-only flags on
	// top of the provider's real template.
	cmd = append(cmd, "--provider", "mock", "--model", "mock-1", "--no-session")
	t.Logf("expanded command: %v", cmd)

	const task = "Reply with exactly the word PONG"
	_, err = reg.Spawn(agent.SpawnRequest{
		Name:         "e2e",
		Type:         agent.TypePolecat,
		Command:      cmd,
		PromptFile:   promptFile,
		Dir:          workDir,
		Env:          []string{"PI_CODING_AGENT_DIR=" + agentDir},
		InitialNudge: task,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	a := reg.Get("e2e")
	if a == nil {
		t.Fatal("agent not in registry after spawn")
	}

	// Acceptance (provider registry): the spawn resolved pi through the
	// per-type config tier — not claude, the registry's global default.
	if got := a.ProviderID(); got != "pi" {
		t.Fatalf("resolved provider = %q, want pi via the [agents.polecat] config tier", got)
	}

	// Acceptance (gh #26): Spawn appended the task to the argv — the polecat's
	// task delivery no longer depends on a PTY idle window that pi-tui's
	// differential renderer may never open.
	if got := a.Command[len(a.Command)-1]; got != task {
		t.Fatalf("last command element = %q, want the argv-delivered task %q", got, task)
	}

	// Acceptance: the argv-delivered task starts a turn — the mock server
	// receives a completion request.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && mock.count() == 0 {
		time.Sleep(500 * time.Millisecond)
	}
	if mock.count() == 0 {
		out := string(agent.StripANSI(a.RecentOutput(65536)))
		t.Fatalf("pi never sent a completion request — argv task delivery "+
			"or model selection failed.\npi output tail:\n%s",
			tail(out, 2000))
	}

	// Acceptance (byte-level, via the captured request): the persona delivered
	// with --append-system-prompt landed in the system prompt, and the
	// argv-delivered task arrived intact as the user message.
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(mock.last(), &req); err != nil {
		t.Fatalf("unmarshal captured request: %v", err)
	}
	personaSeen, taskSeen := false, false
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if strings.Contains(string(m.Content), personaMarker) {
				personaSeen = true
			}
		case "user":
			if strings.Contains(string(m.Content), task) {
				taskSeen = true
			}
		}
	}
	if !personaSeen {
		t.Error("persona from --append-system-prompt did not reach the system prompt")
	}
	if !taskSeen {
		t.Error("argv-delivered task did not arrive intact in the user message")
	}

	// Acceptance (mg-9829 collision regression): AppendFlag injection must
	// coexist with the repo's own AGENTS.md — the persona never touches the
	// worktree, and pi still loads the repo context file. Location-agnostic
	// on purpose: where pi folds AGENTS.md into the request is pi's business;
	// that it arrives (and the file survives) is pogo's contract.
	if !strings.Contains(string(mock.last()), repoContextMarker) {
		t.Error("repo AGENTS.md content did not reach the model request — " +
			"persona injection shadowed the repo's own context file")
	}
	if got, rerr := os.ReadFile(agentsMDPath); rerr != nil {
		t.Errorf("repo AGENTS.md unreadable after spawn: %v", rerr)
	} else if !bytes.Equal(got, repoAgentsMD) {
		t.Errorf("repo AGENTS.md was clobbered by the spawn; contents now:\n%s", got)
	}

	// Acceptance: the calibrated PromptReadySentinel actually appears in pi's
	// output as seen through pogo's own ANSI stripper. Argv delivery (gh #26)
	// means no initial nudge waits on it anymore, but the profile retains it
	// as the measured input-loop-ready marker — this catches it going stale.
	clean := string(agent.StripANSI(a.RecentOutput(1 << 20)))
	if !strings.Contains(clean, provider.Nudge.PromptReadySentinel) {
		t.Errorf("PromptReadySentinel %q not found in stripped pi output — "+
			"the calibrated marker is stale (pi TUI changed its hint line?)",
			provider.Nudge.PromptReadySentinel)
	}

	// Acceptance: the mock reply rendered — the turn completed in the TUI.
	deadline = time.Now().Add(15 * time.Second)
	rendered := false
	for time.Now().Before(deadline) {
		clean = string(agent.StripANSI(a.RecentOutput(1 << 20)))
		if strings.Contains(clean, "PONG") {
			rendered = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !rendered {
		t.Errorf("mock reply never rendered in the pi TUI.\noutput tail:\n%s",
			tail(clean, 1200))
	}

	// Acceptance (NudgeProfile calibration): a settled pi composer emits zero
	// PTY output — the buffer must stop growing once the turn is done.
	before := len(a.RecentOutput(1 << 20))
	time.Sleep(4 * time.Second)
	after := len(a.RecentOutput(1 << 20))
	t.Logf("idle check: PTY buffer %d -> %d bytes over 4s", before, after)
	if after-before > 4096 {
		t.Errorf("pi composer did not settle — buffer grew %d bytes in 4s idle",
			after-before)
	}

	// Acceptance (nudge dialect, mid-session): Agent.Nudge into the settled
	// composer — the calibrated body + "\r" split-write — triggers a second
	// turn whose message arrives intact at the model. This is the path every
	// later `pogo nudge` / mayor message to a running pi polecat takes.
	const followUp = "Reply with exactly the word PONG once more"
	turnsBefore := mock.count()
	if err := a.Nudge(followUp); err != nil {
		t.Fatalf("mid-session Nudge: %v", err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && mock.count() == turnsBefore {
		time.Sleep(500 * time.Millisecond)
	}
	if mock.count() == turnsBefore {
		clean = string(agent.StripANSI(a.RecentOutput(1 << 20)))
		t.Fatalf("mid-session nudge never triggered a completion request — "+
			"submit terminator or idle handling regressed.\noutput tail:\n%s",
			tail(clean, 1200))
	}
	var followReq struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(mock.last(), &followReq); err != nil {
		t.Fatalf("unmarshal follow-up request: %v", err)
	}
	lastUser := ""
	for _, m := range followReq.Messages {
		if m.Role == "user" {
			lastUser = string(m.Content)
		}
	}
	if !strings.Contains(lastUser, followUp) {
		t.Errorf("follow-up nudge is not the latest user message; got %q", lastUser)
	}

	// Acceptance: the registry shuts the agent down cleanly.
	stopped := make(chan struct{})
	go func() {
		reg.StopAll(10 * time.Second)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Log("registry stopped the pi agent cleanly")
	case <-time.After(15 * time.Second):
		t.Error("registry did not stop the pi agent within 15s — unclean shutdown")
	}
}

// TestPiHeadless verifies the provider's spawn flags compose with pi's
// non-interactive mode: the expanded CommandTemplate plus `--print --mode
// json` and a positional task executes one turn and exits — no PTY, no
// registry, no nudge. Same mock-provider setup as TestPiEndToEnd (no API key,
// no network beyond localhost), same POGO_PI_E2E=1 gate. This is the shape
// scripted/batch pi invocations take, and it pins down that --approve and
// --append-system-prompt (file form) behave identically outside the TUI.
func TestPiHeadless(t *testing.T) {
	if os.Getenv("POGO_PI_E2E") != "1" {
		t.Skip("set POGO_PI_E2E=1 to run the live pi headless test")
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi binary not on PATH")
	}

	mock := &mockOpenAI{}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	base := t.TempDir()
	workDir := filepath.Join(base, "worktree")
	agentDir := filepath.Join(base, "pi-agent")
	for _, d := range []string{workDir, agentDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	modelsJSON := fmt.Sprintf(`{"providers":{"mock":{"baseUrl":%q,"api":"openai-completions","apiKey":"mock","models":[{"id":"mock-1","name":"Mock 1","contextWindow":128000,"maxTokens":16000}]}}}`,
		srv.URL+"/v1")
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	const personaMarker = "POGO-PI-HEADLESS-PERSONA-MARKER"
	promptFile := filepath.Join(base, "prompt.md")
	persona := "# pogo persona\n\nYou are a pogo agent (" + personaMarker + ").\n"
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	provider, ok := providers.Resolve("pi")
	if !ok {
		t.Fatal("providers.Resolve(\"pi\") returned ok=false")
	}
	argv, err := agent.ExpandCommand(provider.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  "headless",
		AgentType:  string(agent.TypePolecat),
		WorkDir:    workDir,
	})
	if err != nil {
		t.Fatalf("ExpandCommand: %v", err)
	}
	const task = "Reply with exactly the word PONG"
	argv = append(argv, "--provider", "mock", "--model", "mock-1", "--no-session",
		"--print", "--mode", "json", task)
	t.Logf("headless command: %v", argv)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+agentDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("headless pi run failed: %v\noutput:\n%s", err, tail(string(out), 2000))
	}

	// The turn actually ran: the mock captured a completion request carrying
	// both the appended persona and the positional task.
	if mock.count() == 0 {
		t.Fatalf("headless pi exited 0 but never sent a completion request.\noutput:\n%s",
			tail(string(out), 2000))
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(mock.last(), &req); err != nil {
		t.Fatalf("unmarshal captured request: %v", err)
	}
	personaSeen, taskSeen := false, false
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if strings.Contains(string(m.Content), personaMarker) {
				personaSeen = true
			}
		case "user":
			if strings.Contains(string(m.Content), task) {
				taskSeen = true
			}
		}
	}
	if !personaSeen {
		t.Error("persona from --append-system-prompt missing from headless request")
	}
	if !taskSeen {
		t.Error("positional task missing from headless request's user message")
	}

	// --mode json emits machine-readable events; the mock's reply must appear
	// in them, and at least one output line must be valid JSON.
	if !strings.Contains(string(out), "PONG") {
		t.Errorf("mock reply missing from headless json output.\noutput:\n%s",
			tail(string(out), 2000))
	}
	jsonLine := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v any
		if json.Unmarshal([]byte(line), &v) == nil {
			jsonLine = true
			break
		}
	}
	if !jsonLine {
		t.Errorf("--mode json produced no parseable JSON lines.\noutput:\n%s",
			tail(string(out), 2000))
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
