package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// The gate-reap tests (mg-9af1).
//
// WHAT THEY PIN. A gh-issue TRIAGE polecat is told not to call `mg done` — no
// successor exists until the coordinator files the build ticket after the human
// gate — so its item never reaches a terminal state and the done-reaper's
// condition can never see it. It therefore held a worker slot from the moment it
// delivered its packet until a person answered, which on a `stage: gated` ticket
// is unbounded. Measured three times on 2026-08-12 (p634e, p1539, pc4a9) and
// again on 2026-09-07 (t7476); every one was stopped by hand and every one
// looked perfectly healthy while it sat there.
//
// The reap is only safe because of two facts these tests keep separate from each
// other: the packet is durable on the ticket BODY, and `stage: gated` is
// enforced at the SPAWN POINT (mg-69b1) so the released claim cannot be picked
// up again. The second is the one this package can pin — the branch uses
// config.IsStageGated, the dispatch gate's own predicate — and it is pinned in
// TestGateReapKeysOnTheDispatchGatesOwnPredicate.

// stageStore models client.MGWorkItemStage over a fixed map of item id -> the
// `stage:` line its carrier declares. An item absent from the map declares
// nothing, which is the ordinary case for every item not on the gh-issue track.
func stageStore(stages map[string]string) func(string) (string, error) {
	return func(id string) (string, error) { return stages[id], nil }
}

// gateReaper builds a reaper with the gate branch wired, over a store where
// nothing is terminal — so every reap these tests see came from the stage.
func gateReaper(reg doneReapRegistry, status, stages map[string]string, grace time.Duration) *doneReaper {
	r := newDoneReaper(reg, doneStore(status), noReviews, grace)
	r.SetStageProbe(stageStore(stages))
	return r
}

// TestGateReapBothArms is the acceptance control, and like mg-56d1's it must
// pass in BOTH directions.
//
// POSITIVE ARM: a triage polecat whose item is parked at `stage: gated` and
// which has gone quiet is stopped, freeing its slot. This is the t7476 case.
//
// NEGATIVE ARM: a polecat whose item is at a stage a worker is SUPPOSED to be
// working in survives, however long it has been quiet. Both are asserted from a
// single Check against a single snapshot, so the discrimination is structural
// rather than a property of how the test was set up.
func TestGateReapBothArms(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		// t7476: packet delivered, ticket gated, idle — must be reaped.
		{Name: "t7476", WorkItemID: "mg-7476", IdleFor: 6 * time.Minute, HasOutput: true},
		// fd94: a builder holding after PR-open. Its ticket carries `stage:
		// review`, and stopping it would destroy the modify side of a live
		// review loop — an open PR and an unmerged branch, with no equivalent of
		// "the packet is on the ticket". It must survive.
		{Name: "fd94", WorkItemID: "mg-fd94", IdleFor: 20 * time.Minute, HasOutput: true},
	}}
	r := gateReaper(reg, map[string]string{
		"mg-7476": "claimed",
		"mg-fd94": "claimed",
	}, map[string]string{
		"mg-7476": "gated",
		"mg-fd94": "review",
	}, 2*time.Minute)

	stopped := r.Check(time.Now())

	if len(stopped) != 1 || stopped[0] != "t7476" {
		t.Fatalf("positive arm: want exactly [t7476] stopped, got %v", stopped)
	}
	for _, name := range reg.stops() {
		if name == "fd94" {
			t.Fatalf("negative arm: fd94 was stopped — a builder at `stage: review` owns an open PR and the modify "+
				"side of a live review loop, and the ticket that asked for this reap says explicitly that stopping "+
				"it must NOT be generalised from the triage case (stops=%v)", reg.stops())
		}
	}
	for _, p := range reg.PolecatActivityAt(time.Now()) {
		if p.Name == "t7476" {
			t.Fatalf("t7476 still live after being stopped — its slot was not freed, which is the whole deliverable")
		}
	}
}

// TestGateReapKeysOnTheDispatchGatesOwnPredicate is why the negative arm above
// holds for every stage and not just `review`.
//
// `gated` is the ONE stage that gates dispatch (config.IsStageGated), and that
// is not an accident of this reaper: it is the predicate the spawn point applies
// (mg-69b1), which is the only reason releasing a gated item's claim is safe. So
// the set of stages this reaps and the set the dispatcher refuses are the same
// set by construction. A local re-implementation here — "gated or merge", say —
// would reap a polecat whose item IS re-dispatchable, and a second triage
// polecat's first instructed act is an acknowledgement comment on a stranger's
// open GitHub issue.
func TestGateReapKeysOnTheDispatchGatesOwnPredicate(t *testing.T) {
	// Every stage in the gh-issue vocabulary (mayor.md), plus the empty one that
	// every item off that track carries.
	for _, stage := range []string{"", "triage", "build", "review", "merge", "done", "blocked"} {
		reg := &fakeDoneReg{live: []agent.PolecatActivity{
			{Name: "w", WorkItemID: "mg-w", IdleFor: time.Hour, HasOutput: true},
		}}
		r := gateReaper(reg, map[string]string{"mg-w": "claimed"}, map[string]string{"mg-w": stage}, time.Minute)
		if got := r.Check(time.Now()); len(got) != 0 {
			t.Errorf("stage %q: reaped %v — only `gated` gates dispatch, so only `gated` may be reaped; "+
				"anything else stops a polecat whose item is still dispatchable", stage, got)
		}
	}
	// And the positive control for the same instrument: the one word does fire.
	// Without this, a predicate that answered false for everything would pass the
	// loop above while reaping nothing at all.
	for _, stage := range []string{"gated", "GATED", "  gated  "} {
		reg := &fakeDoneReg{live: []agent.PolecatActivity{
			{Name: "w", WorkItemID: "mg-w", IdleFor: time.Hour, HasOutput: true},
		}}
		r := gateReaper(reg, map[string]string{"mg-w": "claimed"}, map[string]string{"mg-w": stage}, time.Minute)
		if got := r.Check(time.Now()); len(got) != 1 {
			t.Errorf("stage %q: want it reaped, got %v — matching is case-folded and trimmed, as the dispatch "+
				"gate's is, so a hand-edited `Stage: Gated` is the same gate", stage, got)
		}
	}
}

// TestGateReapNamesItself. The two stops mean different things about the ITEM —
// done_reap says the ticket is closed, gate_reap says the worker was released
// and the ticket is still parked awaiting a decision — and an operator holding
// an agent_stopped record has no other way to tell them apart. A fix that
// reused done_reap here would put a completion in the log for work that has not
// completed.
func TestGateReapNamesItself(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "t7476", WorkItemID: "mg-7476", IdleFor: 6 * time.Minute, HasOutput: true},
	}}
	r := gateReaper(reg, map[string]string{"mg-7476": "claimed"}, map[string]string{"mg-7476": "gated"}, time.Minute)
	if got := r.Check(time.Now()); len(got) != 1 {
		t.Fatalf("test premise: want t7476 reaped, got %v", got)
	}
	if len(reg.stopCauses) != 1 || reg.stopCauses[0] != agent.StopCauseGateReap {
		t.Errorf("stop_cause: want [%s], got %v — a gate reap logged as done_reap asserts a completion that did not happen",
			agent.StopCauseGateReap, reg.stopCauses)
	}
}

// TestGateReapDoesNotReportACompletion. The completion notifier mails the agent
// that COMMISSIONED an item to say it is finished (mg-f120). A gated item is
// not finished — that is the entire reason its polecat is still here — so the
// gate branch must not reach that seam. Firing it would tell the commissioner
// the decision had been made, which is a straight falsehood about the one thing
// they are waiting for.
func TestGateReapDoesNotReportACompletion(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "t7476", WorkItemID: "mg-7476", IdleFor: 6 * time.Minute, HasOutput: true},
		// The control: a genuinely done polecat on the same tick DOES notify, so
		// an empty `seen` cannot be mistaken for a wired-up-but-silent notifier.
		{Name: "d764", WorkItemID: "mg-d764", IdleFor: 8 * time.Minute, HasOutput: true},
	}}
	filer := &capturingFiler{}
	r := gateReaper(reg, map[string]string{
		"mg-7476": "claimed",
		"mg-d764": "done",
	}, map[string]string{"mg-7476": "gated"}, time.Minute)
	r.SetFilerNotifier(filer)

	if got := r.Check(time.Now()); len(got) != 2 {
		t.Fatalf("test premise: want both reaped, got %v", got)
	}
	seen := filer.all()
	if len(seen) != 1 {
		t.Fatalf("want exactly one completion notice (the done item's), got %d: %+v", len(seen), seen)
	}
	if seen[0].ItemID != "mg-d764" {
		t.Errorf("the completion notice names %s — a gated item is not complete, and telling its commissioner "+
			"otherwise is a false statement about the decision they are waiting to make", seen[0].ItemID)
	}
}

// TestGateReapStillRequiresTheQuietWindow. The gate branch changes WHICH items
// qualify, never the protection that keeps a polecat from being killed
// mid-sentence. A triage polecat is at its busiest right after it delivers —
// appending the packet to the ticket body, mailing the coordinator — and every
// one of those writes resets the clock.
func TestGateReapStillRequiresTheQuietWindow(t *testing.T) {
	for _, idle := range []time.Duration{0, 30 * time.Second, 119 * time.Second} {
		reg := &fakeDoneReg{live: []agent.PolecatActivity{
			{Name: "t7476", WorkItemID: "mg-7476", IdleFor: idle, HasOutput: true},
		}}
		r := gateReaper(reg, map[string]string{"mg-7476": "claimed"}, map[string]string{"mg-7476": "gated"}, 2*time.Minute)
		if got := r.Check(time.Now()); len(got) != 0 {
			t.Errorf("idle=%s: reaped %v inside the grace — a polecat still writing its packet must not be stopped", idle, got)
		}
	}
	// A polecat that has never written at all is unmeasurable, not idle: it may
	// be seconds into spawn on a ticket someone re-gated. Never reaped.
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "t7476", WorkItemID: "mg-7476", IdleFor: time.Hour, HasOutput: false},
	}}
	r := gateReaper(reg, map[string]string{"mg-7476": "claimed"}, map[string]string{"mg-7476": "gated"}, 2*time.Minute)
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Errorf("a polecat with no PTY output was reaped (%v): its idleness is unmeasurable, not long", got)
	}
}

// TestGateReapLeavesAnUnreadableStageRunning. A probe error means "cannot tell",
// and the action on the table is a STOP, so it must not be taken.
//
// This is the same mg-27d4 error the dispatch gate reads in the OPPOSITE
// direction — it refuses a spawn on an unreadable carrier because the item may
// say `gated`; this declines to reap because it may not — and both fail toward
// the recoverable outcome. It is not a hypothetical shape here: a ticket that
// writes a bare `stage:` line in its own prose (as this feature's tickets do)
// parses as unreadable, and it must cost a slot rather than a worker.
func TestGateReapLeavesAnUnreadableStageRunning(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "t7476", WorkItemID: "mg-7476", IdleFor: time.Hour, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-7476": "claimed"}), noReviews, time.Minute)
	r.SetStageProbe(func(string) (string, error) {
		return "", fmt.Errorf("carrier block is out of the parser's reach")
	})
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Errorf("reaped %v on a stage the probe could not read — a stop taken on a guess is the one "+
			"outcome neither a held slot nor a refused dispatch is", got)
	}
}

// TestGateReapIsOffWithoutAProbe. Nil disables the branch and the reaper behaves
// exactly as it did before mg-9af1, which is what every test predating this seam
// relies on. The direction is fail-CLOSED for this branch — an unwired daemon
// leaks a slot, as it did before, rather than stopping the wrong worker — and
// that is the opposite of the review exemption's nil behaviour, so it is pinned
// rather than left to be inferred.
func TestGateReapIsOffWithoutAProbe(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "t7476", WorkItemID: "mg-7476", IdleFor: time.Hour, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{"mg-7476": "claimed"}), noReviews, time.Minute)
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Errorf("an unwired reaper reaped %v — with no stage probe the gate branch must not exist at all", got)
	}
}

// TestGateReapHonoursTheReviewExemption. Defence in depth: a triage ticket is
// never a review target, so this exemption is not expected to fire on the gate
// branch in practice — but the branch shares the one decision path rather than
// being a second reaper, and this is what says so. A second detector would have
// had to re-derive the exemption and could have reached a different answer about
// the same polecat on the same tick.
func TestGateReapHonoursTheReviewExemption(t *testing.T) {
	reg := &fakeDoneReg{live: []agent.PolecatActivity{
		{Name: "t7476", WorkItemID: "mg-7476", IdleFor: time.Hour, HasOutput: true},
		{Name: "rev", WorkItemID: "mg-rev", IdleFor: time.Second, HasOutput: true},
	}}
	r := newDoneReaper(reg, doneStore(map[string]string{
		"mg-7476": "claimed",
		"mg-rev":  "claimed",
	}), reviewsStore(map[string]string{"mg-rev": "mg-7476"}), time.Minute)
	r.SetStageProbe(stageStore(map[string]string{"mg-7476": "gated"}))
	if got := r.Check(time.Now()); len(got) != 0 {
		t.Errorf("reaped %v while a live polecat declares `reviews: mg-7476` — the gate branch is a second way "+
			"to satisfy the reaper's condition, not a second reaper that skips its guards", got)
	}
}

// TestGateReapDocumentsWhyReleasingTheClaimIsSafe. This reap hands a gated item
// back to available/, and the ONLY thing that stops a second triage polecat
// picking it up — and posting a second acknowledgement comment on a stranger's
// open issue — is the spawn-point stage gate (mg-69b1). That dependency is
// load-bearing and lives in another package, so it cannot be asserted from
// here; what can be asserted is that the file says so, and names it, for
// whoever narrows that gate later.
func TestGateReapDocumentsWhyReleasingTheClaimIsSafe(t *testing.T) {
	src := readSourceFile(t, "donereap.go")
	for _, want := range []string{"mg-69b1", "IsStageGated"} {
		if !strings.Contains(src, want) {
			t.Errorf("donereap.go does not mention %q — the gate reap is safe only because the spawn point "+
				"refuses a `stage: gated` item, and a reader who cannot find that from here cannot check it", want)
		}
	}
}
