package agent

import (
	"sort"
	"time"
)

// PolecatActivity is a snapshot of one live polecat's identity plus how long it
// has been quiet on its PTY. It is the input to the done-item reaper (cmd/pogod
// donereap.go), which needs exactly two facts about a polecat — which work item
// it owns, and whether it is still producing output — and must not hold the
// registry lock while it shells out to `mg show` to learn the third.
//
// It is deliberately NOT an extension of PolecatInfo. PolecatInfo is serialized
// over the /agents drain API for the self-deploy driver; this one is internal
// and carries a time.Duration that has no useful JSON form. Keeping them apart
// also keeps the drain contract from acquiring a field it does not read.
type PolecatActivity struct {
	// Name is the bare registry name — the identity Registry.Stop keys on.
	Name string
	// WorkItemID is the full work-item id (e.g. "mg-d764"), empty for a polecat
	// spawned without one. A consumer that must ask the work item a question has
	// nothing to ask when this is empty.
	WorkItemID string
	// IdleFor is how long since the polecat last wrote to its PTY. Zero when
	// HasOutput is false — see why that is not "idle for 0s" below.
	IdleFor time.Duration
	// HasOutput reports whether the polecat has EVER written to its PTY.
	//
	// It is a separate field rather than an IdleFor sentinel because the two
	// answers point opposite ways and a consumer must not conflate them. A
	// polecat with no output at all has an UNMEASURABLE idle time, not a short
	// one: it may be seconds into spawn, or wedged before its first turn (the
	// mg-ce61 unsubmitted-paste case). diagnoseAgentAt makes the same choice —
	// it refuses to call a zero LastWriteTime stalled — and any consumer that
	// acts on idleness must inherit that refusal rather than read the zero as
	// "idle for no time".
	HasOutput bool
}

// PolecatActivityAt returns a snapshot of every live polecat with its PTY
// idleness measured as of now. Sorted by name so callers and their logs are
// deterministic.
//
// The snapshot exists so the caller can do slow work (a per-polecat `mg show`)
// outside the registry lock. Everything it reports is a fact about the instant
// it was taken, and a polecat can exit between the snapshot and the caller's
// decision — so a consumer acting on it must tolerate the agent being gone by
// then (Registry.Stop on an absent name is an error, not a panic).
func (r *Registry) PolecatActivityAt(now time.Time) []PolecatActivity {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []PolecatActivity
	for _, a := range r.agents {
		if a.Type != TypePolecat || !a.alive() {
			continue
		}
		act := PolecatActivity{Name: a.Name, WorkItemID: a.WorkItemID}
		if a.outputBuf != nil {
			if t := a.outputBuf.LastWriteTime(); !t.IsZero() {
				act.HasOutput = true
				act.IdleFor = now.Sub(t)
			}
		}
		out = append(out, act)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
