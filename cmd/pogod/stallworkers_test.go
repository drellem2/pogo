package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestStallWorkersUncertainIsNotUnknown is the rule the closure's doc states,
// pinned. A witness read error with a NON-empty registry is uncertainty: the
// live half still answers, and discarding it would put both dispatch notices
// back to guessing from item status — mg-1a8a's defect, one layer down.
func TestStallWorkersUncertainIsNotUnknown(t *testing.T) {
	items := map[string]agent.InFlightWorkItem{
		"mg-live": {Polecat: "pc-live", Evidence: agent.InFlightFromRegistry},
	}
	live := []agent.PolecatInfo{{Name: "pc-live", PID: 321}}

	flight, known := stallWorkersFrom(items, errors.New("witness unreadable"), live)
	if !known {
		t.Fatal("known = false while the registry named a worker — live evidence was discarded")
	}
	if got := flight.Items["mg-live"]; got.Name != "pc-live" || got.PID != 321 {
		t.Errorf("Items[mg-live] = %+v, want the worker and its registry pid", got)
	}
	if !strings.Contains(flight.Uncertain, "witness unreadable") {
		t.Errorf("Uncertain = %q, want it to carry the read error", flight.Uncertain)
	}
}

// TestStallWorkersUnknownWhenNothingCouldBeEstablished. A witness error AND an
// empty registry is no information at all, not an idle fleet — the same
// distinction stallCapacityFrom draws, and for the same reason: the in-memory
// registry is empty after a restart, permanently.
func TestStallWorkersUnknownWhenNothingCouldBeEstablished(t *testing.T) {
	flight, known := stallWorkersFrom(nil, errors.New("witness unreadable"), nil)
	if known {
		t.Fatalf("known = true with no evidence from either source: %+v", flight)
	}
}

// TestStallWorkersCleanReadIsKnownAndCertain: the ordinary case says nothing
// about uncertainty, so the note stays rare enough to mean something.
func TestStallWorkersCleanReadIsKnownAndCertain(t *testing.T) {
	items := map[string]agent.InFlightWorkItem{
		"mg-a": {Polecat: "pc-a", Evidence: agent.InFlightFromRegistry},
	}
	flight, known := stallWorkersFrom(items, nil, []agent.PolecatInfo{{Name: "pc-a", PID: 7}})
	if !known {
		t.Fatal("known = false on a clean read")
	}
	if flight.Uncertain != "" {
		t.Errorf("Uncertain = %q on a clean read, want empty", flight.Uncertain)
	}
}

// TestStallWorkersLeavesAWitnessOnlyPidUnset. The notice prints the pid into an
// `mg claim --pid` command, and a witness pid is one we could not DISPROVE;
// naming it there would invite restamping a claim onto a process that is not the
// worker. Zero renders as a placeholder instead, which is honest and still
// actionable.
func TestStallWorkersLeavesAWitnessOnlyPidUnset(t *testing.T) {
	items := map[string]agent.InFlightWorkItem{
		"mg-survivor": {Polecat: "pc-survivor", Evidence: agent.InFlightFromWitness},
	}
	// The survivor is not in this pogod's registry — that is what makes it a
	// survivor.
	flight, known := stallWorkersFrom(items, nil, nil)
	if !known {
		t.Fatal("known = false with a witnessed worker")
	}
	if got := flight.Items["mg-survivor"]; got.PID != 0 || got.Evidence != agent.InFlightFromWitness {
		t.Errorf("Items[mg-survivor] = %+v, want no pid and the witness evidence", got)
	}
}

// TestNewStallWorkersReadsTheLiveRegistry proves the closure is wired to the
// registry rather than to a copy of the union rule. An empty registry with a
// readable witness answers "known, nothing in flight" — which is a ZERO, and
// must not be confused with the unknown above.
func TestNewStallWorkersReadsTheLiveRegistry(t *testing.T) {
	reg, err := agent.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	flight, known := newStallWorkers(reg).InFlight()
	if !known {
		t.Fatalf("known = false against a live registry: %+v", flight)
	}
	if len(flight.Items) != 0 {
		t.Errorf("Items = %+v, want empty on a fleet with no workers", flight.Items)
	}
}
