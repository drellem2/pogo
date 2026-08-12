package main

import (
	"os/exec"
	"strings"
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

	return reg, donecat, workcat, newDoneReaper(reg, client.MGWorkItemDone, noReviews, grace)
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

// mgClaimedItemWithBody is mgClaimedItem with a body, so a fixture item can
// carry a real carrier block for the store parse to read back.
func mgClaimedItemWithBody(t *testing.T, root, title, body string) string {
	t.Helper()
	cmd := exec.Command("mg", "--root", root, "new", "--no-repo", "--title="+title, "--body-file", "-")
	cmd.Stdin = strings.NewReader(body)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mg new: %v: %s", err, out)
	}
	m := mgNewID.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not parse a work item id out of %q", out)
	}
	id := m[1]
	if out, err := exec.Command("mg", "--root", root, "claim", id).CombinedOutput(); err != nil {
		t.Fatalf("mg claim %s: %v: %s", id, err, out)
	}
	return id
}

// TestDoneReapLiveReviewExemptionFiresAndExpires is mg-aaf6's acceptance control
// against REAL processes, a REAL macguffin store, and the REAL probe — the same
// standard mg-56d1's control set for the reap it constrains.
//
// The unit tests in donereap_test.go establish that the code takes the right
// branch. Only this one establishes that the declaration survives the round trip
// it actually makes in production: written into a ticket body by `mg new`, read
// back out of `mg show --json`, parsed by workitem.ParseCarrier, and matched
// against a live registry's polecats. A guard that works against a fake probe
// and not against `mg` is a guard that does not work.
//
// The bar the ticket sets is that it be observed doing BOTH things, so both
// happen here, in sequence, against one reaper and one registry:
//
//	FIRES   — the builder self-closed at PR-open (the gh#131 mistake), its item
//	          is done, its PTY is quiet past the grace, and it SURVIVES because
//	          the review polecat is running.
//	EXPIRES — the reviewer is stopped, nothing else changes, and the very next
//	          Check reaps the builder for real: process gone, slot free.
func TestDoneReapLiveReviewExemptionFiresAndExpires(t *testing.T) {
	root := mgSandboxStore(t)
	buildID := mgClaimedItem(t, root, "a build polecat that self-closed at PR-open")
	// The review ticket, filed the way mayor.md transition 3 files it: the
	// carrier block LEADS the body, and `reviews:` names the build item.
	reviewID := mgClaimedItemWithBody(t, root, "review: the PR from the build ticket",
		"workflow: gh-issue\nstage: review\nreviews: "+buildID+"\n\nReview the PR against the approved recommendation.\n")

	// The mistake this whole ticket is about, made for real.
	if out, err := exec.Command("mg", "--root", root, "done", buildID).CombinedOutput(); err != nil {
		t.Fatalf("mg done %s: %v: %s", buildID, err, out)
	}
	if got := mgItemStatus(t, root, buildID); got != "done" {
		t.Fatalf("precondition: %s should be done, got %q", buildID, got)
	}

	const grace = 200 * time.Millisecond
	reg, buildcat, reviewcat, reaper := liveDoneReapFixture(t, root, buildID, reviewID, grace)
	// liveDoneReapFixture wires noReviews. Swap in the REAL probe: the round trip
	// through `mg show --json` and the shipped carrier parser is the thing under
	// test here, and a fake would skip exactly it.
	reaper.itemReviews = client.MGWorkItemReviews

	// Sanity: the store really does hand the declaration back. If this fails the
	// two arms below would both pass for the wrong reason.
	if got, err := client.MGWorkItemReviews(reviewID); err != nil || got != buildID {
		t.Fatalf("precondition: MGWorkItemReviews(%s) = %q, %v — want %q. The declaration did not survive "+
			"the write/read round trip, so nothing below would be testing the guard", reviewID, got, err, buildID)
	}

	// ── FIRES ──────────────────────────────────────────────────────────────────
	for i := 0; i < 3; i++ {
		if stopped := reaper.Check(time.Now()); len(stopped) != 0 {
			t.Fatalf("pass %d: the builder was reaped while its reviewer was running — this is gh#131 "+
				"reproduced against a real store, and the reviewer now has no counterparty (stopped=%v)", i+1, stopped)
		}
		time.Sleep(grace)
	}
	if !buildcat.Alive() {
		t.Fatal("the build polecat's process is gone despite the exemption")
	}
	if reg.PolecatCount() != 2 {
		t.Fatalf("live polecat count = %d, want 2 — nothing should have been reaped yet", reg.PolecatCount())
	}
	if got := reaper.exempt[buildcat.Name]; got != reviewID {
		t.Fatalf("no positive record of the grant: exempt[%s] = %q, want %q", buildcat.Name, got, reviewID)
	}

	// ── EXPIRES ────────────────────────────────────────────────────────────────
	// The verdict lands and the coordinator stops the reviewer. Nothing is
	// edited, cleared or remembered — the `reviews:` line is still on the ticket
	// and always will be. The only thing that changed is that a process ended.
	if err := reg.Stop(reviewcat.Name, 5*time.Second); err != nil {
		t.Fatalf("stopping the review polecat: %v", err)
	}
	select {
	case <-reviewcat.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the review polecat did not exit, so the expiry condition never became true")
	}

	stopped := reaper.Check(time.Now())
	if len(stopped) != 1 || stopped[0] != buildcat.Name {
		t.Fatalf("the exemption did not expire: want [%s] reaped once its reviewer is gone, got %v. "+
			"An exemption that outlives its reviewer holds the slot forever, which is mg-56d1's leak "+
			"returning through the fix for it", buildcat.Name, stopped)
	}
	select {
	case <-buildcat.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the build polecat's process is still running after the exemption lapsed")
	}
	if reg.Get(buildcat.Name) != nil {
		t.Error("the build polecat is still in the registry, so its slot is still counted as held")
	}
	if n := reg.PolecatCount(); n != 0 {
		t.Errorf("live polecat count = %d, want 0 — both slots should be free", n)
	}
}
