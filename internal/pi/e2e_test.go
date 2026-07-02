package pi_test

// End-to-end verification for the pi provider (mg-9829). It drives a real `pi`
// process through pogo's actual agent.Registry and the resolved pi.Provider —
// the exact pipeline a `provider = "pi"` polecat takes:
//
//	providers.Resolve("pi")
//	  -> ExpandCommand(provider.CommandTemplate, ...)
//	  -> Registry.Spawn
//	  -> initial nudge (the task, typed into the pi composer)
//
// Unlike the Codex e2e test, this one needs NO API key and makes NO network
// request: pi's models.json supports custom OpenAI-compatible providers, so
// the test points a "mock" provider at a local httptest server that captures
// the completion request and streams back a fixed reply. That capture gives
// byte-level assertions the TUI can't: the persona delivered via
// --append-system-prompt lands in the system message, and the nudged task
// arrives intact as the user message. PI_CODING_AGENT_DIR isolates pi's
// config/auth/session state from the developer's real ~/.pi.
//
// It is opt-in only because it needs a `pi` binary (Node >= 22.19) on PATH:
//
//	POGO_PI_E2E=1 go test ./internal/pi/ -run TestPiEndToEnd -v
//
// See docs/investigations/pi-nudge-calibration.md.

import (
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

	// Resolve the provider exactly as cmd/pogod does for provider = "pi".
	provider, ok := providers.Resolve("pi")
	if !ok || provider.ID != "pi" {
		t.Fatalf("providers.Resolve(\"pi\") = (%v, %v), want the pi provider", provider, ok)
	}

	reg, err := agent.NewRegistry(filepath.Join(base, "sock"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.RegisterProvider(provider)
	reg.SetDefaultProvider(provider.ID)
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

	// Acceptance: the initial nudge is submitted and pi runs a turn — the mock
	// server receives a completion request.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && mock.count() == 0 {
		time.Sleep(500 * time.Millisecond)
	}
	if mock.count() == 0 {
		out := string(agent.StripANSI(a.RecentOutput(65536)))
		t.Fatalf("pi never sent a completion request — sentinel gate, nudge "+
			"submission, or model selection failed.\npi output tail:\n%s",
			tail(out, 2000))
	}

	// Acceptance (byte-level, via the captured request): the persona delivered
	// with --append-system-prompt landed in the system prompt, and the nudged
	// task arrived intact as the user message.
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
		t.Error("nudged task did not arrive intact in the user message (startup race?)")
	}

	// Acceptance: the calibrated PromptReadySentinel actually appears in pi's
	// output as seen through pogo's own ANSI stripper — if this fails the
	// initial nudge is running on the degraded wait-idle path (see
	// agent.NudgeProfile.PromptReadySentinel).
	clean := string(agent.StripANSI(a.RecentOutput(1 << 20)))
	if !strings.Contains(clean, provider.Nudge.PromptReadySentinel) {
		t.Errorf("PromptReadySentinel %q not found in stripped pi output — "+
			"sentinel is stale, initial nudges degrade to wait-idle",
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

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
