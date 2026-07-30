package main

import (
	"os/exec"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
)

// This file constructs mg-56d1's acceptance control against REAL processes and a
// REAL macguffin store, rather than reasoning about it. The unit tests in
// donereap_test.go establish that the code takes the right branch; only this one
// establishes that a polecat is actually GONE and its slot actually FREE, and
// that the polecat next to it — idle for longer, item still claimed — is still
// running afterwards.
//
// The ticket is explicit that both arms are required and that a mechanism which
// cannot tell them apart is worse than the manual sweep it replaces, because it
// kills working agents. So both arms are driven from ONE Check against ONE
// registry: the discrimination has to come from the mechanism, not from how the
// fixture was arranged.

// liveDoneReapFixture spawns two real polecats against the sandbox store — one
// whose item has been marked done, one whose item is still claimed — and returns
// the registry, the two agents, and a reaper wired to the real store probe.
//
// The polecats run `sh -c 'printf ready; exec cat'`: they write to their PTY once
// (so their idleness is MEASURABLE — see PolecatActivity.HasOutput) and then
// block forever on stdin, which is what a finished-but-alive polecat looks like.
func liveDoneReapFixture(t *testing.T, root, doneID, claimedID string, grace time.Duration) (*agent.Registry, *agent.Agent, *agent.Agent, *doneReaper) {
	t.Helper()
	sandboxPogoHome(t)
	// client.MGWorkItemDone shells out to `mg show` with no --root, so the probe
	// is pinned at the sandbox through the environment. Without this the reaper
	// would ask the developer's live ~/.macguffin about fixture ids.
	t.Setenv("MG_ROOT", root)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	// Registry.Stop releases the stopped polecat's claim; pin that at the sandbox
	// too, or a Stop here would reach the real store.
	reg.SetClaimReleaser(agent.MGClaimReleaser{Root: root})

	spawn := func(name, item string) *agent.Agent {
		a, err := reg.Spawn(agent.SpawnRequest{
			Name:       name,
			Type:       agent.TypePolecat,
			Command:    []string{"sh", "-c", "printf 'ready\\n'; exec cat"},
			WorkItemID: item,
		})
		if err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
		return a
	}
	donecat := spawn("donecat", doneID)
	workcat := spawn("workcat", claimedID)

	// Wait for both PTYs to carry the `ready` line: until then their idleness is
	// unmeasurable and the reaper deliberately refuses to judge them.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ready := 0
		for _, p := range reg.PolecatActivityAt(time.Now()) {
			if p.HasOutput {
				ready++
			}
		}
		if ready == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("polecats did not produce PTY output within 10s (%d of 2 ready)", ready)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Both are now quiet. Let them cross the grace window for real — no injected
	// clock — so "idle" here means the same thing it means in production.
	time.Sleep(grace + 150*time.Millisecond)

	return reg, donecat, workcat, newDoneReaper(reg, client.MGWorkItemDone, grace)
}

// TestDoneReapLiveBothArms is the acceptance control.
//
// POSITIVE ARM: a non-merge polecat that called `mg done` is stopped — its
// process is gone and the slot it held is free. This is the d764 case measured
// on 2026-07-30: triage delivered, packet mailed, successor filed, item done,
// idle 7m16s, one of five slots held with high-priority work queued and
// undispatchable, and nothing in the fleet that would ever have ended it.
//
// NEGATIVE ARM: a polecat that is merely idle with its item still claimed is
// left alone — that was e9ee the same night, healthy and mid-work at 42 minutes.
//
// Note there is no merge anywhere in this test. That is the point: the refinery
// is never involved, no MergeRequest exists, and the teardown still happens.
func TestDoneReapLiveBothArms(t *testing.T) {
	root := mgSandboxStore(t)
	doneID := mgClaimedItem(t, root, "a triage polecat that finished and called mg done")
	claimedID := mgClaimedItem(t, root, "a polecat that is idle but still mid-work")

	// The completion, made by the polecat itself — exactly as a triage/audit
	// polecat's protocol tells it to, and with no merge in sight.
	if out, err := exec.Command("mg", "--root", root, "done", doneID).CombinedOutput(); err != nil {
		t.Fatalf("mg done %s: %v: %s", doneID, err, out)
	}
	if got := mgItemStatus(t, root, doneID); got != "done" {
		t.Fatalf("precondition: %s should be done, got %q", doneID, got)
	}
	if got := mgItemStatus(t, root, claimedID); got != "claimed" {
		t.Fatalf("precondition: %s should still be claimed, got %q", claimedID, got)
	}

	const grace = 200 * time.Millisecond
	reg, donecat, workcat, reaper := liveDoneReapFixture(t, root, doneID, claimedID, grace)

	before := reg.PolecatCount()
	if before != 2 {
		t.Fatalf("precondition: want 2 live polecats, got %d", before)
	}

	stopped := reaper.Check(time.Now())

	// --- positive arm: gone, and the slot with it ---
	if len(stopped) != 1 || stopped[0] != "donecat" {
		t.Fatalf("positive arm: want exactly [donecat] reaped, got %v", stopped)
	}
	select {
	case <-donecat.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("positive arm: donecat's process is still running after the reap — " +
			"the whole defect is a finished polecat that nothing stops")
	}
	if reg.Get("donecat") != nil {
		t.Error("positive arm: donecat is still in the registry, so its slot is still counted as held")
	}
	if after := reg.PolecatCount(); after != before-1 {
		t.Errorf("positive arm: live polecat count went %d -> %d, want %d — the freed slot is the "+
			"deliverable, not the Stop call", before, after, before-1)
	}

	// --- negative arm: untouched, and still able to work ---
	if !workcat.Alive() {
		t.Fatal("negative arm: workcat was killed. Its item is still CLAIMED — it is mid-work — and " +
			"a mechanism that reaps it is worse than the manual sweep it replaces (mg-56d1)")
	}
	if reg.Get("workcat") == nil {
		t.Error("negative arm: workcat was removed from the registry")
	}
	if got := mgItemStatus(t, root, claimedID); got != "claimed" {
		t.Errorf("negative arm: %s left claimed/ for %q — the reaper must not touch a live polecat's item", claimedID, got)
	}

	// And it stays untouched on subsequent passes: the negative arm is a
	// property of the item's state, not of being newly spawned.
	for i := 0; i < 3; i++ {
		time.Sleep(grace)
		if got := reaper.Check(time.Now()); len(got) != 0 {
			t.Fatalf("negative arm, pass %d: reaped %v with nothing left to reap", i+1, got)
		}
	}
	if !workcat.Alive() {
		t.Fatal("negative arm: workcat died across repeated passes")
	}
}

// TestDoneReapLiveDoesNotStopBeforeCompletion is the same fixture wound back one
// step: NEITHER item is done, both polecats are idle well past the grace, and
// the reaper must stop nothing at all. It pins that idleness alone — the signal
// `pogo agent diagnose` reports and the one an operator would be tempted to
// sweep on — never licenses a stop.
func TestDoneReapLiveDoesNotStopBeforeCompletion(t *testing.T) {
	root := mgSandboxStore(t)
	a := mgClaimedItem(t, root, "idle polecat A, still claimed")
	b := mgClaimedItem(t, root, "idle polecat B, still claimed")

	const grace = 200 * time.Millisecond
	reg, catA, catB, reaper := liveDoneReapFixture(t, root, a, b, grace)

	if got := reaper.Check(time.Now()); len(got) != 0 {
		t.Fatalf("no item is done, so nothing may be reaped; got %v", got)
	}
	if !catA.Alive() || !catB.Alive() {
		t.Fatalf("both polecats must survive; alive: A=%v B=%v", catA.Alive(), catB.Alive())
	}
	if n := reg.PolecatCount(); n != 2 {
		t.Errorf("live polecat count = %d, want 2", n)
	}

	// Now complete one of them and watch the SAME reaper, on the SAME registry,
	// change its answer. This is the arm-crossing: nothing about the fixture
	// changed except the work item's state.
	if out, err := exec.Command("mg", "--root", root, "done", a).CombinedOutput(); err != nil {
		t.Fatalf("mg done %s: %v: %s", a, err, out)
	}
	if got := reaper.Check(time.Now()); len(got) != 1 || got[0] != "donecat" {
		t.Fatalf("after `mg done`, want [donecat] reaped, got %v", got)
	}
	select {
	case <-catA.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the completed polecat is still running")
	}
	if !catB.Alive() {
		t.Error("the still-claimed polecat was reaped alongside it")
	}
}
