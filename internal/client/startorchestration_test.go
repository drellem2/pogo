package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/server"
)

// TestStartOrchestrationCarriesTheReportOverTheWire closes the loop the CLI
// actually depends on: pogod's real handler encodes the report, and the client
// decodes it. Nothing else in the tree checks that those two agree.
//
// It matters because the failure mode is silent. If the encode and decode
// drift — a renamed JSON tag, a type change — the client gets a zero-valued
// report, the CLI prints "restarted, 0 agents", and that is indistinguishable
// from the bug this all exists to fix (gh #108). Neither side's own tests would
// notice.
//
// It uses httptest and never contacts a real daemon: a mutating call against
// the running pogod would stop the live fleet.
func TestStartOrchestrationCarriesTheReportOverTheWire(t *testing.T) {
	srv := server.New(nil, nil)
	srv.SetAgentStarter(func() server.AgentStartOutcome {
		return server.AgentStartOutcome{Results: []agent.AutoStartResult{
			{Name: "mayor", Status: agent.AutoStartStatusStarted},
			{Name: "pm-pogo", Status: agent.AutoStartStatusStarted},
			{Name: "sleepy", Status: agent.AutoStartStatusSkippedParked},
			{Name: "gc", Status: agent.AutoStartStatusFailed, Error: "pty start: no ptys"},
		}}
	})
	if err := srv.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode(index-only): %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterHandlers(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	old := serverURL
	serverURL = ts.URL
	defer func() { serverURL = old }()

	report, err := StartOrchestration()
	if err != nil {
		t.Fatalf("StartOrchestration: %v", err)
	}

	if report.Mode != "full" {
		t.Errorf("Mode = %q, want \"full\"", report.Mode)
	}
	if len(report.AgentsStarted) != 2 ||
		report.AgentsStarted[0] != "mayor" || report.AgentsStarted[1] != "pm-pogo" {
		t.Errorf("AgentsStarted = %v, want [mayor pm-pogo] — the names did not survive the wire",
			report.AgentsStarted)
	}
	if len(report.AgentsParked) != 1 || report.AgentsParked[0] != "sleepy" {
		t.Errorf("AgentsParked = %v, want [sleepy]", report.AgentsParked)
	}
	if len(report.AgentsFailed) != 1 || report.AgentsFailed[0].Name != "gc" ||
		report.AgentsFailed[0].Error != "pty start: no ptys" {
		t.Errorf("AgentsFailed = %+v, want gc with its spawn error", report.AgentsFailed)
	}
}

// A non-200 is still an error, and the report is not half-populated from a
// body the caller should not be reading.
func TestStartOrchestrationSurfacesServerErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "restart refinery: boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	old := serverURL
	serverURL = ts.URL
	defer func() { serverURL = old }()

	report, err := StartOrchestration()
	if err == nil {
		t.Fatal("StartOrchestration returned nil error on a 500")
	}
	if report.Mode != "" || len(report.AgentsStarted) != 0 {
		t.Errorf("report = %+v on error, want the zero value", report)
	}
}
