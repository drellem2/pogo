package strandedwork

import "testing"

// LocalOnlyCommits asks a DIFFERENT question from Inspect's, and the tests below
// are mostly about keeping the two apart (mg-ded2). Inspect asks whether the
// TARGET has these commits, which is right for a branch somebody still intends to
// merge. This asks whether ANY REMOTE has this commit object, which is the only
// question whose answer tracks what git-gc can destroy — and the two populations
// differ by two orders of magnitude on a real box.

// TestLocalOnlyCommitsFindsTheCommitNoRemoteHas is the state the ticket was filed
// about: a commit that exists on no remote ref, in a worktree git-gc reaps.
func TestLocalOnlyCommitsFindsTheCommitNoRemoteHas(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-orphan", "main")
	r.commit("orphan.md", "revert: drop the hStep variant (mg-05d3)")
	r.checkout("main")

	got, err := LocalOnlyCommits(r.dir, "polecat-orphan")
	if err != nil {
		t.Fatalf("LocalOnlyCommits: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1: the branch has one commit and it is on no remote ref", len(got))
	}
	if got[0].Subject != "revert: drop the hStep variant (mg-05d3)" {
		t.Errorf("Subject = %q; a reader has to recognise the work without a git round-trip", got[0].Subject)
	}
	if got[0].SHA == "" {
		t.Error("SHA is empty")
	}
}

// TestLocalOnlyCommitsIsZeroOnceTheBRANCHISPUSHED is the control that bounds the
// row built on this. A pushed branch has a durable copy on a server; whatever
// else is wrong with it, nothing is about to be destroyed.
func TestLocalOnlyCommitsIsZeroOnceTheBranchIsPushed(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-pushed", "main")
	r.commit("work.md", "feat: finished work")
	r.push("polecat-pushed")
	r.checkout("main")

	got, err := LocalOnlyCommits(r.dir, "polecat-pushed")
	if err != nil {
		t.Fatalf("LocalOnlyCommits: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d commits, want 0 after the push: %+v", len(got), got)
	}
}

// TestLocalOnlyCommitsIsNOTUnmergedCommits is the distinction the whole design
// turns on. A branch whose commits are on a remote under ANOTHER ref is durable
// — `git cherry` calls it unmerged forever, and this must call it safe. Getting
// this wrong is what turns a 1-row report into a 435-row one.
func TestLocalOnlyCommitsIsNotUnmergedCommits(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-sibling", "main")
	r.commit("shared.md", "feat: work pushed under a sibling ref")
	r.push("polecat-sibling")
	// A second branch at the same commit, never pushed under its OWN name.
	r.git("branch", "polecat-neverpushed", "polecat-sibling")
	r.checkout("main")

	unmerged, _, err := cherry(r.dir, "refs/remotes/origin/main", "refs/heads/polecat-neverpushed")
	if err != nil {
		t.Fatalf("cherry: %v", err)
	}
	if len(unmerged) == 0 {
		t.Fatal("the control is broken: git cherry should call this branch unmerged")
	}

	got, err := LocalOnlyCommits(r.dir, "polecat-neverpushed")
	if err != nil {
		t.Fatalf("LocalOnlyCommits: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d local-only commits while git cherry counted %d unmerged; the object is on "+
			"a remote under another ref, so nothing can be lost. Reporting it is what makes this "+
			"row unbounded.", len(got), len(unmerged))
	}
}

// TestLocalOnlyCommitsOnAnAbsentBranchIsZeroAndNotAnError. There is no local copy
// to lose, and an error here would turn every reaped worktree into a row.
func TestLocalOnlyCommitsOnAnAbsentBranchIsZero(t *testing.T) {
	r := newRepo(t)
	got, err := LocalOnlyCommits(r.dir, "polecat-never-existed")
	if err != nil {
		t.Fatalf("LocalOnlyCommits on an absent branch: %v, want a clean zero", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d commits from an absent branch", len(got))
	}
}

// TestLocalOnlyCommitsErrorsRatherThanAnsweringZeroOnABrokenRepo. The dangerous
// direction is the one where a failure spells "nothing local-only", because that
// is the answer meaning "this branch needs nothing" — the same asymmetry
// KindUnjudged exists for one level up.
func TestLocalOnlyCommitsErrorsRatherThanAnsweringZero(t *testing.T) {
	if _, err := LocalOnlyCommits(t.TempDir()+"/not-a-repo", "polecat-x"); err == nil {
		t.Error("LocalOnlyCommits on a nonexistent repository returned nil error; a failure that " +
			"spells 'nothing to lose' converts an orphan into an all-clear")
	}
}
