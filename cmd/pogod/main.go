////////////////////////////////////////////////////////////////////////////////
////////// This will eventually be the code that is in `pogod`        //////////
////////////////////////////////////////////////////////////////////////////////

package main

import _ "net/http/pprof"
import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nightlyone/lockfile"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/search"
	"github.com/drellem2/pogo/internal/server"
	"github.com/drellem2/pogo/internal/workspace"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

var agentRegistry *agent.Registry
var mergeQueue *refinery.Refinery
var srv *server.Server
var workspaceMgr *workspace.Manager
var startTime time.Time

var bindFlag = flag.String("bind", "", "address to bind the server to (default: 127.0.0.1)")
var portFlag = flag.Int("port", 0, "port to listen on (default: 10000)")

func health(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /health")
	fmt.Fprintf(w, "pogo is up and bouncing")
}

// FullHealthResponse is the structured JSON response for GET /health/full.
type FullHealthResponse struct {
	Pogod    PogodHealth    `json:"pogod"`
	Agents   AgentsHealth   `json:"agents"`
	Refinery RefineryHealth `json:"refinery"`
}

// PogodHealth reports basic daemon health.
type PogodHealth struct {
	Status string `json:"status"`
	Uptime string `json:"uptime"`
	Mode   string `json:"mode"`
}

// AgentHealthDetail is a summary of one agent.
type AgentHealthDetail struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Restarts int    `json:"restarts,omitempty"`
	Uptime   string `json:"uptime"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// AgentsHealth summarises the agent fleet.
type AgentsHealth struct {
	Total   int                 `json:"total"`
	Running int                 `json:"running"`
	Exited  int                 `json:"exited"`
	Details []AgentHealthDetail `json:"details"`
}

// RefineryHealth summarises the refinery state.
type RefineryHealth struct {
	Enabled        bool   `json:"enabled"`
	Running        bool   `json:"running"`
	QueueLength    int    `json:"queue_length"`
	RecentFailures int    `json:"recent_failures"`
	HistoryLength  int    `json:"history_length"`
	PollInterval   string `json:"poll_interval,omitempty"`
}

func healthFull(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	// Pogod health
	mode := "full"
	if srv != nil {
		mode = srv.Mode().String()
	}
	pogodHealth := PogodHealth{
		Status: "ok",
		Uptime: time.Since(startTime).Truncate(time.Second).String(),
		Mode:   mode,
	}

	// Agents health
	var agentsHealth AgentsHealth
	if agentRegistry != nil {
		agents := agentRegistry.List()
		agentsHealth.Total = len(agents)
		agentsHealth.Details = make([]AgentHealthDetail, len(agents))
		for i, a := range agents {
			info := agent.ExportInfo(a)
			detail := AgentHealthDetail{
				Name:     info.Name,
				Status:   string(info.Status),
				Restarts: info.RestartCount,
				Uptime:   info.Uptime,
				ExitCode: info.ExitCode,
			}
			agentsHealth.Details[i] = detail
			switch info.Status {
			case "running":
				agentsHealth.Running++
			default:
				agentsHealth.Exited++
			}
		}
	}

	// Refinery health
	var refineryHealth RefineryHealth
	if mergeQueue != nil {
		st := mergeQueue.GetStatus()
		refineryHealth.Enabled = st.Enabled
		refineryHealth.Running = st.Running
		refineryHealth.QueueLength = st.QueueLen
		refineryHealth.HistoryLength = st.HistoryLen
		refineryHealth.PollInterval = st.PollInterval

		// Count recent failures from history
		for _, mr := range mergeQueue.History() {
			if mr.Status == refinery.StatusFailed {
				refineryHealth.RecentFailures++
			}
		}
	}

	resp := FullHealthResponse{
		Pogod:    pogodHealth,
		Agents:   agentsHealth,
		Refinery: refineryHealth,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func homePage(w http.ResponseWriter, r *http.Request) {
	// Only match the exact root path. In Go 1.22+ ServeMux, the "/{$}"
	// pattern restricts this to "/", but if registered as "/" (catch-all),
	// we must check manually to avoid swallowing unmatched routes with a
	// confusing 200 response instead of a proper 404.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(w, "greetings from pogo daemon")
}

func allProjects(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects")
	switch r.Method {
	case "GET", "":
		json.NewEncoder(w).Encode(project.Projects())
	case "DELETE":
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if project.Remove(req.Path) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"removed": true,
				"path":    req.Path,
			})
		} else {
			http.Error(w, "project not found", http.StatusNotFound)
		}
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func clean(path string) string {
	// Append a trailing delimiter if it doesn't exist
	p := filepath.Clean(path)
	if p[len(p)-1] != filepath.Separator {
		p += string(filepath.Separator)
	}
	return p
}

func file(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /file")
	switch r.Method {
	case "POST":
		decoder := json.NewDecoder(r.Body)
		var req project.VisitRequest
		decodeErr := decoder.Decode(&req)
		if decodeErr != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		req.Path = clean(req.Path)
		response, err := project.Visit(req)
		if err != nil {
			http.Error(w, err.Message, err.Code)
			return
		}
		json.NewEncoder(w).Encode(response)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func plugin(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugin")
	switch r.Method {
	case "GET":
		encodedPath := r.URL.Query().Get("path")
		path, err := url.QueryUnescape(encodedPath)
		if err != nil {
			fmt.Printf("Error urldecoding path variable: %v\n", err)
			return
		}
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		resp := (*plugin).Info()
		json.NewEncoder(w).Encode(resp)
		return
	case "POST":
		var reqObj pogoPlugin.DataObject
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&reqObj)
		if err != nil {
			fmt.Printf("Request could not be parsed.")
			http.Error(w, "", http.StatusBadRequest)
			return
		}

		path := reqObj.Plugin
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		respString := (*plugin).Execute(reqObj.Value)
		var respObj = pogoPlugin.DataObject{Value: respString}
		json.NewEncoder(w).Encode(respObj)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func plugins(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugins")
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(driver.GetPluginPaths())
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func projectById(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects/{projectId}")
	switch r.Method {
	case "GET":
		projectIdStr := r.PathValue("projectId")
		// If projectIdStr blank we look at the queryParameter 'path'
		if projectIdStr == "file" {
			projectPathStr := r.URL.Query().Get("path")
			// url decode projectIdStr
			path, err := url.QueryUnescape(projectPathStr)
			log.Printf("Path: %s\n", path)
			if err != nil {
				log.Printf("Error urldecoding projectIdStr: %v\n", err)
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			proj := project.GetProjectByPath(path)
			if proj == nil {
				http.Error(w, "", http.StatusNotFound)
				return
			}
			resp := project.GetProject(proj.Id)
			json.NewEncoder(w).Encode(resp)
			return
		}
		projectId, err := strconv.Atoi(projectIdStr)
		if err != nil {
			log.Printf("Error converting projectId to int: %v\n", err)
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		resp := project.GetProject(projectId)
		if resp == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(resp)
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func status(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /status")
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(project.GetProjectStatuses())
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func registerHandlers() {
	http.HandleFunc("/", homePage)
	http.HandleFunc("/file", file)
	http.HandleFunc("/projects/{projectId}", projectById)
	http.HandleFunc("/projects", allProjects)
	http.HandleFunc("/plugin", plugin)
	http.HandleFunc("/plugins", plugins)
	http.HandleFunc("/health", health)
	http.HandleFunc("/health/full", healthFull)
	http.HandleFunc("/status", status)

	// Workspace symbol query endpoints
	workspaceMgr = workspace.New()
	workspaceMgr.RegisterHandlers(http.DefaultServeMux)

	// Agent and refinery endpoints behind orchestration guard.
	// When the server is in index-only mode, these return 503.
	orchestrated := http.NewServeMux()
	agentRegistry.RegisterHandlers(orchestrated)
	if mergeQueue != nil {
		mergeQueue.RegisterHandlers(orchestrated)
	}
	if srv != nil {
		http.Handle("/agents/", srv.RequireOrchestration(orchestrated))
		http.Handle("/agents", srv.RequireOrchestration(orchestrated))
		http.Handle("/refinery/", srv.RequireOrchestration(orchestrated))

		// Server mode endpoints (not guarded — always available)
		srv.RegisterHandlers(http.DefaultServeMux)
	} else {
		// No server coordinator — register directly
		http.Handle("/agents/", orchestrated)
		http.Handle("/agents", orchestrated)
		http.Handle("/refinery/", orchestrated)
	}
}

func main() {
	flag.Parse()
	startTime = time.Now()

	// Acquire lockfile
	lock, err := lockfile.New(filepath.Join(os.TempDir(), "pogo.pid"))
	if err != nil {
		fmt.Printf("Cannot create lock. reason: %v", err)
		os.Exit(1)
	}

	if err = lock.TryLock(); err != nil {
		fmt.Printf("Cannot get lock %q, reason: %v", lock, err)
		os.Exit(1)
	}

	defer func() {
		if err := lock.Unlock(); err != nil {
			fmt.Printf("Cannot unlock %q, reason: %v", lock, err)
		}
	}()

	// Initialize agent registry
	socketDir := filepath.Join(os.TempDir(), "pogo-agents")
	var initErr error
	agentRegistry, initErr = agent.NewRegistry(socketDir)
	if initErr != nil {
		fmt.Printf("Cannot create agent registry: %v\n", initErr)
		os.Exit(1)
	}
	defer agentRegistry.StopAll(5 * time.Second)

	// Load config early so we can use it for agent command setup
	cfg := config.Load()

	// Apply file watcher limit from config
	search.SearchService.SetMaxWatchers(cfg.MaxWatchers)

	// Configure agent command templates and validate the binary exists
	agentRegistry.SetCommandConfig(&cfg.Agents)
	agent.ValidateCommandBinary(cfg.Agents.AgentCommand("crew"))
	if polecatCmd := cfg.Agents.AgentCommand("polecat"); polecatCmd != cfg.Agents.AgentCommand("crew") {
		agent.ValidateCommandBinary(polecatCmd)
	}

	// Set up agent lifecycle callbacks
	agentRegistry.SetOnExit(func(a *agent.Agent, err error) {
		if a.Type == agent.TypeCrew {
			// Crew agents: restart on unexpected exit (backoff: 2s)
			log.Printf("crew agent %s exited unexpectedly, scheduling restart", a.Name)
			go func() {
				time.Sleep(2 * time.Second)
				if _, rerr := agentRegistry.Respawn(a.Name); rerr != nil {
					log.Printf("crew agent %s: restart failed: %v", a.Name, rerr)
				}
			}()
		} else {
			// Polecat agents: clean up worktree and remove from registry
			log.Printf("polecat %s exited, cleaning up", a.Name)
			if a.WorktreeDir != "" {
				if err := exec.Command("git", "-C", a.SourceRepo, "worktree", "remove", a.WorktreeDir, "--force").Run(); err != nil {
					log.Printf("polecat %s: worktree removal failed: %v", a.Name, err)
				} else {
					log.Printf("polecat %s: removed worktree %s", a.Name, a.WorktreeDir)
				}
			}
			a.Cleanup()
			agentRegistry.Remove(a.Name)
		}
	})

	// Start plugins
	driver.Init()

	defer driver.Kill()
	defer project.SaveProjects()

	// Load project list from disk (fast, no indexing)
	project.Init()

	// Start refinery merge queue loop
	refineCfg := refinery.DefaultConfig()
	if cfg.Refinery.Enabled {
		if cfg.Refinery.PollInterval > 0 {
			refineCfg.PollInterval = cfg.Refinery.PollInterval
		}
		var refErr error
		mergeQueue, refErr = refinery.New(refineCfg)
		if refErr != nil {
			fmt.Printf("Warning: refinery failed to start: %v\n", refErr)
		} else {
			mergeQueue.SetOnSubmit(func(mr *refinery.MergeRequest) {
				// When a polecat submits an MR, unlink its worktree so the
				// branch is no longer marked as "checked out" in the source
				// repo. This prevents "already checked out" errors in the
				// refinery's clone. The polecat's directory is left intact
				// so it can continue polling for merge results.
				if mr.Author == "" {
					return
				}
				a := agentRegistry.Get(mr.Author)
				if a == nil || a.WorktreeDir == "" || a.SourceRepo == "" {
					return
				}
				if err := refinery.UnlinkWorktree(a.SourceRepo, a.WorktreeDir); err != nil {
					log.Printf("refinery: failed to unlink polecat worktree for %s: %v", mr.Author, err)
				} else {
					log.Printf("refinery: unlinked polecat worktree for %s at %s", mr.Author, a.WorktreeDir)
				}
			})
			mergeQueue.SetOnMerged(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: merged %s (branch=%s, author=%s)", mr.ID, mr.Branch, mr.Author)

				// Mail the mayor so it can stop the polecat and archive
				// the work item. We used to auto-archive here, but the
				// mayor needs to see the done/ item before it gets archived
				// so it can run cleanup (stop polecat, handle QA, etc.).
				subject := fmt.Sprintf("MERGED: %s (branch=%s)", mr.ID, mr.Branch)
				body := fmt.Sprintf("Merge request %s succeeded.\nBranch: %s\nAuthor: %s", mr.ID, mr.Branch, mr.Author)
				if err := client.SendMGMail("mayor", "refinery", subject, body); err != nil {
					log.Printf("refinery: failed to mail mayor: %v", err)
				}
			})
			mergeQueue.SetOnFailed(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: failed %s (branch=%s, author=%s, error=%s)", mr.ID, mr.Branch, mr.Author, mr.Error)

				subject := fmt.Sprintf("MERGE FAILED: %s (branch=%s)", mr.ID, mr.Branch)
				body := fmt.Sprintf("Merge request %s failed.\nBranch: %s\nAuthor: %s\nError: %s\nGate output: %s", mr.ID, mr.Branch, mr.Author, mr.Error, mr.GateOutput)

				// Mail the author agent so they can fix and resubmit.
				if mr.Author != "" {
					if err := client.SendMGMail(mr.Author, "refinery", subject, body); err != nil {
						log.Printf("refinery: failed to mail author %s: %v", mr.Author, err)
					}
				}

				// Mail the mayor so they can re-dispatch if the author exited.
				if err := client.SendMGMail("mayor", "refinery", subject, body); err != nil {
					log.Printf("refinery: failed to mail mayor: %v", err)
				}

				// Auto-reopen the work item so it moves back to claimed/ for retry.
				// This keeps the item assigned to the original polecat.
				// Polecats use their work item ID as the author field.
				if mr.Author != "" {
					if err := client.ReopenMGWorkItem(mr.Author); err != nil {
						log.Printf("refinery: failed to reopen work item %s: %v", mr.Author, err)
					} else {
						log.Printf("refinery: reopened work item %s after merge failure", mr.Author)
					}
				}
			})
			go mergeQueue.Start(context.Background())
			defer mergeQueue.Stop()
		}
	}

	// Initialize server coordinator
	srv = server.New(agentRegistry, mergeQueue)
	if mergeQueue != nil {
		onMerged := mergeQueue.OnMergedFunc()
		onFailed := mergeQueue.OnFailedFunc()
		srv.SetRefineryStarter(func() error {
			newRef, err := refinery.New(refineCfg)
			if err != nil {
				return err
			}
			if onMerged != nil {
				newRef.SetOnMerged(onMerged)
			}
			if onFailed != nil {
				newRef.SetOnFailed(onFailed)
			}
			mergeQueue = newRef
			go mergeQueue.Start(context.Background())
			return nil
		})
	}

	// Register HTTP handlers
	registerHandlers()

	// Start the HTTP listener BEFORE background indexing so the server
	// is immediately responsive to API calls (especially agent management).
	if *bindFlag != "" {
		cfg.Bind = *bindFlag
	}
	if *portFlag != 0 {
		cfg.Port = *portFlag
	}
	addr := cfg.ListenAddr()
	ln, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		log.Fatalf("pogod: failed to listen on %s: %v", addr, listenErr)
	}
	fmt.Printf("pogod listening on %s\n", addr)

	// Now start background work: indexing and repo scanning.
	// The server is already accepting connections above.
	go func() {
		project.IndexAll()
		log.Printf("pogod: background project indexing complete")
	}()

	if err := project.StartScanner(); err != nil {
		fmt.Printf("Warning: repo scanner failed to start: %v\n", err)
	}
	defer project.StopScanner()

	// Start agent cron: periodically nudge crew agents to check mail
	// and run their coordination loops.
	cronCtx, cronCancel := context.WithCancel(context.Background())
	defer cronCancel()
	go startAgentCron(cronCtx, agentRegistry)

	// Serve HTTP (blocks until shutdown)
	log.Fatal(http.Serve(ln, nil))
}

// startAgentCron runs a ticker that nudges crew agents every 60 seconds.
// The mayor gets a coordination loop prompt; other crew agents get a mail-check prompt.
func startAgentCron(ctx context.Context, registry *agent.Registry) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nudgeCrewAgents(registry)
		}
	}
}

// nudgeCrewAgents iterates running crew agents and nudges them.
func nudgeCrewAgents(registry *agent.Registry) {
	agents := registry.List()
	for _, a := range agents {
		if a.Type != agent.TypeCrew {
			continue
		}
		if a.GetStatus() != agent.StatusRunning {
			continue
		}

		var msg string
		if a.Name == "mayor" {
			msg = "Cron: run your coordination loop. Check for available work, monitor agents, read mail."
		} else {
			msg = fmt.Sprintf("Cron: check your mail with `mg mail list %s` and handle any messages.", a.Name)
		}

		go func(a *agent.Agent, msg string) {
			if err := a.NudgeWithMode(msg, agent.NudgeWaitIdle, 30*time.Second); err != nil {
				log.Printf("agent-cron: failed to nudge %s: %v", a.Name, err)
			} else {
				log.Printf("agent-cron: nudged %s", a.Name)
			}
		}(a, msg)
	}
}
