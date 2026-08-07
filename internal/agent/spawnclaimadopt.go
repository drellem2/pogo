package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// A dispatch that dies holding the claim, and the two commands that then
// disagree about it (mg-790f).
//
// WHAT WAS OBSERVED. Two `spawn-polecat` failures on 2026-08-06, identical from
// the outside — no agent registered, no output — and OPPOSITE inside the store:
//
//	mg-6f5e  the item sat in claimed/ under pogod's OWN pid (4368). `mg show`
//	         had reported `available` minutes earlier; the retry was refused with
//	         "already claimed (by PID 4368)"; `mg unclaim` freed it and the
//	         identical spawn then succeeded immediately.
//	mg-325c  the item was never claimed at all — `mg unclaim` answered "not
//	         claimed, so there is nothing to release".
//
// A worktree was created in both. Half an hour went into the disagreement, and
// the wrong conclusion was nearly drawn twice, because the two commands an
// operator reaches for said different things.
//
// # 1. Where the claim is taken, and what each failure reached
//
// handleSpawnPolecat's order is: dispatch gates, template resolution, prompt
// file, `git worktree add`, command expansion, THEN claimForSpawn, THEN r.Spawn.
// The worktree is created BEFORE the claim, which is why it discriminates
// nothing — exactly what the two data points show:
//
//	died between `worktree add` and claimForSpawn  -> worktree, no claim (mg-325c)
//	died at or after r.Spawn                       -> worktree AND a claim under
//	                                                  pogod's pid    (mg-6f5e)
//
// Two failures, opposite claim states, one order of operations. There is no need
// for two failure modes to explain them.
//
// # 2. `mg show` and the claim check read the same record
//
// They are not two stores, and the stale-record theory is ruled out rather than
// left open. `mg show` is workitem.ReadWithStatus and `mg claim` is
// workitem.Claim; both resolve through the single workitem.ResolveUnique walk
// over <root>/work/*, and an item's status IS the directory its file is in. `mg
// show` therefore cannot report `available` for a file sitting in claimed/.
//
// What the operator saw was two readings SEPARATED IN TIME against a dispatch
// that was still running. The CLI had been killed by a wrapper `timeout`, and
// killing the client does not stop the server-side handler: pogod took the claim
// after the `mg show` that reported available. The store never lied. The defect
// is that a dispatch holding a claim with no agent to show for it is invisible to
// every command an operator has, so a claim taken at 23:41 and a status read at
// 23:38 look like a contradiction instead of a sequence.
//
// # 3. Release-on-failure is necessary and not sufficient
//
// releaseSpawnClaim already gives the claim back when r.Spawn returns an error,
// and it is kept: it is the cheap path and it fires for the common failure. But
// it runs only if the failing dispatch lives long enough to run it, and mg-6f5e
// produced no output at all. So the robustness has to be in the claim CHECK,
// where it survives a spawn that executes no cleanup whatsoever.
//
// The check this file adds: a claim held under pogod's OWN pid, with no dispatch
// in flight and no live agent on the item, cannot be owned by anything. Nothing
// else stamps pogod's pid, and the only window in which that pid is legitimate
// runs from claimForSpawn to the worker's `mg reclaim`. Outside that window it is
// residue, and a dispatch adopts it. Adoption is a NO-OP ON DISK — the claim file
// is already byte-for-byte what a fresh claim-at-spawn writes — so the item is
// never in available/ for an instant and mg-7254's duplicate-dispatch guarantee
// is kept by construction. `mg unclaim` followed by `mg claim` would not do that,
// which is the same reason macguffin grew `mg reclaim`.
//
// # Why the in-flight ledger is load-bearing rather than tidy
//
// Without it, adoption reintroduces the double dispatch it sits downstream of:
// dispatch A takes the claim and blocks inside r.Spawn, dispatch B finds pogod's
// pid and no agent — because A has not registered one yet — and adopts a live
// claim. The ledger is the only thing that distinguishes "no agent YET" from "no
// agent EVER".
//
// It also supplies the answer that was missing on the night. A second dispatch
// onto an item held by a wedged first one is now refused with that dispatch's
// NAME and AGE, not with a bare pid — "a dispatch for polecat 6f5e, started 31m
// ago, still holds it and has not returned" is the sentence that would have ended
// the investigation in one command instead of thirty minutes.
//
// # What is deliberately NOT adopted
//
// A claim stranded by a pogod that has since RESTARTED carries the old daemon's
// pid, which is not this one's, so it is refused rather than adopted. That
// residue is a choice: a claim under some other pid is indistinguishable from a
// human's own `mg claim`, and silently stealing a human's claim is worse than
// making an operator run `mg unclaim`. The refusal names the pid, and `pogo agent
// list` will show no agent — which is the diagnosis, delivered in the refusal.

// inFlightSpawnClaim records that pogod holds a work item's spawn-time claim on
// behalf of a dispatch that has not finished. It exists for exactly the window in
// which the claim is pogod's and no agent is registered under it yet.
type inFlightSpawnClaim struct {
	agentName string
	since     time.Time
}

// beginSpawnClaim opens the in-flight window for a claim this dispatch now holds.
// Called for a fresh claim and for an adopted one alike: both are pogod's, and
// both need the window closed against a concurrent second dispatch.
func (r *Registry) beginSpawnClaim(workItemID, agentName string) {
	if workItemID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spawnClaimsInFlight == nil {
		// Lazily, because a bare &Registry{} is a legitimate construction in this
		// package's tests and must not deadlock or panic on a dispatch path.
		r.spawnClaimsInFlight = make(map[string]inFlightSpawnClaim)
	}
	r.spawnClaimsInFlight[workItemID] = inFlightSpawnClaim{agentName: agentName, since: time.Now()}
}

// endSpawnClaim closes the in-flight window, whichever way the dispatch went. On
// success the registered agent becomes the record of ownership; on failure
// releaseSpawnClaim has already handed the claim back. Leaving an entry behind
// would make a genuinely stranded claim un-adoptable for the life of the daemon,
// so the caller closes it with a defer rather than on each return path.
func (r *Registry) endSpawnClaim(workItemID string) {
	if workItemID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.spawnClaimsInFlight, workItemID)
}

// spawnClaimInFlight reports the dispatch currently holding pogod's spawn-time
// claim on workItemID, if any.
func (r *Registry) spawnClaimInFlight(workItemID string) (inFlightSpawnClaim, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.spawnClaimsInFlight[workItemID]
	return c, ok
}

// liveAgentOnWorkItem returns a registered, non-exited agent assigned to
// workItemID, or nil.
//
// It matches on WorkItemID ONLY — deliberately not GetByWorkItemOrName, whose
// Name arm would match an agent merely NAMED after a different id string and
// report an owner that is not one. Here a false owner suppresses adoption and
// leaves the item stranded, so the looser match fails in the direction this file
// exists to fix.
//
// Status is read after the registry lock is dropped: Agent.GetStatus takes the
// agent's own mutex, and taking two locks in one order here invites the reverse
// order somewhere else.
func (r *Registry) liveAgentOnWorkItem(workItemID string) *Agent {
	if workItemID == "" {
		return nil
	}
	r.mu.RLock()
	var candidates []*Agent
	for _, a := range r.agents {
		if a.WorkItemID == workItemID {
			candidates = append(candidates, a)
		}
	}
	r.mu.RUnlock()

	for _, a := range candidates {
		if a.GetStatus() != StatusExited {
			return a
		}
	}
	return nil
}

// claimHolder is what a ClaimConflict means once pogod has asked who holds the
// claim. kind is a stable slug for events.log; sentence() is the operator prose.
type claimHolder struct {
	// stranded is the only field the dispatch decision turns on: the claim is
	// pogod's own and provably belongs to no one.
	stranded bool
	// kind labels the holder for the emitted event: "self_stranded",
	// "self_in_flight", "live_agent", or "other" (a foreign pid, an older pogod,
	// or a non-claimed status such as done).
	kind string
	// detail is the human half, already phrased as a standalone sentence body.
	detail string
}

func (h claimHolder) sentence() string {
	if h.detail == "" {
		return ""
	}
	return " " + h.detail + "."
}

// classifyClaimConflict decides what a refused claim means, and in particular
// whether the claim is stranded residue this dispatch may adopt.
//
// All three conditions are required, and each rules out a specific way of being
// wrong:
//
//   - THE PID IS OURS. Only pogod stamps pogod's pid. A foreign pid is a human's
//     claim, a re-stamp by a live worker, or an older daemon's — none adoptable.
//     A conflict with NO pid (HolderPID 0: done, shelved, archived, pending, or
//     an unreadable claim file) fails closed here for the same reason.
//   - NO DISPATCH IS IN FLIGHT. Otherwise a second dispatch adopts the claim of a
//     first that is merely slow inside r.Spawn — the double dispatch the claim
//     exists to prevent, rebuilt on top of the fix for it.
//   - NO LIVE AGENT HOLDS THE ITEM. A worker that started but has not yet run
//     `mg reclaim` — or that runs against an mg with no `reclaim` at all — is
//     working under pogod's pid legitimately, and for its whole life in the
//     second case.
//
// Note what is NOT tested: whether the holder pid is ALIVE. A healthy worker's
// re-stamped claim names the pid of the `mg reclaim` subprocess, which exits
// immediately, so "the claim pid is dead" is the ordinary state of a perfectly
// owned item and would condemn the entire fleet.
func (r *Registry) classifyClaimConflict(verdict ClaimVerdict, workItemID string) claimHolder {
	if verdict.HolderPID == 0 || verdict.HolderPID != os.Getpid() {
		return claimHolder{kind: "other"}
	}
	if c, ok := r.spawnClaimInFlight(workItemID); ok {
		return claimHolder{
			kind: "self_in_flight",
			detail: fmt.Sprintf("the claim is pogod's own (pid %d): a dispatch for polecat %s, "+
				"started %s ago, still holds it and has not returned — this daemon is wedged "+
				"part-way through that spawn, not idle",
				verdict.HolderPID, c.agentName, time.Since(c.since).Round(time.Second)),
		}
	}
	if a := r.liveAgentOnWorkItem(workItemID); a != nil {
		return claimHolder{
			kind: "live_agent",
			detail: fmt.Sprintf("the claim is pogod's own (pid %d) because polecat %s is live on "+
				"this item and has not re-stamped it to its own pid yet", verdict.HolderPID, a.Name),
		}
	}
	return claimHolder{stranded: true, kind: "self_stranded"}
}

// adoptStrandedSpawnClaim takes over a claim pogod stranded, and records that it
// did.
//
// There is nothing to write: the claim file already names pogod's pid, which is
// precisely what MGWorkItemClaimer would have written had the item been
// available. The whole operation is a verdict change plus the in-flight entry the
// stranded predecessor never closed — and it is the emitted event that makes the
// recovery observable rather than a silent second chance. mg-d22a is the standing
// lesson here: a state that carries no record of how it arose costs an evening
// the next time it happens, and a self-healing path that logs nothing is the
// purest form of that.
func (r *Registry) adoptStrandedSpawnClaim(verdict ClaimVerdict, spawnReq SpawnPolecatAPIRequest) ClaimVerdict {
	log.Printf("polecat %s: work item %s was in claimed/ under pogod's OWN pid (%d) with no "+
		"dispatch in flight and no live agent — a claim an earlier spawn took and died holding. "+
		"ADOPTING it for this dispatch; the claim file already names this pid, so the item never "+
		"passes back through available/ (mg-790f). mg said: %s",
		spawnReq.Name, spawnReq.Id, verdict.HolderPID, verdict.Detail)
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_stranded_claim_adopted",
		Agent:      "cat-" + spawnReq.Name,
		WorkItemID: spawnReq.Id,
		Repo:       spawnReq.Repo,
		Details: map[string]any{
			"agent_name": spawnReq.Name,
			"claim_pid":  verdict.HolderPID,
			"detail":     verdict.Detail,
		},
	})
	verdict.Outcome = ClaimAdopted
	r.beginSpawnClaim(spawnReq.Id, spawnReq.Name)
	return verdict
}
