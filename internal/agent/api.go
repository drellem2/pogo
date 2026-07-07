package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/gitgc"
)

// AgentInfo is the JSON representation of an agent for the API.
type AgentInfo struct {
	Name           string      `json:"name"`
	PID            int         `json:"pid"`
	Type           AgentType   `json:"type"`
	StartTime      time.Time   `json:"start_time"`
	Command        []string    `json:"command"`
	SocketPath     string      `json:"socket_path"`
	Status         AgentStatus `json:"status"`
	ExitCode       int         `json:"exit_code,omitempty"`
	RestartCount   int         `json:"restart_count,omitempty"`
	RestartOnCrash bool        `json:"restart_on_crash"`
	PromptFile     string      `json:"prompt_file,omitempty"`
	ProcessName    string      `json:"process_name"`
	Uptime         string      `json:"uptime"`
	LastActivity   string      `json:"last_activity,omitempty"`
	WorkItemID     string      `json:"work_item_id,omitempty"`
	// RateLimited is true when the modal watcher has flagged the agent as
	// suspected-usage-limited (rate-limit modal visible + event log stale). It
	// is a distinct condition from idle/stalled: the agent is alive but wedged
	// on the provider's rate-limit modal. Surfaced in `pogo status` rows and
	// `pogo agent diagnose`.
	RateLimited bool `json:"rate_limited,omitempty"`
	// RateLimitedSince (RFC 3339) is when RateLimited was set; omitted when the
	// agent is not rate-limited.
	RateLimitedSince time.Time `json:"rate_limited_since,omitempty"`
	// ParkedAt (RFC 3339) is set only on status=parked entries, which are
	// synthesized from on-disk park flags rather than live registry state.
	ParkedAt string `json:"parked_at,omitempty"`
}

// SpawnAPIRequest is the JSON body for POST /agents.
type SpawnAPIRequest struct {
	Name       string    `json:"name"`
	Type       AgentType `json:"type"`
	Command    []string  `json:"command"`
	Env        []string  `json:"env,omitempty"`
	PromptFile string    `json:"prompt_file,omitempty"`
}

// StartAPIRequest is the JSON body for POST /agents/start.
// Starts a crew agent by name, looking up the prompt file automatically.
type StartAPIRequest struct {
	Name string `json:"name"`
}

// SpawnPolecatAPIRequest is the JSON body for POST /agents/spawn-polecat.
// Spawns a polecat from a template with variable expansion.
type SpawnPolecatAPIRequest struct {
	Name     string   `json:"name"`               // Agent name (e.g., short ID)
	Template string   `json:"template"`           // Template name (default: "polecat")
	Task     string   `json:"task,omitempty"`     // Work item title
	Body     string   `json:"body,omitempty"`     // Work item body
	Id       string   `json:"id,omitempty"`       // Work item ID
	Repo     string   `json:"repo,omitempty"`     // Target repository path
	Branch   string   `json:"branch,omitempty"`   // Target branch for refinery submit
	Env      []string `json:"env,omitempty"`      // Additional env vars
	Provider string   `json:"provider,omitempty"` // Harness provider override (--provider): tier 1 of resolution
	// NoWorktree, when true, skips git worktree creation entirely regardless of
	// Repo or template frontmatter, and the polecat runs in-place from a stable
	// home/scratch dir at ~/.pogo/agents/<name>/. It implies a refinery:NO
	// posture (no branch push, no MR submit) — see the {{.NoWorktree}} template
	// variable. Used for in-place edits to files that aren't under a git repo
	// (e.g. runtime crew prompt mirrors). No Repo is required when set.
	NoWorktree bool `json:"no_worktree,omitempty"`
}

// NudgeAPIRequest is the JSON body for POST /agents/:name/nudge.
type NudgeAPIRequest struct {
	Message string `json:"message"`
	// Mode: "wait-idle" (default) or "immediate".
	Mode string `json:"mode,omitempty"`
	// Timeout in seconds for wait-idle mode. Default: 30.
	Timeout int `json:"timeout,omitempty"`
}

// Stall detection thresholds.
const (
	// StallThresholdPolecat is how long a polecat can be idle before it's
	// considered stalled. Polecats do focused work and should produce output
	// frequently.
	StallThresholdPolecat = 5 * time.Minute

	// StallThresholdCrew is how long a crew agent can be idle before it's
	// considered stalled. Crew agents have longer polling intervals.
	StallThresholdCrew = 10 * time.Minute

	// ActiveRecencyWindow is how recently an agent must have produced PTY output
	// to be reported as "healthy" (actively working / writing right now) rather
	// than "idle" (alive but quiet between cycles). Past this window but within
	// the stall threshold, an agent is "idle"; past the threshold it is
	// "stalled". The short window lets downstream consumers (e.g. the bridget
	// agents-view) distinguish a busy agent from a quiet-but-alive one — gh #16.
	// It is intentionally independent of the per-type stall threshold so the
	// busy/idle split stays the same regardless of how long stall takes.
	ActiveRecencyWindow = 30 * time.Second
)

// DiagnoseInfo contains diagnostic details for an agent, including stall
// detection, process health, and activity analysis.
type DiagnoseInfo struct {
	AgentInfo

	// LastActivity is the timestamp of the most recent PTY output.
	// Zero if no output has been written yet.
	LastActivity time.Time `json:"last_activity"`
	// IdleDuration is how long the agent has been idle (no PTY output).
	IdleDuration string `json:"idle_duration"`
	// ProcessAlive indicates whether the OS process is still running.
	ProcessAlive bool `json:"process_alive"`
	// StallThreshold is the configured threshold for this agent type.
	StallThreshold string `json:"stall_threshold"`
	// Stalled is true when the agent's idle time exceeds its stall threshold
	// AND the idle is not explained by a recurring cron schedule (see
	// CronCovered).
	Stalled bool `json:"stalled"`
	// CronCovered is true when the agent's idle would otherwise cross the stall
	// threshold but is suppressed because the agent is within one cron interval
	// of its last scheduled firing — between-cron idle is by design for a
	// cron-driven crew agent, not a wedge (mg-5b23).
	CronCovered bool `json:"cron_covered,omitempty"`
	// Health is a summary string: "healthy", "idle", "stalled", "rate_limited",
	// "exited", or "dead".
	Health string `json:"health"`
	// RecentOutputTail is the last ~500 bytes of PTY output for quick triage.
	RecentOutputTail string `json:"recent_output_tail,omitempty"`
}

// CronWindow is the minimal view of a recurring cron schedule that stall
// detection needs to decide whether an agent's idle is by-design between-cron
// idle. pogod builds these from its scheduler; LastFire is the schedule's most
// recent firing (zero if it has not fired since pogod started), NextFire its
// upcoming firing, and Interval the spacing between consecutive firings.
type CronWindow struct {
	LastFire time.Time
	NextFire time.Time
	Interval time.Duration
}

// StallScheduleProvider supplies the recurring cron schedules targeting an
// agent identity ("crew-<name>" / "cat-<name>") so stall detection can tell a
// normal between-cron idle from a genuine wedge. pogod backs this with its
// scheduler; a nil provider disables cron-aware suppression, which is the
// default and what unit tests use.
type StallScheduleProvider interface {
	CronWindowsForAgent(agentIdentity string) []CronWindow
}

// StallThresholdFor returns the stall detection threshold for the given agent type.
func StallThresholdFor(t AgentType) time.Duration {
	if t == TypeCrew {
		return StallThresholdCrew
	}
	return StallThresholdPolecat
}

// diagnoseAgent builds a DiagnoseInfo for the given agent with no cron-aware
// stall suppression. It is the cron-unaware path used by unit tests and any
// caller that has no schedule provider; production code goes through
// Registry.diagnose, which threads the agent's cron windows.
func diagnoseAgent(a *Agent) DiagnoseInfo {
	return diagnoseAgentAt(a, time.Now(), nil)
}

// diagnoseAgentAt builds a DiagnoseInfo as of now, suppressing the stalled
// label when the agent's idle is explained by a recurring cron schedule (see
// withinCronInterval and mg-5b23). now and windows are injected so the logic is
// deterministically testable.
func diagnoseAgentAt(a *Agent, now time.Time, windows []CronWindow) DiagnoseInfo {
	info := agentInfo(a)
	lastWrite := a.outputBuf.LastWriteTime()
	threshold := StallThresholdFor(a.Type)

	var idleDur time.Duration
	if !lastWrite.IsZero() {
		idleDur = now.Sub(lastWrite)
	}

	// Check if the OS process is still alive via kill(pid, 0).
	processAlive := pidAlive(a.PID)

	// An agent past its stall threshold is only a genuine wedge when its idle
	// is not explained by waiting between cron firings: a cron-driven crew
	// agent (e.g. doctor's */30 mail-check) produces no PTY output for the
	// whole between-cron gap, which is by design (mg-5b23).
	idlePastThreshold := !lastWrite.IsZero() && idleDur >= threshold
	cronCovered := idlePastThreshold && withinCronInterval(now, windows)
	stalled := idlePastThreshold && !cronCovered

	// Determine overall health. "healthy" means the agent produced output within
	// ActiveRecencyWindow (actively working); past that window but within the
	// stall threshold it is "idle" (alive, between cycles); past the threshold it
	// is "stalled". A cron-covered agent reports "idle" rather than "stalled" —
	// its idle exceeds the recency window by construction (mg-5b23, gh #16).
	//
	// "rate_limited" is a distinct condition that outranks stalled/idle: a
	// usage-limited agent is alive but wedged on the provider's rate-limit modal
	// and would otherwise read as stalled. Surfacing it separately keeps
	// operators from mistaking a limit wait for a genuine wedge (gh #45).
	health := "healthy"
	switch {
	case info.Status == StatusExited:
		health = "exited"
	case a.PID > 0 && !processAlive && info.Status == StatusRunning:
		health = "dead"
	case info.RateLimited:
		health = "rate_limited"
	case stalled:
		health = "stalled"
	case !lastWrite.IsZero() && idleDur >= ActiveRecencyWindow:
		health = "idle"
	}

	// Grab a short tail of recent output for triage.
	tail := string(a.RecentOutput(500))

	return DiagnoseInfo{
		AgentInfo:        info,
		LastActivity:     lastWrite,
		IdleDuration:     idleDur.Truncate(time.Second).String(),
		ProcessAlive:     processAlive,
		StallThreshold:   threshold.String(),
		Stalled:          stalled,
		CronCovered:      cronCovered,
		Health:           health,
		RecentOutputTail: tail,
	}
}

// withinCronInterval reports whether now falls within one cron interval of the
// most recent scheduled firing among the given cron windows. When true the
// agent's current idle is the expected gap between firings and must not be
// flagged as a stall. A window with no usable interval is ignored; one that has
// never fired is anchored to the firing one interval before its NextFire.
func withinCronInterval(now time.Time, windows []CronWindow) bool {
	for _, w := range windows {
		if w.Interval <= 0 {
			continue
		}
		last := w.LastFire
		if last.IsZero() && !w.NextFire.IsZero() {
			last = w.NextFire.Add(-w.Interval)
		}
		if last.IsZero() {
			continue
		}
		if now.Sub(last) < w.Interval {
			return true
		}
	}
	return false
}

// OutputAPIRequest query params for GET /agents/:name/output.
// ?lines=N or ?bytes=N

// ExportInfo returns the public AgentInfo for an agent.
func ExportInfo(a *Agent) AgentInfo {
	return agentInfo(a)
}

func agentInfo(a *Agent) AgentInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	info := AgentInfo{
		Name:           a.Name,
		PID:            a.PID,
		Type:           a.Type,
		StartTime:      a.StartTime,
		Command:        a.Command,
		SocketPath:     a.SocketPath(),
		Status:         a.Status,
		ExitCode:       a.ExitCode,
		RestartCount:   a.RestartCount,
		RestartOnCrash: a.RestartOnCrash,
		PromptFile:     a.PromptFile,
		ProcessName:    ProcessName(a.Type, a.Name),
		Uptime:         agentUptime(a),
		WorkItemID:     a.WorkItemID,
		RateLimited:    a.RateLimited,
	}
	if a.RateLimited {
		info.RateLimitedSince = a.RateLimitedSince
	}
	if a.outputBuf != nil {
		if t := a.outputBuf.LastWriteTime(); !t.IsZero() {
			info.LastActivity = formatLastActivity(t)
		}
	}
	return info
}

// agentUptime returns the human-readable uptime for an agent.
// For exited agents, it returns the duration from start to exit rather than
// continuing to count up from the current time.
func agentUptime(a *Agent) string {
	if a.Status == StatusExited && !a.ExitTime.IsZero() {
		return a.ExitTime.Sub(a.StartTime).Truncate(time.Second).String()
	}
	return time.Since(a.StartTime).Truncate(time.Second).String()
}

// formatLastActivity returns a human-readable "time ago" string for the last activity timestamp.
func formatLastActivity(t time.Time) string {
	d := time.Since(t).Truncate(time.Second)
	if d < time.Second {
		return "just now"
	}
	return d.String() + " ago"
}

// RegisterHandlers registers agent API endpoints on the given mux.
func (r *Registry) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/agents", r.handleAgents)
	mux.HandleFunc("/agents/start", r.handleStart)
	mux.HandleFunc("/agents/spawn-polecat", r.handleSpawnPolecat)
	mux.HandleFunc("/agents/prompts", r.handlePrompts)
	mux.HandleFunc("/agents/{name}", r.handleAgent)
	mux.HandleFunc("/agents/{name}/park", r.handlePark)
	mux.HandleFunc("/agents/{name}/wake", r.handleWake)
	mux.HandleFunc("/agents/{name}/diagnose", r.handleDiagnose)
	mux.HandleFunc("/agents/{name}/nudge", r.handleNudge)
	mux.HandleFunc("/agents/{name}/output", r.handleOutput)
	mux.HandleFunc("/agents/{name}/terminal", r.handleTerminal)
}

func (r *Registry) handleAgents(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		agents := r.List()
		infos := make([]AgentInfo, len(agents))
		for i, a := range agents {
			infos[i] = agentInfo(a)
		}
		// Surface parked (dormant) agents alongside running ones so the
		// mayor's stall-watch can skip them mechanically (mg-41e1). Parked
		// agents have no registry entry; synthesize their info from the
		// on-disk park flags. A registry entry with the same name wins (e.g.
		// mid-wake).
		if parked, err := ListParked(); err == nil {
			for _, p := range parked {
				if r.Get(p.Name) != nil {
					continue
				}
				infos = append(infos, AgentInfo{
					Name:        p.Name,
					Type:        TypeCrew,
					Status:      StatusParked,
					ProcessName: ProcessName(TypeCrew, p.Name),
					ParkedAt:    p.ParkedAt.Format(time.RFC3339),
				})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(infos)

	case "POST":
		var spawnReq SpawnAPIRequest
		if err := json.NewDecoder(req.Body).Decode(&spawnReq); err != nil {
			http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
			return
		}
		a, err := r.Spawn(SpawnRequest{
			Name:           spawnReq.Name,
			Type:           spawnReq.Type,
			Command:        spawnReq.Command,
			Env:            spawnReq.Env,
			PromptFile:     spawnReq.PromptFile,
			RestartOnCrash: ResolveRestartOnCrash(spawnReq.PromptFile, spawnReq.Type),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(agentInfo(a))

	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func (r *Registry) handleAgent(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")

	switch req.Method {
	case "GET":
		agent := r.Get(name)
		if agent == nil {
			http.Error(w, fmt.Sprintf("agent %q not found", name), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agentInfo(agent))

	case "DELETE":
		err := r.Stop(name, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

// ParkAPIResponse is the JSON body returned by POST /agents/{name}/park.
type ParkAPIResponse struct {
	Status          string `json:"status"` // "parked"
	Agent           string `json:"agent"`
	SchedulesPaused int    `json:"schedules_paused"`
}

// WakeAPIResponse is the JSON body returned by POST /agents/{name}/wake.
type WakeAPIResponse struct {
	Status            string `json:"status"` // "woken"
	Agent             string `json:"agent"`
	PID               int    `json:"pid"`
	SchedulesRestored int    `json:"schedules_restored"`
}

func (r *Registry) handlePark(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	name := req.PathValue("name")
	paused, err := r.Park(name, 5*time.Second)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrPromptNotFound):
			status = http.StatusNotFound
		case strings.Contains(err.Error(), "polecat"):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ParkAPIResponse{
		Status:          "parked",
		Agent:           name,
		SchedulesPaused: paused,
	})
}

func (r *Registry) handleWake(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	name := req.PathValue("name")
	a, restored, err := r.Wake(name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case strings.Contains(err.Error(), "is not parked"):
			status = http.StatusConflict
		case errors.Is(err, ErrPromptNotFound):
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(WakeAPIResponse{
		Status:            "woken",
		Agent:             name,
		PID:               a.PID,
		SchedulesRestored: restored,
	})
}

func (r *Registry) handleDiagnose(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	name := req.PathValue("name")
	agent := r.Get(name)
	if agent == nil {
		http.Error(w, fmt.Sprintf("agent %q not found", name), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(r.diagnose(agent))
}

// diagnose builds a DiagnoseInfo for an agent, consulting the registry's
// stall-schedule provider (if installed) so a cron-driven agent's between-cron
// idle is not reported as stalled (mg-5b23).
func (r *Registry) diagnose(a *Agent) DiagnoseInfo {
	r.mu.RLock()
	provider := r.stallSchedules
	r.mu.RUnlock()

	var windows []CronWindow
	if provider != nil {
		windows = provider.CronWindowsForAgent(a.EventAgent())
	}
	return diagnoseAgentAt(a, time.Now(), windows)
}

// NudgeAPIResponse is returned for wait-idle nudges to report delivery status.
type NudgeAPIResponse struct {
	Status string `json:"status"` // "delivered" or "not_running"
	Agent  string `json:"agent"`
}

func (r *Registry) handleNudge(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	name := req.PathValue("name")
	agent := r.Get(name)
	if agent == nil || agent.Status != StatusRunning {
		// Return 404 with structured response so clients can detect and fall back.
		// This covers both missing agents and agents that exist in the registry
		// but aren't running (e.g. crew agents between exit and respawn).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(NudgeAPIResponse{
			Status: "not_running",
			Agent:  name,
		})
		return
	}

	var nudgeReq NudgeAPIRequest
	if err := json.NewDecoder(req.Body).Decode(&nudgeReq); err != nil {
		req.Body.Close()
		http.Error(w, "bad request: message required", http.StatusBadRequest)
		return
	}

	// Determine mode and timeout
	mode := NudgeWaitIdle
	if nudgeReq.Mode == string(NudgeImmediate) {
		mode = NudgeImmediate
	}
	timeout := DefaultNudgeTimeout
	if nudgeReq.Timeout > 0 {
		timeout = time.Duration(nudgeReq.Timeout) * time.Second
	}

	if err := agent.NudgeWithMode(nudgeReq.Message, mode, timeout); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(NudgeAPIResponse{
		Status: "delivered",
		Agent:  name,
	})
}

func (r *Registry) handleOutput(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	name := req.PathValue("name")
	agent := r.Get(name)
	if agent == nil {
		http.Error(w, fmt.Sprintf("agent %q not found", name), http.StatusNotFound)
		return
	}

	// Default to last 4KB of output
	n := 4096
	output := agent.RecentOutput(n)
	if req.URL.Query().Get("plain") == "true" {
		output = StripANSI(output)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	io.WriteString(w, string(output))
}

// CrewPromptDir is the directory where crew agent prompt files live.
// Default: $POGO_HOME/agents/crew (~/.pogo/agents/crew).
func CrewPromptDir() string {
	return filepath.Join(PromptDir(), "crew")
}

// ErrPromptNotFound indicates the prompt file for a crew agent could not be
// located on disk. Callers (e.g. the HTTP handler) can detect this with
// errors.Is to map it to a 404.
var ErrPromptNotFound = errors.New("prompt file not found")

// PromptNotFoundError carries the missing prompt path so callers can surface
// it (e.g. in a structured 404 response body). errors.Is(err, ErrPromptNotFound)
// still matches.
type PromptNotFoundError struct {
	Path string
}

func (e *PromptNotFoundError) Error() string {
	return fmt.Sprintf("prompt file not found: %s (run 'pogo agent prompt install' to install defaults)", e.Path)
}

func (e *PromptNotFoundError) Unwrap() error { return ErrPromptNotFound }

// StartErrorResponse is the JSON body returned by /agents/start when
// StartCrewAgent fails in a way the CLI can act on. Reason is a stable
// machine-readable code; Message is always populated with the human-readable
// text. Older pogod builds returned plain text, so the CLI must remain
// tolerant of either form.
type StartErrorResponse struct {
	Reason  string `json:"reason"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// StartCrewAgent starts a crew agent by name, looking up its prompt file
// under ~/.pogo/agents/ and applying any frontmatter overrides
// (nudge_on_start, restart_on_crash).
//
// The coordinator's prompt lives at ~/.pogo/agents/mayor.md (the file name is
// mechanism and stays put; the agent's name follows [agents] coordinator);
// all other crew prompts live at ~/.pogo/agents/crew/<name>.md.
//
// Returns ErrPromptNotFound (wrapped) when the prompt file is missing, or any
// error from Spawn (e.g. when the agent is already registered).
func (r *Registry) StartCrewAgent(name string) (*Agent, error) {
	// Look up prompt file: the coordinator's mayor.md is in PromptDir, crew
	// in CrewPromptDir. This on-disk file is the operator-editable stub —
	// kept separate from the (possibly synthesized) promptFile below because
	// stub-level frontmatter overrides win for restart_on_crash.
	stubFile, err := crewPromptPath(name)
	if err != nil {
		return nil, err
	}
	promptFile := stubFile

	// Give crew agents a stable working directory under $POGO_HOME/agents/<name>/
	agentDir := filepath.Join(PromptDir(), name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create agent dir: %w", err)
	}

	// If the crew prompt is an `extends <template> with config <config>`
	// redirect (PM tier), synthesize the merged prompt to a stable per-agent
	// path so restart-on-crash reuses it. Empty result means no directive —
	// use the original prompt as-is.
	synth, err := SynthesizeExtendsPrompt(promptFile, filepath.Join(agentDir, "synthesized-prompt.md"))
	if err != nil {
		return nil, fmt.Errorf("synthesize extends prompt: %w", err)
	}
	if synth != "" {
		promptFile = synth
	}

	// Parse the (possibly synthesized) prompt's frontmatter once — it feeds
	// both provider resolution (provider:) and the initial nudge
	// (nudge_on_start). A parse error is non-fatal: meta stays a usable zero
	// value and the type defaults apply.
	meta, _, _ := ParsePromptFrontmatter(promptFile)
	var fmProvider string
	if meta != nil {
		fmProvider = meta.Provider
	}

	// Resolve the harness provider for this crew agent. Precedence: provider:
	// frontmatter > per-type [agents.crew] provider > global default. (Crew
	// has no --provider flag — see mg-b31b §5.)
	provider, perr := r.resolveProvider(TypeCrew, "", fmProvider)
	if perr != nil {
		return nil, fmt.Errorf("resolve provider: %w", perr)
	}

	// Build command from the configured template. commandTemplate resolves an
	// explicit [agents] command if set, otherwise the resolved provider's
	// default. The Claude default carries --dangerously-skip-permissions,
	// required for autonomous agent execution; --permission-mode
	// bypassPermissions does NOT work without additional setup.
	cmd, err := ExpandCommand(r.commandTemplate(TypeCrew, provider), CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  name,
		AgentType:  string(TypeCrew),
		WorkDir:    agentDir,
	})
	if err != nil {
		return nil, fmt.Errorf("agent command template error: %w", err)
	}

	// All crew agents get an initial nudge to bypass the CLI interactive prompt.
	// A prompt file may declare its own message via `nudge_on_start` in the
	// TOML frontmatter; otherwise the coordinator gets a coordination message
	// and everyone else gets a generic start message.
	var nudgeMsg string
	if meta != nil && meta.NudgeOnStart != "" {
		nudgeMsg = meta.NudgeOnStart
	} else if name == CoordinatorName() {
		nudgeMsg = "You are now running. Begin your coordination loop."
	} else {
		nudgeMsg = "You are now running. Check your mail with `mg mail list " + name + "` and begin your work."
	}

	return r.Spawn(SpawnRequest{
		Name:           name,
		Type:           TypeCrew,
		Command:        cmd,
		PromptFile:     promptFile,
		Dir:            agentDir,
		InitialNudge:   nudgeMsg,
		RestartOnCrash: ResolveRestartOnCrashWithStub(stubFile, promptFile, TypeCrew),
		Provider:       provider,
	})
}

func (r *Registry) handleStart(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	var startReq StartAPIRequest
	if err := json.NewDecoder(req.Body).Decode(&startReq); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	if startReq.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// A parked agent must be woken, not started: start alone would leave the
	// park flag in place, silently suppressing the next respawn and autostart.
	if IsParked(startReq.Name) {
		http.Error(w, fmt.Sprintf("agent %q is parked; wake it with 'pogo agent wake %s'", startReq.Name, startReq.Name), http.StatusConflict)
		return
	}

	a, err := r.StartCrewAgent(startReq.Name)
	if err != nil {
		switch {
		case errors.Is(err, ErrPromptNotFound):
			// Emit a structured JSON body so the CLI can distinguish
			// "prompt missing on the server" from "endpoint missing because
			// pogod is stale" (GitHub Issue #15 / mg-be51). The Message field
			// preserves the actionable text from err.Error() — including the
			// missing path and the 'pogo agent prompt install' hint — for old
			// CLIs that just print the body verbatim.
			resp := StartErrorResponse{
				Reason:  "prompt-not-found",
				Message: err.Error(),
			}
			var pnf *PromptNotFoundError
			if errors.As(err, &pnf) {
				resp.Path = pnf.Path
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(resp)
		case strings.Contains(err.Error(), "already running"):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(agentInfo(a))
}

func (r *Registry) handleSpawnPolecat(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	var spawnReq SpawnPolecatAPIRequest
	if err := json.NewDecoder(req.Body).Decode(&spawnReq); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	if spawnReq.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Default template name
	tmplName := spawnReq.Template
	if tmplName == "" {
		tmplName = "polecat"
	}

	// Resolve template path
	tmplPath, err := ResolveTemplate(tmplName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Frontmatter on the template controls spawn-time behavior (worktree
	// creation, initial nudge). Defaults preserve the legacy hardcoded
	// behavior so templates without frontmatter are unaffected.
	tmplMeta, _, err := ParsePromptFrontmatter(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("template frontmatter parse failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Resolve the harness provider for this polecat before any side effects
	// (temp prompt file, git worktree), so a bad --provider fails fast and
	// clean. Precedence: --provider flag (spawnReq.Provider) > provider:
	// template frontmatter (tmplMeta.Provider) > per-type / global config. An
	// unknown flag value is a hard error; an unknown frontmatter/config value
	// warns and falls back to the default.
	provider, perr := r.resolveProvider(TypePolecat, spawnReq.Provider, tmplMeta.Provider)
	if perr != nil {
		http.Error(w, perr.Error(), http.StatusBadRequest)
		return
	}

	// Decide whether this polecat gets an isolated git worktree. Default:
	// create one when a Repo is supplied. A template can opt out by setting
	// `worktree = false` in its frontmatter. The --no-worktree flag is an
	// explicit, highest-precedence opt-out: it skips creation regardless of
	// Repo or frontmatter so a caller can dispatch in-place edits without a
	// placeholder --repo (gh #17).
	createWorktree := spawnReq.Repo != ""
	if tmplMeta.HasField("worktree") {
		createWorktree = tmplMeta.Worktree && spawnReq.Repo != ""
	}
	if spawnReq.NoWorktree {
		createWorktree = false
	}

	// Compute worktree path before template expansion so it can be included
	// in the prompt. gitgc.DefaultPolecatsDir is the single source of truth
	// for this location — its orphan-dir scan must see the same directory.
	var worktreeDir, sourceRepo, branchName string
	if createWorktree {
		polecatsDir, _ := gitgc.DefaultPolecatsDir()
		worktreeDir = filepath.Join(polecatsDir, spawnReq.Name)
	}

	// Resolve the polecat's working directory. With a worktree it's the
	// worktree itself. In --no-worktree mode there is no checkout, so give the
	// polecat a stable home/scratch dir at ~/.pogo/agents/<name>/ (the same
	// place crew agents run from) to operate in while it edits the absolute
	// paths named in its task body.
	workDir := worktreeDir
	if spawnReq.NoWorktree {
		agentDir := filepath.Join(PromptDir(), spawnReq.Name)
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			http.Error(w, fmt.Sprintf("failed to create agent dir: %v", err), http.StatusInternalServerError)
			return
		}
		workDir = agentDir
	}

	// Capture recent activity in the source repo as best-effort FYI
	// context for the polecat. The helpers return "" on any failure; the
	// template gates the section behind `{{if .RecentCommits}}` so a repo
	// without commits or `.git` simply produces no extra prompt content.
	// See polecat_context.go for the rationale (mg-b372).
	recentCommits := captureRecentCommits(spawnReq.Repo, defaultRecentCommits)
	recentFiles := captureRecentFiles(spawnReq.Repo, defaultRecentCommits, defaultRecentFiles)

	// The resolved provider's id gates harness-specific template blocks
	// ({{if eq .Provider "claude"}}). resolveProvider returns (nil, nil) for a
	// bare registry with no providers registered; fall back to the built-in
	// default id so gated blocks keep their current-behavior (Claude) shape —
	// never empty-string, which would silently hide them.
	providerID := DefaultProviderID
	if provider != nil && provider.ID != "" {
		providerID = provider.ID
	}

	// Expand template to a temp file
	vars := TemplateVars{
		Task:          spawnReq.Task,
		Body:          spawnReq.Body,
		Id:            spawnReq.Id,
		Repo:          spawnReq.Repo,
		Branch:        spawnReq.Branch,
		WorktreeDir:   workDir,
		NoWorktree:    spawnReq.NoWorktree,
		RecentCommits: recentCommits,
		RecentFiles:   recentFiles,
		Provider:      providerID,
	}
	promptFile, err := ExpandTemplateToFile(tmplPath, vars)
	if err != nil {
		http.Error(w, fmt.Sprintf("template expansion failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Ensure POGO_ROLE is set for mg prime and role detection. Its value is the
	// frozen agent-type literal (string(TypePolecat)), never the worker DISPLAY
	// name — a cross-tool identifier, so a display rename must never move it
	// (mg-6a24 §1.1). Byte-identical to the previous "polecat" literal.
	env := append(spawnReq.Env, "POGO_ROLE="+string(TypePolecat))

	// Create git worktree for polecat isolation
	if createWorktree {
		sourceRepo = spawnReq.Repo
		branchName = gitgc.BranchPrefix + spawnReq.Name

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
			os.Remove(promptFile)
			http.Error(w, fmt.Sprintf("failed to create polecats dir: %v", err), http.StatusInternalServerError)
			return
		}

		// Base the worktree on origin/<target-branch> rather than local HEAD so
		// the polecat sees recently merged commits even if the local checkout
		// is behind origin. Falls back to local HEAD when no usable origin
		// exists (tests, repos without a remote).
		baseRef := resolvePolecatBaseRef(sourceRepo, spawnReq.Branch)

		wtArgs := []string{"-C", sourceRepo, "worktree", "add", worktreeDir, "-b", branchName}
		if baseRef != "" {
			wtArgs = append(wtArgs, baseRef)
		}
		wtCmd := exec.Command("git", wtArgs...)
		if out, err := wtCmd.CombinedOutput(); err != nil {
			os.Remove(promptFile)
			http.Error(w, fmt.Sprintf("worktree creation failed: %v\n%s", err, out), http.StatusInternalServerError)
			return
		}
		log.Printf("polecat %s: created worktree at %s (branch %s, base %q)", spawnReq.Name, worktreeDir, branchName, baseRef)
		// No --add-dir needed: the process CWD is set to worktreeDir via SpawnRequest.Dir,
		// and --add-dir triggers a directory trust prompt that blocks autonomous execution.
	}

	// Build command from the configured template. commandTemplate resolves an
	// explicit [agents] command if set, otherwise the resolved provider's
	// default. ValidatePolecatCommand then warns if the template is missing any
	// of the provider's required non-interactive flags (Claude:
	// --dangerously-skip-permissions), which polecats need to run unattended in
	// a freshly-created worktree directory.
	polecatCmdTmpl := r.commandTemplate(TypePolecat, provider)
	ValidatePolecatCommand(polecatCmdTmpl, provider)
	cmd, cmdErr := ExpandCommand(polecatCmdTmpl, CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  spawnReq.Name,
		AgentType:  string(TypePolecat),
		WorkDir:    workDir,
	})
	if cmdErr != nil {
		os.Remove(promptFile)
		cleanupFailedPolecatSpawn(sourceRepo, worktreeDir, branchName)
		http.Error(w, fmt.Sprintf("agent command template error: %v", cmdErr), http.StatusInternalServerError)
		return
	}

	// Build the initial nudge message for the polecat. A template can override
	// the default by declaring `nudge_on_start = "..."` in its frontmatter.
	// The nudge string is run through the same text/template pass as the
	// prompt body so it can reference TemplateVars (e.g. {{.Id}}).
	var nudgeMsg string
	if tmplMeta.NudgeOnStart != "" {
		expanded, err := ExpandString(tmplMeta.NudgeOnStart, vars)
		if err != nil {
			os.Remove(promptFile)
			cleanupFailedPolecatSpawn(sourceRepo, worktreeDir, branchName)
			http.Error(w, fmt.Sprintf("nudge_on_start template error: %v", err), http.StatusInternalServerError)
			return
		}
		nudgeMsg = expanded
	} else if spawnReq.Id != "" {
		nudgeMsg = "Look at the system prompt and complete the steps for this work item: " + spawnReq.Id
	} else {
		nudgeMsg = "You are now running. Begin your assigned task."
	}

	a, err := r.Spawn(SpawnRequest{
		Name:           spawnReq.Name,
		Type:           TypePolecat,
		Command:        cmd,
		Env:            env,
		PromptFile:     promptFile,
		Dir:            workDir,
		WorktreeDir:    worktreeDir,
		SourceRepo:     sourceRepo,
		InitialNudge:   nudgeMsg,
		RestartOnCrash: ResolveRestartOnCrash(promptFile, TypePolecat),
		WorkItemID:     spawnReq.Id,
		Provider:       provider,
	})
	if err != nil {
		os.Remove(promptFile) // Clean up temp file on spawn failure
		cleanupFailedPolecatSpawn(sourceRepo, worktreeDir, branchName)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Auto-register the polecat's mail-check loop so a builder<->reviewer review
	// loop can round-trip without the mayor registering schedules by hand
	// (mg-e633). Addressed to the bare agent name, which is the identity pogod
	// delivers nudges to and reaps under when the polecat exits. Best-effort:
	// the polecat is already running, so a registration failure is logged, not
	// fatal.
	r.registerPolecatMailCheck(spawnReq.Name, spawnReq.Id)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(agentInfo(a))
}

// cleanupFailedPolecatSpawn undoes the git side effects of a polecat spawn
// that failed after its worktree was created: it removes the worktree and
// deletes the polecat-<name> branch that `git worktree add -b` made. Without
// the branch deletion a retry of the same work item fails with "branch
// already exists" (gh #27). Both commands are best-effort; `git branch -D`
// refuses to delete a branch still checked out in another worktree, so a
// live polecat's branch is never at risk.
func cleanupFailedPolecatSpawn(sourceRepo, worktreeDir, branchName string) {
	if worktreeDir == "" {
		return
	}
	exec.Command("git", "-C", sourceRepo, "worktree", "remove", worktreeDir, "--force").Run()
	// Backstop: if git refused (e.g. files created after checkout) or the
	// worktree was never registered, reclaim the directory anyway so no
	// orphan is left behind (gh #31).
	if err := os.RemoveAll(worktreeDir); err != nil {
		log.Printf("polecat spawn cleanup: failed to remove worktree dir %s: %v", worktreeDir, err)
	}
	if branchName != "" {
		if out, err := exec.Command("git", "-C", sourceRepo, "branch", "-D", branchName).CombinedOutput(); err != nil {
			log.Printf("polecat spawn cleanup: failed to delete branch %s in %s: %v\n%s", branchName, sourceRepo, err, out)
		}
	}
}

// resolvePolecatBaseRef returns the git ref a new polecat worktree should be
// based on. It fetches origin so the polecat sees the latest merged commits,
// then prefers origin/<branch> (the branch the polecat will target via the
// refinery) and falls back to origin/HEAD's default branch. Returns "" when
// no usable origin is available, in which case the caller should base the
// worktree on the source repo's local HEAD.
//
// This exists because creating the worktree from local HEAD made polecats
// invisible to commits that had been merged to origin/main but not yet
// pulled into the source checkout, producing false reports of unmerged work.
func resolvePolecatBaseRef(sourceRepo, branch string) string {
	if sourceRepo == "" {
		return ""
	}
	if err := exec.Command("git", "-C", sourceRepo, "remote", "get-url", "origin").Run(); err != nil {
		return ""
	}
	if out, err := exec.Command("git", "-C", sourceRepo, "fetch", "origin").CombinedOutput(); err != nil {
		log.Printf("polecat: fetch origin failed in %s, falling back to local HEAD: %v\n%s", sourceRepo, err, out)
		return ""
	}
	if branch != "" {
		ref := "origin/" + branch
		if exec.Command("git", "-C", sourceRepo, "rev-parse", "--verify", ref).Run() == nil {
			return ref
		}
	}
	if out, err := exec.Command("git", "-C", sourceRepo, "symbolic-ref", "refs/remotes/origin/HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		if r := strings.TrimPrefix(ref, "refs/remotes/"); r != ref {
			return r
		}
	}
	if exec.Command("git", "-C", sourceRepo, "rev-parse", "--verify", "origin/main").Run() == nil {
		return "origin/main"
	}
	return ""
}

func (r *Registry) handlePrompts(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	prompts, err := ListPrompts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(prompts)
}
