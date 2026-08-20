package gitgc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitInWorktree writes a file inside a linked worktree and commits it there
// — the shape of a polecat that got its work committed and was stopped before
// it pushed. That is the state the nightly pre-deploy stop creates on purpose.
func commitInWorktree(t *testing.T, wt, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", name}, {"commit", "-q", "-m", "work on " + name}} {
		if out, err := gitIn(wt, args...); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, wt, err, out)
		}
	}
}

// gitInDetach puts a worktree on a detached HEAD — `pogo gc --list-preserved`
// prints `branch HEAD` for such a tree, and one is in that state on this box.
func gitInDetach(t *testing.T, wt string) {
	t.Helper()
	if out, err := gitIn(wt, "checkout", "-q", "--detach"); err != nil {
		t.Fatalf("detach %s: %v\n%s", wt, err, out)
	}
}

func gitIn(dir string, args ...string) (string, error) {
	out, err := git(dir, args...)
	return string(out), err
}

// TestPreservedForItemsFindsTheCleanTreeHoldingUnpushedCommits is mg-fcba's
// population, and the exact case mg-836c's probe walked past.
//
// A polecat committed and was stopped before it pushed. `git status` is clean,
// so the removal guard has nothing to refuse and PreservedForItems used to
// `continue` — and with it silent, the item read dispatchable to stall-watch,
// to priority-wake, and to the spawn path. The work is on disk the whole time.
func TestPreservedForItemsFindsTheCleanTreeHoldingUnpushedCommits(t *testing.T) {
	r := newTestRepo(t)
	r.originRef("main", "main") // the base is published; only the new work is not
	r.branch("polecat-p3d0e")
	wt := r.worktreeOwnedBy("p3d0e", "polecat-p3d0e")
	commitInWorktree(t, wt, "ineffect.go", "package ineffect // a whole new command\n")

	// The tree is CLEAN. This assertion is the premise of the whole finding: if
	// it ever fails, this test is measuring the mg-836c population instead.
	if dirty, files, err := WorktreeDirty(wt); err != nil || dirty {
		t.Fatalf("the fixture must be clean — dirty=%v files=%v err=%v", dirty, files, err)
	}

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-3d0e"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	trees := rep.Trees["mg-3d0e"]
	if len(trees) != 1 {
		t.Fatalf("a clean worktree holding a commit that exists nowhere else must be reported; "+
			"got %d tree(s) %+v. This is the state the pre-deploy stop creates every night",
			len(trees), rep.Trees)
	}
	got := trees[0]
	if got.Outcome != "unpushed" {
		t.Errorf("Outcome = %q, want %q — calling it \"preserved\" would assert uncommitted work "+
			"in a tree git calls clean", got.Outcome, "unpushed")
	}
	if got.Total != 0 || got.Modified != 0 || got.Untracked != 0 {
		t.Errorf("dirty counts must stay zero on a clean tree, got %d/%d/%d",
			got.Total, got.Modified, got.Untracked)
	}
	if got.Commits == nil {
		t.Fatal("Commits is nil on the tree that was reported FOR its commits")
	}
	if got.Commits.Verdict != DurabilityLocalOnly {
		t.Errorf("Verdict = %s, want local-only (detail: %s)", got.Commits.Verdict, got.Commits.Detail)
	}
	if len(got.Commits.Commits) != 1 || !strings.Contains(got.Commits.Commits[0], "ineffect.go") {
		t.Errorf("Commits = %v, want the one commit named — a refusal that cannot say WHICH commits "+
			"exist nowhere else cannot be acted on", got.Commits.Commits)
	}
	if got.Branch != "polecat-p3d0e" {
		t.Errorf("Branch = %q, want polecat-p3d0e", got.Branch)
	}
}

// TestPreservedForItemsFindsTheDetachedTreeNoRefScanCanSee is the sub-case that
// no other guard covers, and the reason this could not be left to the
// stranded-work gate.
//
// That gate reads refs/heads as well as refs/remotes/origin, so an unpushed
// polecat BRANCH already refuses a spawn (mg-bfe0). A worktree on a detached
// HEAD has no ref at all: its commits are reachable from that tree's own HEAD
// and from nothing else, so a scan over refs cannot see them by construction.
// `~/.pogo/polecats/p6b2d` is in exactly this state on this box.
func TestPreservedForItemsFindsTheDetachedTreeNoRefScanCanSee(t *testing.T) {
	r := newTestRepo(t)
	r.originRef("main", "main")
	r.branch("polecat-p6b2d")
	wt := r.worktreeOwnedBy("p6b2d", "polecat-p6b2d")
	gitInDetach(t, wt)
	commitInWorktree(t, wt, "overtaken.py", "print('census')\n")

	// No ref anywhere holds it. Stated as a fixture assertion because it is the
	// whole reason this case is separate: a ref-based guard is not merely
	// unlucky here, it is looking somewhere the commit is not.
	out, err := gitIn(r.dir, "for-each-ref", "--contains", "HEAD", "--format=%(refname)", "refs/")
	if err == nil && strings.TrimSpace(out) != "" {
		t.Logf("refs containing the source repo's HEAD: %q (the worktree's own head is elsewhere)", out)
	}

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-6b2d"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	trees := rep.Trees["mg-6b2d"]
	if len(trees) != 1 {
		t.Fatalf("a detached worktree holding a commit no ref holds must be reported; got %+v", rep.Trees)
	}
	got := trees[0]
	if got.Commits == nil || got.Commits.Verdict != DurabilityLocalOnly {
		t.Fatalf("want a local-only commits finding, got %+v", got.Commits)
	}
	// git's literal "HEAD" is passed through for a detached head on purpose, so
	// it stays distinguishable from a branch read that failed — every consumer
	// keys its "no ref holds these" wording on exactly this.
	if got.Branch != "HEAD" {
		t.Errorf("Branch = %q, want the literal \"HEAD\" — that is what tells a consumer there is "+
			"no branch to push, and it is what `pogo gc --list-preserved` prints", got.Branch)
	}
}

// TestPreservedForItemsIgnoresARebaseLandedTree is the control that keeps this
// guard usable, and it is why BranchDurable is the predicate rather than the
// obvious `git log HEAD --not --remotes`.
//
// THE REFINERY REBASES BEFORE MERGING, so a polecat's commits land on
// origin/main under different SHAs. A SHA test calls every such tree unpushed
// forever — measured at 71 of this repo's 146 polecat branches reading
// `--no-merged main` while demonstrably having landed. A gate that refuses
// those is disarmed within the week, and then it is not protecting the real
// population either.
func TestPreservedForItemsIgnoresARebaseLandedTree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-p1a2b")
	wt := r.worktreeOwnedBy("p1a2b", "polecat-p1a2b")
	commitInWorktree(t, wt, "landed.txt", "the work\n")

	// The same patch lands on main under a different SHA — what a rebase merge
	// leaves behind — and main is published.
	r.commit("landed.txt", "the work\n")
	r.originRef("main", "main")

	if a, b := r.rev("polecat-p1a2b"), r.rev("main"); a == b {
		t.Fatalf("the fixture must produce DIFFERENT shas for the same patch, got %s twice", a)
	}
	// And no origin ref holds the branch's own SHA — so `git log HEAD --not
	// --remotes`, the obvious spelling, WOULD have fired here. The silence
	// below is therefore the patch-id test doing its job and not the fixture
	// being published by the back door.
	if out, _ := gitIn(r.dir, "for-each-ref", "--contains", r.rev("polecat-p1a2b"),
		"--format=%(refname)", "refs/remotes/origin/"); strings.TrimSpace(out) != "" {
		t.Fatalf("the fixture published the branch's own sha (%s), so a SHA test would pass this "+
			"too and the control proves nothing", strings.TrimSpace(out))
	}

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-1a2b"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	if len(rep.Trees) != 0 {
		t.Fatalf("a tree whose commits have a patch-equivalent on the published integration branch "+
			"must be silent; got %+v", rep.Trees)
	}

	// The silence above is DURABILITY, not a failure to attribute the tree to
	// the item. Without this the test would pass on any owner name that
	// resolves to no work item — which is how a negative control stops
	// controlling anything.
	if !OwnerMatchesItem("p1a2b", "mg-1a2b") {
		t.Fatal("the fixture's worktree does not resolve to the item asked about, so the empty " +
			"result above measured attribution rather than durability")
	}
}

// TestWorktreeCommitsAtRiskAnswersForAWorktreeHeadNotARef pins the redirection
// the whole change rests on: BranchDurable is asked about a COMMIT, so a
// worktree that names no ref gets the same three tests a branch does.
func TestWorktreeCommitsAtRiskAnswersForAWorktreeHeadNotARef(t *testing.T) {
	r := newTestRepo(t)
	r.originRef("main", "main")
	r.branch("polecat-pdet")
	wt := r.worktreeOwnedBy("pdet", "polecat-pdet")

	// Before any work: the tree's HEAD is published, so there is no finding.
	// Without this arm the guard would fire on every fresh worktree in the
	// fleet, which is the shape that gets a gate deleted rather than debugged.
	f, err := WorktreeCommitsAtRisk(wt, r.dir, "main")
	if err != nil {
		t.Fatalf("WorktreeCommitsAtRisk on a fresh tree: %v", err)
	}
	if f.AtRisk() {
		t.Fatalf("a worktree whose HEAD is on origin/main must not be at risk: %s / %s",
			f.Verdict, f.Detail)
	}

	gitInDetach(t, wt)
	commitInWorktree(t, wt, "a.txt", "one\n")
	commitInWorktree(t, wt, "b.txt", "two\n")

	f, err = WorktreeCommitsAtRisk(wt, r.dir, "main")
	if err != nil {
		t.Fatalf("WorktreeCommitsAtRisk after two detached commits: %v", err)
	}
	if !f.AtRisk() || f.Verdict != DurabilityLocalOnly {
		t.Fatalf("two commits on a detached HEAD are held by nothing else; verdict = %s (%s)",
			f.Verdict, f.Detail)
	}
	if len(f.Commits) != 2 {
		t.Errorf("Commits = %v, want both — the count is what a reader sizes the loss by", f.Commits)
	}
	if f.Head == "" {
		t.Error("Head is empty; it is the only handle a rescuer has on a detached tree")
	}

	// Publishing them under ANY origin ref clears it — not origin/<branch>
	// specifically. That is BranchDurable's first test and it is inherited
	// here rather than re-derived.
	r.originRef("rescue-pdet", f.Head)
	f, err = WorktreeCommitsAtRisk(wt, r.dir, "main")
	if err != nil {
		t.Fatalf("WorktreeCommitsAtRisk after publishing: %v", err)
	}
	if f.AtRisk() {
		t.Errorf("published under origin/rescue-pdet, still at risk: %s / %s", f.Verdict, f.Detail)
	}
}

// TestWorktreeCommitsAtRiskNeedsASourceRepo. The refs live in the source
// repository's namespace; a linked worktree shares it but is not it. An
// unresolvable repo is an ERROR rather than a silent "durable", because the
// caller's failure direction depends on telling those apart — PreservedForItems
// records it as an incompleteness note, which every consumer already carries.
func TestWorktreeCommitsAtRiskNeedsASourceRepo(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-pnone")
	wt := r.worktreeOwnedBy("pnone", "polecat-pnone")

	if _, err := WorktreeCommitsAtRisk(wt, "", "main"); err == nil {
		t.Error("an empty source repo must be an error, not a durable verdict")
	}
	if _, err := WorktreeCommitsAtRisk("", r.dir, "main"); err == nil {
		t.Error("an empty worktree path must be an error")
	}
}
