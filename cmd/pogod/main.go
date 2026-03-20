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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nightlyone/lockfile"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/refinery"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

var agentRegistry *agent.Registry
var mergeQueue *refinery.Refinery

var bindFlag = flag.String("bind", "", "address to bind the server to (default: 127.0.0.1)")

func health(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /health")
	fmt.Fprintf(w, "pogo is up and bouncing")
}

func homePage(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /")
	fmt.Fprintf(w, "greetings from pogo daemon")
}

func allProjects(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects")
	json.NewEncoder(w).Encode(project.Projects())
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

func handleRequests() {
	cfg := config.Load()
	if *bindFlag != "" {
		cfg.Bind = *bindFlag
	}
	http.HandleFunc("/", homePage)
	http.HandleFunc("/file", file)
	http.HandleFunc("/projects/{projectId}", projectById)
	http.HandleFunc("/projects", allProjects)
	http.HandleFunc("/plugin", plugin)
	http.HandleFunc("/plugins", plugins)
	http.HandleFunc("/health", health)
	http.HandleFunc("/status", status)

	// Agent management endpoints
	agentRegistry.RegisterHandlers(http.DefaultServeMux)

	// Refinery endpoints
	if mergeQueue != nil {
		mergeQueue.RegisterHandlers(http.DefaultServeMux)
	}

	addr := cfg.ListenAddr()
	fmt.Printf("pogod starting on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func main() {
	flag.Parse()

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
			// Polecat agents: remove from registry on exit
			log.Printf("polecat %s exited, cleaning up", a.Name)
			a.Cleanup()
			agentRegistry.Remove(a.Name)
		}
	})

	// Start plugins
	driver.Init()

	defer driver.Kill()
	defer project.SaveProjects()
	project.Init()

	// Start background repo scanner
	if err := project.StartScanner(); err != nil {
		fmt.Printf("Warning: repo scanner failed to start: %v\n", err)
	}
	defer project.StopScanner()

	// Start refinery merge queue loop
	cfg := config.Load()
	if cfg.Refinery.Enabled {
		refineCfg := refinery.DefaultConfig()
		if cfg.Refinery.PollInterval > 0 {
			refineCfg.PollInterval = cfg.Refinery.PollInterval
		}
		var refErr error
		mergeQueue, refErr = refinery.New(refineCfg)
		if refErr != nil {
			fmt.Printf("Warning: refinery failed to start: %v\n", refErr)
		} else {
			mergeQueue.SetOnMerged(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: merged %s (branch=%s, author=%s)", mr.ID, mr.Branch, mr.Author)
			})
			mergeQueue.SetOnFailed(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: failed %s (branch=%s, author=%s, error=%s)", mr.ID, mr.Branch, mr.Author, mr.Error)
			})
			go mergeQueue.Start(context.Background())
			defer mergeQueue.Stop()
		}
	}

	handleRequests()
}
