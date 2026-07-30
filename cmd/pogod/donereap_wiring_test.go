package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
)

// TestDoneReapRegistrySatisfiesInterface is the compile-time half of the wiring
// proof: the REAL registry — not just the test fake — is a doneReapRegistry, and
// client.MGWorkItemDone has the shape the reaper's probe expects. Without this,
// a rename on either side breaks the daemon while every unit test above still
// passes against the fake.
func TestDoneReapRegistrySatisfiesInterface(t *testing.T) {
	var reg doneReapRegistry = (*agent.Registry)(nil)
	if reg == nil {
		t.Fatal("unreachable: the assignment above is the assertion")
	}
	r := newDoneReaper(reg, client.MGWorkItemDone, 0)
	if r.itemDone == nil {
		t.Fatal("client.MGWorkItemDone did not fit the terminal-state probe signature")
	}
}

// TestDoneReapIsWiredToTheHeartbeat is the other half. A reaper that is
// constructed and never called is exactly the failure mg-032b describes — a
// judgement that exists with no reader — so the tick has to actually invoke it.
// mg-56d1's leak was invisible for the same structural reason, and the fix must
// not reproduce it one layer up.
func TestDoneReapIsWiredToTheHeartbeat(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))
	if !strings.Contains(src, "newDoneReaper(agentRegistry, client.MGWorkItemDone") {
		t.Error("pogod does not construct the done-item reaper over the real registry and store probe")
	}
	if !strings.Contains(src, "doneReap.Check(now)") {
		t.Error("pogod constructs the done-item reaper but never calls Check — a detector with no " +
			"reader is the mg-032b failure, not a fix (mg-56d1)")
	}
	if !strings.Contains(src, "cfg.DoneReap.Enabled") {
		t.Error("the done-item reaper is not gated on its config switch, so it cannot be turned off")
	}
}
