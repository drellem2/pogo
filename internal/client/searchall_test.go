package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/project"
	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

// newFakePogod stands up an httptest server that answers the endpoints
// SearchAllStreaming touches, and repoints the package-level serverURL at it
// for the duration of the test.
func newFakePogod(t *testing.T, projectCount int, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	oldURL := serverURL
	serverURL = srv.URL
	t.Cleanup(func() {
		serverURL = oldURL
		srv.Close()
	})
	return srv
}

// TestSearchAllStreamingParallelKeepAlive verifies the gh #39 SearchAll
// rework: the per-project fan-out runs in parallel, health is probed a
// constant number of times regardless of project count, and every project
// still produces a result.
func TestSearchAllStreamingParallelKeepAlive(t *testing.T) {
	const projectCount = 12

	var healthCalls, pluginCalls atomic.Int32
	var inFlight, maxInFlight atomic.Int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			healthCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		case "/projects":
			projs := make([]project.Project, projectCount)
			for i := range projs {
				projs[i] = project.Project{Id: i + 1, Path: fmt.Sprintf("/repo/%d/", i)}
			}
			json.NewEncoder(w).Encode(projs)
		case "/plugins":
			json.NewEncoder(w).Encode([]string{"/plugins/pogo-plugin-search"})
		case "/plugin":
			pluginCalls.Add(1)
			cur := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			// Hold the request open long enough that a parallel fan-out
			// demonstrably overlaps.
			time.Sleep(30 * time.Millisecond)

			var dataObj pogoPlugin.DataObject
			if err := json.NewDecoder(r.Body).Decode(&dataObj); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var req SearchRequest
			if err := json.Unmarshal([]byte(dataObj.Value), &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp := SearchResponse{
				Index: IndexedProject{Root: req.ProjectRoot},
				Results: SearchResults{Files: []PogoFileMatch{
					{Path: "main.go", Matches: []PogoChunkMatch{{Line: 1, Content: "match"}}},
				}},
			}
			respJSON, _ := json.Marshal(resp)
			json.NewEncoder(w).Encode(pogoPlugin.DataObject{Value: string(respJSON)})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}
	newFakePogod(t, projectCount, handler)

	var mu sync.Mutex
	var roots []string
	var concurrentCallbacks atomic.Int32
	err := SearchAllStreaming("match", func(resp *SearchResponse) {
		if concurrentCallbacks.Add(1) != 1 {
			t.Error("onResult invoked concurrently; callers rely on serialized callbacks")
		}
		defer concurrentCallbacks.Add(-1)
		mu.Lock()
		roots = append(roots, resp.Index.Root)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("SearchAllStreaming failed: %v", err)
	}

	if len(roots) != projectCount {
		t.Errorf("expected %d results, got %d", projectCount, len(roots))
	}
	if got := pluginCalls.Load(); got != projectCount {
		t.Errorf("expected %d plugin calls, got %d", projectCount, got)
	}
	// GetProjects and GetPlugins each health-check once; the per-project
	// fan-out must not add any (it used to probe /health before every
	// project's search).
	if got := healthCalls.Load(); got > 2 {
		t.Errorf("health probed %d times; must not scale with project count", got)
	}
	if got := maxInFlight.Load(); got < 2 {
		t.Errorf("expected parallel per-project searches, saw max %d in flight", got)
	}
	if got := maxInFlight.Load(); got > searchAllConcurrency {
		t.Errorf("in-flight searches (%d) exceeded the concurrency bound (%d)", got, searchAllConcurrency)
	}
}

// TestSearchAllStreamingReportsPerProjectErrors verifies a failing project
// search surfaces as an error result without aborting the other projects.
func TestSearchAllStreamingReportsPerProjectErrors(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/projects":
			json.NewEncoder(w).Encode([]project.Project{
				{Id: 1, Path: "/repo/ok/"},
				{Id: 2, Path: "/repo/bad/"},
			})
		case "/plugins":
			json.NewEncoder(w).Encode([]string{"/plugins/pogo-plugin-search"})
		case "/plugin":
			var dataObj pogoPlugin.DataObject
			json.NewDecoder(r.Body).Decode(&dataObj)
			var req SearchRequest
			json.Unmarshal([]byte(dataObj.Value), &req)
			if req.ProjectRoot == "/repo/bad/" {
				// Malformed payload -> client-side unmarshal error for this repo.
				fmt.Fprint(w, "not json")
				return
			}
			resp := SearchResponse{
				Index: IndexedProject{Root: req.ProjectRoot},
				Results: SearchResults{Files: []PogoFileMatch{
					{Path: "a.go", Matches: []PogoChunkMatch{{Line: 1, Content: "x"}}},
				}},
			}
			respJSON, _ := json.Marshal(resp)
			json.NewEncoder(w).Encode(pogoPlugin.DataObject{Value: string(respJSON)})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}
	newFakePogod(t, 2, handler)

	results := map[string]string{}
	var mu sync.Mutex
	err := SearchAllStreaming("x", func(resp *SearchResponse) {
		mu.Lock()
		results[resp.Index.Root] = resp.Error
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("SearchAllStreaming failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected results for both projects, got %v", results)
	}
	if results["/repo/ok/"] != "" {
		t.Errorf("healthy project unexpectedly errored: %s", results["/repo/ok/"])
	}
	if results["/repo/bad/"] == "" {
		t.Error("failing project should surface an error result")
	}
}
