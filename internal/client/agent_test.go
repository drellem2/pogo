package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// withTestServer points the package-level serverURL at a test handler for
// the duration of the test, restoring it on cleanup. The handler may return
// any status / body / Content-Type to simulate different pogod responses.
func withTestServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	old := serverURL
	serverURL = ts.URL
	t.Cleanup(func() {
		serverURL = old
		ts.Close()
	})
}

// TestStartAgent_PromptNotFoundStructured covers the GitHub Issue #15 /
// mg-be51 fix: when pogod returns a structured 404 because the prompt file
// is missing, the CLI surfaces the actionable message verbatim instead of
// telling the user to rebuild pogod.
func TestStartAgent_PromptNotFoundStructured(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(agent.StartErrorResponse{
			Reason:  "prompt-not-found",
			Path:    "/home/user/.pogo/agents/crew/foo.md",
			Message: "prompt file not found: /home/user/.pogo/agents/crew/foo.md (run 'pogo agent prompt install' to install defaults)",
		})
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "prompt file not found") {
		t.Errorf("expected message to name the missing prompt, got: %v", err)
	}
	if !strings.Contains(msg, "pogo agent prompt install") {
		t.Errorf("expected message to include the fix command, got: %v", err)
	}
	if strings.Contains(msg, "rebuild") || strings.Contains(msg, "restart pogod") {
		t.Errorf("must NOT suggest rebuilding pogod for a missing prompt, got: %v", err)
	}
}

// TestStartAgent_PromptNotFoundPlainText covers the backwards-compat path:
// an older pogod returns a plain-text 404 body via http.Error. The CLI must
// still surface the body verbatim rather than blaming the build.
func TestStartAgent_PromptNotFoundPlainText(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "prompt file not found: /home/user/.pogo/agents/crew/foo.md (run 'pogo agent prompt install' to install defaults)", http.StatusNotFound)
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "prompt file not found") {
		t.Errorf("expected message to surface plain-text body, got: %v", err)
	}
	if strings.Contains(msg, "rebuild") || strings.Contains(msg, "restart pogod") {
		t.Errorf("must NOT suggest rebuilding pogod for a missing prompt, got: %v", err)
	}
}

// TestStartAgent_EndpointMissing covers the legitimate "rebuild pogod"
// path — a 404 from a daemon that doesn't know /agents/start (Go's default
// ServeMux 404 body).
func TestStartAgent_EndpointMissing(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "restart pogod") {
		t.Errorf("expected rebuild-pogod message for default 404, got: %v", err)
	}
}

// TestStartAgent_GreetingsSentinel covers the other rebuild-pogod path: a
// stale pogod (or a different process on the port) whose root handler
// answers with "greetings from pogo daemon".
func TestStartAgent_GreetingsSentinel(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("greetings from pogo daemon"))
	})

	_, err := StartAgent("foo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "restart pogod") {
		t.Errorf("expected rebuild-pogod message for greetings sentinel, got: %v", err)
	}
}

// TestSpawnAgent_EndpointMissing and TestSpawnPolecat_EndpointMissing
// confirm the rebuild-pogod branch is reachable through all three call
// sites, not just StartAgent.
func TestSpawnAgent_EndpointMissing(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_, err := SpawnAgent(agent.SpawnAPIRequest{Name: "x", Type: agent.TypePolecat, Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "restart pogod") {
		t.Fatalf("expected rebuild-pogod error, got: %v", err)
	}
}

func TestSpawnPolecat_EndpointMissing(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "restart pogod") {
		t.Fatalf("expected rebuild-pogod error, got: %v", err)
	}
}

// TestSpawnPolecat_TemplateNotFound is the symmetric case for SpawnPolecat:
// when handleSpawnPolecat returns a real 404 with a meaningful body
// ("template foo not found"), the CLI surfaces it rather than blaming the
// build.
func TestSpawnPolecat_TemplateNotFound(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "template \"missing\" not found", http.StatusNotFound)
	})
	_, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "x", Template: "missing"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "template") {
		t.Errorf("expected template message to surface, got: %v", err)
	}
	if strings.Contains(err.Error(), "restart pogod") {
		t.Errorf("must NOT suggest rebuilding pogod for a meaningful 404 body, got: %v", err)
	}
}
