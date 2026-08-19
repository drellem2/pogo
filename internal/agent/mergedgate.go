package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// MergedWorkGate answers, at the moment of dispatch, whether a work item's work
// has ALREADY MERGED — whether a worker sent here would re-derive something that
// is on the target branch right now.
//
// It is the fourth refusal at this chokepoint and the complement of the third.
// The stranded-work gate (strandedgate.go) refuses an item whose branch holds
// pushed, UNMERGED work; this one refuses the case that opens the moment that
// branch lands. Between them they cover a work item's whole post-push life:
//
//	pushed, unmerged  -> strandedWorkRefusal: "resubmit the branch"
//	pushed, MERGED    -> mergedWorkRefusal:   "the work is on the target; close the item"
//
// WHY THAT SECOND CASE EXISTS AT ALL, given that pogod closes an item at merge
// (reapMergedPolecat). Because the close can be REFUSED, and the refusal is
// correct when it happens. An item tagged `declares-remainder` names work that
// must outlive it, and `mg done` refuses such an item until a successor is
// named. So the merge lands, the close is turned away, the polecat is stopped,
// its claim is released, and the item returns to available/ — genuinely
// unclaimed, genuinely open, and completely done. On 2026-08-12 that happened to
// mg-0e8c at 23:42Z and to mg-ac0c at 23:51Z; priority-wake advertised each as
// "high priority, ready and unclaimed" within minutes of its branch merging, and
// the only thing that stopped a dispatch was a coordinator who happened to
// remember watching the branch go through the refinery (mg-9d4e).
//
// That memory is not a control. Three surfaces read the item as unstarted — `mg
// list --status=available`, priority-wake, and this handler — and dispatch is the
// one of the three where the harm actually starts.
//
// THE GUARD THAT PRODUCED THE STATE IS NOT THE BUG AND MUST NOT BE WEAKENED. Its
// alternative failure is strictly worse and happened the same night: mg-69f1 was
// untagged, closed cleanly, and silently dropped its remainder. A loud,
// recoverable re-offer beats a silent loss, so the ordering stays and this gate
// addresses the side effect.
//
// An interface so the handler is testable without a refinery, mirroring
// DispatchGate, DispatchPairingGate and StrandedWorkGate. Unlike those three the
// production implementation cannot live in this package: the answer comes from
// the refinery's merge-request history, and internal/agent must not import
// internal/refinery (TestAgentPackageDoesNotImportRefinery). cmd/pogod owns both
// and builds it — see refineryMergedWork.
type MergedWorkGate interface {
	// MergedWork reports the merge that already landed for workItemID, and
	// whether there was one.
	//
	// False must be returned whenever the question could not be ASKED — no
	// refinery, no id, an empty history. See mergedWorkRefusal for why that
	// direction is open.
	MergedWork(workItemID string) (MergedWork, bool)
}

// MergedWorkGateFunc adapts a plain function to MergedWorkGate, so cmd/pogod can
// install a closure over the live refinery without declaring a type for it.
type MergedWorkGateFunc func(workItemID string) (MergedWork, bool)

// MergedWork implements MergedWorkGate.
func (f MergedWorkGateFunc) MergedWork(workItemID string) (MergedWork, bool) {
	return f(workItemID)
}

// MergedWork is one landed merge, reduced to what a refusal has to be able to
// name. Every field is quoted in the message, because a reader told only "this
// already merged" cannot check the claim, and a guard whose claim cannot be
// checked is one somebody overrides on reflex.
type MergedWork struct {
	// MR is the merge request id, so `pogo refinery show <id>` reaches the
	// record this refusal was derived from.
	MR string
	// Branch is the branch that merged.
	Branch string
	// Target is the ref it merged INTO. Quoted because "merged" is not one fact:
	// landing on the repo default is completion-shaped, landing on an
	// integration branch is not — see refineryMergedWork for which of the two
	// this gate refuses on.
	Target string
	// MergedSHA is the commit on the target, so the claim is checkable with one
	// `git log` and no daemon.
	MergedSHA string
	// MergedAt is when it landed, or the zero time when the record does not say.
	// Rendered as an age, because "merged 4 minutes ago" and "merged last week"
	// call for different reactions from a coordinator holding a queue.
	MergedAt time.Time
}

// SetMergedWorkGate installs the gate consulted before a polecat is dispatched.
// Passing nil DISARMS it — and that is the one place this gate differs from the
// three beside it, whose nil default is a functional implementation.
//
// The difference is structural, not a lapse. Every other gate's default can read
// its own input (the macguffin store, a git repository) from inside this package.
// This one's input is the refinery's history, which this package may not import,
// so there is no answer to fall back to: a nil gate here means "nothing can tell
// me whether anything merged", and the honest response to that is to dispatch.
// cmd/pogod wires it unconditionally beside the other three, so whether it
// enforces is a question about whether this daemon has a refinery at all — and a
// daemon with no refinery has no merges for it to catch.
func (r *Registry) SetMergedWorkGate(g MergedWorkGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mergedWorkGate = g
}

func (r *Registry) getMergedWorkGate() MergedWorkGate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mergedWorkGate
}

// mergedWorkRefusal returns the refusal message for a work item whose branch has
// already merged, or "" when dispatch is allowed.
//
// THE FAILURE DIRECTION IS OPEN, matching every other gate at this chokepoint: no
// id, no gate installed, a refinery that has forgotten the merge, all dispatch.
// Only a merge positively found in the refinery's record is refused. A gate that
// failed closed on an unanswerable question would refuse every id-less spawn and
// every spawn made while the refinery is disabled, which is most of them on a
// sandbox.
//
// What that costs is worth stating rather than leaving to be discovered. The
// refinery's in-memory history is NOT an archive: pruneHistoryLocked deletes past
// MaxHistoryLen (100) and MaxHistoryAge (7 days), so this gate is blind to a
// merge that has aged out. That is acceptable for the failure it exists to stop —
// the observed re-offer window is minutes, not weeks, because priority-wake
// advertises the item on its next sweep — but it means a quiet "no merged work"
// from this gate is never proof that none happened.
//
// IT DOES NOT CONSULT THE ITEM, and that has one known false positive: an item
// that merged, closed cleanly, and was later REOPENED for follow-up work is
// refused, because the merge is still on the refinery's record and this gate
// cannot see that the item's open state was deliberate. The alternative is a
// second store read that would have to distinguish "never closed" from "closed
// and reopened" — a distinction mg's status does not carry — so the honest
// design is to state the limit and make the override cheap. That case is exactly
// what --merged-override is for, and it is the one override at this chokepoint
// whose use is not a mistake by the gate.
//
// The message names the ITEM's way out first and the OVERRIDE second, in that
// order deliberately. The usual cause is a remainder that was never filed, and
// the fix for that is two commands the reader can run now; reaching for the
// override instead leaves the item in exactly the state that produced the
// refusal, for the next coordinator to hit.
func (r *Registry) mergedWorkRefusal(workItemID string) string {
	if strings.TrimSpace(workItemID) == "" {
		return ""
	}
	g := r.getMergedWorkGate()
	if g == nil {
		return ""
	}
	m, merged := g.MergedWork(workItemID)
	if !merged {
		return ""
	}
	return fmt.Sprintf("work item %s HAS ALREADY MERGED: branch %s landed on %s as %s%s (%s). A worker "+
		"dispatched here would re-derive work that is on the target right now — the item reads as "+
		"available because something REFUSED TO CLOSE IT after the merge, not because it is unstarted. "+
		"The usual cause is a `declares-remainder` item whose successor was never filed: `mg done` "+
		"correctly refuses such an item, so the merge landed and the close was turned away (mg-9d4e). "+
		"File the successor and close this item (`mg done %s --successor=<new id>`), or archive it if "+
		"the remainder is already filed elsewhere. If this item genuinely has work AFTER that merge, "+
		"dispatch anyway with --merged-override=\"<why>\"",
		workItemID, m.Branch, m.Target, shortMergedSHA(m.MergedSHA), m.age(), m.MR, workItemID)
}

// age renders how long ago the merge landed, or "" when the record does not say.
// A merge four minutes old and one four days old are different situations for the
// reader, and the timestamp alone makes them do the arithmetic.
func (m MergedWork) age() string {
	if m.MergedAt.IsZero() {
		return ""
	}
	d := time.Since(m.MergedAt)
	if d < 0 {
		return ""
	}
	return fmt.Sprintf(" %s ago", d.Round(time.Second))
}

// shortMergedSHA keeps a sha quotable in one line, and says so when there is
// none rather than rendering an empty gap the reader has to interpret.
func shortMergedSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "(sha not recorded)"
	}
	return shortSHA(sha)
}

// emitPolecatMergedOverridden records a dispatch that went ahead over the
// merged-work gate, with the reason its caller gave. Mirrors
// emitPolecatStrandedOverridden — overriding is meant to be cheap, overriding
// SILENTLY is not possible.
func emitPolecatMergedOverridden(spawnReq SpawnPolecatAPIRequest, reason, refusal string) {
	actor := "pogod"
	if spawnReq.Name != "" {
		actor = "cat-" + spawnReq.Name
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "dispatch_merged_work_overridden",
		Agent:      actor,
		WorkItemID: spawnReq.Id,
		Repo:       spawnReq.Repo,
		Details: map[string]any{
			"agent_type": string(TypePolecat),
			"agent_name": spawnReq.Name,
			"reason":     reason,
			"refusal":    refusal,
		},
	})
}
