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
	r := newDoneReaper(reg, client.MGWorkItemDone, client.MGWorkItemReviews, 0)
	if r.itemDone == nil {
		t.Fatal("client.MGWorkItemDone did not fit the terminal-state probe signature")
	}
	if r.itemReviews == nil {
		t.Fatal("client.MGWorkItemReviews did not fit the review-declaration probe signature (mg-aaf6)")
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
	// The review exemption is fail-OPEN when its probe is nil (the builder gets
	// reaped), so a daemon that constructs the reaper without one is a daemon
	// where the mg-aaf6 guard silently does not exist. That is precisely the
	// shape this ticket's acceptance refuses, so it is pinned at the wiring.
	if !strings.Contains(src, "client.MGWorkItemReviews") {
		t.Error("pogod constructs the done-item reaper with no `reviews:` probe — the review exemption is " +
			"then absent, not merely unused, and a builder is reaped mid-review exactly as in gh#131 (mg-aaf6)")
	}
	if !strings.Contains(src, "doneReap.Check(now)") {
		t.Error("pogod constructs the done-item reaper but never calls Check — a detector with no " +
			"reader is the mg-032b failure, not a fix (mg-56d1)")
	}
	if !strings.Contains(src, "cfg.DoneReap.Enabled") {
		t.Error("the done-item reaper is not gated on its config switch, so it cannot be turned off")
	}
}
