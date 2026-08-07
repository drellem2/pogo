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

// RefineryStarter is a function that starts the refinery loop in a goroutine
// and returns the new instance. It is provided by the caller so the server
// package doesn't need to know about refinery start details (context,
// callbacks, etc.). Returning the instance lets the server track the
// currently-running refinery, so a later transition to index-only stops the
// replacement — not the long-dead original. That matters doubly now that
// refinery state is persisted: a stale instance left running would clobber
// the shared state file with its own (stale) view.
type RefineryStarter func() (*refinery.Refinery, error)

// AgentStarter re-runs pogod's crew auto-start sweep when transitioning back to
// ModeFull, and reports what it did. It is the agent-side analogue of
// RefineryStarter and exists for the same reason: the server package should not
// own the policy (which prompts are eligible, whether autostart is configured
// off at all) — only the sequencing.
//
// It RE-RUNS THE SWEEP. It does not restore the set that happened to be running
// before the stop: the desired state is what the prompt frontmatter declares,
// which is the same rule the boot path applies. AutoStartAgents is idempotent,
// so an agent that survived the drain is skipped rather than double-started.
//
// Polecats are deliberately not covered. They are ephemeral, dispatched per
// work item, and correctly stay gone — but the report says so out loud rather
// than letting a bare success imply otherwise.
type AgentStarter func() AgentStartOutcome

// AgentStartOutcome is what an AgentStarter reports back.
type AgentStartOutcome struct {
	// Skipped, when non-empty, is the human-readable reason NO sweep ran —
	// e.g. `[agents] autostart = false`. It is not an error: a daemon
	// configured never to spawn a fleet must not acquire a side door into
	// doing so via a mode round-trip. It is reported so the operator sees
	// "started nothing, on purpose" instead of "started nothing".
	Skipped string
	// Results is the per-prompt outcome of the sweep, empty when Skipped is set.
	Results []agent.AutoStartResult
}

// StartReport describes what a transition back to full mode actually brought
// back. It exists because the CLI used to print "Orchestration restarted" and
// exit zero whether or not a single agent came back (gh #108): a green return
// is precisely what the defect produced, so the fix has to name the fleet, not
// just the mode.
type StartReport struct {
	Mode string `json:"mode"`
	// AlreadyFull means the server was in full mode before the call, so
	// nothing was stopped and nothing was restarted.
	AlreadyFull       bool `json:"already_full,omitempty"`
	RefineryRestarted bool `json:"refinery_restarted"`
	// AgentsStarted names the crew agents this transition spawned.
	AgentsStarted []string `json:"agents_started"`
	// AgentsAlreadyRunning names crew agents the sweep found already up.
	AgentsAlreadyRunning []string `json:"agents_already_running,omitempty"`
	// AgentsParked names crew agents left dormant by a park flag — down on
	// purpose, and reported so they are not read as casualties.
	AgentsParked []string `json:"agents_parked,omitempty"`
	// AgentsFailed names crew agents whose spawn errored out. Restoring the
	// fleet partially is not success and must not read as success.
	AgentsFailed []AgentStartFailure `json:"agents_failed,omitempty"`
	// AgentStartSkipped is non-empty when no sweep ran at all, and says why.
	AgentStartSkipped string `json:"agent_start_skipped,omitempty"`
}

// AgentStartFailure is one crew agent that did not come back, and why.
type AgentStartFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// Server coordinates subsystem lifecycle and mode transitions.
type Server struct {
	// mu guards the fields below and is held only for quick reads/writes —
	// never across subsystem stops or starts, which can take seconds
	// (StopAll has a 5s timeout) and would block every guarded request's
	// Mode() check on RLock (gh #38).
	mu             sync.RWMutex
	mode           config.RunMode
	agents         *agent.Registry
	refinery       *refinery.Refinery
	refineryCtx    context.Context
	refineryCancel context.CancelFunc
	refineryCfg    *refinery.Config
	startRefinery  RefineryStarter
	startAgents    AgentStarter

	// transitionMu serializes mode transitions so overlapping SetMode calls
	// can't interleave stop/start work (e.g. stopping a refinery instance
	// that a concurrent transition just replaced).
	transitionMu sync.Mutex
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

// SetAgentStarter sets the function used to re-run the crew auto-start sweep
// when transitioning back to ModeFull. Leaving it unset means the transition
// restarts no agents — and says so in its StartReport rather than reporting a
// bare success.
func (s *Server) SetAgentStarter(fn AgentStarter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startAgents = fn
}

// Mode returns the current run mode.
func (s *Server) Mode() config.RunMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// SetMode transitions the server to the given run mode. Callers that need to
// know what a return to full mode actually restored should use
// StartOrchestration, which returns the report this discards.
func (s *Server) SetMode(mode config.RunMode) error {
	if mode == config.ModeFull {
		_, err := s.StartOrchestration()
		return err
	}

	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	if s.Mode() == mode {
		return nil // already in requested mode
	}

	switch mode {
	case config.ModeIndexOnly:
		return s.transitionToIndexOnly()
	default:
		return fmt.Errorf("unknown mode: %d", mode)
	}
}

// StartOrchestration transitions to full mode and reports what came back —
// the refinery, and each crew agent by name and outcome.
//
// When the daemon is ALREADY in full mode it still runs the crew auto-start
// sweep, and this is the point of mg-060c rather than a detail of it. Full mode
// is a statement about which subsystems are permitted to run, not about which
// agents are actually up: a mayor that crashed, was stopped by hand, or failed
// its boot spawn leaves a daemon that is in full mode with no coordinator. That
// daemon looks entirely healthy from outside — the port answers, schedules
// fire, /health is green — because the only thing missing is the one agent that
// dispatches work, and nothing else notices its absence. `pogo server start`
// against that daemon returned "already running" and touched nothing, so the
// operator's own recovery action was a no-op, three times reported and twice
// closed as a docs problem. The sweep is idempotent, so re-running it on a
// healthy fleet costs a registry lookup per crew prompt and reports them all as
// already running.
func (s *Server) StartOrchestration() (StartReport, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	if s.Mode() == config.ModeFull {
		report := StartReport{Mode: config.ModeFull.String(), AlreadyFull: true}
		s.sweepCrew(&report)
		log.Printf("server: already in full mode — crew sweep started=%v already_running=%v parked=%v failed=%d%s",
			report.AgentsStarted, report.AgentsAlreadyRunning, report.AgentsParked,
			len(report.AgentsFailed), skippedSuffix(report.AgentStartSkipped))
		return report, nil
	}
	return s.transitionToFull()
}

// transitionToIndexOnly stops agents and refinery, keeping indexing alive.
// Caller must hold s.transitionMu (not s.mu).
func (s *Server) transitionToIndexOnly() error {
	log.Printf("server: transitioning to index-only mode")

	// Flip the mode first so guarded endpoints start rejecting with 503
	// immediately, then snapshot the subsystems and stop them outside the
	// lock — StopAll can take up to its full 5s timeout.
	s.mu.Lock()
	s.mode = config.ModeIndexOnly
	agents := s.agents
	ref := s.refinery
	s.mu.Unlock()

	if agents != nil {
		agents.StopAll(5 * time.Second)
	}
	if ref != nil {
		ref.Stop()
	}

	log.Printf("server: now in index-only mode")
	return nil
}

// transitionToFull restarts the refinery, re-arms crash-respawn, and re-runs
// the crew auto-start sweep, reporting what came back.
// Caller must hold s.transitionMu (not s.mu).
func (s *Server) transitionToFull() (StartReport, error) {
	log.Printf("server: transitioning to full mode")

	s.mu.RLock()
	start := s.startRefinery
	agents := s.agents
	s.mu.RUnlock()

	report := StartReport{Mode: config.ModeFull.String()}

	// Restart refinery if we have a starter function; run it outside the
	// lock so Mode() checks don't block on startup work.
	var newRef *refinery.Refinery
	if start != nil {
		var err error
		newRef, err = start()
		if err != nil {
			// Nothing has been mutated yet — deliberately. The latch stays
			// set and the mode stays index-only, so a failed transition
			// leaves the daemon in the state it was already in rather than
			// half-way between two.
			return StartReport{Mode: s.Mode().String()}, fmt.Errorf("restart refinery: %w", err)
		}
		report.RefineryRestarted = true
	}

	s.mu.Lock()
	if newRef != nil {
		s.refinery = newRef
	}
	s.mode = config.ModeFull
	s.mu.Unlock()

	log.Printf("server: now in full mode")

	// Clear StopAll's shutdown latch HERE: after the drain has returned and
	// the mode is full, and before the sweep below spawns anything.
	//
	// Later would be wrong. Resume bumps the registry generation, invalidating
	// every respawn scheduled before it — so clearing after the sweep would
	// leave exactly the agents that crashed during the sweep permanently
	// unsupervised, in a fleet that reports itself restored.
	//
	// Earlier would be wrong for a different reason. The drain's own late
	// respawn goroutines (scheduled by the OnExit hook, firing 2s after
	// StopAll returned synchronously) would be re-admitted into a fleet that
	// is being rebuilt, racing the sweep whose only guard is a check-then-act
	// on r.Get(name). The generation bump is what makes that impossible rather
	// than merely unlikely — a stale goroutine holds a stale generation and
	// loses however late it fires — but the clear still belongs after the
	// point of no return, not before it: a refinery failure above returns
	// early, and re-arming crash-respawn for a transition that did not happen
	// would leave the daemon supervising a fleet it never restarted.
	if agents != nil {
		agents.Resume()
	}

	// Re-run the auto-start sweep. Missing this is the defect gh #108
	// reported: mode flipped to full, refinery came back, and s.agents was
	// never touched, so the daemon ran on with no crew and reported success.
	s.sweepCrew(&report)

	log.Printf("server: full mode restored — crew started=%v already_running=%v parked=%v failed=%d%s",
		report.AgentsStarted, report.AgentsAlreadyRunning, report.AgentsParked,
		len(report.AgentsFailed), skippedSuffix(report.AgentStartSkipped))
	return report, nil
}

// sweepCrew runs the configured AgentStarter and folds its per-prompt results
// into report. It is shared by the two paths that must end with the declared
// crew running: the transition back to full mode, and a start against a daemon
// that was already in full mode.
//
// It never changes the mode and never touches the refinery, so it is safe on a
// healthy daemon — the sweep itself is idempotent, and an agent that is already
// up is reported as such rather than restarted.
func (s *Server) sweepCrew(report *StartReport) {
	s.mu.RLock()
	startAgents := s.startAgents
	s.mu.RUnlock()

	if startAgents == nil {
		report.AgentStartSkipped = "no agent starter configured for this server"
		return
	}
	outcome := startAgents()
	if outcome.Skipped != "" {
		report.AgentStartSkipped = outcome.Skipped
	}
	for _, res := range outcome.Results {
		switch res.Status {
		case agent.AutoStartStatusStarted:
			report.AgentsStarted = append(report.AgentsStarted, res.Name)
		case agent.AutoStartStatusSkippedRunning:
			report.AgentsAlreadyRunning = append(report.AgentsAlreadyRunning, res.Name)
		case agent.AutoStartStatusSkippedParked:
			report.AgentsParked = append(report.AgentsParked, res.Name)
		case agent.AutoStartStatusFailed:
			report.AgentsFailed = append(report.AgentsFailed,
				AgentStartFailure{Name: res.Name, Error: res.Error})
		}
	}
}

func skippedSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (no auto-start sweep ran: " + reason + ")"
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

// handleStartOrchestration transitions to full mode, restarting agents and
// refinery. The response body carries the full StartReport — the client prints
// it, so an operator sees which agents came back rather than a bare "mode":
// "full" that is equally true when nothing was restarted (gh #108).
func (s *Server) handleStartOrchestration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	report, err := s.StartOrchestration()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
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
