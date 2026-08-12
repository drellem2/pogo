package gitgc

import (
	"os/exec"
	"strings"
	"testing"
)

// originRef writes refs/remotes/origin/<name> at rev — exactly the shape a
// fetch or a push leaves behind, since a remote-tracking ref is just a ref.
// Building it directly rather than through a real remote keeps the fixtures
// about the PREDICATE instead of about git's transport.
func (r *testRepo) originRef(name, rev string) {
	r.t.Helper()
	r.git("update-ref", "refs/remotes/origin/"+name, r.rev(rev))
}

// rev resolves a revision to its commit SHA.
func (r *testRepo) rev(rev string) string {
	r.t.Helper()
	return strings.TrimSpace(r.git("rev-parse", "--verify", rev+"^{commit}"))
}

// commitOn creates branch off the current HEAD, commits file on it, and leaves
// the repo back on main. It is the shape of a polecat that committed and did
// not push.
func (r *testRepo) commitOn(branch, file, content string) {
	r.t.Helper()
	r.git("checkout", "-q", "-b", branch)
	r.commit(file, content)
	r.git("checkout", "-q", "main")
}

func TestBranchDurableAnyOriginRefHoldsHead(t *testing.T) {
	r := newTestRepo(t)
	r.commitOn("polecat-builder", "feat.txt", "feat")
	// The reviewer shape from gh#134, and the general case this predicate
	// exists for: the head lives on an origin ref that is NOT this branch's
	// own, and the branch is not an ancestor of main. Ancestry answers "not
	// merged" here and would license a deletion that loses nothing — but the
	// SAME shape with no origin ref at all is the one that loses everything,
	// and ancestry cannot tell the two apart.
	r.git("branch", "polecat-reviewer", "polecat-builder")
	r.originRef("polecat-builder", "polecat-builder")

	verdict, detail := BranchDurable(r.dir, "polecat-reviewer", "main")
	if verdict != DurabilityDurable {
		t.Fatalf("verdict = %s (%s), want durable", verdict, detail)
	}
	// The holder is NAMED so a deletion can be audited rather than trusted.
	if !strings.Contains(detail, "origin/polecat-builder") {
		t.Errorf("detail should name the carrying ref, got %q", detail)
	}

	// Control: identical branch, nothing on origin. This is the pair that makes
	// the assertion above about durability rather than about ancestry.
	if v, d := BranchDurable(r.dir, "polecat-builder", "main"); v != DurabilityDurable {
		t.Fatalf("the branch origin holds should be durable too, got %s (%s)", v, d)
	}
	r.git("update-ref", "-d", "refs/remotes/origin/polecat-builder")
	v, d := BranchDurable(r.dir, "polecat-reviewer", "main")
	if v != DurabilityLocalOnly {
		t.Fatalf("with origin/polecat-builder gone the head is local-only, got %s (%s)", v, d)
	}
}

func TestBranchDurableExcludesOriginHEAD(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-landed")
	r.originRef("main", "main")
	// origin/HEAD is a symref at the default branch. It contains the head and
	// sorts BEFORE origin/main ("H" < "m"), so without the exclusion it is the
	// ref that gets named — under a name that says nothing about who pushed.
	r.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	verdict, detail := BranchDurable(r.dir, "polecat-landed", "main")
	if verdict != DurabilityDurable {
		t.Fatalf("verdict = %s (%s), want durable", verdict, detail)
	}
	// THE EXCLUSION CHANGES THE NAME, NEVER THE VERDICT — asserted rather than
	// commented, because an exclusion nothing exercises is indistinguishable
	// from a comment.
	if strings.Contains(detail, "origin/HEAD") {
		t.Errorf("origin/HEAD must not be the named holder, got %q", detail)
	}
	if !strings.Contains(detail, "origin/main") {
		t.Errorf("detail should name origin/main, got %q", detail)
	}
}

func TestBranchDurableRebaseLanded(t *testing.T) {
	r := newTestRepo(t)
	r.commitOn("polecat-rebased", "landed.txt", "landed")
	// What the refinery actually does: rebase onto main (new SHA, same patch),
	// merge, then `push origin --delete <branch>` (step=branch-reap). So after
	// a perfect landing there is no origin/<branch>, and the branch is NOT an
	// ancestor of main. BranchMerged says "not merged" about work that
	// demonstrably landed — the measurement behind mg-0a43's warning that 71 of
	// 146 `--no-merged` branches is not an exposure count.
	//
	// main advances FIRST so the cherry-pick lands on a different parent. Onto
	// an unchanged main it would reproduce the branch commit byte for byte —
	// same tree, same parent, same author, same fixed timestamps — and git
	// would hand back the identical SHA, making the branch an ancestor and the
	// fixture a test of nothing.
	r.commit("other.txt", "other")
	r.git("cherry-pick", r.rev("polecat-rebased"))
	r.originRef("main", "main")

	if merged, err := BranchMerged(r.dir, "polecat-rebased", "main"); err != nil || merged {
		t.Fatalf("fixture precondition: ancestry must say NOT merged (merged=%v err=%v)", merged, err)
	}
	if holder, err := originRefContaining(r.dir, r.rev("polecat-rebased")); err != nil || holder != "" {
		t.Fatalf("fixture precondition: no origin ref may hold the head (holder=%q err=%v)", holder, err)
	}

	verdict, detail := BranchDurable(r.dir, "polecat-rebased", "main")
	if verdict != DurabilityDurable {
		t.Fatalf("verdict = %s (%s), want durable — a rebase-landed branch is not lost", verdict, detail)
	}
	if !strings.Contains(detail, "patch-equivalent") {
		t.Errorf("detail should say the patches landed, got %q", detail)
	}
}

func TestBranchDurableLocalOnly(t *testing.T) {
	r := newTestRepo(t)
	r.commitOn("polecat-stopped", "unpushed.txt", "unpushed")
	r.originRef("main", "main")

	verdict, detail := BranchDurable(r.dir, "polecat-stopped", "main")
	if verdict != DurabilityLocalOnly {
		t.Fatalf("verdict = %s (%s), want local-only", verdict, detail)
	}
	if !verdict.AtRisk() {
		t.Error("local-only must be at risk")
	}
	if !strings.Contains(detail, "1 commit(s)") {
		t.Errorf("detail should count the commits at risk, got %q", detail)
	}
}

func TestBranchDurablePrefersOriginTarget(t *testing.T) {
	r := newTestRepo(t)
	r.commitOn("polecat-landed", "landed.txt", "landed")
	// origin/main has the landed patch; the LOCAL main lags it, as an unfetched
	// checkout does. Comparing against the lagging ref would report a landed
	// branch as holding unique commits — safe, but permanently wrong.
	r.git("checkout", "-q", "-b", "ahead")
	r.git("cherry-pick", r.rev("polecat-landed"))
	r.originRef("main", "ahead")
	r.git("checkout", "-q", "main")

	if merged, _ := BranchMerged(r.dir, "polecat-landed", "main"); merged {
		t.Fatal("fixture precondition: local main must not contain the branch")
	}
	verdict, detail := BranchDurable(r.dir, "polecat-landed", "main")
	if verdict != DurabilityDurable {
		t.Fatalf("verdict = %s (%s), want durable against origin/main", verdict, detail)
	}
	if !strings.Contains(detail, "origin/main") {
		t.Errorf("detail should name origin/main as the ref compared against, got %q", detail)
	}
}

func TestBranchDurableUnknown(t *testing.T) {
	r := newTestRepo(t)
	r.commitOn("polecat-orphan", "x.txt", "x")

	// No integration ref resolves and no origin ref holds the head: there is
	// nothing left to compare against, so the answer is `unknown` rather than a
	// guess in either direction.
	verdict, detail := BranchDurable(r.dir, "polecat-orphan", "no-such-branch")
	if verdict != DurabilityUnknown {
		t.Fatalf("verdict = %s (%s), want unknown", verdict, detail)
	}
	if !verdict.AtRisk() {
		t.Error("unknown must be at risk — a question we failed to ask is not an answer of durable")
	}

	// A head that does not resolve is unknown too, never durable.
	if v, d := BranchDurable(r.dir, "polecat-does-not-exist", "main"); v != DurabilityUnknown {
		t.Errorf("unresolvable branch: verdict = %s (%s), want unknown", v, d)
	}
}

// TestBranchDurableIsNotAncestry is the guard on the mayor's ruling for
// mg-0a43: the obvious repair — reusing BranchMerged on the archived arm — is
// not this predicate, and swapping one for the other must go red. Two rows
// disagree, in opposite directions, so neither test can substitute for the
// other.
func TestBranchDurableIsNotAncestry(t *testing.T) {
	r := newTestRepo(t)
	r.commitOn("polecat-pushed", "pushed.txt", "pushed")
	r.originRef("polecat-pushed", "polecat-pushed")
	r.originRef("main", "main")
	r.branch("polecat-atmain") // an ancestor of main, on no origin ref of its own

	rows := []struct {
		branch    string
		ancestry  bool
		durable   bool
		whyItDiff string
	}{
		{"polecat-pushed", false, true, "pushed but unlanded: ancestry says lose it is unsafe, durability says it is published"},
		{"polecat-atmain", true, true, "already in main: both agree, the control"},
	}
	for _, row := range rows {
		merged, err := BranchMerged(r.dir, row.branch, "main")
		if err != nil {
			t.Fatalf("BranchMerged(%s): %v", row.branch, err)
		}
		if merged != row.ancestry {
			t.Errorf("%s: ancestry = %v, want %v (%s)", row.branch, merged, row.ancestry, row.whyItDiff)
		}
		verdict, detail := BranchDurable(r.dir, row.branch, "main")
		if got := verdict == DurabilityDurable; got != row.durable {
			t.Errorf("%s: durable = %v (%s), want %v (%s)", row.branch, got, detail, row.durable, row.whyItDiff)
		}
	}
}

// TestBranchDurableInARepoWithAPathNamedLikeTheBranch is a standing guard, not
// a regression: `git cherry` and `for-each-ref` take no pathspec, so today's
// implementation was never exposed. What it pins is the shape — a repository
// holding BOTH a branch `main` and a path `main`, which /Users/daniel/dev/pogo
// does — so that a future rewrite of any step here in terms of a command that
// accepts revisions AND paths (`git log`, `git rev-list`) goes red instead of
// answering with a silent empty stream. Three such false negatives went into
// the mg-0a43 investigation as findings before a positive control caught them.
func TestBranchDurableInARepoWithAPathNamedLikeTheBranch(t *testing.T) {
	r := newTestRepo(t)
	r.commit("main", "a file that shares the branch's name")
	r.originRef("main", "main")

	// The trap, demonstrated on the exact fixture, so the assertions below are
	// known to be about a repo where the naive spelling really does fail.
	naive := exec.Command("git", "-C", r.dir, "log", "--oneline", "main")
	if out, err := naive.CombinedOutput(); err == nil {
		t.Fatalf("fixture precondition: `git log --oneline main` must be ambiguous here, got:\n%s", out)
	}

	r.commitOn("polecat-ambig", "x.txt", "x")
	if verdict, detail := BranchDurable(r.dir, "polecat-ambig", "main"); verdict != DurabilityLocalOnly {
		t.Errorf("unpushed branch: verdict = %s (%s), want local-only", verdict, detail)
	}
	r.branch("polecat-inmain")
	if verdict, detail := BranchDurable(r.dir, "polecat-inmain", "main"); verdict != DurabilityDurable {
		t.Errorf("branch on origin/main: verdict = %s (%s), want durable", verdict, detail)
	}
}
