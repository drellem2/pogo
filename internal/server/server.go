// Package server provides the top-level coordinator for pogod's run mode,
// allowing transitions between full mode and index-only mode.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/refinery"
)

// RefineryStarter is a function that starts the refinery loop in a goroutine.
// It is provided by the caller so the server package doesn't need to know
// about refinery start details (context, callbacks, etc.).
type RefineryStarter func() error

// Server coordinates subsystem lifecycle and mode transitions.
type Server struct {
	mu             sync.RWMutex
	mode           config.RunMode
	agents         *agent.Registry
	refinery       *refinery.Refinery
	refineryCtx    context.Context
	refineryCancel context.CancelFunc
	refineryCfg    *refinery.Config
	startRefinery  RefineryStarter
}

// New creates a Server in ModeFull.
func New(agents *agent.Registry, ref *refinery.Refinery) *Server {
	return &Server{
		mode:     config.ModeFull,
		agents:   agents,
		refinery: ref,
	}
}

// SetRefineryStarter sets the function used to restart the refinery loop
// when transitioning back to ModeFull.
func (s *Server) SetRefineryStarter(fn RefineryStarter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startRefinery = fn
}

// Mode returns the current run mode.
func (s *Server) Mode() config.RunMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// SetMode transitions the server to the given run mode.
func (s *Server) SetMode(mode config.RunMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mode == mode {
		return nil // already in requested mode
	}

	switch mode {
	case config.ModeIndexOnly:
		return s.transitionToIndexOnly()
	case config.ModeFull:
		return s.transitionToFull()
	default:
		return fmt.Errorf("unknown mode: %d", mode)
	}
}

// transitionToIndexOnly stops agents and refinery, keeping indexing alive.
// Caller must hold s.mu.
func (s *Server) transitionToIndexOnly() error {
	log.Printf("server: transitioning to index-only mode")

	// Stop all agents
	if s.agents != nil {
		s.agents.StopAll(5 * time.Second)
	}

	// Stop refinery
	if s.refinery != nil {
		s.refinery.Stop()
	}

	s.mode = config.ModeIndexOnly
	log.Printf("server: now in index-only mode")
	return nil
}

// transitionToFull restarts agents registry and refinery.
// Caller must hold s.mu.
func (s *Server) transitionToFull() error {
	log.Printf("server: transitioning to full mode")

	// Restart refinery if we have a starter function
	if s.startRefinery != nil {
		if err := s.startRefinery(); err != nil {
			return fmt.Errorf("restart refinery: %w", err)
		}
	}

	s.mode = config.ModeFull
	log.Printf("server: now in full mode")
	return nil
}

// RequireOrchestration returns middleware that rejects requests with 503
// when the server is in index-only mode. Use this to guard agent and
// refinery endpoints that require full orchestration.
func (s *Server) RequireOrchestration(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Mode() != config.ModeFull {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "orchestration is stopped",
				"mode":  s.Mode().String(),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RegisterHandlers registers the server mode HTTP endpoints.
func (s *Server) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/server/mode", s.handleMode)
	mux.HandleFunc("/server/stop-orchestration", s.handleStopOrchestration)
	mux.HandleFunc("/server/start-orchestration", s.handleStartOrchestration)
}

// handleMode returns the current run mode as JSON.
func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"mode": s.Mode().String(),
	})
}

// handleStartOrchestration transitions to full mode, restarting agents and refinery.
func (s *Server) handleStartOrchestration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	if err := s.SetMode(config.ModeFull); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"mode": s.Mode().String(),
	})
}

// handleStopOrchestration transitions to index-only mode.
func (s *Server) handleStopOrchestration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"mode": s.Mode().String(),
	})
}
