package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// statusRecorder captures the status pogod's handler wrote, so the test can
// read the server's answer even when the requesting client died before it
// arrived.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// TestSelfIssuedStopRespawnsRestartOnCrashAgent covers the path [crew_reset]
// (mg-5b58d) asks crew agents to take: the agent runs `pogo agent stop <self>`
// from its OWN shell. The requester is then a descendant of the process being
// stopped, and it is blocked on the very request whose handler is killing its
// ancestor. That must still end with the agent back in a FRESH session: a new
// pid and a new start time, through the ordinary restart_on_crash respawn and
// not a park.
//
// The agent here is a shell that, on its first life only, sends
// DELETE /agents/<self> to a real HTTP server over the registry's own handlers
// and stays in the foreground waiting for the answer, the way a harness tool
// call does. It takes the full stop timeout (the shell cannot act on SIGINT
// while its foreground child runs), which is the realistic shape.
func TestSelfIssuedStopRespawnsRestartOnCrashAgent(t *testing.T) {
	testsandbox.Isolate(t)
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl not on PATH; the self-issued stop needs an HTTP client inside the agent")
	}

	socketDir, err := os.MkdirTemp("/tmp", "pogo-selfstop-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	var mu sync.Mutex
	var deleteStatus []int
	mux := http.NewServeMux()
	reg.RegisterHandlers(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(rec, r)
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleteStatus = append(deleteStatus, rec.status)
			mu.Unlock()
		}
	}))
	defer srv.Close()

	// Mirror the production OnExit hook in cmd/pogod/main.go.
	restartCh := make(chan *Agent, 4)
	reg.SetOnExit(func(a *Agent, exitErr error) {
		if !a.ShouldRespawn() {
			a.Cleanup()
			reg.Remove(a.Name)
			return
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			respawned, rerr := reg.Respawn(a.Name)
			if rerr != nil {
				t.Logf("respawn: %v", rerr)
				return
			}
			restartCh <- respawned
		}()
	})

	marker := filepath.Join(t.TempDir(), "stopped-once")
	script := strings.Join([]string{
		`if [ ! -e "` + marker + `" ]; then`,
		`  : > "` + marker + `"`,
		`  "` + curl + `" -s -o /dev/null -X DELETE "` + srv.URL + `/agents/self-stop"`,
		`fi`,
		`exec sleep 60`,
	}, "\n")
	a, err := reg.Spawn(SpawnRequest{
		Name:           "self-stop",
		Type:           TypeCrew,
		Command:        []string{"sh", "-c", script},
		RestartOnCrash: true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	originalPID, originalStart := a.PID, a.StartTime

	select {
	case respawned := <-restartCh:
		if respawned.PID == originalPID {
			t.Errorf("respawn kept pid %d — not a new process", respawned.PID)
		}
		if !respawned.StartTime.After(originalStart) {
			t.Errorf("respawned StartTime %s is not after the original %s — crew-reset would count it as the same session",
				respawned.StartTime, originalStart)
		}
		if IsParked("self-stop") {
			t.Error("a self-issued stop parked the agent")
		}
		got := reg.Get("self-stop")
		if got == nil || got.PID != respawned.PID {
			t.Fatalf("registry does not hold the respawned agent: %+v", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("agent was not respawned after stopping itself — the restart_on_crash contract failed for a self-issued stop")
	}

	// The handler finished its work and answered 204, whether or not the
	// requesting curl lived to read it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(deleteStatus)
		var st int
		if n > 0 {
			st = deleteStatus[0]
		}
		mu.Unlock()
		if n > 0 {
			if n != 1 {
				t.Errorf("saw %d DELETE requests, want 1 (the marker should stop the respawn re-stopping)", n)
			}
			if st != http.StatusNoContent {
				t.Errorf("DELETE /agents/self-stop answered %d, want 204", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("DELETE handler never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The fresh session is not stopped again.
	time.Sleep(300 * time.Millisecond)
	if got := reg.Get("self-stop"); got == nil || !got.alive() {
		t.Error("respawned agent is not alive")
	}
}
