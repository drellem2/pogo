package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	Name     string   `json:"name"`           // Agent name (e.g., short ID)
	Template string   `json:"template"`       // Template name (default: "polecat")
	Task     string   `json:"task,omitempty"` // Work item title
	Body     string   `json:"body,omitempty"` // Work item body
	Id       string   `json:"id,omitempty"`   // Work item ID
	Repo     string   `json:"repo,omitempty"` // Target repository path
	Env      []string `json:"env,omitempty"`  // Additional env vars
}

// NudgeAPIRequest is the JSON body for POST /agents/:name/nudge.
type NudgeAPIRequest struct {
	Message string `json:"message"`
	// Mode: "wait-idle" (default) or "immediate".
	Mode string `json:"mode,omitempty"`
	// Timeout in seconds for wait-idle mode. Default: 30.
	Timeout int `json:"timeout,omitempty"`
}

// OutputAPIRequest query params for GET /agents/:name/output.
// ?lines=N or ?bytes=N

func agentInfo(a *Agent) AgentInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AgentInfo{
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
		Uptime:       time.Since(a.StartTime).Truncate(time.Second).String(),
	}
}

// RegisterHandlers registers agent API endpoints on the given mux.
func (r *Registry) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/agents", r.handleAgents)
	mux.HandleFunc("/agents/start", r.handleStart)
	mux.HandleFunc("/agents/spawn-polecat", r.handleSpawnPolecat)
	mux.HandleFunc("/agents/prompts", r.handlePrompts)
	mux.HandleFunc("/agents/{name}", r.handleAgent)
	mux.HandleFunc("/agents/{name}/nudge", r.handleNudge)
	mux.HandleFunc("/agents/{name}/output", r.handleOutput)
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
	if agent == nil {
		// Return 404 with structured response so clients can detect and fall back
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

	// Read prompt file and pass via --append-system-prompt
	promptContent, err := os.ReadFile(promptFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read prompt file: %v", err), http.StatusInternalServerError)
		return
	}

	a, err := r.Spawn(SpawnRequest{
		Name:       startReq.Name,
		Type:       TypeCrew,
		Command:    []string{"claude", "--append-system-prompt", string(promptContent)},
		PromptFile: promptFile,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
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

	// Expand template to a temp file
	vars := TemplateVars{
		Task: spawnReq.Task,
		Body: spawnReq.Body,
		Id:   spawnReq.Id,
		Repo: spawnReq.Repo,
	}
	promptFile, err := ExpandTemplateToFile(tmplPath, vars)
	if err != nil {
		http.Error(w, fmt.Sprintf("template expansion failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Read expanded prompt and pass via --append-system-prompt
	expandedContent, err := os.ReadFile(promptFile)
	if err != nil {
		os.Remove(promptFile)
		http.Error(w, fmt.Sprintf("failed to read expanded prompt: %v", err), http.StatusInternalServerError)
		return
	}

	a, err := r.Spawn(SpawnRequest{
		Name:       spawnReq.Name,
		Type:       TypePolecat,
		Command:    []string{"claude", "--append-system-prompt", string(expandedContent)},
		Env:        spawnReq.Env,
		PromptFile: promptFile,
	})
	if err != nil {
		os.Remove(promptFile) // Clean up temp file on spawn failure
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
