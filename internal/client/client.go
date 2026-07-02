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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

func StartServer() error {
	return startServerCmd(newServerCmd(), HealthCheck, startupHealthTimeout)
}

// newServerCmd builds the pogod invocation used by StartServer. The daemon is
// started in its own session (Setsid) so it is detached from the invoking
// CLI's process group and controlling terminal. Without this, an auto-started
// pogod is a member of whatever foreground group ran the CLI — a Ctrl-C at
// that terminal, the terminal closing, or a harness tearing down the CLI's
// process group SIGTERMs pogod along with it, and pogod's shutdown then stops
// every agent (the gh #22 cascade: LastExitStatus=15 with no crash trace).
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
	pidPath := filepath.Join(os.TempDir(), "pogo.pid")

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
		// StartServer now blocks until pogod is healthy or returns a
		// descriptive error (including captured stderr) — no separate
		// retry loop needed here.
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
	// Marshal searchRequest to json
	searchRequestJson, err := json.Marshal(searchRequest)
	if err != nil {
		return nil, err
	}
	results, err := RunWithHealthCheck(func() (*SearchResponse, error) {
		client := &http.Client{}

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
		req.Close = true
		req.Header.Set("Content-Type", "application/json")
		r, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		// Deserialize projResp
		// Do json demarshal from http response
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

// SearchAllStreaming searches across all known projects, calling onResult for
// each repo's results as soon as they are available. This allows callers to
// display results incrementally instead of waiting for every repo to finish.
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

	for _, proj := range projs {
		req := SearchRequest{
			Type:        "search",
			ProjectRoot: proj.Path,
			Duration:    "10s",
			Data:        query,
		}
		reqJSON, err := json.Marshal(req)
		if err != nil {
			return err
		}
		resp, err := RunWithHealthCheck(func() (*SearchResponse, error) {
			client := &http.Client{}
			dataObj := pogoPlugin.DataObject{
				Plugin: searchPluginPath,
				Value:  string(reqJSON),
			}
			dataObjJson, err := json.Marshal(dataObj)
			if err != nil {
				return nil, err
			}
			httpReq, err := http.NewRequest("POST", serverURL+"/plugin",
				strings.NewReader(string(dataObjJson)))
			if err != nil {
				return nil, err
			}
			httpReq.Close = true
			httpReq.Header.Set("Content-Type", "application/json")
			r, err := client.Do(httpReq)
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
			var sr SearchResponse
			err = json.Unmarshal([]byte(dataObject.Value), &sr)
			if err != nil {
				return nil, err
			}
			return &sr, nil
		})
		if err != nil {
			// Include the error as a result rather than aborting the whole search
			onResult(&SearchResponse{
				Index: IndexedProject{Root: proj.Path},
				Error: err.Error(),
			})
			continue
		}
		if resp != nil && (len(resp.Results.Files) > 0 || resp.Error != "") {
			onResult(resp)
		}
	}
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
