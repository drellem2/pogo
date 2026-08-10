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
	// emit is the events sink for mode-transition records; nil means the
	// package default (events.Emit). See modeaudit.go.
	emit Emitter

	// transitionMu serializes mode transitions so overlapping SetMode calls
	// can't interleave stop/start work (e.g. stopping a refinery instance
	// that a concurrent transition just replaced).
	transitionMu sync.Mutex

	// resume holds the restart obligation created by a stop — the half of a
	// stop/restart sequence that must not belong to the stopper. It has its
	// own mutex rather than sharing s.mu because the resumer reads it on every
	// heartbeat tick and must never contend with a guarded request's Mode()
	// check. See orchestrationresume.go.
	resume resumeState
}

// New creates a Server in ModeFull and records the boot mode unconditionally.
//
// The record is written here, not by the caller, so it cannot be skipped: "which
// mode did this process boot into" must be answerable from an artifact rather
// than inferred from the absence of a later transition (mg-293c, requirement 2).
func New(agents *agent.Registry, ref *refinery.Refinery) *Server {
	return newWithEmitter(agents, ref, nil)
}

// newWithEmitter is New with an injectable events sink, so tests can observe
// the boot record that New emits before any setter could be called.
func newWithEmitter(agents *agent.Registry, ref *refinery.Refinery, emit Emitter) *Server {
	s := &Server{
		mode:     config.ModeFull,
		agents:   agents,
		refinery: ref,
		emit:     emit,
	}
	s.resume.grace = DefaultResumeGrace
	s.recordBootMode(s.mode)
	return s
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

// SetMode transitions the server to the given run mode with no caller
// attribution. Callers that know who asked — every HTTP path does — must use
// SetModeWithCause; this entry point records the transition as UNATTRIBUTED,
// which is a finding rather than a formatting choice.
//
// Callers that need to know what a return to full mode actually restored should
// use StartOrchestration, which returns the report this discards.
func (s *Server) SetMode(mode config.RunMode) error {
	return s.SetModeWithCause(mode, unattributedCause("direct SetMode call"))
}

// SetModeWithCause transitions the server to the given run mode, recording what
// caused the change. A transition that does not change the mode records
// nothing: the audit trail says "dispatch availability moved", not "somebody
// asked".
func (s *Server) SetModeWithCause(mode config.RunMode, cause Cause) error {
	if mode == config.ModeFull {
		_, err := s.StartOrchestrationWithCause(cause)
		return err
	}
	if mode == config.ModeIndexOnly {
		_, err := s.StopOrchestrationWithCause(cause, 0)
		return err
	}
	return fmt.Errorf("unknown mode: %d", mode)
}

// StopOrchestrationWithCause transitions to index-only mode and returns the
// obligation the stop just created: when the fleet went down, and when pogod
// will put it back if the caller does not.
//
// hold is the caller's declaration of how long the fleet may legitimately stay
// down. Zero — which is what every existing caller passes — means "no
// declaration", and takes the configured default grace. See
// orchestrationresume.go for why the default is a finite deadline rather than
// "until somebody says otherwise".
func (s *Server) StopOrchestrationWithCause(cause Cause, hold time.Duration) (StopReport, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	if s.Mode() == config.ModeIndexOnly {
		// Already stopped. Deliberately does NOT re-arm: the obligation runs
		// from the ORIGINAL stop, so a procedure that stops an already-stopped
		// fleet — or a retry loop that stops it every thirty seconds — cannot
		// push the deadline out indefinitely. That is the shape by which a
		// watchdog gets silently disabled by the thing it watches.
		ob, armed := s.ResumeObligation()
		if !armed {
			return StopReport{Mode: config.ModeIndexOnly.String(), AlreadyStopped: true}, nil
		}
		return resumeReport(config.ModeIndexOnly.String(), ob, true), nil
	}

	if err := s.transitionToIndexOnly(cause, hold); err != nil {
		return StopReport{Mode: s.Mode().String()}, err
	}
	ob, _ := s.ResumeObligation()
	return resumeReport(config.ModeIndexOnly.String(), ob, false), nil
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
	return s.StartOrchestrationWithCause(unattributedCause("direct StartOrchestration call"))
}

// StartOrchestrationWithCause is StartOrchestration with caller attribution
// recorded on the transition. See SetModeWithCause.
func (s *Server) StartOrchestrationWithCause(cause Cause) (StartReport, error) {
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
	return s.transitionToFull(cause)
}

// transitionToIndexOnly stops agents and refinery, keeping indexing alive.
// Caller must hold s.transitionMu (not s.mu).
func (s *Server) transitionToIndexOnly(cause Cause, hold time.Duration) error {
	// Recorded BEFORE the stop work, not after. This transition disables every
	// agent, refinery and scheduler endpoint on the daemon, and StopAll can
	// take its full 5s timeout — a record written afterwards would be missing
	// for the whole window in which the fleet is already dark, and missing
	// entirely if the process dies mid-drain.
	s.recordTransition(config.ModeFull, config.ModeIndexOnly, cause)
	log.Printf("server: transitioning to index-only mode")

	// Arm the restart obligation here, for the same reason and at the same
	// moment as the record above: the window this covers opens now, not when
	// StopAll returns. A stopper that dies during its own drain has still
	// stopped the fleet, and something other than the stopper has to be
	// holding the way back (mg-5af1).
	ob := s.armResume(cause, hold)
	switch {
	case ob.Due.IsZero():
		log.Printf("server: NO resume deadline armed — orchestration stays stopped until somebody starts it. " +
			"The fleet coming back now depends entirely on the caller that stopped it.")
	default:
		log.Printf("server: resume deadline armed: if orchestration is not back by %s (%s from now), pogod restores it and alarms (mg-5af1)",
			ob.Due.UTC().Format(time.RFC3339), ob.Due.Sub(ob.Since).Round(time.Second))
	}

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
func (s *Server) transitionToFull(cause Cause) (StartReport, error) {
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

	// The obligation is discharged the moment full mode is real, and it is
	// discharged for a return by ANY route — the original stopper coming back,
	// an operator's `pogo server start`, or the resumer's own restore. Nothing
	// latches: a fleet that came back is not owed a restart, and a detector
	// that stayed RED after the thing it watched recovered would fail every
	// subsequent stop.
	s.disarmResume()

	// Recorded AFTER the flip, unlike the stop path above, and the asymmetry is
	// deliberate: the refinery restart above is fallible and returns early
	// leaving the mode index-only, so a record written before it would claim a
	// transition that did not happen. This line is the point of no return.
	s.recordTransition(config.ModeIndexOnly, config.ModeFull, cause)
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
	// "mode" stays FIRST and keeps its exact spelling. scripts/pogo-self-deploy
	// reads this body with a BRE sed (`json_str mode`) whose leading `.*` is
	// greedy, so any later key spelled `...mode` would shadow it. The keys added
	// here deliberately do not end in "mode".
	body := map[string]string{"mode": s.Mode().String()}
	if ob, armed := s.ResumeObligation(); armed {
		body["stopped_since"] = ob.Since.UTC().Format(time.RFC3339)
		if !ob.Due.IsZero() {
			body["resume_due"] = ob.Due.UTC().Format(time.RFC3339)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
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
	report, err := s.StartOrchestrationWithCause(
		causeFromRequest(r, "POST /server/start-orchestration"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// handleStopOrchestration transitions to index-only mode.
//
// The optional `hold` query parameter is the caller's declaration of how long
// the fleet may legitimately stay down — `?hold=2h` for a maintenance window.
// Absent means no declaration, which takes the configured default grace. A
// malformed hold is REFUSED rather than silently defaulted: a caller that meant
// to declare a two-hour window and got fifteen minutes because it wrote "2hr"
// would be surprised by a restart, and the surprise would look like the
// watchdog misfiring.
func (s *Server) handleStopOrchestration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	var hold time.Duration
	if raw := r.URL.Query().Get("hold"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			http.Error(w, fmt.Sprintf("invalid hold %q: want a Go duration such as 2h", raw),
				http.StatusBadRequest)
			return
		}
		hold = d
	}
	report, err := s.StopOrchestrationWithCause(
		causeFromRequest(r, "POST /server/stop-orchestration"), hold)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}
