package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/gitgc"
)

// The live-owner gate: refuse a dispatch that would put a SECOND worker on a
// name or a work item a live polecat already holds (drellem2/pogo#167).
//
// # What it protects, and why that makes it different from every other gate
//
// The rest of the dispatch gates protect THROUGHPUT. They refuse work that is
// already done (merged), already written (stranded), not meant to run yet
// (assignee), or unaffordable right now (repo cap, load). Getting one of them
// wrong costs a re-dispatch. Every one of them is overridable, and has to be,
// because a gate that can be wrong with no way past it gets disarmed rather
// than fixed.
//
// This one protects a RUNNING WORKER'S TREE. A polecat's worktree holds
// uncommitted files that are on no branch, in no stash and on no remote — the
// normal mid-flight state of every worker — and the spawn path's own cleanup
// removes whatever is at the target path with --force. So the cost of a wrong
// dispatch here is not a wasted worker: it is the only copy of somebody's work,
// destroyed silently, while `pogo agent list` keeps reporting the victim
// healthy. That is why this refusal is NOT OVERRIDABLE. There is no reason to
// dispatch a second worker onto a live one that is better served by a flag than
// by `pogo agent stop`, and the operator reaching for an override is by
// construction overriding a check they believe is misfiring — which is exactly
// the belief that produced the incident this gate was written for.
//
// # Why it is a gate and not a repair of the destructor
//
// The destructor is repaired too (see cleanupFailedPolecatSpawn, which no
// longer force-removes a directory this spawn did not create). But repairing it
// alone leaves the ordering that made the incident legible as absurd: the two
// cleanup call sites reached AFTER a live owner has already been established —
// a claim conflict, and Registry.Spawn's "already running" — mean pogod refuses
// the dispatch and then destroys the running agent's worktree as the cleanup
// for the refusal it just issued. A guard placed with the other 409s, ahead of
// every side effect (the mg-ef80 rule), means those paths are not reached at
// all for a live owner: nothing is created, so nothing has to be rolled back.
//
// # The two halves
//
// A NAME collision and an ITEM collision are different failures and both are
// refused, because the destructive path can be reached through either. The name
// is what the worktree directory and the branch are made from, so a second
// polecat under a live one's name targets that polecat's tree by construction.
// The item is the coordination fact — a duplicate dispatch onto claimed work
// usually reuses neither the name nor the branch, but it is what puts two
// workers on one deliverable, and it is what the stall-watch/priority-wake
// nag actually produces (drellem2/pogo#99 is the upstream half of that).
//
// # Liveness is the UNION, not this process's registry
//
// Both halves read the registry unioned with the persisted polecat witness —
// LivePolecatSet and Registry.WorkItemsInFlight, the same two answers gitgc's
// sweep and stall-watch are already gated on. The union is the whole point: the
// in-memory registry is EMPTY after a pogod restart, permanently, because it has
// no adopt path (mg-13a3), and a polecat that outlived the pogod that spawned it
// is precisely the worker nobody remembers is running. Reading the registry
// alone would leave this gate blind to the population it exists for.

// liveOwnerRefusal returns the refusal for a dispatch a live polecat already
// owns — by name or by work item — or "" when no live owner can be found.
//
// THE FAILURE DIRECTION IS CLOSED, and unlike the surrounding gates that is not
// a judgement call. An unreadable witness store is not an empty fleet; it is the
// one record of a restart-surviving polecat, and gitgc already SKIPS ITS SWEEP
// rather than act against a live set it knows is missing survivors (mg-0130). A
// gate that dispatched over the same failure would be strictly less careful than
// the reaper it exists to cover for, which is the incoherence the preserved-tree
// gate refuses on for its own read failures. Positives found before the read
// error are still reported, so the refusal names an owner when one is known.
func (r *Registry) liveOwnerRefusal(name, workItemID string) string {
	name = strings.TrimSpace(name)
	workItemID = strings.TrimSpace(workItemID)

	// This process's own registry first. It is always readable, it is the
	// strongest evidence there is, and it is the case where the remedy can name
	// a worktree path rather than describe one.
	registryNames := make([]string, 0)
	for _, p := range r.Polecats() {
		registryNames = append(registryNames, p.Name)
		if name != "" && p.Name == name {
			return liveNameRefusal(name, p.WorktreeDir, InFlightFromRegistry)
		}
	}

	// The item half, unioned with the witness. WorkItemsInFlight returns a
	// PARTIAL map alongside a witness read error, so a positive hit is honoured
	// before the error is: naming the owner beats reporting that we could not
	// look for one.
	inFlight, itemErr := r.WorkItemsInFlight()
	if workItemID != "" {
		if owner, ok := inFlight[workItemID]; ok {
			return liveItemRefusal(workItemID, owner)
		}
	}

	// The name half, unioned with the witness — the restart-surviving polecat
	// this gate is mostly for.
	live, nameErr := LivePolecatSet(registryNames)
	if nameErr == nil && name != "" && live[name] {
		return liveNameRefusal(name, "", InFlightFromWitness)
	}

	if err := itemErr; err != nil {
		return liveOwnerUnreadableRefusal(name, workItemID, err)
	}
	if err := nameErr; err != nil {
		return liveOwnerUnreadableRefusal(name, workItemID, err)
	}
	return ""
}

// liveNameRefusal is the refusal for a dispatch that reuses a live polecat's
// name. It names the two artefacts the name resolves to, because that is the
// whole mechanism: the collision is not cosmetic, the spawn is aimed at the live
// worker's own directory.
//
// worktreeDir is the live polecat's registered tree when the registry knows it,
// and "" for a witness-only survivor, whose tree we can locate but not confirm.
// The message says which it is rather than asserting a path it did not read —
// in the PROSE. The command it hands out takes the bare path either way: a
// `git -C` whose argument carries an explanatory parenthetical is not a command
// (round 1 of PR #172 shipped exactly that).
func liveNameRefusal(name, worktreeDir, evidence string) string {
	tree := worktreeDir
	where := worktreeDir
	if tree == "" {
		tree = polecatWorktreePath(name)
		where = "its worktree"
		if tree != "" {
			where = tree + " (the path this dispatch would have targeted)"
		}
	}
	var exits string
	if evidence == InFlightFromWitness {
		exits = survivorExits(name, tree)
	} else {
		read := "`pogo agent list` for the worker"
		if tree != "" {
			read += fmt.Sprintf(", `git -C %s status` for what is in it that exists nowhere else", tree)
		}
		exits = fmt.Sprintf("Read the tree first (%s); if that worker really is finished, "+
			"`pogo agent stop %s` — which preserves a dirty tree rather than reaping it — and then "+
			"re-dispatch.", read, name)
	}
	return fmt.Sprintf(
		"polecat %q is ALREADY RUNNING (evidence: %s), and this dispatch would give a second worker "+
			"the same name. The name is not a label: the worktree directory and the branch are made "+
			"from it, so this spawn targets %s and the branch %s%s — the live worker's own. Nothing "+
			"was dispatched. %s %s",
		name, evidence, where, gitgc.BranchPrefix, name, liveOwnerNotOverridable, exits)
}

// liveItemRefusal is the refusal for a dispatch onto a work item a live polecat
// is already on. It sends the reader to the claim as well as to `pogo agent
// list`, because those answer different halves: the claim says the item is
// spoken for, the agent list says who is standing in the tree.
//
// IT NAMES BOTH EXITS, and it has to. This refusal cannot be overridden, so
// every legitimate way past it must be in the message or the message is a
// dead end. They are not interchangeable and the order is the point: stop the
// worker FIRST, because `mg unclaim` on an item a live polecat is working
// strands that worker on an item it no longer owns — the mirror image of the
// state claim-at-spawn was built to prevent (mg-7254). Stopping normally
// releases the claim on its own (mg-fb13); unclaiming is for the case where it
// did not.
//
// A WITNESS-evidence owner gets different exits (survivorExits), because the
// registry-evidence ones do not work on it: it is absent from `pogo agent list`
// and `pogo agent stop` answers 404 for it. Nothing releases its claim when it
// is killed, so there the unclaim is part of the kill line rather than a
// fallback.
func liveItemRefusal(workItemID string, owner InFlightWorkItem) string {
	head := fmt.Sprintf(
		"work item %s is ALREADY BEING WORKED by live polecat %q (evidence: %s), so this is a "+
			"DUPLICATE dispatch. Two workers on one item is what drellem2/pogo#167 is about: the "+
			"second spawn's cleanup path force-removes a worktree, and the tree it reaches is the "+
			"first worker's, holding uncommitted files that are on no branch, in no stash and on no "+
			"remote. Nothing was dispatched. %s ",
		workItemID, owner.Polecat, owner.Evidence, liveOwnerNotOverridable)
	if owner.Evidence == InFlightFromWitness {
		return head + fmt.Sprintf("`mg show %s` names the pid holding the claim. ", workItemID) +
			survivorExits(owner.Polecat, polecatWorktreePath(owner.Polecat))
	}
	return head + fmt.Sprintf(
		"Read it first: `pogo agent list` names the live "+
			"worker and `mg show %s` names the pid holding the claim. If that worker really is "+
			"finished, `pogo agent stop %s` — which preserves a dirty tree rather than reaping it — "+
			"and re-dispatch; stopping normally hands the claim back by itself, and only if the item "+
			"is still claimed once NOTHING is live on it does it need `mg unclaim %s`. Do not run "+
			"that first: unclaiming an item its worker is still on strands the worker, which is the "+
			"same loss in the other direction.",
		workItemID, owner.Polecat, workItemID)
}

// survivorExits is the way past this gate for a live owner known ONLY from the
// witness — a polecat that outlived the pogod that spawned it.
//
// WHY IT IS NOT `pogo agent stop` (round 1 of PR #172). The registry has no
// adopt path (mg-13a3), so this pogod holds no handle on a survivor: `pogo agent
// list` does not show it and `pogo agent stop <name>` answers 404
// (Registry.StopWithCause). A non-overridable refusal whose only exits fail is a
// dead end, and a dead end on a gate with no flag is how gates get disarmed. So
// the message names what DOES work for a process this pogod did not spawn: its
// pid, `pogo agent witness` (the same (pid, start_time) probe the gate itself
// used, over a process boundary), and a kill gated on that probe — the orphan
// alert's line (mailOrphanAlert, mg-da48), for the orphan alert's reason: the
// refusal is read later than it is written, and a bare `kill <pid>` aimed by a
// decayed record lands on whatever recycled the pid.
//
// Every command here is paste-runnable. An Unreadable verdict is not listed by
// `pogo agent witness` (it lists only CONFIRMED survivors), so gating the kill on
// it would be a line that silently never fires; that case names the pid and a
// `ps` that lets a human do the identity check the kernel would not.
func survivorExits(name, tree string) string {
	// The two commands that do NOT work are named without backticks on purpose:
	// every backticked span in this gate's refusals is an offered command, and
	// the test holds it to being runnable.
	notHere := "THIS WORKER IS NOT IN pogo agent list, AND pogo agent stop WILL ANSWER 404 FOR IT: it " +
		"outlived the pogod that spawned it, so this pogod has no handle on it and cannot stop it."
	read := ""
	if tree != "" {
		read = fmt.Sprintf(" Read its tree first — `git -C %s status` — for what is in it that exists "+
			"nowhere else.", tree)
	}
	rec, verdict, ok := witnessedPolecat(name)
	if !ok || rec.PID <= 0 {
		return notHere + " `pogo agent witness` lists its pid." + read + " Re-dispatch only once " +
			"`pogo agent witness` no longer lists it."
	}
	unclaim := ""
	claimNote := ""
	if item := strings.TrimSpace(rec.WorkItemID); item != "" {
		unclaim = " && mg unclaim " + item
		claimNote = fmt.Sprintf(" The unclaim is in the line because nothing hands %s back when a "+
			"survivor dies: the pogod that would have is gone.", item)
	}
	after := " The worktree stays on disk — a kill removes no files — so re-dispatch into it, or " +
		"recover what it holds, afterwards."
	if verdict == WitnessUnreadable {
		return notHere + fmt.Sprintf(" It holds pid %d, which answers signals but whose start time "+
			"could not be read, so pogod cannot confirm that pid is still this polecat — and "+
			"`pogo agent witness`, which lists only CONFIRMED survivors, will not show it.%s Confirm "+
			"it by hand before killing anything: `ps -o lstart=,command= -p %d`. Only if that is "+
			"the polecat and it really is finished: `kill %d%s`.%s%s",
			rec.PID, read, rec.PID, rec.PID, unclaim, claimNote, after)
	}
	return notHere + fmt.Sprintf(" It is pid %d, and `pogo agent witness` lists it.%s If that worker "+
		"really is finished, end it with this line, which re-confirms identity (pid AND start time) "+
		"before it kills, so a pid recycled since this refusal was written cannot be hit: "+
		"`pogo agent witness --json | grep -q '%s' && kill %d%s`.%s%s",
		rec.PID, read, WitnessAliveGrep(name, rec.PID), rec.PID, unclaim, claimNote, after)
}

// witnessedPolecat returns name's witness record and its verdict right now, or
// ok=false when the store has none (or cannot be read — the gate has already
// reported a read failure by the time a refusal is being worded, and the
// fallback wording still names a runnable way to find the pid).
func witnessedPolecat(name string) (witnessRecord, WitnessVerdict, bool) {
	witnessMu.Lock()
	recs, err := loadPolecatWitness()
	witnessMu.Unlock()
	if err != nil {
		return witnessRecord{}, WitnessNoRecord, false
	}
	for _, r := range recs {
		if r.Name == name {
			return r, witnessVerdict(r), true
		}
	}
	return witnessRecord{}, WitnessNoRecord, false
}

// liveOwnerUnreadableRefusal is the refusal for a gate that could not establish
// an answer. It is a DIFFERENT message from the two above on purpose: those name
// an owner, this one names an instrument, and a reader who cannot tell them
// apart will go looking for a polecat that was never asserted to exist.
func liveOwnerUnreadableRefusal(name, workItemID string, err error) string {
	subject := "this dispatch"
	switch {
	case workItemID != "" && name != "":
		subject = fmt.Sprintf("work item %s or the name %q", workItemID, name)
	case workItemID != "":
		subject = "work item " + workItemID
	case name != "":
		subject = fmt.Sprintf("the name %q", name)
	}
	return fmt.Sprintf(
		"cannot establish whether a live polecat already owns %s: %v. Nothing was dispatched. "+
			"This gate refuses rather than dispatching blind, for the reason gitgc SKIPS ITS SWEEP "+
			"on the identical failure (mg-0130): the witness store is the only record of a polecat "+
			"that outlived the pogod that spawned it, and an unreadable store is not an empty "+
			"fleet — reading it as one is exactly how a second worker lands on a live tree. %s "+
			"Repair or remove %s, then re-dispatch. `pogo agent list` still answers for what THIS "+
			"pogod spawned; it cannot see survivors of an earlier one, which is the population "+
			"the store was unreadable about.",
		subject, err, liveOwnerNotOverridable, WitnessPath())
}

// liveOwnerNotOverridable is the sentence every refusal from this gate carries,
// stated once so the three cannot drift apart. It says WHY there is no flag,
// not merely that there is none: a reader who is only told "no override" reads
// it as an omission and goes looking for the flag, and the shape of this fleet's
// other four gates makes that a reasonable thing to expect.
const liveOwnerNotOverridable = "THERE IS NO OVERRIDE FOR THIS REFUSAL, and that is deliberate: " +
	"every other dispatch gate here protects throughput, so overriding one costs at worst a wasted " +
	"worker, while this one protects work that may exist in no other copy on this machine. Stop the " +
	"live worker or dispatch a different name — do not look for a flag."

// polecatWorktreePath is where a polecat of this name has (or would have) its
// worktree. Best-effort: it returns "" when the polecats directory cannot be
// resolved, and the callers degrade to describing the path rather than naming
// it — a refusal that invents a path is worse than one that does not have it.
func polecatWorktreePath(name string) string {
	if name == "" {
		return ""
	}
	dir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, name)
}
