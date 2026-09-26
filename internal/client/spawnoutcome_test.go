package client

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// withWriteTimeoutServer serves h under a real http.Server with the given
// WriteTimeout — the mechanism that produced the false EOF (mg-c252). An
// httptest.Server has no write deadline, so it cannot reproduce it.
func withWriteTimeoutServer(t *testing.T, writeTimeout time.Duration, h http.Handler) {
	t.Helper()
	ts := httptest.NewUnstartedServer(h)
	ts.Config.WriteTimeout = writeTimeout
	ts.Start()
	old := serverURL
	serverURL = ts.URL
	t.Cleanup(func() {
		serverURL = old
		ts.Close()
	})
}

func slowSpawn(delay time.Duration, finished chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(agent.AgentInfo{Name: "late", PID: 42})
		close(finished)
	}
}

// The acceptance case, with the fix: a spawn forced past the server's write
// deadline, behind the same wrapper pogod puts on /agents/spawn-polecat, comes
// back as an explicit still-running answer — distinguishable from failure —
// and the spawn itself completes server-side.
func TestSpawnPolecat_PastWriteDeadline_IsStillRunningNotFailure(t *testing.T) {
	const writeTimeout = 400 * time.Millisecond
	old := agent.SpawnResponseDeadline()
	agent.SetSpawnResponseDeadline(agent.ResponseDeadlineFor(writeTimeout))
	t.Cleanup(func() { agent.SetSpawnResponseDeadline(old) })

	finished := make(chan struct{})
	withWriteTimeoutServer(t, writeTimeout,
		agent.WithSpawnResponseDeadline("/agents/spawn-polecat", slowSpawn(2*writeTimeout, finished)))

	info, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "late", Id: "mg-0000"})
	if info != nil {
		t.Fatalf("got an agent for an unfinished spawn: %+v", info)
	}
	var u *SpawnOutcomeUnknownError
	if !errors.As(err, &u) {
		t.Fatalf("err = %v (%T), want *SpawnOutcomeUnknownError", err, err)
	}
	if !u.StillRunning {
		t.Errorf("StillRunning = false; pogod said so explicitly and the client lost it: %v", err)
	}
	if !strings.Contains(err.Error(), "#167") {
		t.Errorf("error does not warn against redispatch: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the spawn was abandoned server-side")
	}
}

// The same forced overrun WITHOUT the server-side wrapper — the shape any
// future slow handler, or an older pogod, produces: net/http cuts the
// connection and the client reads a transport error. That must still classify
// as outcome-unknown, never as a plain failure a caller would retry.
func TestSpawnPolecat_TransportEOFAfterSend_IsOutcomeUnknown(t *testing.T) {
	const writeTimeout = 200 * time.Millisecond
	finished := make(chan struct{})
	withWriteTimeoutServer(t, writeTimeout, slowSpawn(3*writeTimeout, finished))

	_, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "late"})
	var u *SpawnOutcomeUnknownError
	if !errors.As(err, &u) {
		t.Fatalf("err = %v (%T), want *SpawnOutcomeUnknownError", err, err)
	}
	if u.StillRunning {
		t.Errorf("StillRunning = true for a bare transport failure")
	}
	if !strings.Contains(err.Error(), "do NOT dispatch again") {
		t.Errorf("error does not warn against redispatch: %v", err)
	}
	<-finished
}

// Positive control for the classification: a request that never left (pogod
// not listening) is a plain error, not "unknown" — otherwise every error would
// be unknown and the distinction would say nothing.
func TestSpawnPolecat_DialFailure_IsNotOutcomeUnknown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	old := serverURL
	serverURL = "http://" + addr
	t.Cleanup(func() { serverURL = old })

	_, err = SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsSpawnOutcomeUnknown(err) {
		t.Errorf("a refused dial was classified outcome-unknown: %v", err)
	}
}

// Refusals and successes are unchanged by the classification.
func TestSpawnPolecat_RefusalAndSuccessUnchanged(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "live owner holds it", http.StatusConflict)
	})
	_, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "x"})
	if err == nil || IsSpawnOutcomeUnknown(err) || !strings.Contains(err.Error(), "live owner holds it") {
		t.Errorf("409 refusal: err = %v", err)
	}

	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(agent.AgentInfo{Name: "x", PID: 7})
	})
	info, err := SpawnPolecat(agent.SpawnPolecatAPIRequest{Name: "x"})
	if err != nil || info == nil || info.PID != 7 {
		t.Errorf("201: info=%+v err=%v", info, err)
	}
}

func TestStartAgent_StillRunning202_IsOutcomeUnknown(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(agent.StartErrorResponse{Reason: agent.SpawnStillRunningReason, Message: "still going"})
	})
	_, err := StartAgent("doctor")
	var u *SpawnOutcomeUnknownError
	if !errors.As(err, &u) || !u.StillRunning || !strings.Contains(err.Error(), "still going") {
		t.Errorf("err = %v", err)
	}
}
