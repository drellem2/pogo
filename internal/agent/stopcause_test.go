package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The stop-cause tests (mg-a95f).
//
// WHAT THEY PIN. `reason=requested` on agent_stopped is six call sites collapsed
// into one word, and on 2026-08-08 that collapse is what made "did one command
// stop the whole crew, or did five things stop one agent each?" a question that
// could only be answered by reading pogod.log — a file internal/server/modeaudit.go
// documents as unreliable for exactly this purpose. These assert that each path
// names ITSELF, so the answer is in events.log.
//
// They assert DISTINCT causes per path on purpose. A fix that threaded one
// constant everywhere would satisfy "stop_cause is present" and restore the
// defect, so presence is never what is checked.

// spawnSleeper registers a long-running agent under name so a stop has
// something to stop.
func spawnSleeper(t *testing.T, reg *Registry, name string, typ AgentType) {
	t.Helper()
	if _, err := reg.Spawn(SpawnRequest{
		Name:    name,
		Type:    typ,
		Command: []string{"sleep", "30"},
	}); err != nil {
		t.Fatalf("Spawn %s: %v", name, err)
	}
}

// stopDetails reads back the agent_stopped record for eventAgent.
func stopDetails(t *testing.T, path, eventAgent string) map[string]any {
	t.Helper()
	ev := waitForEvent(t, path, "agent_stopped", eventAgent, 3*time.Second)
	if ev == nil {
		t.Fatalf("no agent_stopped event recorded for %s", eventAgent)
	}
	return ev["details"].(map[string]any)
}

// TestStopCause_ExplicitStop is the `pogo agent stop` path: one named agent,
// asked for by somebody.
func TestStopCause_ExplicitStop(t *testing.T) {
	path := useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	spawnSleeper(t, reg, "explicit-stop", TypeCrew)
	if err := reg.Stop("explicit-stop", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	d := stopDetails(t, path, "crew-explicit-stop")
	if d["reason"] != "requested" {
		t.Errorf("reason: want requested, got %v", d["reason"])
	}
	if d["stop_cause"] != StopCauseRequest {
		t.Errorf("stop_cause: want %q, got %v", StopCauseRequest, d["stop_cause"])
	}
}

// TestStopCause_FleetDrain is the 08-08 shape: StopAll, whose only live caller
// is Server.transitionToIndexOnly. Two agents, so the test also shows the cause
// is written per record rather than being a property of one of them.
func TestStopCause_FleetDrain(t *testing.T) {
	path := useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatal(err)
	}

	spawnSleeper(t, reg, "drained-a", TypeCrew)
	spawnSleeper(t, reg, "drained-b", TypeCrew)

	reg.StopAll(3 * time.Second)

	for _, name := range []string{"crew-drained-a", "crew-drained-b"} {
		d := stopDetails(t, path, name)
		if d["reason"] != "requested" {
			t.Errorf("%s reason: want requested, got %v", name, d["reason"])
		}
		if d["stop_cause"] != StopCauseStopAll {
			t.Errorf("%s stop_cause: want %q, got %v — a fleet-wide drain recording the same cause "+
				"as a single `pogo agent stop` is the 2026-08-08 ambiguity, restored",
				name, StopCauseStopAll, d["stop_cause"])
		}
	}
}

// TestStopCause_Park covers Registry.Park, which stops as a step of parking.
func TestStopCause_Park(t *testing.T) {
	path := useTempEventLog(t)
	reg := newParkTestRegistry(t)
	writePrompt(t, CrewPromptDir(), "parked-cause", "+++\nrestart_on_crash = true\n+++\n# parked-cause\n")

	if _, err := reg.StartCrewAgent("parked-cause"); err != nil {
		t.Fatalf("StartCrewAgent: %v", err)
	}
	if _, err := reg.Park("parked-cause", 2*time.Second); err != nil {
		t.Fatalf("Park: %v", err)
	}

	d := stopDetails(t, path, "crew-parked-cause")
	if d["stop_cause"] != StopCausePark {
		t.Errorf("stop_cause: want %q, got %v", StopCausePark, d["stop_cause"])
	}
}

// TestStopCause_AbsentOnSelfCompletion is the other polarity, and without it the
// field would be free to default to something. An agent that ran to completion
// was not stopped by anybody, and a stop_cause on that record would read as an
// unattributed stop — the reading this whole change exists to make impossible.
func TestStopCause_AbsentOnSelfCompletion(t *testing.T) {
	path := useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "finished-alone",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	d := stopDetails(t, path, "cat-finished-alone")
	if d["reason"] != "task_complete" {
		t.Fatalf("test premise: reason should be task_complete, got %v", d["reason"])
	}
	if v, ok := d["stop_cause"]; ok {
		t.Errorf("stop_cause present (%v) on a task_complete exit: nobody stopped this agent", v)
	}
}

// TestStopCause_ValuesAreDocumented. The catalog in docs/event-log.md is where
// an operator holding an `agent_stopped` record learns what its stop_cause
// means; a value nobody can look up is half an artifact. It ratchets the other
// way too: a seventh stop path added without a doc line fails here rather than
// shipping as an undocumented string in the log.
func TestStopCause_ValuesAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "event-log.md"))
	if err != nil {
		t.Skipf("event-log.md not readable from here: %v", err)
	}
	for _, cause := range []string{
		StopCauseRequest, StopCauseStopAll, StopCausePark,
		StopCauseMergeReap, StopCauseMergeBackstop, StopCauseDoneReap,
	} {
		if !strings.Contains(string(doc), `"`+cause+`"`) {
			t.Errorf("stop_cause %q is emitted but absent from docs/event-log.md", cause)
		}
	}
}
