package gitgc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeDetachedSeparatesDetachedFromNotAWorktree is the POSITIVE CONTROL
// for the instrument, and it is the first test in this file because mg-8d25 was
// filed with the instrument wrong.
//
// `git symbolic-ref -q --short HEAD` prints nothing for a detached HEAD AND
// nothing for a directory that is not a worktree at all. A first pass at
// measuring this population read that stdout and labelled 19 orphan directories
// on this host "DETACHED", which would have reported a large fake population of
// trees at risk. The EXIT CODE separates them: 1 for detachment, 128 for not a
// repository.
//
// All three states are asserted together on purpose. A test that checked only
// the detached case would pass against an implementation that answers "detached"
// for everything, which is precisely the implementation that was wrong.
func TestWorktreeDetachedSeparatesDetachedFromNotAWorktree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-attach")
	attached := r.worktree("polecat-attach")

	r.branch("polecat-detach")
	detached := r.worktree("polecat-detach")
	gitInDetach(t, detached)

	notARepo := t.TempDir()

	if got, err := WorktreeDetached(attached); err != nil || got {
		t.Errorf("attached worktree: WorktreeDetached = %v, %v; want false, nil", got, err)
	}
	if got, err := WorktreeDetached(detached); err != nil || !got {
		t.Errorf("detached worktree: WorktreeDetached = %v, %v; want true, nil", got, err)
	}
	// The discriminating assertion. A directory that is not a worktree must
	// produce an ERROR, not the answer "not detached" and not the answer
	// "detached" — either would be a claim about a tree nobody could ask.
	got, err := WorktreeDetached(notARepo)
	if err == nil {
		t.Fatalf("a non-repository must not produce an answer; got detached=%v, nil error — this is "+
			"the exact collapse that mislabelled 19 orphan dirs as detached", got)
	}
	if got {
		t.Errorf("a failed probe must not answer true; got detached=true alongside %v", err)
	}
}

// detachedHoldingOnlyCopy builds mg-8d25's loss case: a polecat worktree that
// COMMITTED, was put on a detached HEAD, and whose commits are held by that
// worktree's HEAD and by nothing else.
//
// The branch is deleted after the detach, because leaving it would make the
// fixture the harmless case: a ref in the source repo's namespace survives the
// worktree removal and keeps the objects.
func detachedHoldingOnlyCopy(t *testing.T, r *testRepo, owner, file string) string {
	t.Helper()
	branch := "polecat-" + owner
	r.originRef("main", "main") // the base is published; only the new work is not
	r.branch(branch)
	wt := r.worktreeOwnedBy(owner, branch)
	commitInWorktree(t, wt, file, "package rescue // the only copy on the machine\n")
	gitInDetach(t, wt)
	r.git("branch", "-D", branch)

	// Premises, asserted rather than assumed — if either fails this test is
	// measuring something else.
	if dirty, files, err := WorktreeDirty(wt); err != nil || dirty {
		t.Fatalf("the fixture must be CLEAN — dirty=%v files=%v err=%v", dirty, files, err)
	}
	if det, err := WorktreeDetached(wt); err != nil || !det {
		t.Fatalf("the fixture must be DETACHED — got %v, %v", det, err)
	}
	return wt
}

// TestRemoveWorktreeRefusesDetachedTreeHoldingTheOnlyCopy is mg-8d25's whole
// point, in the shape the worker report described: the removal guard clears a
// CLEAN worktree without asking the durability question, and on a detached HEAD
// that orphans the only copy of its commits.
//
// On the pre-fix code this test FAILS — `git status` is silent about committed
// work by construction, so the guard returned no refusal and the tree went.
func TestRemoveWorktreeRefusesDetachedTreeHoldingTheOnlyCopy(t *testing.T) {
	r := newTestRepo(t)
	wt := detachedHoldingOnlyCopy(t, r, "p8d25", "rescue.go")
	head := strings.TrimSpace(mustGit(t, wt, "rev-parse", "HEAD"))

	err := RemoveWorktree(r.dir, wt, OwnerUnproven)
	if err == nil {
		t.Fatal("RemoveWorktree must refuse a detached worktree holding the only copy of its " +
			"commits; it returned nil and the commits are now reachable from no ref at all")
	}

	var oce *OrphanCommitsError
	if !errors.As(err, &oce) {
		t.Fatalf("want a *OrphanCommitsError, got %T: %v", err, err)
	}
	if oce.Path != wt {
		t.Errorf("OrphanCommitsError.Path = %q, want %q", oce.Path, wt)
	}
	if !oce.DetachedKnown {
		t.Error("DetachedKnown must be true — detachment was positively established here, and a " +
			"refusal that hedges on it reads as a guess")
	}
	if oce.Finding.Head != head {
		t.Errorf("Finding.Head = %q, want the worktree's HEAD %q", oce.Finding.Head, head)
	}
	if len(oce.Finding.Commits) != 1 {
		t.Errorf("want the one at-risk commit listed, got %v", oce.Finding.Commits)
	}
	// The refusal has to say DETACHED, because that is what makes this
	// unrecoverable rather than merely unpushed: there is no ref to fetch,
	// cherry-pick or push FROM.
	if !strings.Contains(err.Error(), "DETACHED") {
		t.Errorf("refusal must name the detached HEAD, got: %v", err)
	}

	// The whole point: the tree, and with it the commits, survive.
	if _, serr := os.Stat(filepath.Join(wt, "rescue.go")); serr != nil {
		t.Fatalf("THE COMMITS WERE DESTROYED — the tree should survive a refused removal: %v", serr)
	}
	if out, gerr := gitIn(r.dir, "cat-file", "-e", head+"^{commit}"); gerr != nil {
		t.Fatalf("the commit should still be in the object store: %v\n%s", gerr, out)
	}
}

// TestRemoveWorktreeReapsDetachedTreeWhoseCommitsAreDurable is the counterweight,
// and it is not optional garnish: it is the case the mayor measured on this host
// on 2026-09-03 (`~/.pogo/polecats/p6b2d`, HEAD 11b8803, an ancestor of
// origin/main), and the reason detachment alone must not be the trigger.
//
// A guard that refused here would fire on the common, harmless configuration and
// teach every reader to pass --force by reflex — disarming it for the case above.
func TestRemoveWorktreeReapsDetachedTreeWhoseCommitsAreDurable(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-p6b2d")
	wt := r.worktreeOwnedBy("p6b2d", "polecat-p6b2d")
	gitInDetach(t, wt)
	// Its HEAD is an ancestor of origin/main — detached, and holding nothing
	// that any ref does not already hold.
	r.originRef("main", "main")

	if det, err := WorktreeDetached(wt); err != nil || !det {
		t.Fatalf("the fixture must be DETACHED — got %v, %v", det, err)
	}
	if err := RemoveWorktree(r.dir, wt, OwnerUnproven); err != nil {
		t.Fatalf("a detached worktree whose commits are held by a ref must still be reaped; "+
			"refusing here is how the guard gets overridden by reflex: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("the worktree dir should be gone, stat says %v", err)
	}
}

// TestRemoveWorktreeReapsCleanBranchTreeWithUnpushedCommits states the OTHER
// half of the scope, which is the half that keeps this guard affordable.
//
// A worktree on a BRANCH holding commits that exist nowhere else is mg-fcba's
// population and it is large — most polecat trees are in it between the commit
// and the merge. Removing such a tree loses nothing: the branch ref lives in the
// source repo's namespace, survives the removal, and keeps the objects, and the
// sweep's phase 2 asks BranchDurable before deleting that ref. Refusing here
// would pin nearly every tree in the fleet.
func TestRemoveWorktreeReapsCleanBranchTreeWithUnpushedCommits(t *testing.T) {
	r := newTestRepo(t)
	r.originRef("main", "main")
	r.branch("polecat-pfcba")
	wt := r.worktreeOwnedBy("pfcba", "polecat-pfcba")
	commitInWorktree(t, wt, "dispatch.go", "package dispatch // committed, not pushed\n")
	head := strings.TrimSpace(mustGit(t, wt, "rev-parse", "HEAD"))

	// The commits ARE at risk in the durability sense — that is the premise.
	find, err := WorktreeCommitsAtRisk(wt, r.dir, "main")
	if err != nil || !find.AtRisk() {
		t.Fatalf("the fixture must hold at-risk commits — %+v, %v", find, err)
	}

	if err := RemoveWorktree(r.dir, wt, OwnerUnproven); err != nil {
		t.Fatalf("a CLEAN worktree on a BRANCH must still be reaped — its branch ref outlives the "+
			"removal and phase 2 guards that ref. Refusing here pins the whole fleet: %v", err)
	}
	// And the commits really did survive, which is why not refusing was right.
	if out, gerr := gitIn(r.dir, "cat-file", "-e", head+"^{commit}"); gerr != nil {
		t.Fatalf("the branch should still hold the commit after the tree went: %v\n%s", gerr, out)
	}
}

// TestSweepNeverReachesADetachedWorktree records a MEASURED fact that changes
// where this fix bites, and that the ticket's own framing does not predict.
//
// `pogo gc`'s sweep is not the loss path for a detached tree, because it never
// touches one. Phase 1 skips every worktree whose Branch lacks the "polecat-"
// prefix, and `git worktree list --porcelain` reports an EMPTY branch for a
// detached tree — so Worktree.IsPolecat() is false. Phase 1b does not pick it up
// either: it scans only directories with no registration, and this one has one.
//
// The path that DOES reap such a tree is RemoveWorktree, called directly from
// pogod's polecat exit hook on every agent exit regardless of branch — which is
// where the guard added here actually fires. The consequence for an operator is
// that `pogo gc --apply --force` will not reclaim a retained detached tree; see
// forceReclaims, which now says so rather than answering "yes".
//
// The attached tree in the same sweep is the POSITIVE CONTROL: without it, a
// sweep that did nothing for an unrelated reason would pass this test.
func TestSweepNeverReachesADetachedWorktree(t *testing.T) {
	r := newTestRepo(t)
	wt := detachedHoldingOnlyCopy(t, r, "p8d25", "rescue.go")

	// The control: an ordinary polecat tree on a branch, same sweep, concluded.
	r.branch("polecat-pc7c7")
	ctrl := r.worktreeOwnedBy("pc7c7", "polecat-pc7c7")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		DryRun:       true,
		Tickets: TicketIndex{
			"mg-8d25": TicketArchived,
			"mg-c7c7": TicketArchived,
		},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, a := range append(append([]WorktreeAction{}, res.WorktreesRemoved...), res.WorktreesKept...) {
		if a.Path == wt {
			t.Fatalf("the sweep considered the detached tree (%+v) — if IsPolecat has been widened "+
				"to cover detached trees, the reclaim column and the exit-hook notice both need "+
				"revisiting, and this test is the place that says so", a)
		}
	}
	var sawControl bool
	for _, a := range res.WorktreesRemoved {
		if a.Path == ctrl {
			sawControl = true
		}
	}
	if !sawControl {
		t.Fatalf("POSITIVE CONTROL FAILED: the sweep did not reach the ordinary branch worktree "+
			"either, so this test says nothing about detachment. removed=%+v kept=%+v",
			res.WorktreesRemoved, res.WorktreesKept)
	}
}

// TestRemoveWorktreeForceStillReclaimsAnOrphanHoldingTree asserts the escape
// hatch is intact. Every refusal in this package is permanent and unconditional,
// so an override is the only way out; a guard with no way past it turns a
// retained tree into a permanent pin.
//
// It goes through RemoveWorktreeForce rather than `pogo gc --apply --force`
// because, per the test above, the sweep never reaches a detached tree — the
// operator-facing spelling is `git worktree remove --force`, which is what
// RemoveWorktreeForce runs and what the notice and the listing now recommend.
func TestRemoveWorktreeForceStillReclaimsAnOrphanHoldingTree(t *testing.T) {
	r := newTestRepo(t)
	wt := detachedHoldingOnlyCopy(t, r, "p8d25", "rescue.go")

	if err := RemoveWorktreeForce(r.dir, wt); err != nil {
		t.Fatalf("RemoveWorktreeForce: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("--force should have reclaimed the dir, stat says %v", err)
	}
}

// TestSweepNamesTheOrphanRefusalIfItEverSeesOne covers refusalReason's new arm.
//
// The arm is UNREACHABLE from the sweep today — see the test above — and it is
// written and tested anyway, because the sweep's fallback would otherwise render
// this refusal as its raw Error() string if IsPolecat is ever widened, and
// because a keep whose reason reused the dirty wording would send an operator to
// `git status` and a clean tree. Tested at the function rather than through a
// sweep, so the test does not have to fake a state production does not produce.
func TestSweepNamesTheOrphanRefusalIfItEverSeesOne(t *testing.T) {
	err := &OrphanCommitsError{
		Path:          "/polecats/p8d25",
		Target:        "main",
		DetachedKnown: true,
		Finding: WorktreeCommitFinding{
			Head: "11b8803deadbeef", Verdict: DurabilityLocalOnly,
			Commits: []string{"11b8803 rescue: the only copy"},
		},
	}
	reason, named := refusalReason(err)
	if !named {
		t.Fatal("refusalReason must recognise an orphan-commits refusal as a REFUSAL; reporting it " +
			"as a failure would put a retained tree in the errors half of the report")
	}
	if !strings.Contains(reason, "detached HEAD") || !strings.Contains(reason, "exist nowhere else") {
		t.Errorf("reason must name the detached HEAD and the orphaned commits, got %q", reason)
	}
	if strings.Contains(reason, "uncommitted") {
		t.Errorf("reason must NOT say uncommitted — git read this tree and found it clean; got %q",
			reason)
	}
	if !strings.Contains(reason, "--force") {
		t.Errorf("reason must name the escape hatch, got %q", reason)
	}

	// The unknown verdict is a different sentence, because it is a different
	// claim: we failed to ask, which is not a report of none.
	unknown, named := refusalReason(&OrphanCommitsError{
		Path: "/polecats/p8d25", DetachedKnown: true,
		Finding: WorktreeCommitFinding{Verdict: DurabilityUnknown},
	})
	if !named || !strings.Contains(unknown, "could NOT be established") {
		t.Errorf("an unknown verdict must say so rather than asserting a count, got %q", unknown)
	}
}

// TestScanPreservedListsTheDetachedOrphanHolder closes the listing's own loop.
//
// `pogo gc --list-preserved` calls this guard rather than re-implementing it,
// "so the listing cannot claim a tree is retained that gc would happily reap".
// The converse now matters as much: a tree gc REFUSES must not be counted clean
// and dropped, or the standing list is silent about the one population whose
// loss is unrecoverable.
func TestScanPreservedListsTheDetachedOrphanHolder(t *testing.T) {
	r := newTestRepo(t)
	wt := detachedHoldingOnlyCopy(t, r, "p8d25", "rescue.go")

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: r.polecatsDir(),
		Repo:        r.dir,
		Target:      "main",
	})
	if err != nil {
		t.Fatalf("ScanPreserved: %v", err)
	}
	if rep.CleanCount != 0 {
		t.Errorf("CleanCount = %d, want 0 — this tree is not one gc would reap", rep.CleanCount)
	}
	if len(rep.Retained) != 1 {
		t.Fatalf("want the tree listed as retained, got %d: %+v", len(rep.Retained), rep.Retained)
	}
	got := rep.Retained[0]
	if got.Path != wt {
		t.Errorf("listed %q, want %q", got.Path, wt)
	}
	if got.Outcome != "unpushed" {
		t.Errorf("Outcome = %q, want \"unpushed\" — \"preserved\" would assert uncommitted work in "+
			"a tree git calls clean, and \"undetermined\" would assert git failed", got.Outcome)
	}
	if got.Total != 0 || got.Modified != 0 || got.Untracked != 0 {
		t.Errorf("dirty counts must stay zero on a clean tree, got %d/%d/%d",
			got.Total, got.Modified, got.Untracked)
	}
	if got.StatusError != "" {
		t.Errorf("StatusError = %q, want empty — `git status` read this tree fine", got.StatusError)
	}
	if got.Commits == nil || !got.Commits.AtRisk() {
		t.Fatalf("the COMMITS annotation is what this entry is for, got %+v", got.Commits)
	}

	// The rendered report is what an operator actually reads, and the two
	// sentences that must not appear are the ones from the other outcomes.
	sum := rep.Summary()
	if strings.Contains(sum, "UNREADABLE: git status failed") {
		t.Errorf("the report calls a readable tree UNREADABLE:\n%s", sum)
	}
	if !strings.Contains(sum, "CLEAN — nothing uncommitted") {
		t.Errorf("the report must say the tree is clean, so nobody hunts files in it:\n%s", sum)
	}
	if !strings.Contains(sum, "COMMIT(S) EXIST ONLY HERE") {
		t.Errorf("the report must name the commits this tree is retained for:\n%s", sum)
	}
	if !strings.Contains(sum, "DETACHED HEAD") {
		t.Errorf("the report must say the commits are held by no ref at all:\n%s", sum)
	}
	if !strings.Contains(sum, "1 clean but holding commits that exist nowhere else") {
		t.Errorf("the headline count must break this population out rather than folding it into "+
			"\"holding uncommitted work\", which is false of it:\n%s", sum)
	}
}

// TestPreservedForItemsStillCallsTheDetachedHolderUnpushed guards mg-fcba's
// dispatch surfaces against a regression introduced by this fix.
//
// PreservedForItems classified this tree by `chk.Refusal == nil`, which was true
// before the guard learned to refuse it and is false after. Left alone, the same
// tree would have fallen through to the unnamed "retained" arm — a refusal the
// classifier could not name, for a tree it had already read correctly — and
// internal/agent's preserved gate keys its refusal text on the word.
func TestPreservedForItemsStillCallsTheDetachedHolderUnpushed(t *testing.T) {
	r := newTestRepo(t)
	detachedHoldingOnlyCopy(t, r, "p8d25", "rescue.go")

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-8d25"},
		Target:      "main",
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	trees := rep.Trees["mg-8d25"]
	if len(trees) != 1 {
		t.Fatalf("want the tree reported, got %d: %+v", len(trees), rep.Trees)
	}
	if trees[0].Outcome != "unpushed" {
		t.Errorf("Outcome = %q, want \"unpushed\" — the removal guard's new refusal must not "+
			"reclassify mg-fcba's population into the unnamed \"retained\" arm", trees[0].Outcome)
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitIn(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// TestOrphanArmRefusesWhenTheQuestionCannotBeASKED is the remedy's own
// self-check, and it exists because this fix is an artifact of the same kind as
// the defect it repairs: a guard that fails to ask a question.
//
// The way a durability guard goes quiet is not by being deleted — it is by
// treating a FAILED read as a negative one. "We could not establish that these
// commits exist elsewhere" must refuse exactly as "they do not" does; the
// subsystem this predicate came from has a documented history of that collapse
// costing files (mg-65b2, mg-0b77, mg-76e5), and DurabilityUnknown exists as its
// own verdict for that reason.
//
// The failure is induced at the SOURCE REPO, which is what the refs live in: a
// detached tree whose .git pointer names a repository that is not there can be
// read by `git status` (its object store is intact) and cannot be asked a
// question about refs.
func TestOrphanArmRefusesWhenTheQuestionCannotBeASKED(t *testing.T) {
	r := newTestRepo(t)
	wt := detachedHoldingOnlyCopy(t, r, "p8d25", "rescue.go")

	// Positive control first: with a resolvable repo this tree refuses with a
	// COUNT, so a refusal below is attributable to the induced failure rather
	// than to the tree being at risk for the ordinary reason.
	if err := checkOrphanCommits(wt, r.dir, "main"); err == nil {
		t.Fatal("control failed: the fixture must refuse before anything is broken")
	}

	// Now ask with a repo that does not exist. The tree is unchanged.
	err := checkOrphanCommits(wt, filepath.Join(t.TempDir(), "gone"), "main")
	if err == nil {
		t.Fatal("a durability question that could not be ASKED must refuse; answering \"proceed\" " +
			"is how a guard goes quiet without anyone deleting it")
	}
	var oce *OrphanCommitsError
	if !errors.As(err, &oce) {
		t.Fatalf("want a *OrphanCommitsError, got %T: %v", err, err)
	}
	if oce.Finding.Verdict != DurabilityUnknown {
		t.Errorf("Verdict = %v, want unknown — reporting this as local-only would assert a "+
			"measurement nobody took", oce.Finding.Verdict)
	}
	// And the sentence must say which it is. "There are commits here" and "I
	// could not look" send a reader to different places.
	if !strings.Contains(err.Error(), "could NOT be established") {
		t.Errorf("refusal must say the question failed rather than asserting a finding, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not a report of none") {
		t.Errorf("refusal must say a failed read is not a negative one, got: %v", err)
	}
}

// TestOrphanArmDoesNotClaimDetachmentItDidNotObserve guards the wording of the
// fail-closed path.
//
// When detachment itself cannot be established the arm still asks the durability
// question and still refuses — the same rule the cannot-tell status arm applies
// one step up. What it must not do is print "is on a DETACHED HEAD" about a tree
// nobody established that of; a refusal that overstates its evidence is how the
// next reader learns to discount refusals.
func TestOrphanArmDoesNotClaimDetachmentItDidNotObserve(t *testing.T) {
	observed := (&OrphanCommitsError{
		Path: "/polecats/p8d25", DetachedKnown: true,
		Finding: WorktreeCommitFinding{Head: "11b8803", Verdict: DurabilityLocalOnly,
			Commits: []string{"11b8803 rescue"}},
	}).Error()
	if !strings.Contains(observed, "is on a DETACHED HEAD") {
		t.Errorf("an observed detachment must be stated plainly, got: %s", observed)
	}

	unobserved := (&OrphanCommitsError{
		Path: "/polecats/p8d25", DetachedKnown: false, DetachError: "symbolic-ref: exit status 128",
		Finding: WorktreeCommitFinding{Head: "11b8803", Verdict: DurabilityLocalOnly,
			Commits: []string{"11b8803 rescue"}},
	}).Error()
	if strings.Contains(unobserved, "is on a DETACHED HEAD") {
		t.Errorf("the refusal claims a detached HEAD nobody observed, got: %s", unobserved)
	}
	if !strings.Contains(unobserved, "symbolic-ref: exit status 128") {
		t.Errorf("the refusal must name which read failed, got: %s", unobserved)
	}
}
