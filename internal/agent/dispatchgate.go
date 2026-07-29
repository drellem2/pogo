package agent

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// DispatchGate answers, at the moment of dispatch, whether a work item is gated
// away from automatic execution — whether handing it to a worker is allowed at
// all.
//
// This is the mg-4798 ruling, built. The rule itself (an item is dispatchable
// unless its assignee names a non-dispatchable executor) already existed and was
// already enforced in Go — but only in internal/stallwatch, which decides what to
// WATCH. The path that actually DISPATCHES carried the rule as a sentence in a
// prompt template instead: `internal/agent/prompts/mayor.md` told the coordinator
// that a `--assignee=human` item "won't be dispatched", 170 lines away from the
// listing advice it needed to be read with. So `pogo agent spawn-polecat` would
// cheerfully put a polecat on a human-gated item, and the only thing standing in
// the way was whether an agent had read and retained a paragraph.
//
// mg-4798 rejected fixing that in the prose and rejected fixing it in `mg list`
// (the CLI is convenience, not gatekeeper — ARCHITECTURE.md M2; and status and
// assignee are orthogonal by construction). It ruled the gate belongs in the
// executable path at the dispatch point, reusing the predicate rather than
// restating it. Hence: one predicate in config.IsDispatchGated, two enforcement
// sites that cannot drift apart.
//
// An interface so the handler is testable without a macguffin store, mirroring
// ClaimReleaser.
type DispatchGate interface {
	// DispatchGated reports whether workItemID is gated. It returns the gating
	// assignee alongside the verdict so the refusal can name what gated it —
	// "refused: assigned to human" is actionable, a bare refusal is a bug report.
	//
	// It deliberately has no error return. See MGDispatchGate.DispatchGated for
	// why an unreadable store must not be an error here.
	DispatchGated(workItemID string) (assignee string, gated bool)
}

// MGDispatchGate is the production DispatchGate: it reads the work item out of
// the macguffin store and tests its assignee against the configured gate
// vocabulary.
type MGDispatchGate struct {
	// Root overrides the macguffin store location. Empty resolves via
	// macguffinStoreRoot, which under a test binary is a throwaway temp store
	// rather than the live one.
	Root string
	// Gates is the non-dispatchable assignee vocabulary. Empty falls back to
	// config.DefaultNonDispatchableAssignees, so an unwired daemon still gates
	// "human" and "parked" — the default is the enforcing one, because a gate
	// that needs wiring to work is a gate that is off wherever someone forgot.
	Gates []string
}

// DispatchGated implements DispatchGate.
//
// THE FAILURE DIRECTION IS DELIBERATE AND IT IS OPEN. An item that cannot be
// found, an id that was never supplied, an unreadable store — all report "not
// gated" and let the spawn proceed. Only a work item positively read from disk
// whose assignee positively matches the gate vocabulary is refused.
//
// That is the opposite of the asymmetry in stall-watch's twin, and for a reason
// worth stating rather than inheriting. Stall-watch fails toward WATCHING because
// its cost of guessing wrong is a spurious nudge — loud and self-correcting. This
// gate's cost of guessing wrong is a refused dispatch: it would break every
// legitimate spawn whose item is not in the store, and `--id` is optional by
// design (a spawn without one merely forfeits start-verification, mg-2437), so
// failing closed would refuse every id-less spawn outright. A gate that fails
// closed on a missing store also means one bad path in macguffin halts the entire
// fleet.
//
// What that costs is worth being precise about, because "fails open" is the kind
// of property that gets discovered rather than read. This gate stops a
// coordinator from dispatching a gated item it read out of available/ — the
// actual mg-4798 failure, where the item is present and the assignee is right
// there in its frontmatter. It does NOT stop a caller who supplies no id, or a
// wrong one. It is a guard on the dispatch decision, not proof that no worker can
// ever reach gated work.
func (m MGDispatchGate) DispatchGated(workItemID string) (string, bool) {
	if workItemID == "" {
		return "", false
	}
	root := macguffinStoreRoot(m.Root)
	if root == "" {
		return "", false
	}
	item, found, err := workitem.FindFrom(filepath.Join(root, "work"), workItemID)
	if err != nil {
		// Loud but not fatal: the spawn proceeds (see the fail-open rationale
		// above), and the log records that the gate could not answer, so a store
		// that has become unreadable is visible as something other than silence.
		log.Printf("dispatch gate: could not read work item %s from %s: %v — "+
			"dispatching WITHOUT the assignee gate; if this item is assigned to a "+
			"non-dispatchable executive (%v) the spawn was not refused",
			workItemID, root, err, m.gates())
		return "", false
	}
	if !found {
		return "", false
	}
	if config.IsDispatchGated(item.Assignee, m.Gates) {
		return item.Assignee, true
	}
	return "", false
}

// gates returns the effective gate vocabulary, for messages.
func (m MGDispatchGate) gates() []string {
	if len(m.Gates) == 0 {
		return config.DefaultNonDispatchableAssignees
	}
	return m.Gates
}

// SetDispatchGate installs the gate consulted before a polecat is dispatched.
// Passing nil restores the default, which is MGDispatchGate{} — functional, not
// a no-op, for the same reason SetClaimReleaser's default is: a guard that only
// engages once someone remembers to wire it is a guard that is absent in every
// deployment where they didn't.
func (r *Registry) SetDispatchGate(g DispatchGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatchGate = g
}

func (r *Registry) getDispatchGate() DispatchGate {
	r.mu.RLock()
	g := r.dispatchGate
	r.mu.RUnlock()
	if g == nil {
		return MGDispatchGate{}
	}
	return g
}

// dispatchGateRefusal returns the refusal message for a gated work item, or ""
// when dispatch is allowed. The message names the item, the assignee that gated
// it, and the two ways out, because the reader is usually an agent that must
// decide what to do next without a human in the loop.
func (r *Registry) dispatchGateRefusal(workItemID string) string {
	assignee, gated := r.getDispatchGate().DispatchGated(workItemID)
	if !gated {
		return ""
	}
	return fmt.Sprintf("work item %s is assigned to %q, which is gated away from automatic "+
		"dispatch (non_dispatchable_assignees); it must be done by hand or unparked. "+
		"Reassign it or clear its assignee to dispatch a worker onto it",
		workItemID, assignee)
}
