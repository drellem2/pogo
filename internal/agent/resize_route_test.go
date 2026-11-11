package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// postResize drives handleResize directly with a POST of body for agent name.
// Mount reachability is TestEveryAgentRouteIsMounted's job, not this one's.
func postResize(reg *Registry, name, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/agents/"+name+"/resize", strings.NewReader(body))
	req.SetPathValue("name", name)
	reg.handleResize(rr, req)
	return rr
}

// TestResizeRouteAppliesPTYSize is the end-to-end check for the resize route:
// a POST /agents/{name}/resize with a JSON {cols,rows} body must drive the
// named agent's PTY to that size through applyResize, so pogo agent attach
// over the raw unix socket (which carries no resize frame) honors the client
// size. This is the behaviour the remote attach plane depends on (pogo#298).
func TestResizeRouteAppliesPTYSize(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	a, err := reg.Spawn(SpawnRequest{
		Name:    "resize-route",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	rr := postResize(reg, "resize-route", `{"cols":120,"rows":40}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("POST resize: got %d %q, want 204", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	if cols, rows := waitForPTYSize(t, a, 120, 40, 1*time.Second); cols != 120 || rows != 40 {
		t.Errorf("resize route did not apply: PTY = %dx%d, want 120x40", cols, rows)
	}

	// The top of the uint16 range is a legal size, not an overflow.
	rr = postResize(reg, "resize-route", `{"cols":65535,"rows":1}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("POST resize 65535x1: got %d %q, want 204", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	if cols, rows := waitForPTYSize(t, a, 65535, 1, 1*time.Second); cols != 65535 || rows != 1 {
		t.Errorf("resize route did not apply 65535x1: PTY = %dx%d", cols, rows)
	}
}

// TestResizeRouteRepeatDoesNotResignal shows the route inherits applyResize's
// idempotence: POSTing the size the PTY already has must not re-issue
// TIOCSWINSZ, which would SIGWINCH a healthy TUI into a redraw. The agent
// prints "WINCH" from a trap so each signal is observable. The applyResize
// half of this guarantee is TestApplyResizeIdempotent
// (attach_regression_test.go); this pins that the route goes through it.
func TestResizeRouteRepeatDoesNotResignal(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	a, err := reg.Spawn(SpawnRequest{
		Name: "resize-winch",
		Type: TypePolecat,
		Command: []string{"sh", "-c",
			"trap 'printf \"WINCH\\n\"' WINCH; while true; do sleep 0.1; done"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // let the shell install its trap

	countWINCH := func() int {
		return strings.Count(string(a.RecentOutput(64*1024)), "WINCH")
	}

	if rr := postResize(reg, "resize-winch", `{"cols":101,"rows":31}`); rr.Code != http.StatusNoContent {
		t.Fatalf("first POST resize: got %d, want 204", rr.Code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for countWINCH() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	before := countWINCH()
	if before < 1 {
		t.Fatalf("a real resize through the route did not signal SIGWINCH")
	}

	if rr := postResize(reg, "resize-winch", `{"cols":101,"rows":31}`); rr.Code != http.StatusNoContent {
		t.Fatalf("repeat POST resize: got %d, want 204", rr.Code)
	}
	time.Sleep(500 * time.Millisecond)
	if c := countWINCH(); c != before {
		t.Errorf("repeated same-size POST resize signaled again: %d -> %d WINCH", before, c)
	}
}

// TestResizeRouteRejectsBadBody pins the 400 contract. The int fields matter:
// decoding into uint16 would reject negatives and >65535 but turn a missing
// field or an explicit 0 into a 0 that applyResize silently drops, answering
// 204 for a resize that never happened.
func TestResizeRouteRejectsBadBody(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	a, err := reg.Spawn(SpawnRequest{
		Name:    "resize-bad",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	for _, body := range []string{
		``,
		`not json`,
		`{"cols":"80","rows":"24"}`,
		`{}`,
		`{"rows":24}`,
		`{"cols":80}`,
		`{"cols":0,"rows":24}`,
		`{"cols":80,"rows":0}`,
		`{"cols":-1,"rows":24}`,
		`{"cols":80,"rows":-5}`,
		`{"cols":65536,"rows":24}`,
		`{"cols":80,"rows":70000}`,
		`{"cols":80.5,"rows":24}`,
	} {
		rr := postResize(reg, "resize-bad", body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("POST resize body %q: got %d %q, want 400", body, rr.Code, strings.TrimSpace(rr.Body.String()))
		}
	}
	// None of them may have touched the PTY.
	if cols, rows := waitForPTYSize(t, a, defaultPTYCols, defaultPTYRows, 100*time.Millisecond); cols != defaultPTYCols || rows != defaultPTYRows {
		t.Errorf("a rejected resize changed the PTY: %dx%d, want %dx%d", cols, rows, defaultPTYCols, defaultPTYRows)
	}
}

// TestResizeRouteUnknownAgent404 pins the contract the plane relies on: an
// unknown agent answers 404, which the caller treats as an advisory no-op.
// That is what makes the route purely additive and safe.
func TestResizeRouteUnknownAgent404(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if rr := postResize(reg, "no-such-agent", `{"cols":120,"rows":40}`); rr.Code != http.StatusNotFound {
		t.Errorf("POST resize for unknown agent: got %d, want 404", rr.Code)
	}
}

// TestResizeRouteNotRunning404 covers the two registry entries that exist but
// cannot be resized: an exited agent, and a "running" one with no PTY master.
// Both answer 404, before the body is even read, like nudge.
func TestResizeRouteNotRunning404(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.mu.Lock()
	reg.agents["resize-exited"] = &Agent{Name: "resize-exited", Type: TypePolecat, Status: StatusExited, done: make(chan struct{})}
	reg.agents["resize-nopty"] = &Agent{Name: "resize-nopty", Type: TypePolecat, Status: StatusRunning, done: make(chan struct{})}
	reg.mu.Unlock()

	for _, name := range []string{"resize-exited", "resize-nopty"} {
		if rr := postResize(reg, name, `{"cols":120,"rows":40}`); rr.Code != http.StatusNotFound {
			t.Errorf("POST resize for %s: got %d %q, want 404", name, rr.Code, strings.TrimSpace(rr.Body.String()))
		}
	}
}

// TestResizeRouteRejectsNonPOST keeps the method guard honest: the plane only
// ever POSTs, and a GET/PUT must not reach applyResize.
func TestResizeRouteRejectsNonPOST(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/agents/whatever/resize", nil)
		req.SetPathValue("name", "whatever")
		reg.handleResize(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s resize: got %d, want 405", method, rr.Code)
		}
	}
}
