package client

import (
	"net/http"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/apimount"
)

// TestGetHostLoadReachesTheDaemonsMountedRoute runs the client against the mux
// pogod actually serves.
//
// This is the test whose absence let `pogo host load` 404 for two weeks
// (mg-c26d). The endpoint had three tests and they were all correct; every one
// of them called handleHostLoad directly, so none could observe that the route
// was registered on a sub-mux nobody mounted it under. The client — the one
// component that would have noticed, because it is the thing that builds the
// path — had no test that ran against a live mux at all.
//
// It deliberately builds the server side out of the same two pieces the daemon
// does, agent.RegisterHandlers and apimount.Mount, rather than registering
// GetHostLoad's path directly. A test that mounts the path it is about to
// request proves only that it can agree with itself.
func TestGetHostLoadReachesTheDaemonsMountedRoute(t *testing.T) {
	reg, err := agent.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	orchestrated := http.NewServeMux()
	reg.RegisterHandlers(orchestrated)

	root := http.NewServeMux()
	root.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		// pogod's own fallthrough is the "/" home page, which answers 200 to
		// anything. 404 here is the shape the client actually saw.
		http.Error(w, "404 page not found", http.StatusNotFound)
	})
	apimount.Mount(root, orchestrated)

	withTestServer(t, root.ServeHTTP)

	resp, err := GetHostLoad("")
	if err != nil {
		t.Fatalf("GetHostLoad against a mounted daemon mux: %v\n"+
			"the client is asking at a path the daemon does not serve", err)
	}
	if resp.Advice == "" {
		t.Error("advice is empty: the response did not come from handleHostLoad")
	}
}

// TestGetHostLoadRepoReachesTheDaemonsMountedRoute covers the --repo form,
// which builds a different URL and so is a separate reachability question.
func TestGetHostLoadRepoReachesTheDaemonsMountedRoute(t *testing.T) {
	reg, err := agent.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	orchestrated := http.NewServeMux()
	reg.RegisterHandlers(orchestrated)

	root := http.NewServeMux()
	root.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "404 page not found", http.StatusNotFound)
	})
	apimount.Mount(root, orchestrated)

	withTestServer(t, root.ServeHTTP)

	resp, err := GetHostLoad("/Users/someone/dev/pogo")
	if err != nil {
		t.Fatalf("GetHostLoad(repo) against a mounted daemon mux: %v", err)
	}
	if resp.RepoOccupancy == nil {
		t.Error("repo_occupancy is absent: the repo query did not reach the handler")
	}
}
