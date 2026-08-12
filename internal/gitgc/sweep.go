package gitgc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options configures a single GC sweep.
type Options struct {
	// Repo is the git repository to sweep (the source repo whose .git
	// holds the polecat branches and worktree registrations).
	Repo string
	// TargetBranch is the merge target a *done* ticket's branch must be
	// merged into before deletion. Empty defaults to DefaultTargetBranch.
	TargetBranch string
	// LivePolecats is the do-not-touch set, keyed by polecat NAME. pogod
	// fills it from its agent registry unioned with the restart-surviving
	// polecat witness (mg-0130), so a sweep never disturbs a running polecat
	// — including one that outlived the pogod that spawned it, and one whose
	// worktree was unlinked at refinery submit time and so is no longer
	// git-checked-out.
	//
	// A polecat's name reaches this set from TWO independent directions, and
	// conflating them is what caused gh #94:
	//
	//   - a worktree's PATH basename — whose tree this is. The only sound key
	//     for "may I delete this directory" (see PolecatNameForWorktree).
	//   - a branch's "polecat-" suffix — whose work this is. The sound key
	//     for "may I delete this ref", used in phase 2.
	//
	// They agree only while a polecat stays on its own branch. Phase 1 must
	// use the first and phase 2 the second; neither substitutes for the other.
	//
	// TICKET STATE now follows the same division (mg-bdda): a directory is
	// classified by its owner (classifyTree), a ref by its own name. The
	// worktree phases had disagreed about which — see classifyTree.
	LivePolecats map[string]bool
	// Tickets, when non-nil, supplies work-item states directly. When nil,
	// Sweep loads them via LoadTicketIndex (`mg list`). Injecting a map
	// keeps Sweep unit-testable without the mg binary.
	Tickets TicketIndex
	// PolecatsDir, when set, is additionally scanned for orphan polecat
	// directories — dirs whose git worktree registration is gone but whose
	// files were never deleted, e.g. because pogod died before the polecat's
	// exit cleanup ran (gh #31). Such dirs are invisible to `git worktree
	// list`, so the worktree scan alone can never reclaim them. Empty means
	// skip the scan.
	//
	// New orphans of this shape stopped accruing when the submit-time unlink
	// was deleted (gh #88) — that hook was what stripped the registration
	// from a live polecat in the first place. The scan stays for the legacy
	// dirs it left behind, and for the pogod-died-mid-polecat case.
	PolecatsDir string
	// DryRun reports what would be done without deleting anything.
	DryRun bool
	// Force reclaims worktrees holding uncommitted work. Off by default: a
	// dirty worktree is KEPT and reported, because a concluded ticket is not
	// proof that everything in the tree made it into the merge (mg-ee02).
	// This is the operator's deliberate escape hatch — without it, a
	// preserved tree pins its branch indefinitely.
	Force bool
	// Logf, when set, receives a line per action for progress logging.
	Logf func(format string, args ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// removeWorktreeFn is the worktree-removal EXECUTOR, indirected so a test can
// force the FAILED-removal branch. A failed removal is otherwise unreachable
// from a test — it needs git and os.RemoveAll to both lose to the filesystem —
// and it is the branch the freed-flag ordering turns on (mg-dd92). Production
// always uses RemoveWorktreeForce.
//
// It sits UNDER RemoveWorktree's guard rather than beside it (gh #97): the
// sweep reaches it through RemoveWorktree on the normal path and directly only
// for opts.Force, so overriding this var still covers both. Moving the seam
// above the guard would silently un-test the branch mg-dd92 added.
var removeWorktreeFn = RemoveWorktreeForce

// BranchAction records the GC decision for one polecat branch.
type BranchAction struct {
	Branch string
	ID     string // resolved work-item ID; "" when unknown
	State  TicketState
	Reason string
	// Durability is what was established about WHERE THE COMMITS ARE, as
	// opposed to what the ticket says about the work (mg-0a43). It stays
	// DurabilityNotChecked on every path that never asked — a live polecat, an
	// in-flight ticket, a branch still checked out — because "not asked" and
	// "asked and could not tell" are different facts and only the second one
	// belongs in the at-risk report.
	Durability DurabilityVerdict
}

// AtRisk reports whether this branch was kept because deleting it might have
// lost commits. It is what separates the keeps an operator must act on from the
// routine ones, and it is a method rather than an inlined comparison because
// two callers ask (Summary here, and the sweep's own log line).
func (a BranchAction) AtRisk() bool { return a.Durability.AtRisk() }

// WorktreeAction records the GC decision for one polecat worktree.
type WorktreeAction struct {
	Path string
	// Owner is the polecat the worktree BELONGS to, derived from Path (see
	// PolecatNameForWorktree). It is what the liveness gate is keyed on, and
	// it is recorded separately from Branch because the two differ in exactly
	// the case gh #94 was about — reading a removal line without it cannot
	// tell you whose tree was taken.
	Owner  string
	Branch string
	Reason string
}

// String renders one worktree action for an operator: whose tree it was, what
// was checked out in it, and why the sweep decided what it decided. It is the
// unit of a GC log line, and it is exported because the caller that writes
// those lines (cmd/pogod) is what has to be tested for actually carrying them.
func (a WorktreeAction) String() string {
	owner := a.Owner
	if owner == "" {
		owner = "unknown"
	}
	branch := a.Branch
	if branch == "" {
		branch = "none"
	}
	return fmt.Sprintf("%s (owner %s, branch %s): %s", a.Path, owner, branch, a.Reason)
}

// Result is the outcome of a sweep.
type Result struct {
	Repo             string
	DryRun           bool
	BranchesDeleted  []BranchAction
	BranchesKept     []BranchAction
	WorktreesRemoved []WorktreeAction
	WorktreesKept    []WorktreeAction
	PruneOutput      string
	Errors           []string
}

// Sweep runs one GC pass over opts.Repo:
//
//  1. Remove worktrees of concluded, non-live polecats, then `git worktree
//     prune` any registration whose directory has vanished.
//  2. Delete `polecat-*` branches whose ticket is concluded AND whose commits
//     survive the deletion — done only once merged into the target branch,
//     archived only once some origin ref holds the head or its patches landed
//     (mg-0a43) — skipping any branch that is live or still checked out.
//
// Worktrees are handled before branches so that removing a worktree frees
// its branch for deletion in the same pass. Sweep is conservative: an
// unresolvable ticket, an in-flight ticket, or a live polecat is always
// kept. Errors on individual items are collected into Result.Errors and do
// not abort the sweep.
func Sweep(opts Options) (Result, error) {
	if opts.TargetBranch == "" {
		opts.TargetBranch = DefaultTargetBranch
	}
	tickets := opts.Tickets
	if tickets == nil {
		loaded, err := LoadTicketIndex()
		if err != nil {
			return Result{}, fmt.Errorf("load ticket index: %w", err)
		}
		tickets = loaded
	}

	res := Result{Repo: opts.Repo, DryRun: opts.DryRun}

	// --- Phase 1: worktrees ---------------------------------------------
	worktrees, err := ListWorktrees(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list worktrees: %w", err)
	}

	// Branches whose worktree was (or would be) removed this pass — they
	// become un-checked-out and thus deletable in phase 2.
	freed := map[string]bool{}

	for _, wt := range worktrees {
		if wt.Main || wt.Bare || !wt.IsPolecat() {
			continue
		}
		// WHOSE tree this is, not what is checked out in it. These differ
		// whenever a polecat works a foreign branch, and reading liveness off
		// the branch is what deleted a live polecat's tree in gh #94.
		owner := PolecatNameForWorktree(wt.Path)
		if opts.LivePolecats[owner] {
			kept := WorktreeAction{
				Path: wt.Path, Owner: owner, Branch: wt.Branch, Reason: "live polecat " + owner,
			}
			res.WorktreesKept = append(res.WorktreesKept, kept)
			continue
		}
		// Ticket state is keyed on the OWNER, like liveness and like phase 1b
		// (mg-bdda). The branch answers only when the owner cannot.
		state, why := classifyTree(tickets, owner, wt.Branch)
		if !state.Concluded() {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: wt.Path, Owner: owner, Branch: wt.Branch, Reason: why,
			})
			continue
		}
		// A concluded ticket says the WORK was accepted; it does not say the
		// tree is empty — and it says nothing whatever when `git status` could
		// not be READ. Both are RemoveWorktree's guard to decide (mg-ee02,
		// mg-4d45); phase 1 used to run its own dirty check and then call
		// RemoveWorktreeForce around the guard, so a status error was treated
		// identically to a clean tree and the files went (gh #97).
		//
		// The decision is taken here, before the dry-run branch, so a dry run
		// reports exactly what an apply would do — and so the log line can name
		// the status failure rather than the ticket state. It is the guard's
		// own function, so the two cannot drift.
		chk := checkWorktreeRemoval(wt.Path)
		if chk.Refusal != nil && !opts.Force {
			kept := WorktreeAction{Path: wt.Path, Owner: owner, Branch: wt.Branch}
			// A refusal with no arm in refusalReason cannot happen today, but a
			// silently EMPTY reason on a preserved tree is the one failure mode
			// this whole ticket is about, so fall back to the error itself
			// rather than to nothing.
			var named bool
			if kept.Reason, named = refusalReason(chk.Refusal); !named {
				kept.Reason = chk.Refusal.Error()
			}
			res.WorktreesKept = append(res.WorktreesKept, kept)
			opts.logf("kept worktree %s", kept.String())
			continue
		}
		action := WorktreeAction{
			Path: wt.Path, Owner: owner, Branch: wt.Branch,
			Reason: removalReason(why, chk.StatusErr, opts.Force),
		}
		if opts.DryRun {
			// A dry run reports what an apply WOULD do, so the branch is
			// treated as freed for phase 2's benefit — no removal is attempted
			// and none can fail.
			freed[wt.Branch] = true
			opts.logf("would remove worktree %s", action.String())
		} else {
			// --force goes straight to the executor: an operator's explicit
			// --force is a positive reason to discard, and that must not
			// change. Everything else goes THROUGH the guard, which re-runs
			// its check immediately before destroying anything.
			var rerr error
			if opts.Force {
				rerr = removeWorktreeFn(opts.Repo, wt.Path)
			} else {
				rerr = RemoveWorktree(opts.Repo, wt.Path, OwnerUnproven)
			}
			if rerr != nil {
				// A refusal here rather than a failure means the tree changed
				// between the check above and the removal — someone wrote to
				// it, or git broke — and the guard said no on the second look.
				// That is a keep, not an error, and reporting it as an error
				// would put a preserved tree in the wrong half of the report.
				if reason, refused := refusalReason(rerr); refused {
					kept := WorktreeAction{Path: wt.Path, Owner: owner, Branch: wt.Branch, Reason: reason}
					res.WorktreesKept = append(res.WorktreesKept, kept)
					opts.logf("kept worktree %s", kept.String())
					continue
				}
				res.Errors = append(res.Errors, fmt.Sprintf("remove worktree %s: %v", wt.Path, rerr))
				continue
			}
			// Marked freed only AFTER the removal actually succeeded. Setting
			// it up front survived a failed removal and told phase 2 the
			// branch was no longer checked out when it still was, so the
			// sweep went on to attempt a deletion git was always going to
			// refuse — reporting an error instead of the correct "kept:
			// checked out in a worktree" (mg-dd92; code-read, never
			// reproduced in the wild).
			freed[wt.Branch] = true
			opts.logf("removed worktree %s", action.String())
		}
		res.WorktreesRemoved = append(res.WorktreesRemoved, action)
	}

	// --- Phase 1b: orphan polecat dirs ------------------------------------
	// This shape of orphan dates from when a polecat's worktree was unlinked
	// from git at refinery-submit time (its .git pointer removed, its
	// registration pruned) so the branch stopped being "checked out" while the
	// polecat kept polling; that submit-time unlink was deleted (gh #88), so
	// new orphans of this shape no longer accrue. Normally the polecat's exit
	// cleanup RemoveAll's the directory, but if pogod dies first the dir
	// survives with no .git and no registration — orphaned files that the
	// worktree scan above can never see (gh #31). Scan
	// PolecatsDir for such dirs and rm -rf the concluded ones; there is no
	// registration left for `git worktree remove --force` to act on, so
	// RemoveAll is the whole job.
	if opts.PolecatsDir != "" {
		registered := map[string]bool{}
		for _, wt := range worktrees {
			registered[wt.Path] = true
		}
		sweepOrphanDirs(opts, tickets, registered, &res)
	}

	// Drop registrations whose directory is already gone.
	if pruneOut, err := PruneWorktrees(opts.Repo, opts.DryRun); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("worktree prune: %v", err))
	} else {
		res.PruneOutput = pruneOut
	}

	// --- Phase 2: branches ----------------------------------------------
	branches, err := ListPolecatBranches(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list branches: %w", err)
	}
	checkedOut, err := CheckedOutBranches(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list checked-out branches: %w", err)
	}

	for _, br := range branches {
		name := BranchSuffix(br)
		id, state := tickets.BranchState(br)
		action := BranchAction{Branch: br, ID: id, State: state}

		if opts.LivePolecats[name] {
			action.Reason = "live polecat"
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		// A branch checked out in a worktree we did not remove cannot be
		// deleted — git refuses, and we should not want to.
		if checkedOut[br] && !freed[br] {
			action.Reason = "checked out in a worktree"
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		if !state.Concluded() {
			action.Reason = "ticket " + state.String()
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		// THIS IS THE ONLY STEP IN THE WHOLE POLECAT LIFECYCLE THAT DESTROYS
		// COMMITS. The worktree reap above does not — `git worktree remove`
		// drops the tree, the local branch keeps the objects, and the commits
		// stay reachable. The deploy drain does not. The refinery's branch-reap
		// runs only after a successful merge. This does.
		//
		// So both arms below have to be facts about the BRANCH. The archived arm
		// used to be an inference about the TICKET — `action.Reason = "ticket
		// archived"`, with no check of any kind, on the reasoning that "the work
		// has concluded" (mg-0a43). Ticket and branch are different objects, and
		// the case where they diverge is routine rather than exotic: a polecat
		// stops holding committed-but-unpushed work, the drain reports DEPARTED
		// UNSATISFIED and proceeds (correctly — waiting protects nothing once the
		// holder is dead, mg-797d), someone archives the item, and the commits
		// are gone. Every other guard in this chain is conservative; this one was
		// not, and it is the last one.
		if state == TicketDone {
			// UNCHANGED, and deliberately so. Ancestry is the wrong instrument
			// for the destructive question — see BranchDurable — but here it
			// fails CLOSED: it keeps branches that landed via rebase, which is a
			// leak rather than a loss, and mg-0a43 is scoped to the arm that
			// loses. Widening this one to BranchDurable would collect that leak
			// and is a separate, non-urgent change.
			merged, err := BranchMerged(opts.Repo, br, opts.TargetBranch)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("merge check %s: %v", br, err))
				action.Reason = "merge check failed"
				res.BranchesKept = append(res.BranchesKept, action)
				continue
			}
			if !merged {
				action.Reason = "ticket done but branch not merged into " + opts.TargetBranch
				res.BranchesKept = append(res.BranchesKept, action)
				continue
			}
			action.Reason = "ticket done, merged"
		} else {
			// An archived ticket licenses a WEAKER durability bar than a done
			// one — any origin ref holding the head clears it, not just the
			// integration branch — but it does not license the absence of one.
			verdict, detail := BranchDurable(opts.Repo, br, opts.TargetBranch)
			action.Durability = verdict
			if verdict.AtRisk() {
				// Kept, and SAID. A branch holding the only copy of some commits
				// is exactly the shape the dirty-worktree guard preserves one
				// phase up (mg-ee02), and that guard logs its keeps; a silent
				// one here would pin a branch nobody can find.
				action.Reason = fmt.Sprintf("ticket archived, but %s", detail)
				res.BranchesKept = append(res.BranchesKept, action)
				opts.logf("kept branch %s (%s)", br, action.Reason)
				continue
			}
			// The holder is named in the reason rather than summarised, so a
			// deletion can be audited after the fact rather than trusted.
			action.Reason = "ticket archived; " + detail
		}

		if opts.DryRun {
			opts.logf("would delete branch %s (%s)", br, action.Reason)
		} else {
			if err := DeleteBranch(opts.Repo, br); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("delete branch %s: %v", br, err))
				continue
			}
			opts.logf("deleted branch %s (%s)", br, action.Reason)
		}
		res.BranchesDeleted = append(res.BranchesDeleted, action)
	}

	return res, nil
}

// refusalReason renders a removal refusal as the reason line an operator reads
// out of the GC report, and reports whether err WAS a refusal at all. A false
// second return means a genuine failure, which belongs in Result.Errors — the
// two must not be conflated, because one preserved a tree on purpose and the
// other failed to remove one.
//
// Each arm names a DIFFERENT fact, because they are different facts and the
// operator's next move differs: "there are N uncommitted files" sends them to
// look at the files; "git status could not be read" sends them to look at the
// worktree's .git. A single shared wording would send them to the wrong place.
func refusalReason(err error) (string, bool) {
	var dwe *DirtyWorktreeError
	if errors.As(err, &dwe) {
		return fmt.Sprintf("%d uncommitted change(s) — rerun with --force to discard", dwe.Total), true
	}
	var uwe *UndeterminedWorktreeError
	if errors.As(err, &uwe) {
		// The age is REPORTED and decides nothing — this keep is permanent and
		// unconditional, so the line has to carry whatever a human needs to
		// clear the pin in one read, because nothing else ever will.
		return fmt.Sprintf("git status could not be read (%v)%s — refusing to act on a tree we could "+
			"not read; rerun with --force to discard", uwe.Err,
			untouchedClause(uwe.Untouched, uwe.UntouchedKnown)), true
	}
	return "", false
}

// removalReason renders the reason line for a worktree the sweep DID remove.
//
// When the removal happened despite a `git status` that failed, the status
// failure is named alongside the ticket state — because naming the ticket state
// alone is a false report of why the files went. That is the second half of
// gh #97: the log line shipped as gh #94's remedy read "removed worktree <path>
// (owner damaged, branch polecat-damaged): ticket archived" on exactly the path
// where git had errored, and the discarded error appeared nowhere. A forensic
// log that names an innocent cause is worse than silence — a missing line
// prompts investigation, a plausible one ends it.
//
// Since cannot-tell became an unconditional refusal, exactly one route reaches
// here with a failed status: the operator's --force. That is the whole reason
// the force flag is a parameter rather than inferred — the line has to say the
// removal happened because a human overrode a refusal, not because anything was
// established about the tree.
func removalReason(why string, statusErr error, force bool) string {
	if statusErr == nil {
		return why
	}
	if !force {
		// Unreachable: without --force a failed status always refuses above.
		// Report it rather than printing the ticket state alone, because if
		// this ever does fire, the ticket state is exactly the innocent reason
		// gh #97 was about.
		return fmt.Sprintf("%s; git status could not be read (%v) — removed WITHOUT --force, "+
			"which should not be reachable; treat this line as a bug report", why, statusErr)
	}
	return fmt.Sprintf("%s; git status could not be read (%v) — removed anyway because --force was given",
		why, statusErr)
}

// classifyTree decides the ticket state governing a worktree DIRECTORY, and
// returns it with the reason line an operator reads out of the GC log.
//
// # Why the owner and not the branch (mg-bdda)
//
// mg-dd92 moved worktree LIVENESS to the owner — the path basename — but left
// ticket-state classification split: phase 1 read it off the CHECKED-OUT
// BRANCH while phase 1b, which has no branch to read, read it off the
// directory name. For one dead owner and one directory name the two reached
// opposite conclusions, decided purely by whether git still held the
// registration:
//
//	registered worktree, owner 0047 (archived), parked on foreign in-flight
//	polecat-a773  -> KEPT  ("ticket in-flight")
//	orphan dir,     owner 0047, registration gone
//	              -> REMOVED ("orphan dir, ticket archived")
//
// The sets are disjoint within a sweep so nothing self-contradicted, but the
// consequence was real: a dead polecat's tree could be pinned forever by a
// foreign ticket that never concludes, and `git worktree prune` flipping one
// registration flipped the verdict.
//
// The owner is the sound key because it is the one the DIRECTORY is a fact
// about — the tree was created for that polecat's work and nothing else will
// ever come back to it. The branch is a fact about the REF, and the ref has
// its own gate: phase 2 classifies branches by BranchState, so removing a tree
// parked on an unconcluded foreign branch un-checks-out that branch without
// deleting it. Commits stay reachable, and uncommitted files are held back
// separately by the dirty guard (mg-ee02). Nothing is lost by reaping the
// directory.
//
// # The branch is the fallback, not a second gate
//
// Keying on the owner alone would strand every worktree whose basename
// resolves to no work item — legacy layouts, hand-made worktrees — which is
// the symmetric defect the whole of gh #94 was warned against: never reaping a
// dead tree. So an unresolvable owner defers to the branch rather than
// silently becoming "unknown, keep forever". That is the ONLY thing the branch
// decides here. It is deliberately not an additional must-also-be-concluded
// condition: that direction would preserve the indefinite pin this exists to
// remove.
func classifyTree(tickets TicketIndex, owner, branch string) (TicketState, string) {
	if id, state := tickets.OwnerState(owner); id != "" {
		return state, "owner's ticket " + state.String()
	}
	// The owner resolves to no work item at all. Say so, and let the branch
	// answer — an explicit fallback, not an accidental "unknown".
	_, state := tickets.BranchState(branch)
	return state, fmt.Sprintf("branch's ticket %s (owner %q resolves to no work item)", state, owner)
}

// sweepOrphanDirs scans opts.PolecatsDir for orphaned polecat directories
// and removes the eligible ones (see the phase 1b comment in Sweep).
// Eligibility mirrors the worktree phase: never a live polecat, never an
// in-flight or unclassifiable ticket. Removal is scoped strictly to
// directories directly under PolecatsDir whose basename resolves to a
// concluded work item; anything with a .git entry is still a linked
// worktree and is left to the registered-worktree scan of its owning repo.
func sweepOrphanDirs(opts Options, tickets TicketIndex, registered map[string]bool, res *Result) {
	entries, err := os.ReadDir(opts.PolecatsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			res.Errors = append(res.Errors, fmt.Sprintf("read polecats dir %s: %v", opts.PolecatsDir, err))
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(opts.PolecatsDir, name)
		// A dir that is a registered worktree, or still carries a .git
		// pointer (possibly into a repo outside this sweep), is not an
		// orphan — the registered-worktree scan owns it.
		if registered[path] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			continue
		}
		branch := BranchPrefix + name
		if opts.LivePolecats[name] {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: path, Owner: name, Branch: branch, Reason: "orphan dir, live polecat " + name,
			})
			continue
		}
		// Keyed on the owner, which here is all there is — and since mg-bdda
		// that is the same key the registered-worktree phase uses, so the two
		// phases can no longer reach opposite verdicts about the same dead
		// owner (see classifyTree). The reported Branch is the one this owner
		// WOULD have; it is not read from anything.
		_, state := tickets.OwnerState(name)
		if !state.Concluded() {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: path, Owner: name, Branch: branch, Reason: "orphan dir, owner's ticket " + state.String(),
			})
			continue
		}
		action := WorktreeAction{Path: path, Owner: name, Branch: branch, Reason: "orphan dir, owner's ticket " + state.String()}
		// No WorktreeDirty guard here, unlike phase 1 (mg-ee02) — and that is
		// the answer, not an oversight. WorktreeDirty shells out to `git -C
		// <path> status`, and an orphan dir has no .git by construction (that
		// is what makes it an orphan and what got it past the check above), so
		// the call can only ever fail. There is no index and no HEAD to
		// compare these files against: "uncommitted" is not a property this
		// directory has. What it holds is unclassifiable leftovers, and the
		// only signal available about them is the owner's concluded ticket.
		if opts.DryRun {
			opts.logf("would remove orphan dir %s", action.String())
		} else {
			if err := os.RemoveAll(path); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("remove orphan dir %s: %v", path, err))
				continue
			}
			opts.logf("removed orphan dir %s", action.String())
		}
		res.WorktreesRemoved = append(res.WorktreesRemoved, action)
	}
}

// Summary renders a human-readable multi-line report of a sweep result.
func (r Result) Summary() string {
	var b strings.Builder
	verb := "removed"
	if r.DryRun {
		verb = "would remove"
	}
	delVerb := "deleted"
	if r.DryRun {
		delVerb = "would delete"
	}
	fmt.Fprintf(&b, "git GC sweep of %s%s\n", r.Repo, dryRunTag(r.DryRun))
	fmt.Fprintf(&b, "  worktrees: %s %d, kept %d\n", verb, len(r.WorktreesRemoved), len(r.WorktreesKept))
	fmt.Fprintf(&b, "  branches:  %s %d, kept %d\n", delVerb, len(r.BranchesDeleted), len(r.BranchesKept))

	if len(r.WorktreesRemoved) > 0 {
		fmt.Fprintf(&b, "  worktrees %s:\n", verb)
		for _, w := range sortedWorktrees(r.WorktreesRemoved) {
			fmt.Fprintf(&b, "    %s\n", w.String())
		}
	}
	// Kept worktrees are itemised, not just counted. `pogo gc --help` promises
	// a worktree holding uncommitted work is "KEPT and reported", and a
	// preserved tree pins its branch until someone acts on it — so a bare
	// count tells an operator a branch is stuck without telling them which
	// tree to go rescue. pogod's log has always emitted the full line for
	// these; the CLI summary was the half that only counted.
	if len(r.WorktreesKept) > 0 {
		fmt.Fprintf(&b, "  worktrees kept:\n")
		for _, w := range sortedWorktrees(r.WorktreesKept) {
			fmt.Fprintf(&b, "    %s\n", w.String())
		}
	}
	if len(r.BranchesDeleted) > 0 {
		fmt.Fprintf(&b, "  branches %s:\n", delVerb)
		for _, br := range sortedBranches(r.BranchesDeleted) {
			fmt.Fprintf(&b, "    %s — %s\n", br.Branch, br.Reason)
		}
	}
	// Branches kept because deleting them might have lost commits are itemised;
	// the other keeps — live polecat, in-flight ticket, still checked out — stay
	// a count, because there are typically a hundred of them and none needs
	// anyone to do anything. This is the same asymmetry the kept-worktree
	// listing above settled on: a preserved thing that nothing will ever
	// self-clear has to name itself, or the operator is told a branch is stuck
	// without being told which one.
	if atRisk := atRiskBranches(r.BranchesKept); len(atRisk) > 0 {
		fmt.Fprintf(&b, "  branches kept holding commits that may exist nowhere else:\n")
		for _, br := range atRisk {
			fmt.Fprintf(&b, "    %s — %s\n", br.Branch, br.Reason)
		}
	}
	if r.PruneOutput != "" {
		fmt.Fprintf(&b, "  worktree prune: %s\n", strings.ReplaceAll(r.PruneOutput, "\n", "; "))
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(&b, "  errors (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "    %s\n", e)
		}
	}
	return b.String()
}

func dryRunTag(dry bool) string {
	if dry {
		return " (dry run)"
	}
	return ""
}

func sortedBranches(in []BranchAction) []BranchAction {
	out := append([]BranchAction(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Branch < out[j].Branch })
	return out
}

// atRiskBranches returns the kept branches whose commits may exist nowhere
// else, sorted by name. Both local-only and could-not-tell are included: see
// DurabilityUnknown for why the second is not quietly dropped.
func atRiskBranches(in []BranchAction) []BranchAction {
	var out []BranchAction
	for _, br := range in {
		if br.AtRisk() {
			out = append(out, br)
		}
	}
	return sortedBranches(out)
}

func sortedWorktrees(in []WorktreeAction) []WorktreeAction {
	out := append([]WorktreeAction(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
