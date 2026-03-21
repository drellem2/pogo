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
	"strings"
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
		Uptime:       agentUptime(a),
	}
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

	// Give crew agents a stable working directory under ~/.pogo/agents/<name>/
	home, _ := os.UserHomeDir()
	agentDir := filepath.Join(home, ".pogo", "agents", startReq.Name)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create agent dir: %v", err), http.StatusInternalServerError)
		return
	}

	a, err := r.Spawn(SpawnRequest{
		Name:       startReq.Name,
		Type:       TypeCrew,
		// DO NOT change --dangerously-skip-permissions. Polecats have regressed this
		// flag twice. --permission-mode bypassPermissions does NOT work without
		// additional setup. This flag is required for autonomous agent execution.
		Command:    []string{"claude", "--dangerously-skip-permissions", "--append-system-prompt", string(promptContent)},
		PromptFile: promptFile,
		Dir:        agentDir,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Nudge crew agent after a brief delay to kick off execution.
	// Claude Code interactive mode waits for input — this sends the first message.
	go func() {
		time.Sleep(5 * time.Second)
		a.Nudge("You are now running. Begin your coordination loop.")
	}()

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

	// Build command — interactive mode so Claude can actually run commands.
	// Use --bare to isolate polecats from shared project memories: skips auto-memory,
	// CLAUDE.md auto-discovery, hooks, and other persistent state. Context is provided
	// explicitly via --append-system-prompt and --add-dir.
	// DO NOT change --dangerously-skip-permissions. See comment in handleStart.
	cmd := []string{"claude", "--bare", "--dangerously-skip-permissions", "--append-system-prompt", string(expandedContent)}

	// Ensure POGO_ROLE is set for mg prime and role detection
	env := append(spawnReq.Env, "POGO_ROLE=polecat")

	// Create git worktree for polecat isolation
	var worktreeDir, sourceRepo string
	if spawnReq.Repo != "" {
		home, _ := os.UserHomeDir()
		worktreeDir = filepath.Join(home, ".pogo", "polecats", spawnReq.Name)
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

		// With --bare, CLAUDE.md is not auto-discovered. Add the worktree dir
		// explicitly so project instructions (CLAUDE.md) are still loaded.
		cmd = append(cmd, "--add-dir", worktreeDir)
	}

	a, err := r.Spawn(SpawnRequest{
		Name:       spawnReq.Name,
		Type:       TypePolecat,
		Command:    cmd,
		Env:        env,
		PromptFile: promptFile,
		Dir:        worktreeDir,
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

	// Nudge polecat after a brief delay to kick off execution.
	// Claude Code interactive mode waits for input — this sends the initial prompt.
	if spawnReq.Task != "" {
		go func() {
			time.Sleep(5 * time.Second)
			prompt := buildPolecatPrompt(spawnReq)
			a.Nudge(prompt)
		}()
	}

	// Store worktree info on the agent for cleanup
	a.WorktreeDir = worktreeDir
	a.SourceRepo = sourceRepo

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(agentInfo(a))
}

// buildPolecatPrompt constructs a structured prompt for polecat execution.
// Instead of passing just the task title (which Claude treats as a coding task
// and ignores the protocol steps in the system prompt), this embeds the mandatory
// mg lifecycle steps directly in the prompt so they are treated as first-class
// instructions.
func buildPolecatPrompt(req SpawnPolecatAPIRequest) string {
	var b strings.Builder
	b.WriteString("Execute this polecat assignment. Follow ALL steps — skipping any step is a failure.\n\n")
	fmt.Fprintf(&b, "## Assignment: %s\n\n", req.Task)
	if req.Body != "" {
		fmt.Fprintf(&b, "### Details\n%s\n\n", req.Body)
	}
	b.WriteString("## Mandatory Steps (execute in order)\n\n")
	fmt.Fprintf(&b, "1. **Claim the work item** (prevents duplicate work):\n   ```bash\n   mg claim %s\n   ```\n\n", req.Id)
	b.WriteString("2. **Do the work** in your current directory (worktree). Stay focused on the task above.\n\n")
	fmt.Fprintf(&b, "3. **Commit and push your branch:**\n   ```bash\n   git add <files>\n   git commit -m \"<type>: <description> (%s)\"\n   git push origin polecat-%s\n   ```\n\n", req.Id, req.Id)
	fmt.Fprintf(&b, "4. **Submit to the merge queue:**\n   ```bash\n   pogo refinery submit polecat-%s --repo=%s --author=%s\n   ```\n\n", req.Id, req.Repo, req.Id)
	fmt.Fprintf(&b, "5. **Mark done:**\n   ```bash\n   mg done %s --result='{\"branch\": \"polecat-%s\"}'\n   ```\n\n", req.Id, req.Id)
	b.WriteString("6. **Exit.** The refinery handles testing and merging.\n\n")
	b.WriteString("CRITICAL: You MUST run `mg claim` before starting work and `mg done` after submitting. These are not optional.\n")
	return b.String()
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
