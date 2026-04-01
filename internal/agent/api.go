package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// AgentInfo is the JSON representation of an agent for the API.
type AgentInfo struct {
	Name         string      `json:"name"`
	PID          int         `json:"pid"`
	Type         AgentType   `json:"type"`
	StartTime    time.Time   `json:"start_time"`
	Command      []string    `json:"command"`
	SocketPath   string      `json:"socket_path"`
	Status       AgentStatus `json:"status"`
	ExitCode     int         `json:"exit_code,omitempty"`
	RestartCount int         `json:"restart_count,omitempty"`
	PromptFile   string      `json:"prompt_file,omitempty"`
	ProcessName  string      `json:"process_name"`
	Uptime       string      `json:"uptime"`
	LastActivity string      `json:"last_activity,omitempty"`
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
	Name     string   `json:"name"`             // Agent name (e.g., short ID)
	Template string   `json:"template"`         // Template name (default: "polecat")
	Task     string   `json:"task,omitempty"`   // Work item title
	Body     string   `json:"body,omitempty"`   // Work item body
	Id       string   `json:"id,omitempty"`     // Work item ID
	Repo     string   `json:"repo,omitempty"`   // Target repository path
	Branch   string   `json:"branch,omitempty"` // Target branch for refinery submit
	Env      []string `json:"env,omitempty"`    // Additional env vars
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
	// Stalled is true when the agent's idle time exceeds its stall threshold.
	Stalled bool `json:"stalled"`
	// Health is a summary string: "healthy", "idle", "stalled", "exited", or "dead".
	Health string `json:"health"`
	// RecentOutputTail is the last ~500 bytes of PTY output for quick triage.
	RecentOutputTail string `json:"recent_output_tail,omitempty"`
}

// StallThresholdFor returns the stall detection threshold for the given agent type.
func StallThresholdFor(t AgentType) time.Duration {
	if t == TypeCrew {
		return StallThresholdCrew
	}
	return StallThresholdPolecat
}

// diagnoseAgent builds a DiagnoseInfo for the given agent.
func diagnoseAgent(a *Agent) DiagnoseInfo {
	info := agentInfo(a)
	lastWrite := a.outputBuf.LastWriteTime()
	threshold := StallThresholdFor(a.Type)

	var idleDur time.Duration
	if !lastWrite.IsZero() {
		idleDur = time.Since(lastWrite)
	}

	// Check if the OS process is still alive via kill(pid, 0).
	processAlive := false
	if a.PID > 0 {
		err := syscall.Kill(a.PID, 0)
		processAlive = err == nil
	}

	// Determine overall health.
	health := "healthy"
	switch {
	case info.Status == StatusExited:
		health = "exited"
	case a.PID > 0 && !processAlive && info.Status == StatusRunning:
		health = "dead"
	case !lastWrite.IsZero() && idleDur >= threshold:
		health = "stalled"
	case !lastWrite.IsZero() && idleDur >= threshold/2:
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
		Stalled:          !lastWrite.IsZero() && idleDur >= threshold,
		Health:           health,
		RecentOutputTail: tail,
	}
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
		Name:         a.Name,
		PID:          a.PID,
		Type:         a.Type,
		StartTime:    a.StartTime,
		Command:      a.Command,
		SocketPath:   a.SocketPath(),
		Status:       a.Status,
		ExitCode:     a.ExitCode,
		RestartCount: a.RestartCount,
		PromptFile:   a.PromptFile,
		ProcessName:  ProcessName(a.Type, a.Name),
		Uptime:       agentUptime(a),
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(infos)

	case "POST":
		var spawnReq SpawnAPIRequest
		if err := json.NewDecoder(req.Body).Decode(&spawnReq); err != nil {
			http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
			return
		}
		a, err := r.Spawn(SpawnRequest{
			Name:       spawnReq.Name,
			Type:       spawnReq.Type,
			Command:    spawnReq.Command,
			Env:        spawnReq.Env,
			PromptFile: spawnReq.PromptFile,
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
	json.NewEncoder(w).Encode(diagnoseAgent(agent))
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
// Default: ~/.pogo/agents/crew/
func CrewPromptDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo", "agents", "crew")
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

	// Look up prompt file: mayor.md is in PromptDir, crew in CrewPromptDir
	var promptFile string
	if startReq.Name == "mayor" {
		promptFile = filepath.Join(PromptDir(), "mayor.md")
	} else {
		promptFile = filepath.Join(CrewPromptDir(), startReq.Name+".md")
	}
	if _, err := os.Stat(promptFile); os.IsNotExist(err) {
		http.Error(w, fmt.Sprintf("prompt file not found: %s (run 'pogo agent prompt install' to install defaults)", promptFile), http.StatusNotFound)
		return
	}

	// Give crew agents a stable working directory under ~/.pogo/agents/<name>/
	home, _ := os.UserHomeDir()
	agentDir := filepath.Join(home, ".pogo", "agents", startReq.Name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create agent dir: %v", err), http.StatusInternalServerError)
		return
	}

	// Build command from configurable template.
	// Default: "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}"
	// NOTE: --dangerously-skip-permissions is required for autonomous agent execution.
	// --permission-mode bypassPermissions does NOT work without additional setup.
	cmd, err := ExpandCommand(r.commandTemplate(TypeCrew), CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  startReq.Name,
		AgentType:  string(TypeCrew),
		WorkDir:    agentDir,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("agent command template error: %v", err), http.StatusInternalServerError)
		return
	}

	// All crew agents get an initial nudge to bypass the CLI interactive prompt.
	// The mayor gets a specific coordination message; others get a generic start message.
	var nudgeMsg string
	if startReq.Name == "mayor" {
		nudgeMsg = "You are now running. Begin your coordination loop."
	} else {
		nudgeMsg = "You are now running. Check your mail with `mg mail list " + startReq.Name + "` and begin your work."
	}

	a, spawnErr := r.Spawn(SpawnRequest{
		Name:         startReq.Name,
		Type:         TypeCrew,
		Command:      cmd,
		PromptFile:   promptFile,
		Dir:          agentDir,
		InitialNudge: nudgeMsg,
	})
	if spawnErr != nil {
		http.Error(w, spawnErr.Error(), http.StatusConflict)
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

	// Compute worktree path before template expansion so it can be included in the prompt.
	var worktreeDir, sourceRepo string
	if spawnReq.Repo != "" {
		home, _ := os.UserHomeDir()
		worktreeDir = filepath.Join(home, ".pogo", "polecats", spawnReq.Name)
	}

	// Expand template to a temp file
	vars := TemplateVars{
		Task:        spawnReq.Task,
		Body:        spawnReq.Body,
		Id:          spawnReq.Id,
		Repo:        spawnReq.Repo,
		Branch:      spawnReq.Branch,
		WorktreeDir: worktreeDir,
	}
	promptFile, err := ExpandTemplateToFile(tmplPath, vars)
	if err != nil {
		http.Error(w, fmt.Sprintf("template expansion failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Ensure POGO_ROLE is set for mg prime and role detection
	env := append(spawnReq.Env, "POGO_ROLE=polecat")

	// Create git worktree for polecat isolation
	if spawnReq.Repo != "" {
		sourceRepo = spawnReq.Repo
		branchName := "polecat-" + spawnReq.Name

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
			os.Remove(promptFile)
			http.Error(w, fmt.Sprintf("failed to create polecats dir: %v", err), http.StatusInternalServerError)
			return
		}

		wtCmd := exec.Command("git", "-C", sourceRepo, "worktree", "add", worktreeDir, "-b", branchName)
		if out, err := wtCmd.CombinedOutput(); err != nil {
			os.Remove(promptFile)
			http.Error(w, fmt.Sprintf("worktree creation failed: %v\n%s", err, out), http.StatusInternalServerError)
			return
		}
		log.Printf("polecat %s: created worktree at %s (branch %s)", spawnReq.Name, worktreeDir, branchName)
		// No --add-dir needed: the process CWD is set to worktreeDir via SpawnRequest.Dir,
		// and --add-dir triggers a directory trust prompt that blocks autonomous execution.
	}

	// Build command from configurable template.
	// Default: "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}"
	// NOTE: --dangerously-skip-permissions is required for autonomous execution.
	polecatCmdTmpl := r.commandTemplate(TypePolecat)
	ValidatePolecatCommand(polecatCmdTmpl)
	cmd, cmdErr := ExpandCommand(polecatCmdTmpl, CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  spawnReq.Name,
		AgentType:  string(TypePolecat),
		WorkDir:    worktreeDir,
	})
	if cmdErr != nil {
		os.Remove(promptFile)
		if worktreeDir != "" {
			exec.Command("git", "-C", sourceRepo, "worktree", "remove", worktreeDir, "--force").Run()
		}
		http.Error(w, fmt.Sprintf("agent command template error: %v", cmdErr), http.StatusInternalServerError)
		return
	}

	// Build the initial nudge message for the polecat.
	nudgeMsg := "Look at the system prompt and complete the steps for this work item: " + spawnReq.Id
	if spawnReq.Id == "" {
		nudgeMsg = "You are now running. Begin your assigned task."
	}

	a, err := r.Spawn(SpawnRequest{
		Name:         spawnReq.Name,
		Type:         TypePolecat,
		Command:      cmd,
		Env:          env,
		PromptFile:   promptFile,
		Dir:          worktreeDir,
		WorktreeDir:  worktreeDir,
		SourceRepo:   sourceRepo,
		InitialNudge: nudgeMsg,
	})
	if err != nil {
		os.Remove(promptFile) // Clean up temp file on spawn failure
		// Clean up worktree on spawn failure
		if worktreeDir != "" {
			exec.Command("git", "-C", sourceRepo, "worktree", "remove", worktreeDir, "--force").Run()
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(agentInfo(a))
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
