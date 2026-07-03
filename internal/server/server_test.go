package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/refinery"
)

func TestNewServerStartsInFullMode(t *testing.T) {
	s := New(nil, nil)
	if s.Mode() != config.ModeFull {
		t.Fatalf("expected ModeFull, got %s", s.Mode())
	}
}

func TestSetModeIndexOnly(t *testing.T) {
	s := New(nil, nil)
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode(ModeIndexOnly): %v", err)
	}
	if s.Mode() != config.ModeIndexOnly {
		t.Fatalf("expected ModeIndexOnly, got %s", s.Mode())
	}
}

func TestSetModeIdempotent(t *testing.T) {
	s := New(nil, nil)
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatal(err)
	}
	// Setting same mode again should be a no-op
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatal(err)
	}
	if s.Mode() != config.ModeIndexOnly {
		t.Fatalf("expected ModeIndexOnly, got %s", s.Mode())
	}
}

func TestSetModeFullAfterIndexOnly(t *testing.T) {
	refineryCalled := false
	s := New(nil, nil)
	s.SetRefineryStarter(func() (*refinery.Refinery, error) {
		refineryCalled = true
		return nil, nil
	})

	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMode(config.ModeFull); err != nil {
		t.Fatal(err)
	}
	if s.Mode() != config.ModeFull {
		t.Fatalf("expected ModeFull, got %s", s.Mode())
	}
	if !refineryCalled {
		t.Fatal("expected refinery starter to be called")
	}
}

func TestHandleModeGET(t *testing.T) {
	s := New(nil, nil)
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/server/mode", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["mode"] != "full" {
		t.Fatalf("expected mode=full, got %s", resp["mode"])
	}
}

func TestHandleStopOrchestration(t *testing.T) {
	s := New(nil, nil)
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/server/stop-orchestration", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["mode"] != "index-only" {
		t.Fatalf("expected mode=index-only, got %s", resp["mode"])
	}

	// Verify mode actually changed
	if s.Mode() != config.ModeIndexOnly {
		t.Fatalf("expected ModeIndexOnly after stop-orchestration")
	}
}

func TestHandleStopOrchestrationWrongMethod(t *testing.T) {
	s := New(nil, nil)
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/server/stop-orchestration", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestRequireOrchestrationAllowsFullMode(t *testing.T) {
	s := New(nil, nil)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := s.RequireOrchestration(inner)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 in full mode, got %d", w.Code)
	}
}

func TestRequireOrchestrationRejects503InIndexOnly(t *testing.T) {
	s := New(nil, nil)
	s.SetMode(config.ModeIndexOnly)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called in index-only mode")
	})
	handler := s.RequireOrchestration(inner)

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != "orchestration is stopped" {
		t.Fatalf("expected error message, got %q", resp["error"])
	}
	if resp["mode"] != "index-only" {
		t.Fatalf("expected mode=index-only, got %s", resp["mode"])
	}
}

func TestRequireOrchestrationResumesAfterModeChange(t *testing.T) {
	s := New(nil, nil)
	s.SetRefineryStarter(func() (*refinery.Refinery, error) { return nil, nil })
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := s.RequireOrchestration(inner)

	// Stop orchestration
	s.SetMode(config.ModeIndexOnly)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 in index-only, got %d", w.Code)
	}

	// Resume orchestration
	s.SetMode(config.ModeFull)
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after resuming, got %d", w.Code)
	}
}

func TestHandleStartOrchestration(t *testing.T) {
	s := New(nil, nil)
	s.SetRefineryStarter(func() (*refinery.Refinery, error) { return nil, nil })
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	// First stop orchestration
	req := httptest.NewRequest(http.MethodPost, "/server/stop-orchestration", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if s.Mode() != config.ModeIndexOnly {
		t.Fatalf("expected ModeIndexOnly, got %s", s.Mode())
	}

	// Now start orchestration
	req = httptest.NewRequest(http.MethodPost, "/server/start-orchestration", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["mode"] != "full" {
		t.Fatalf("expected mode=full, got %s", resp["mode"])
	}
	if s.Mode() != config.ModeFull {
		t.Fatalf("expected ModeFull after start-orchestration")
	}
}

func TestHandleStartOrchestrationWrongMethod(t *testing.T) {
	s := New(nil, nil)
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/server/start-orchestration", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleStartOrchestrationIdempotent(t *testing.T) {
	s := New(nil, nil) // starts in ModeFull
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	// Calling start-orchestration when already in full mode should be a no-op
	req := httptest.NewRequest(http.MethodPost, "/server/start-orchestration", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if s.Mode() != config.ModeFull {
		t.Fatalf("expected ModeFull, got %s", s.Mode())
	}
}

func TestHandleModeAfterStopOrchestration(t *testing.T) {
	s := New(nil, nil)
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	// Stop orchestration
	req := httptest.NewRequest(http.MethodPost, "/server/stop-orchestration", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Check mode endpoint reflects the change
	req = httptest.NewRequest(http.MethodGet, "/server/mode", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["mode"] != "index-only" {
		t.Fatalf("expected mode=index-only, got %s", resp["mode"])
	}
}

// TestModeReadableDuringSlowTransition guards the gh #38 lock fix: SetMode
// used to hold the server write-lock across slow subsystem work (a 5s
// StopAll, refinery restart), blocking every guarded request's Mode() check
// for the duration. The slow work must run outside s.mu. The refinery
// starter is the injectable slow step: while it is blocked mid-transition,
// Mode() must still return promptly.
func TestModeReadableDuringSlowTransition(t *testing.T) {
	s := New(nil, nil)
	starterEntered := make(chan struct{})
	releaseStarter := make(chan struct{})
	s.SetRefineryStarter(func() (*refinery.Refinery, error) {
		close(starterEntered)
		<-releaseStarter
		return nil, nil
	})

	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatal(err)
	}

	transitionDone := make(chan error, 1)
	go func() { transitionDone <- s.SetMode(config.ModeFull) }()
	<-starterEntered

	modeRead := make(chan config.RunMode, 1)
	go func() { modeRead <- s.Mode() }()
	select {
	case m := <-modeRead:
		// Mode flips to full only after the starter returns.
		if m != config.ModeIndexOnly {
			t.Errorf("expected ModeIndexOnly mid-transition, got %s", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Mode() blocked while a mode transition was in progress")
	}

	close(releaseStarter)
	if err := <-transitionDone; err != nil {
		t.Fatal(err)
	}
	if s.Mode() != config.ModeFull {
		t.Fatalf("expected ModeFull after transition, got %s", s.Mode())
	}
}

// TestModeCycleTracksReplacementRefinery guards the stale-instance bug: after
// an index-only -> full transition creates a fresh Refinery, a later
// transition to index-only must stop the replacement, not the long-dead
// original. Detection: Stop() flushes refinery state to disk, so the
// replacement's state file exists iff the server stopped the right instance.
func TestModeCycleTracksReplacementRefinery(t *testing.T) {
	ref1, err := refinery.New(refinery.Config{WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	ref2, err := refinery.New(refinery.Config{WorktreeDir: t.TempDir(), StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}

	s := New(nil, ref1)
	s.SetRefineryStarter(func() (*refinery.Refinery, error) { return ref2, nil })

	if err := s.SetMode(config.ModeIndexOnly); err != nil { // stops ref1
		t.Fatal(err)
	}
	if err := s.SetMode(config.ModeFull); err != nil { // starts ref2
		t.Fatal(err)
	}
	if err := s.SetMode(config.ModeIndexOnly); err != nil { // must stop ref2
		t.Fatal(err)
	}

	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("replacement refinery was not stopped on the second index-only transition (no state flush): %v", err)
	}
}
