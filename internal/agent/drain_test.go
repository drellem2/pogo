package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// livePolecat returns a polecat Agent whose process is "alive" — it borrows the
// test process's own PID so alive() (syscall.Kill(pid,0)) succeeds — with an
// open done channel. This mirrors the injection pattern in diagnose_test.go.
func livePolecat(name, workItem string) *Agent {
	return &Agent{
		Name:        name,
		Type:        TypePolecat,
		PID:         os.Getpid(),
		Status:      StatusRunning,
		StartTime:   time.Now(),
		WorkItemID:  workItem,
		WorktreeDir: "/tmp/polecats/" + name,
		SourceRepo:  "/repo",
		done:        make(chan struct{}),
	}
}

func newDrainTestRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}

func TestPolecatCountIgnoresCrewAndDead(t *testing.T) {
	reg := newDrainTestRegistry(t)
	// Two live polecats, one crew agent (must not count), one dead polecat
	// (PID 0 → not alive, must not count).
	reg.agents["cat-a"] = livePolecat("cat-a", "mg-aaaa")
	reg.agents["cat-b"] = livePolecat("cat-b", "mg-bbbb")
	reg.agents["mayor"] = &Agent{Name: "mayor", Type: TypeCrew, PID: os.Getpid(), Status: StatusRunning, done: make(chan struct{})}
	reg.agents["cat-dead"] = &Agent{Name: "cat-dead", Type: TypePolecat, PID: 0, Status: StatusExited, done: make(chan struct{})}

	if got := reg.PolecatCount(); got != 2 {
		t.Errorf("PolecatCount() = %d, want 2", got)
	}
	pcs := reg.Polecats()
	if len(pcs) != 2 {
		t.Fatalf("Polecats() len = %d, want 2", len(pcs))
	}
	// Sorted by name, so cat-a first — verify cleanup fields travel through.
	if pcs[0].Name != "cat-a" || pcs[0].WorkItemID != "mg-aaaa" || pcs[0].WorktreeDir != "/tmp/polecats/cat-a" {
		t.Errorf("Polecats()[0] = %+v, want cat-a with work item and worktree", pcs[0])
	}
}

func TestSetDrainingToggles(t *testing.T) {
	reg := newDrainTestRegistry(t)
	if reg.Draining() {
		t.Fatal("fresh registry should not be draining")
	}
	reg.SetDraining(true)
	if !reg.Draining() {
		t.Fatal("SetDraining(true) did not take")
	}
	reg.SetDraining(false)
	if reg.Draining() {
		t.Fatal("SetDraining(false) did not take")
	}
}

func TestHandleDrainGetReportsCount(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.agents["cat-a"] = livePolecat("cat-a", "mg-aaaa")

	req := httptest.NewRequest("GET", "/agents/drain", nil)
	rr := httptest.NewRecorder()
	reg.handleDrain(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agents/drain status = %d", rr.Code)
	}
	var ds DrainStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &ds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ds.Draining {
		t.Error("GET should report draining=false initially")
	}
	if ds.Count != 1 || len(ds.Polecats) != 1 {
		t.Errorf("GET count = %d, polecats = %d, want 1/1", ds.Count, len(ds.Polecats))
	}
}

func TestHandleDrainPostSetsFlag(t *testing.T) {
	reg := newDrainTestRegistry(t)
	body, _ := json.Marshal(DrainAPIRequest{Draining: true})
	req := httptest.NewRequest("POST", "/agents/drain", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleDrain(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /agents/drain status = %d", rr.Code)
	}
	if !reg.Draining() {
		t.Error("POST {draining:true} did not enable drain mode")
	}
	var ds DrainStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &ds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ds.Draining {
		t.Error("POST response should echo draining=true")
	}
}

// TestSpawnPolecatRefusedWhileDraining is the load-bearing guard: no new
// polecat is dispatched while the fleet is draining for a redeploy. The 503
// fires immediately after body decode — early enough that an empty request
// still exercises exactly the gate and nothing below it, late enough that the
// refusal's event can name the agent it refused (mg-d22a).
func TestSpawnPolecatRefusedWhileDraining(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetDraining(true)

	req := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader([]byte(`{}`)))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("spawn while draining status = %d, want 503", rr.Code)
	}
	// And it must recover once drain clears (503 is not sticky on the gate).
	reg.SetDraining(false)
	req2 := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader([]byte(`{}`)))
	rr2 := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr2, req2)
	if rr2.Code == http.StatusServiceUnavailable {
		t.Fatal("spawn still refused after drain cleared")
	}
}
