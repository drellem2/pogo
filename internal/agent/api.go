package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AgentInfo is the JSON representation of an agent for the API.
type AgentInfo struct {
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	Type       AgentType `json:"type"`
	StartTime  time.Time `json:"start_time"`
	Command    []string  `json:"command"`
	SocketPath string    `json:"socket_path"`
}

// SpawnAPIRequest is the JSON body for POST /agents.
type SpawnAPIRequest struct {
	Name    string    `json:"name"`
	Type    AgentType `json:"type"`
	Command []string  `json:"command"`
	Env     []string  `json:"env,omitempty"`
}

// NudgeAPIRequest is the JSON body for POST /agents/:name/nudge.
type NudgeAPIRequest struct {
	Message string `json:"message"`
}

// OutputAPIRequest query params for GET /agents/:name/output.
// ?lines=N or ?bytes=N

func agentInfo(a *Agent) AgentInfo {
	return AgentInfo{
		Name:       a.Name,
		PID:        a.PID,
		Type:       a.Type,
		StartTime:  a.StartTime,
		Command:    a.Command,
		SocketPath: a.SocketPath(),
	}
}

// RegisterHandlers registers agent API endpoints on the given mux.
func (r *Registry) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/agents", r.handleAgents)
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
		agent, err := r.Spawn(SpawnRequest{
			Name:    spawnReq.Name,
			Type:    spawnReq.Type,
			Command: spawnReq.Command,
			Env:     spawnReq.Env,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(agentInfo(agent))

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

func (r *Registry) handleNudge(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	name := req.PathValue("name")
	agent := r.Get(name)
	if agent == nil {
		http.Error(w, fmt.Sprintf("agent %q not found", name), http.StatusNotFound)
		return
	}

	var nudgeReq NudgeAPIRequest
	if err := json.NewDecoder(req.Body).Decode(&nudgeReq); err != nil {
		// If no JSON body, read raw body as the message
		req.Body.Close()
		http.Error(w, "bad request: message required", http.StatusBadRequest)
		return
	}

	if err := agent.Nudge(nudgeReq.Message); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
