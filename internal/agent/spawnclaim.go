package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/drellem2/pogo/internal/events"
)

// Work-item ownership at dispatch time (mg-7254).
//
// THE DEFECT. Claiming the work item was the polecat's own first step: every
// worker template opens with `mg claim {{.Id}}`. That made ownership contingent
// on the polecat executing a turn, and a polecat executes a turn only if the
// model API answers. On 2026-07-29 polecat 86e7 was `healthy` and active with a
// session title reading "Complete work item mg-86e7" while mg-86e7 sat in
// available/, and the polecat dispatched for THIS ticket reproduced it in the
// same hour: spawned ~20:43Z, it logged `synthetic_failure_detected` with
// failing_turns=2 against `API Error: 529 Overloaded` and executed no turn at
// all for the next 27 minutes. `mg claim` was never reached. Not skipped —
// unreachable.
//
// The consequence is not untidiness. An unclaimed-but-worked item is invisible
// to every ownership check the fleet has, all of which read item status:
//
//   - stall-watch scans available/ only (internal/stallwatch) and reported the
//     item as neglected — seven false wakes in one evening, which is how a reader
//     learns to dismiss the alarm;
//   - nothing structural prevented a second dispatch onto the same item. The
//     only guard was the dispatcher remembering to run `pogo agent list` first;
//   - `mg done` refuses an item that is not in claimed/ (macguffin
//     workitem.Done), so the failure surfaced at the very END, after all the work.
//
// THE FIX AND WHY IT IS HERE. pogod already knows the work-item id at spawn — it
// arrives as `--id` — so it can take the claim itself, before the process starts.
// That converts a convention someone must remember into a mechanism, the same
// move registerPolecatMailCheck made for the mail-check loop (mg-e633).
//
// The claim is taken BEFORE r.Spawn, not after, and the ordering is the whole
// point rather than an implementation detail. Claiming after the process is
// running would leave exactly the window this ticket is about — narrower, but the
// same shape — and a claim that then failed would leave a live agent believing it
// owns an item it does not, which the acceptance criteria rule out. Claiming
// first means a claim we cannot take is a spawn that never happens.
//
// This is the ownership half of drellem2/pogo#99. That issue is about the wake
// threshold being shorter than the claim latency; no threshold, however correct,
// helps while the signal it reads is wrong.

// ClaimOutcome labels what happened when pogod tried to claim a work item at
// dispatch. The values are the strings stamped into the emitted event, so they
// are readable straight out of events.log.
type ClaimOutcome string

const (
	// ClaimTaken means pogod now holds the claim: the item left available/
	// before the polecat's first turn. The dispatch proceeds.
	ClaimTaken ClaimOutcome = "claimed"
	// ClaimConflict means the item exists and is NOT available — already
	// claimed, or done. The dispatch is refused; see claimForSpawnRefusal.
	ClaimConflict ClaimOutcome = "conflict"
	// ClaimUnknown means the attempt could not establish anything: no such
	// item, no mg binary, unreadable store. The dispatch proceeds — see the
	// fail-open rationale on MGWorkItemClaimer.ClaimForSpawn.
	ClaimUnknown ClaimOutcome = "unknown"
	// ClaimSkipped means there was nothing to claim because the spawn carried
	// no --id. The dispatch proceeds; `--id` is optional by design (mg-2437).
	ClaimSkipped ClaimOutcome = "skipped"
	// ClaimAdopted means the item was in claimed/ under THIS pogod's own pid
	// with no dispatch in flight and no live agent on it — residue of an earlier
	// dispatch that died between taking the claim and producing a worker. The
	// claim file is already byte-for-byte what a fresh claim-at-spawn writes, so
	// this dispatch adopts it and proceeds. See spawnclaimadopt.go (mg-790f).
	ClaimAdopted ClaimOutcome = "adopted"
)

// ClaimVerdict is the result of one dispatch-time claim attempt.
type ClaimVerdict struct {
	// Outcome is the verdict.
	Outcome ClaimOutcome
	// Detail carries the underlying tool's own message verbatim when there is
	// one — mg already explains a conflict better than we can paraphrase it
	// ("already claimed (by PID 32194)"), and the caller puts it in front of an
	// agent that has to decide what to do next with no human in the loop.
	Detail string
	// HolderPID is the pid stamped on the conflicting claim, or 0 when there is
	// no claim file to read one off — which is not the same as "nobody holds
	// it": a conflict on a done, shelved, pending or archived item names no pid
	// either. Set on ClaimConflict only; the stranded-claim check in
	// spawnclaimadopt.go turns on it, so a claimer that cannot report it fails
	// closed and refuses, which is the safe direction (mg-790f).
	HolderPID int
}

// Held reports whether pogod holds the work item's claim as a result of this
// dispatch — taken fresh, or adopted from a dispatch that stranded it.
//
// Callers that must roll the claim back, or that treat the claim as pogod's
// rather than the polecat's, ask this rather than testing ClaimTaken. An adopted
// claim is pogod's on identical terms: it names pogod's pid, it must be released
// if this spawn also fails, and the polecat is expected to re-stamp it. Every
// site that compared against ClaimTaken alone would have quietly done the wrong
// thing for an adopted one.
func (v ClaimVerdict) Held() bool {
	return v.Outcome == ClaimTaken || v.Outcome == ClaimAdopted
}

// WorkItemClaimer takes a work item's claim on behalf of a polecat that is about
// to be spawned.
//
// An interface so the spawn handler is testable without a macguffin store,
// mirroring ClaimReleaser and DispatchGate. It deliberately has no Release
// method: releasing a claim is ClaimReleaser's job and it already exists, with
// the probe-first semantics a rollback needs. See Registry.releaseSpawnClaim for
// the one coupling that creates.
type WorkItemClaimer interface {
	// ClaimForSpawn attempts to claim workItemID. It never returns an error:
	// every failure is a ClaimOutcome the caller must route on, because the
	// three failure kinds want three different dispatch decisions and an
	// error return collapses them into one.
	ClaimForSpawn(workItemID string) ClaimVerdict
}

// MGWorkItemClaimer is the production WorkItemClaimer: it shells out to
// `mg claim`, whose rename(2) is the atomicity the duplicate-dispatch guard
// rests on. Two concurrent spawns of one item cannot both succeed — macguffin's
// workitem.Claim renames out of available/, so exactly one wins and the loser
// gets a conflict. That is a property of the store, not of our timing, which is
// why the guard is worth more than the `pogo agent list` check it replaces.
type MGWorkItemClaimer struct {
	// Root overrides the macguffin store location, passed to mg as --root.
	// Empty resolves via macguffinStoreRoot, which under a test binary is a
	// throwaway temp store rather than the live one.
	Root string
	// Bin is the mg binary. Empty means "mg" on PATH.
	Bin string
}

func (m MGWorkItemClaimer) bin() string {
	if m.Bin != "" {
		return m.Bin
	}
	return "mg"
}

// ClaimForSpawn implements WorkItemClaimer.
//
// THE FAILURE DIRECTION IS SPLIT, AND THE SPLIT IS THE DESIGN. Two of the three
// failure kinds fail open and one fails closed, which is not a hedge — the two
// directions answer different questions:
//
//   - CONFLICT fails CLOSED. The item was positively read and is positively not
//     available. We know a conflict exists, and dispatching anyway is precisely
//     the double-dispatch this ticket exists to prevent: two polecats on one
//     item means duplicated branches, a merge race, and one of them discovering
//     mid-flight that its work was redundant.
//   - NOT-FOUND and UNREADABLE fail OPEN. Here we know nothing. `--id` is
//     optional by design and callers legitimately dispatch against ids that are
//     not macguffin items at all, so failing closed would refuse working
//     dispatches and let one bad path in macguffin halt the whole fleet. This is
//     the same asymmetry MGDispatchGate.DispatchGated documents, for the same
//     reason, and the cost is the same: this guard stops a dispatch onto an item
//     someone else holds; it is not proof that no polecat can ever run
//     unclaimed.
//
// The discriminator is mg's frozen exit-code contract (internal/mgerr:
// 3 = not_found, 4 = conflict, additive-only, existing numbers never change), not
// error-text matching. A guard keyed on prose breaks the first time a message is
// reworded, and nothing would report that it had.
func (m MGWorkItemClaimer) ClaimForSpawn(workItemID string) ClaimVerdict {
	if workItemID == "" {
		return ClaimVerdict{Outcome: ClaimSkipped}
	}
	root := macguffinStoreRoot(m.Root)
	if root == "" {
		return ClaimVerdict{
			Outcome: ClaimUnknown,
			Detail:  "cannot resolve the macguffin store root (no MG_ROOT, no home directory)",
		}
	}

	verdict := m.attemptClaim(root, workItemID)

	// A conflict that names NO claim file is the one shape in which mg and this
	// dispatch can disagree about the same instant: macguffin's claim_race — the
	// rename out of available/ failed, the item is still available, and macguffin
	// flags the error retryable for exactly that reason. Refusing there would put
	// `mg show`'s "available" against a 409, which is the disagreement mg-790f
	// exists to end. One retry resolves it either way: the claim succeeds, or the
	// second attempt returns the honest already-claimed/already-done conflict with
	// a pid on it. The same retry runs for a done or shelved item, where it costs
	// one subprocess on a path that was refusing the dispatch anyway.
	if verdict.Outcome == ClaimConflict && verdict.HolderPID == 0 {
		verdict = m.attemptClaim(root, workItemID)
	}
	return verdict
}

// attemptClaim runs one `mg claim` and reads the holder pid off the store when
// the attempt conflicts.
func (m MGWorkItemClaimer) attemptClaim(root, workItemID string) ClaimVerdict {
	// --pid names pogod, not the short-lived `mg claim` subprocess mg would
	// record by default. The pid is informational to macguffin — it never tests it
	// for liveness and `mg unclaim` targets by id (see MGClaimReleaser) — but pogo
	// reads it in two places that depend on it being pogod's: the claim-pid
	// started-signal (claimrestamp.go) and the stranded-claim check
	// (spawnclaimadopt.go). pogod outlives the polecat and is the process that
	// releases the claim, so it is the honest owner of record. The polecat's own
	// pid is not an option: it does not exist yet, and waiting for it is what
	// reopens the window.
	out, err := exec.Command(m.bin(), "--root", root, "claim", workItemID,
		"--pid", strconv.Itoa(os.Getpid())).CombinedOutput()
	if err == nil {
		return ClaimVerdict{Outcome: ClaimTaken}
	}

	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = err.Error()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == mgExitConflict {
		// Read the holder off the claim FILE rather than out of mg's prose. The
		// message does carry a pid ("already claimed (by PID 4368)"), but a guard
		// keyed on prose breaks the first time the sentence is reworded and nothing
		// reports that it has — the same reason the conflict itself is keyed on the
		// frozen exit code. A pid we cannot read stays 0, and 0 refuses.
		pid, held, pidErr := claimPID(root, workItemID)
		if pidErr != nil || !held {
			pid = 0
		}
		return ClaimVerdict{Outcome: ClaimConflict, Detail: detail, HolderPID: pid}
	}
	return ClaimVerdict{Outcome: ClaimUnknown, Detail: detail}
}

// mgExitConflict is macguffin's frozen exit code for "the entity exists but is in
// the wrong state for the operation" — already claimed, already done, race-lost
// claim. See internal/mgerr in drellem2/macguffin, which freezes 0/1/2/3/4 and
// documents new categories as taking 5 and upward, so this number cannot move
// under us.
const mgExitConflict = 4

// SetWorkItemClaimer installs the claimer used to take a work item's claim at
// dispatch. Passing nil restores the default, which is MGWorkItemClaimer{} —
// functional, not a no-op, for the reason SetClaimReleaser's and
// SetDispatchGate's defaults are: a mechanism that engages only once someone
// remembers to wire it is absent in every deployment where they didn't, and this
// one replaces a convention that failed for exactly that reason.
func (r *Registry) SetWorkItemClaimer(c WorkItemClaimer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workItemClaimer = c
}

func (r *Registry) getWorkItemClaimer() WorkItemClaimer {
	r.mu.RLock()
	c := r.workItemClaimer
	r.mu.RUnlock()
	if c == nil {
		return MGWorkItemClaimer{}
	}
	return c
}

// claimForSpawn takes the work item's claim ahead of a spawn and reports the
// verdict plus a refusal message — "" when the dispatch may proceed.
//
// Every outcome is recorded. A taken claim emits work_item_claimed_at_spawn so
// the mechanism is observable rather than inferred, and a fail-open emits
// work_item_claim_at_spawn_failed so an item left in available/ under a running
// polecat has a cause attached to it. That second event is the mg-d22a lesson
// applied here: this ticket cost an evening precisely because the state carried
// no record of how it arose, and a fail-open that logs nothing recreates that.
func (r *Registry) claimForSpawn(spawnReq SpawnPolecatAPIRequest) (ClaimVerdict, string) {
	verdict := r.getWorkItemClaimer().ClaimForSpawn(spawnReq.Id)
	actor := "pogod"
	if spawnReq.Name != "" {
		actor = "cat-" + spawnReq.Name
	}

	switch verdict.Outcome {
	case ClaimSkipped:
		return verdict, ""

	case ClaimTaken:
		log.Printf("polecat %s: claimed work item %s at spawn; it is out of available/ before "+
			"the first turn (mg-7254)", spawnReq.Name, spawnReq.Id)
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_claimed_at_spawn",
			Agent:      actor,
			WorkItemID: spawnReq.Id,
			Repo:       spawnReq.Repo,
			Details: map[string]any{
				"agent_name": spawnReq.Name,
				"claim_pid":  os.Getpid(),
			},
		})
		r.beginSpawnClaim(spawnReq.Id, spawnReq.Name)
		return verdict, ""

	case ClaimConflict:
		holder := r.classifyClaimConflict(verdict, spawnReq.Id)
		if holder.stranded {
			return r.adoptStrandedSpawnClaim(verdict, spawnReq), ""
		}
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_claim_at_spawn_failed",
			Agent:      actor,
			WorkItemID: spawnReq.Id,
			Repo:       spawnReq.Repo,
			Details: map[string]any{
				"agent_name": spawnReq.Name,
				"outcome":    string(verdict.Outcome),
				"detail":     verdict.Detail,
				"holder":     holder.kind,
				"holder_pid": verdict.HolderPID,
				"dispatched": false,
			},
		})
		return verdict, fmt.Sprintf("work item %s could not be claimed for this dispatch: %s — "+
			"it is not available, so something already owns it.%s Check `pogo agent list` for a live "+
			"polecat on it before doing anything else; if the owner is gone, `mg unclaim %s` releases "+
			"the claim and the dispatch can be retried",
			spawnReq.Id, verdict.Detail, holder.sentence(), spawnReq.Id)

	default: // ClaimUnknown — loud, but the dispatch proceeds.
		// What this line used to say — "stall-watch will report it as neglected
		// while this polecat works it" — was true when it was written and is the
		// defect mg-1a8a fixed. stall-watch now asks the live agent registry
		// (Registry.WorkItemsInFlight) before calling an available item neglected,
		// and reports a worked-but-unclaimed one with the opposite remedy. The
		// prediction is kept as a CONDITIONAL rather than deleted, because it is
		// still what happens when the attribution cannot be made: a --no-worktree
		// dispatch whose polecat never reaches the registry, or a witness the
		// watcher cannot read.
		log.Printf("polecat %s: could not claim work item %s at spawn: %s — dispatching ANYWAY "+
			"(claim failures fail open, see MGWorkItemClaimer.ClaimForSpawn). If %s is a real item "+
			"it stays in available/ while this polecat works it; stall-watch consults the live agent "+
			"registry and will report it as worked-but-unclaimed rather than neglected (mg-1a8a), but "+
			"anything reading item status alone — including a human reading the board — still sees it "+
			"as work nobody started",
			spawnReq.Name, spawnReq.Id, verdict.Detail, spawnReq.Id)
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_claim_at_spawn_failed",
			Agent:      actor,
			WorkItemID: spawnReq.Id,
			Repo:       spawnReq.Repo,
			Details: map[string]any{
				"agent_name": spawnReq.Name,
				"outcome":    string(verdict.Outcome),
				"detail":     verdict.Detail,
				"dispatched": true,
			},
		})
		return verdict, ""
	}
}

// releaseSpawnClaim gives back a claim taken by claimForSpawn when the spawn it
// was taken for then failed. Without it a failed spawn strands the item in
// claimed/ under pogod's pid with no worker on it — dispatch will not offer it
// again and stall-watch does not look in claimed/, so it would be lost in the
// mirror image of the state this ticket is about.
//
// It routes through ClaimReleaser rather than re-implementing an unclaim, which
// couples the two: they must resolve the SAME macguffin store or a rollback
// silently releases nothing. In production both go through macguffinStoreRoot
// and agree by construction; that agreement is pinned by a test rather than
// left as a thing to remember, and a test registry that sets one must set both.
func (r *Registry) releaseSpawnClaim(verdict ClaimVerdict, spawnReq SpawnPolecatAPIRequest) {
	// Held rather than == ClaimTaken: an ADOPTED claim (mg-790f) is pogod's on
	// the same terms and strands identically if this spawn fails too. Leaving it
	// behind would turn one stranded claim into a permanent one, since the next
	// dispatch would find it in-flight-free and adopt it in turn, forever.
	if !verdict.Held() {
		return
	}
	released, err := r.getClaimReleaser().ReleaseClaim(spawnReq.Id)
	if err != nil {
		log.Printf("polecat %s: spawn failed AND the claim taken for it on work item %s could not "+
			"be released: %v — the item is stranded in claimed/ with no worker on it, where dispatch "+
			"will not see it and stall-watch does not look; release it by hand with `mg unclaim %s`",
			spawnReq.Name, spawnReq.Id, err, spawnReq.Id)
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_claim_release_failed",
			Agent:      "cat-" + spawnReq.Name,
			WorkItemID: spawnReq.Id,
			Repo:       spawnReq.Repo,
			Details: map[string]any{
				"reason": "spawn_failed_after_claim",
				"error":  err.Error(),
			},
		})
		return
	}
	if !released {
		return
	}
	log.Printf("polecat %s: spawn failed; released the claim taken for it on work item %s — "+
		"it is back in available/ (mg-7254)", spawnReq.Name, spawnReq.Id)
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_claim_released",
		Agent:      "cat-" + spawnReq.Name,
		WorkItemID: spawnReq.Id,
		Repo:       spawnReq.Repo,
		Details: map[string]any{
			"reason": "spawn_failed_after_claim",
		},
	})
}
