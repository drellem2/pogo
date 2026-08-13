package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/apimount"
)

// unmountedSentinel is what pogod's "/" home page stands in for here. A
// request that reaches it is a request that missed every mounted prefix —
// which in the daemon is a 404 for a route that exists.
const unmountedSentinel = "FELL-THROUGH-TO-HOME-PAGE"

// mountedTestMux builds the mux pogod actually serves: the agent handlers on a
// sub-mux, that sub-mux mounted under apimount.Prefixes, and a catch-all "/"
// standing in for homePage.
//
// Every route test below goes through this, and that is the entire point. The
// three tests that already cover handleHostLoad call it directly, and they were
// green for the whole two weeks `pogo host load` returned 404 (mg-c26d): the
// path string they pass to httptest.NewRequest is inert, because nothing
// routes on it. Only a request that traverses a mount can fail on one.
func mountedTestMux(t *testing.T) (*http.ServeMux, *Registry) {
	t.Helper()
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	orchestrated := http.NewServeMux()
	reg.RegisterHandlers(orchestrated)

	root := http.NewServeMux()
	root.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, unmountedSentinel, http.StatusNotFound)
	})
	apimount.Mount(root, orchestrated)
	return root, reg
}

// TestEveryAgentRouteIsCoveredByAMountedPrefix is the cheap half: it asks
// whether each declared route could be reached at all, without standing
// anything up.
//
// It fails on exactly the condition that shipped: a pattern registered on the
// orchestrated sub-mux whose path begins with none of the prefixes that sub-mux
// is mounted under. `/hostload` was that pattern, and it was the only agent
// route outside `/agents` — which is why it was the only one broken.
func TestEveryAgentRouteIsCoveredByAMountedPrefix(t *testing.T) {
	patterns := RoutePatterns()
	if len(patterns) == 0 {
		t.Fatal("RoutePatterns() is empty — the route list is not being read")
	}
	for _, pattern := range patterns {
		if !apimount.Covers(pattern) {
			t.Errorf("route %q is registered on the orchestrated sub-mux but lies outside "+
				"every mounted prefix %v — pogod will 404 it, and no handler-level test "+
				"can notice", pattern, apimount.Prefixes)
		}
	}
}

// TestEveryAgentRouteIsMounted is the expensive half: it issues a real request
// through the real mount for every declared route and checks the request
// arrived somewhere other than the home page.
//
// The probe method is OPTIONS on purpose. No agent handler accepts it, so every
// one of them refuses at its method guard without spawning, parking, stopping
// or measuring anything — the response says "a handler was reached" and nothing
// else, which is the only question a mount test should ask.
func TestEveryAgentRouteIsMounted(t *testing.T) {
	root, _ := mountedTestMux(t)

	for _, pattern := range RoutePatterns() {
		// Wildcard segments need a concrete value to route on; the handlers
		// answer 404 for an unknown agent, which is still a handler answering.
		path := strings.ReplaceAll(pattern, "{name}", "no-such-agent")

		rr := httptest.NewRecorder()
		root.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, path, nil))

		if strings.Contains(rr.Body.String(), unmountedSentinel) {
			t.Errorf("GET %s (route %q) fell through to the home page: the route is "+
				"registered but not reachable", path, pattern)
		}
	}
}

// TestHostLoadIsReachableThroughTheMountedMux is the regression test for
// mg-c26d, stated at the end the caller uses.
//
// A fourth `reg.handleHostLoad(rr, ...)` test would have passed against the
// unmounted route exactly as the existing three did. This one asks the mux.
func TestHostLoadIsReachableThroughTheMountedMux(t *testing.T) {
	root, _ := mountedTestMux(t)

	rr := httptest.NewRecorder()
	root.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/agents/hostload", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents/hostload through the mounted mux: got %d %q, want 200",
			rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	var resp HostLoadResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	// Advice is populated on both branches — a taken sample and a failed one —
	// so an empty one means the body did not come from handleHostLoad.
	if resp.Advice == "" {
		t.Errorf("advice is empty: the 200 did not come from handleHostLoad")
	}
}

// TestHostLoadMethodGuardSurvivesTheMount checks that the mount forwards the
// method too, rather than the mux answering on the handler's behalf.
func TestHostLoadMethodGuardSurvivesTheMount(t *testing.T) {
	root, _ := mountedTestMux(t)

	rr := httptest.NewRecorder()
	root.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/agents/hostload", nil))

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /agents/hostload through the mounted mux: got %d, want 405", rr.Code)
	}
}

// TestHostLoadRepoQuerySurvivesTheMount checks the query string reaches the
// handler through the mount, since the per-repo occupancy the mayor asks for
// rides on it.
func TestHostLoadRepoQuerySurvivesTheMount(t *testing.T) {
	root, _ := mountedTestMux(t)

	rr := httptest.NewRecorder()
	root.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/agents/hostload?repo=/some/repo", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	var resp HostLoadResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	if resp.RepoOccupancy == nil {
		t.Error("repo_occupancy is absent: the query string did not survive the mount")
	}
}

// TestOldHostLoadPathIsNotServed pins the move. `/hostload` is not a path this
// daemon answers on — it never was — and re-adding it would restore the one
// property that produced the bug: an agent route outside `/agents`.
func TestOldHostLoadPathIsNotServed(t *testing.T) {
	root, _ := mountedTestMux(t)

	rr := httptest.NewRecorder()
	root.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hostload", nil))

	if !strings.Contains(rr.Body.String(), unmountedSentinel) {
		t.Errorf("/hostload is being served (%d): every agent route belongs under "+
			"/agents, which is what makes it reachable", rr.Code)
	}
}
