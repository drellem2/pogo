package agent

import (
	"os"
	"testing"
)

// TestWorkItemsInFlightUnionsRegistryAndWitness is the whole point of the union
// (mg-1a8a): a polecat that outlived an earlier pogod is invisible to the
// in-memory registry — permanently, it has no adopt path — so a reader that
// consulted the registry alone would report that survivor's item as unworked on
// every redeploy, which is exactly when survivors exist.
func TestWorkItemsInFlightUnionsRegistryAndWitness(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.agents["in-registry"] = livePolecat("in-registry", "mg-live")
	if err := RecordPolecatWitness("survivor", liveProcess(t), "mg-survivor", "/repo"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	items, err := reg.WorkItemsInFlight()
	if err != nil {
		t.Fatalf("WorkItemsInFlight: %v", err)
	}
	if got := items["mg-live"]; got.Polecat != "in-registry" || got.Evidence != InFlightFromRegistry {
		t.Errorf("mg-live -> %+v, want in-registry via the registry", got)
	}
	if got := items["mg-survivor"]; got.Polecat != "survivor" || got.Evidence != InFlightFromWitness {
		t.Errorf("mg-survivor -> %+v, want survivor via the witness — a restart must not un-work an item", got)
	}
	if ids := InFlightWorkItemIDs(items); len(ids) != 2 || ids[0] != "mg-live" || ids[1] != "mg-survivor" {
		t.Errorf("ids = %v, want both, sorted", ids)
	}
}

// TestWorkItemsInFlightPrefersTheRegistry: where the two sources disagree about
// who is on an item, the live registry is the record of the dispatch actually in
// progress and the witness is what an earlier one left behind.
func TestWorkItemsInFlightPrefersTheRegistry(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.agents["current"] = livePolecat("current", "mg-contested")
	if err := RecordPolecatWitness("stale-name", liveProcess(t), "mg-contested", "/repo"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	items, err := reg.WorkItemsInFlight()
	if err != nil {
		t.Fatalf("WorkItemsInFlight: %v", err)
	}
	if got := items["mg-contested"]; got.Polecat != "current" || got.Evidence != InFlightFromRegistry {
		t.Errorf("mg-contested -> %+v, want the live registry's answer", got)
	}
}

// TestWorkItemsInFlightIgnoresItemlessAndDeadWorkers. A crew agent and a
// --no-worktree polecat carry no work item, and a dead witness record is what
// the store exists to rule out; none of the three may put an id in the map,
// because a spurious entry SILENCES a genuine neglect report.
func TestWorkItemsInFlightIgnoresItemlessAndDeadWorkers(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.agents["itemless"] = livePolecat("itemless", "")
	// PID 0 is not alive, so Polecats() drops it.
	dead := livePolecat("dead", "mg-dead")
	dead.PID = 0
	reg.agents["dead"] = dead

	items, err := reg.WorkItemsInFlight()
	if err != nil {
		t.Fatalf("WorkItemsInFlight: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want empty — a worker with no item, and a dead one, are not in flight", items)
	}
}

// TestWorkItemsInFlightReturnsTheRegistryHalfWithTheError is the failure
// direction stated in the doc comment: an unreadable witness must not discard
// what the registry knows. Suppressing the registry half would put stall-watch
// back to guessing from item status for polecats THIS pogod spawned — mg-1a8a's
// defect, one layer down.
func TestWorkItemsInFlightReturnsTheRegistryHalfWithTheError(t *testing.T) {
	sandboxWitness(t)
	if err := os.WriteFile(WitnessPath(), []byte(`{"version":9999,"polecats":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	reg := newDrainTestRegistry(t)
	reg.agents["in-registry"] = livePolecat("in-registry", "mg-live")

	items, err := reg.WorkItemsInFlight()
	if err == nil {
		t.Fatal("a store written by a newer pogod read as a complete answer")
	}
	if got := items["mg-live"]; got.Polecat != "in-registry" {
		t.Errorf("items = %+v, want the registry half kept alongside the error", items)
	}
}
