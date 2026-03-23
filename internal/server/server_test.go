package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drellem2/pogo/internal/config"
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
	s.SetRefineryStarter(func() error {
		refineryCalled = true
		return nil
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
	s.SetRefineryStarter(func() error { return nil })
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
	s.SetRefineryStarter(func() error { return nil })
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
