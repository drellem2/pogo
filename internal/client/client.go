////////////////////////////////////////////////////////////////////////////////
////////// Http client for pogod ///////////////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nightlyone/lockfile"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/health"
	"github.com/drellem2/pogo/internal/project"
	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

var serverURL = config.Load().ServerURL()

type ProjectStatusResponse struct {
	Id        int    `json:"id"`
	Path      string `json:"path"`
	Status    string `json:"indexing_status"`
	FileCount int    `json:"file_count"`
}

type ClientResp interface {
	[]project.Project | *project.VisitResponse | *SearchResponse | []string | []ProjectStatusResponse
}

type PogoChunkMatch struct {
	Line    uint32 `json:"line"`
	Content string `json:"content"`
}

type PogoFileMatch struct {
	Path    string           `json:"path"`
	Matches []PogoChunkMatch `json:"matches"`
}

type SearchResults struct {
	Files []PogoFileMatch `json:"files"`
}

type IndexedProject struct {
	Root   string   `json:"root"`
	Paths  []string `json:"paths"`
	Status string   `json:"indexing_status"`
}

type SearchResponse struct {
	Index   IndexedProject `json:"index"`
	Results SearchResults  `json:"results"`
	Error   string         `json:"error"`
}

type SearchRequest struct {
	// Values: "search" or "files"
	Type        string `json:"type"`
	ProjectRoot string `json:"projectRoot"`
	// Command timeout duration - only for 'search'-type requests
	Duration string `json:"string"`
	Data     string `json:"data"`
}

func HealthCheck() error {
	_, err := http.Post(serverURL+"/health", "application/json",
		nil)
	return err
}

// GetFullHealth fetches the structured /health/full report from pogod.
func GetFullHealth() (*health.FullResponse, error) {
	resp, err := http.Get(serverURL + "/health/full")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out health.FullResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// startupHealthTimeout bounds how long StartServer waits for pogod to bind
// after spawning it. 5s matches the design in mg-71e6: long enough that a
// cold-start pogod has time to come up, short enough that a true bind
// failure surfaces promptly to the user instead of a silent false-success.
const startupHealthTimeout = 5 * time.Second

// daemonProbeTimeout bounds the TCP dial used to detect whether a pogod is
// already bound to the daemon port. It is short because the probe targets
// loopback: a live daemon answers the SYN immediately, and a free port refuses
// immediately, so the only thing this timeout guards against is a dropped
// packet on a wedged host.
const daemonProbeTimeout = 500 * time.Millisecond

// daemonBound reports whether something is already listening on the pogod TCP
// port. It probes the actual bind (a raw TCP dial to 127.0.0.1:<port>) rather
// than /health, answering the specific question "would a second pogod fail to
// bind :10000?".
func daemonBound() bool {
	return portBound(config.Load().DialAddr())
}

// portBound reports whether a TCP listener currently holds addr.
func portBound(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, daemonProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// StartServer ensures a pogod is running, spawning one only when the daemon
// port is actually free.
//
// If the port is already bound we must NOT spawn a rival pogod: a second pogod
// either loses the bind and exits — closing the PTY masters of any agents it
// had started — or, during a launchd restart window, wins the bind and
// displaces the launchd-managed daemon. Either path hangs up the agent fleet
// via SIGHUP (#22). When the port is held we treat the daemon as up and return
// nil so the caller connects to it instead. The primary guard against a double
// daemon is still the shared singleton lockfile (config.LockfilePath); this is
// a cheaper, earlier check on the client side.
func StartServer() error {
	if daemonBound() {
		return nil
	}
	return startServerCmd(newServerCmd(), HealthCheck, startupHealthTimeout)
}

// newServerCmd builds the pogod invocation used by StartServer. The daemon is
// started in its own session (Setsid) so it is detached from the invoking
// CLI's process group and controlling terminal. Without this, an auto-started
// pogod is a member of whatever foreground group ran the CLI — a Ctrl-C at
// that terminal, the terminal closing, or a harness tearing down the CLI's
// process group SIGTERMs pogod along with it, and every agent dies with it (the
// gh #22 cascade: LastExitStatus=15 with no crash trace). Not because pogod
// stops them — it has no signal handler and never gets to run cleanup — but
// because its death force-closes the PTY masters it owns, hanging up each
// agent's controlling terminal (mg-6b66).
// Same isolation detach.go uses for `pogod --detach`.
func newServerCmd() *exec.Cmd {
	cmd := exec.Command("pogod")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// startServerCmd spawns the given command and waits for healthCheck to
// succeed within timeout. It captures pogod's stdout+stderr so that,
// when the daemon fails to bind (or exits early), the error message
// surfaces the underlying cause rather than reporting a false success.
// Both streams are captured because pogod's startup-error path is
// inconsistent — lockfile errors go to stdout (fmt.Printf) while runtime
// log lines go to stderr (log package).
//
// On success, the spawned process is left running. On failure, the
// process is killed and its captured output is included in the returned
// error (truncated to a sane prefix).
//
// Ownership note: startServerCmd spawns a background goroutine that calls
// cmd.Wait and keeps it after returning. Callers (including tests) must
// never call cmd.Wait themselves — os/exec forbids concurrent Waits, and
// the losing Wait blocks forever in awaitGoroutines because the internal
// goroutineErr channel is sent exactly one value (mg-59d5). To stop the
// process, Kill it and let the background goroutine reap it.
func startServerCmd(cmd *exec.Cmd, healthCheck func() error, timeout time.Duration) error {
	var output bytes.Buffer
	var outputMu sync.Mutex
	writer := &lockedWriter{w: &output, mu: &outputMu}
	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to spawn pogod: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	readOutput := func() string {
		outputMu.Lock()
		defer outputMu.Unlock()
		msg := strings.TrimSpace(output.String())
		const max = 1024
		if len(msg) > max {
			msg = msg[:max] + "..."
		}
		return msg
	}

	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	if err := healthCheck(); err == nil {
		return nil
	}

	for {
		select {
		case waitErr := <-exited:
			msg := readOutput()
			if msg == "" {
				return fmt.Errorf("pogod exited before binding: %v", waitErr)
			}
			return fmt.Errorf("pogod exited before binding: %s", msg)
		case <-deadline:
			_ = cmd.Process.Kill()
			<-exited
			msg := readOutput()
			if msg == "" {
				return fmt.Errorf("pogod did not become healthy within %s", timeout)
			}
			return fmt.Errorf("pogod did not become healthy within %s: %s", timeout, msg)
		case <-ticker.C:
			if err := healthCheck(); err == nil {
				return nil
			}
		}
	}
}

// lockedWriter serializes writes to an underlying buffer so that the
// background cmd.Wait goroutine and the polling goroutine can safely
// read pogod's stderr after a deadline trips.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// GetServerMode returns the current run mode of the server ("full" or "index-only").
func GetServerMode() (string, error) {
	resp, err := http.Get(serverURL + "/server/mode")
	if err != nil {
		return "", fmt.Errorf("failed to contact server: %w", err)
	}
	defer resp.Body.Close()
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	return result["mode"], nil
}

// StartOrchestration tells pogod to transition to full mode,
// restarting agents and refinery without re-indexing.
func StartOrchestration() error {
	resp, err := http.Post(serverURL+"/server/start-orchestration", "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to contact server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// StopOrchestration tells pogod to transition to index-only mode,
// stopping agents and refinery while keeping the server alive.
func StopOrchestration() error {
	resp, err := http.Post(serverURL+"/server/stop-orchestration", "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to contact server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func StopServer() error {
	// Must match cmd/pogod's lock path (config.LockfilePath) so we read the
	// same lockfile the running daemon holds — a TempDir-based path would miss
	// the launchd-domain lock and report "not running" (#22).
	pidPath := config.LockfilePath()

	lock, err := lockfile.New(pidPath)
	if err != nil {
		return fmt.Errorf("cannot access lockfile: %w", err)
	}

	proc, err := lock.GetOwner()
	if err != nil {
		return fmt.Errorf("server is not running (no valid lockfile): %w", err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to pid %d: %w", proc.Pid, err)
	}

	// Wait for clean shutdown by polling for process exit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			// Process is gone — clean shutdown
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("server pid %d did not stop within 5 seconds", proc.Pid)
}

// Run closure with health check
func RunWithHealthCheck[T ClientResp](run func() (T, error)) (T, error) {
	if err := HealthCheck(); err != nil {
		// StartServer blocks until pogod is healthy or returns a descriptive
		// error (including captured stderr) — no separate retry loop needed
		// here. It also refuses to spawn a rival pogod when :10000 is already
		// bound (connecting to the existing daemon instead), so a missed
		// /health probe can't displace the running daemon (#22).
		if err := StartServer(); err != nil {
			return nil, err
		}
	}
	return run()
}

func GetProjects() ([]project.Project, error) {
	projs, err := RunWithHealthCheck(func() ([]project.Project, error) {
		r, err := http.Get(serverURL + "/projects")
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		// Deserialize projResp
		// Do json demarshal from http response
		var projs []project.Project
		err = json.Unmarshal(body, &projs)
		if err != nil {
			return nil, err
		}
		return projs, nil
	})
	if err != nil {
		return nil, err
	}
	return projs, nil
}

func GetPlugins() ([]string, error) {
	plugins, err := RunWithHealthCheck(func() ([]string, error) {
		r, err := http.Get(serverURL + "/plugins")
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		// Deserialize projResp
		// Do json demarshal from http response
		var plugins []string
		err = json.Unmarshal(body, &plugins)
		if err != nil {
			return nil, err
		}
		return plugins, nil
	})
	if err != nil {
		return nil, err
	}
	return plugins, nil
}

func GetSearchPlugin() (string, error) {
	plugins, err := GetPlugins()
	if err != nil {
		return "", err
	}
	for _, plugin := range plugins {
		if strings.Contains(plugin, "pogo-plugin-search") {
			return plugin, nil
		}
	}
	return "", errors.New("search plugin not found")
}

// searchProject sends one plugin search request for a single project root
// and decodes the response. It performs no health probe of its own — callers
// establish server liveness once up front rather than per call — and uses
// the default keep-alive client, so consecutive calls reuse one TCP
// connection instead of paying a fresh handshake each time (gh #39).
func searchProject(searchPluginPath string, searchRequest SearchRequest) (*SearchResponse, error) {
	searchRequestJson, err := json.Marshal(searchRequest)
	if err != nil {
		return nil, err
	}
	dataObj := pogoPlugin.DataObject{
		Plugin: searchPluginPath,
		Value:  string(searchRequestJson),
	}
	dataObjJson, err := json.Marshal(dataObj)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", serverURL+"/plugin",
		strings.NewReader(string(dataObjJson)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var dataObject pogoPlugin.DataObject
	err = json.Unmarshal(body, &dataObject)
	if err != nil {
		return nil, err
	}
	var results SearchResponse
	err = json.Unmarshal([]byte(dataObject.Value), &results)
	if err != nil {
		return nil, err
	}
	return &results, nil
}

// dir may be inside of a project path. First we have to look up the
func Search(query string, dir string) (*SearchResponse, error) {
	// corresponding project root, if any
	projectResp, err := Visit(dir)
	if err != nil {
		return nil, err
	}
	if projectResp == nil {
		return nil, errors.New("response nil")
	}
	projectRoot := projectResp.ParentProject.Path
	searchPluginPath, err := GetSearchPlugin()
	if err != nil {
		return nil, err
	}
	var searchRequest = SearchRequest{
		Type:        "search",
		ProjectRoot: projectRoot,
		Duration:    "10s",
		Data:        query,
	}
	results, err := RunWithHealthCheck(func() (*SearchResponse, error) {
		return searchProject(searchPluginPath, searchRequest)
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func GetStatus() ([]ProjectStatusResponse, error) {
	statuses, err := RunWithHealthCheck(func() ([]ProjectStatusResponse, error) {
		r, err := http.Get(serverURL + "/status")
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var statuses []ProjectStatusResponse
		err = json.Unmarshal(body, &statuses)
		if err != nil {
			return nil, err
		}
		return statuses, nil
	})
	if err != nil {
		return nil, err
	}
	return statuses, nil
}

// SearchAll searches across all known projects, returning results for each.
func SearchAll(query string) ([]*SearchResponse, error) {
	var results []*SearchResponse
	err := SearchAllStreaming(query, func(resp *SearchResponse) {
		results = append(results, resp)
	})
	return results, err
}

// searchAllConcurrency bounds the parallel per-project fan-out of
// SearchAllStreaming. Enough to hide per-request latency at fleet scale,
// small enough not to stampede pogod.
const searchAllConcurrency = 8

// SearchAllStreaming searches across all known projects, calling onResult for
// each repo's results as soon as they are available. This allows callers to
// display results incrementally instead of waiting for every repo to finish.
//
// Server liveness is established once by the initial project listing; the
// per-project requests then fan out in parallel over kept-alive connections
// with no per-call health probe — previously each project cost two serial
// round-trips on a fresh connection, the dominant CLI latency at fleet scale
// (gh #39). onResult calls are serialized, so callers need no locking, but
// arrival order is not the registry order.
func SearchAllStreaming(query string, onResult func(*SearchResponse)) error {
	projs, err := GetProjects()
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}
	if len(projs) == 0 {
		return errors.New("no projects registered with pogo")
	}

	searchPluginPath, err := GetSearchPlugin()
	if err != nil {
		return err
	}

	sem := make(chan struct{}, searchAllConcurrency)
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	for _, proj := range projs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := searchProject(searchPluginPath, SearchRequest{
				Type:        "search",
				ProjectRoot: proj.Path,
				Duration:    "10s",
				Data:        query,
			})
			resultMu.Lock()
			defer resultMu.Unlock()
			if err != nil {
				// Include the error as a result rather than aborting the whole search
				onResult(&SearchResponse{
					Index: IndexedProject{Root: proj.Path},
					Error: err.Error(),
				})
				return
			}
			if resp != nil && (len(resp.Results.Files) > 0 || resp.Error != "") {
				onResult(resp)
			}
		}()
	}
	wg.Wait()
	return nil
}

// RemoveProject removes a project from pogod by path.
func RemoveProject(path string) error {
	err := HealthCheck()
	if err != nil {
		return fmt.Errorf("server is not running: %w", err)
	}
	req, err := http.NewRequest("DELETE", serverURL+"/projects",
		strings.NewReader(fmt.Sprintf(`{"path": "%s"}`, path)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("project not found: %s", path)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func Visit(path string) (*project.VisitResponse, error) {
	visitResp, err := RunWithHealthCheck(func() (*project.VisitResponse, error) {
		r, err := http.Post(serverURL+"/file",
			"application/json",
			strings.NewReader(
				fmt.Sprintf(`{"path": "%s"}`, path)))
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var resp project.VisitResponse
		err = json.Unmarshal(body, &resp)
		if err != nil {
			return nil, err
		}
		return &resp, nil
	})
	if err != nil {
		return nil, err
	}
	return visitResp, nil
}
