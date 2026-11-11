package stallwatch

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/drellem2/pogo/internal/workitem"
)

// A claimed item whose claimant is dead and that no live worker is on
// (mg-5dec2).
//
// THE GAP THIS CLOSES. Every other check in this package reads available/. A
// claim moves the item to claimed/<id>.md.<pid>, and from then on dispatch
// skips it (claimed items are never dispatched) and so does every notice here.
// That is right while the claimant lives. When it dies without releasing, the
// item is invisible to every recovery path at once: stall-watch, priority-wake,
// check-stranded and the restart sweep all look elsewhere.
//
// The releases pogod does own cover the exits it WITNESSES: Registry.Stop
// (mg-fb13) and the reaper (ReleaseClaimAfterExit). The exit they cannot
// witness is the one this fleet takes every night: pogod itself dies. Agents
// are not stopped on the way out — the PTY hangup kills them (cmd/pogod/main.go,
// mg-6b66) — and the successor pogod has no registry entry for any of them, so
// neither release door is ever reachable again. On 2026-11-08 the 03:02Z
// redeploy did exactly that to eight polecats that had sat behind a usage-
// cap modal since 23:18Z; seven of their claims then sat under dead pids for
// three and a half days, and three of them left uncommitted edits in retained
// worktrees. The restart sweep saw all of them and counted them "clean",
// because it asks only about PUSHED work (strandedsweep.go).
//
// WHAT COUNTS AS ORPHANED. Two independent negatives, both required:
//
//   - The claim pid does not answer signal 0. This is the WEAK half, and it is
//     weaker than it looks: mg records $POGO_PID, else its OWN pid, and
//     POGO_PID is not set in agent shells — so a polecat's `mg reclaim` stamps
//     a pid that exits the instant mg does (measured 2026-11-11: every claim in
//     claimed/, including three under running polecats, was under a dead pid).
//     The pid stays useful as a positive: pogod's claim-at-spawn stamps
//     pogod's own pid, and a live pid there means "do not judge yet".
//   - The in-flight probe — registry plus live witness, the same snapshot the
//     available/ checks share — names no worker for the item. This is the
//     half that actually separates a running polecat from a dead one, and it
//     is exactly the half that a pogod restart cannot fool: the registry is
//     empty after a restart, and the witness record of a polecat killed by
//     the hangup reads WitnessDead.
//
// FALSE POSITIVES THIS GUARDS AGAINST, AND HOW.
//
//   - A human's `mg claim` from a shell records mg's own pid, which is dead a
//     moment later. Dispatch-gated assignees (human, parked, blocked:<x>) are
//     not this watcher's population anywhere else and are not here either; an
//     unassigned item claimed by hand will be reported, and the notice says
//     that is one of the possibilities. Crew claims are the same shape (no
//     crew agent is in the in-flight probe, which counts polecats), and were
//     measured before shipping: of 80 claims taken by mayor and the PMs
//     between 2026-10-01 and 2026-11-11, 78 closed inside a minute and 2
//     outlived the grace — one of them by ~70h, which is this defect.
//   - A release in flight: a polecat exiting, and the reaper or Stop about to
//     unclaim. The condition must hold continuously for orphanedClaimGrace
//     before anything is sent, so a release that is merely slow is never
//     reported.
//
// FALSE NEGATIVES IT ACCEPTS. A dead claimant's pid recycled by an unrelated
// process reads as alive. Matching a process start time against the claim would
// close it, but the only claim time the store keeps is the file's mtime, which a
// rename preserves — so for an item filed before its worker started, the
// worker's start would look LATER than the claim and a live worker would read as
// recycled. Missing a recycled pid is the safe direction.
//
// REPORT-ONLY. It never unclaims. The claim may guard a retained worktree with
// uncommitted work (three of the seven did), and releasing it would put the item
// back in available/ looking untouched — the defect the preserved-worktree check
// exists to stop. The coordinator decides.

// categoryOrphanedClaim keys this check's cooldown and stamps its event.
const categoryOrphanedClaim = "orphaned_claim"

// orphanedClaimGrace is how long a claim must be continuously observed as
// orphaned before it is reported. It bounds the race with pogod's own release
// paths (the reaper, Registry.Stop), which run seconds after an exit, and with a
// polecat that is between its claim being stamped and its registry entry being
// read. It is far below the cost of the defect, which is measured in days.
const orphanedClaimGrace = 10 * time.Minute

// PIDAlive reports whether a pid names a running process.
type PIDAlive func(pid int) bool

// defaultPIDAlive is signal 0. EPERM means the process exists and belongs to
// someone else, which is alive.
func defaultPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// orphanKey identifies one orphaned-claim observation. The pid is part of it so
// that a re-stamp onto a new pid — a fresh claimant — starts a fresh grace.
func orphanKey(id string, pid int) string {
	return fmt.Sprintf("%s\x00%d", id, pid)
}

// checkOrphanedClaims reports claimed items held by a dead pid with no live
// worker on them.
func (w *Watcher) checkOrphanedClaims(now time.Time) {
	items, err := workitem.ListAllFrom(w.workRoot, "claimed")
	if err != nil {
		return
	}

	// Cheap filter first: only a dead claim pid makes an item a candidate, and
	// the in-flight probe is consulted only when there is one.
	var dead []workitem.WorkItem
	for _, it := range items {
		if it.ClaimPID <= 0 || w.isDispatchGated(it.Assignee) {
			continue
		}
		if w.pidAlive(it.ClaimPID) {
			continue
		}
		dead = append(dead, it)
	}

	// Not probeInFlight: that collapses "the probe could not answer" into an
	// empty snapshot, which is right for the available/ checks (they then fall
	// back to reporting) and is also what this check does — but here the notice
	// must SAY the second negative went unchecked, because it is half of what
	// the finding rests on.
	var flight WorkInFlight
	attributed := false
	if len(dead) > 0 && w.workers != nil {
		if f, known := w.workers.InFlight(); known {
			flight, attributed = f, true
		}
	}

	var orphaned []workitem.WorkItem
	seen := make(map[string]bool, len(dead))
	w.mu.Lock()
	for _, it := range dead {
		if _, worked := flight.worker(it.ID); worked {
			continue
		}
		key := orphanKey(it.ID, it.ClaimPID)
		seen[key] = true
		first, ok := w.orphanSince[key]
		if !ok {
			w.orphanSince[key] = now
			continue
		}
		if now.Sub(first) >= orphanedClaimGrace {
			orphaned = append(orphaned, it)
		}
	}
	for k := range w.orphanSince {
		if !seen[k] {
			delete(w.orphanSince, k)
		}
	}
	since := make(map[string]time.Time, len(orphaned))
	for _, it := range orphaned {
		since[it.ID] = w.orphanSince[orphanKey(it.ID, it.ClaimPID)]
	}
	w.mu.Unlock()

	sort.Slice(orphaned, func(i, j int) bool { return orphaned[i].ID < orphaned[j].ID })

	// selectDue is called even with no candidates: it prunes the backoff of
	// items that stopped being orphaned, so a later recurrence reads as new.
	due, sel := w.selectDue(categoryOrphanedClaim, orphaned, now, w.cfg.NudgeCooldown)
	if len(due) == 0 {
		return
	}

	ids := itemIDs(due)
	clauses := make([]string, 0, len(due))
	claims := make([]map[string]any, 0, len(due))
	for _, it := range due {
		clauses = append(clauses, fmt.Sprintf("%s (claim pid %d, dead)", it.ID, it.ClaimPID))
		d := map[string]any{"item_id": it.ID, "claim_pid": it.ClaimPID}
		if !since[it.ID].IsZero() {
			d["observed_seconds"] = now.Sub(since[it.ID]).Seconds()
		}
		if it.Repo != "" {
			d["repo"] = it.Repo
		}
		claims = append(claims, d)
	}

	msg := fmt.Sprintf(
		"stall-watch: %d work item(s) sit in claimed/ under a pid that is NOT ALIVE, and no live agent "+
			"is on them — %s. Nothing dispatches a claimed item and no other check reads claimed/, so "+
			"these are stuck until someone acts. The usual cause is a polecat killed by a pogod restart "+
			"(its PTY hangs up; no claim release runs) or a crash before the reaper saw it; a bare "+
			"`mg claim` run by hand also records a short-lived pid. Before releasing, check for work the "+
			"dead worker left: `pogo gc --list-preserved` (uncommitted edits in a retained worktree) and "+
			"the item's polecat branch on origin. Then `mg unclaim <id>` to return it to available/ — "+
			"or, if someone really is on it, re-stamp the claim onto them with `mg reclaim <id> --pid <their pid>`.",
		len(due), strings.Join(clauses, "; "))
	msg += sel.repeatNotice()
	if !attributed {
		msg += " (pogod could not say which agents are live, so only the dead claim pid was checked — " +
			"confirm with `pogo agent list` before releasing.)"
	} else if flight.Uncertain != "" {
		msg += " (Live-worker attribution may be incomplete — " + flight.Uncertain + ".)"
	}

	details := map[string]any{
		"category":      categoryOrphanedClaim,
		"watched_agent": w.cfg.Agent,
		"item_count":    len(due),
		"item_ids":      ids,
		"claims":        claims,
		"grace":         orphanedClaimGrace.String(),
	}
	details["workers_probed"] = attributed
	if flight.Uncertain != "" {
		details["attribution_uncertain"] = flight.Uncertain
	}
	sel.stampDetails(details)
	w.fire(categoryOrphanedClaim, Notice{
		Subject: subject(nItems(len(due))+" claimed by a DEAD pid", ids),
		Message: msg,
	}, details)
}
