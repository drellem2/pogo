package gitgc

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// withCommandBudget shrinks the per-command budget for one test.
//
// 1ns rather than a small-but-real duration: the context is already expired
// when exec.Cmd.Start consults it, so the kill is deterministic instead of a
// race against how fast git happens to be on the machine running the suite. A
// budget test that is itself load-sensitive would join the family this repo
// already has three members of (mg-6c90, mg-84f0).
func withCommandBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := commandBudget
	commandBudget = d
	t.Cleanup(func() { commandBudget = prev })
}

// TestCommandBudgetKillsAndSaysWhichOne is the arithmetic half of
// drellem2/pogo#158 turned into a bound.
//
// One measured run of `pogo gc --list-preserved` on this host spawned 114
// git/mg subprocesses over 60 directories, and not one of them had a timeout.
// Any of them can block forever — a git index lock nobody releases, a
// filesystem that stopped answering — and the command then produces no output
// and never returns, which is what the issue reports.
//
// Both halves are asserted, because a bound that cannot fire proves nothing and
// a bound that always fires is not a bound: the RED case kills and names the
// budget, the GREEN control runs the same command to completion.
func TestCommandBudgetKillsAndSaysWhichOne(t *testing.T) {
	// GREEN control first, so a broken helper cannot pass the RED case by
	// failing at everything.
	out, err := runOutput("echo", "reachable")
	if err != nil || strings.TrimSpace(string(out)) != "reachable" {
		t.Fatalf("control: a command inside the budget must run normally, got %q / %v", out, err)
	}

	withCommandBudget(t, time.Nanosecond)
	_, err = runOutput("sleep", "30")
	if !errors.Is(err, ErrCommandBudget) {
		t.Fatalf("a command over budget must come back as ErrCommandBudget, got %v", err)
	}
	// The text is the point, not the sentinel. "git status failed" and "git
	// status was still running after the budget" send an operator to different
	// places, and one of those places is a repository that is not broken.
	if msg := err.Error(); !strings.Contains(msg, "sleep 30") || !strings.Contains(msg, "still running") {
		t.Errorf("the error must name the invocation and say it was killed for time, got %q", msg)
	}
}

// TestCommandBudgetFailsTowardRefusingRemoval is the safety direction, and it
// is the one assertion here that guards files rather than legibility.
//
// checkWorktreeRemoval is SHARED — the listing calls it, and so do the
// destructive sweep and pogod's exit hook, deliberately ("the same guard the
// sweep and the exit hook consult, called rather than re-implemented"). So a
// bound added under it must never turn "I could not read this tree" into "this
// tree is clean, take it". A timed-out `git status` has to land on the
// cannot-tell arm, which refuses unconditionally at any age and any ownership.
func TestCommandBudgetFailsTowardRefusingRemoval(t *testing.T) {
	polecats := sharedPolecats(t)
	repo := newTestRepo(t)
	repo.branch("polecat-cln1")
	wt := addWorktree(t, repo, polecats, "cln1", "polecat-cln1")

	// GREEN control: the tree really is clean, and the guard really does permit
	// removal. Without this the RED case below could pass on a tree that was
	// dirty all along.
	if chk := checkWorktreeRemoval(wt, repo.dir, ""); chk.Refusal != nil {
		t.Fatalf("control: a clean tree must be removable, got refusal %v", chk.Refusal)
	}

	withCommandBudget(t, time.Nanosecond)
	chk := checkWorktreeRemoval(wt, repo.dir, "")
	if chk.Refusal == nil {
		t.Fatalf("REGRESSION: a tree whose `git status` was KILLED for time was reported removable. "+
			"The bound must fail toward refusing — this guard has no merge gate behind it, and %s "+
			"would be deleted on a timeout.", wt)
	}
	var uwe *UndeterminedWorktreeError
	if !errors.As(chk.Refusal, &uwe) {
		t.Fatalf("refusal = %T (%v), want *UndeterminedWorktreeError — a timeout is a tree we could "+
			"not look at, not a tree we looked at and found dirty", chk.Refusal, chk.Refusal)
	}
	if !errors.Is(uwe.Err, ErrCommandBudget) {
		t.Errorf("the refusal must carry WHY the read failed, got %v", uwe.Err)
	}
}

// TestAgeWalkBudgetListsTheTreeAndSaysWhyItHasNoAge covers the risk the triage
// packet put first, in the shape gh#97 got backwards: a bound that DROPS what
// it could not measure is a data-loss instrument wearing an inventory's
// clothes.
//
// The per-tree age walk is the measured cost sink here — 21.6-28.1s for one
// node_modules worktree in drellem2/pogo#168, against a host holding 196
// retained trees in #162. Bounding it is what stops a scan waiting forever on
// one tree. What must NOT happen is the tree vanishing from the listing, or
// appearing with a silent blank where its age goes: the reader's next command
// is repo-scoped and forced.
func TestAgeWalkBudgetListsTheTreeAndSaysWhyItHasNoAge(t *testing.T) {
	polecats, _, owners := threeRetained(t)

	// GREEN control: with the real budget every tree reports a measured age.
	// If it did not, the RED case below would be asserting nothing.
	full, err := ScanPreserved(PreservedScanOptions{PolecatsDir: polecats, Tickets: tickets(owners...)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range full.Retained {
		if !tr.UntouchedKnown {
			t.Fatalf("control: %s has no age under the real budget (%v)", tr.Owner, tr.UntouchedError)
		}
	}

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir:   polecats,
		Tickets:       tickets(owners...),
		AgeWalkBudget: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RetainedCount != full.RetainedCount {
		t.Fatalf("the bound DROPPED trees: %d retained under it against %d without. A tree over "+
			"budget must be LISTED with an unknown age, never omitted — an operator reading a "+
			"short list as a complete one deletes the repository it does not mention.",
			rep.RetainedCount, full.RetainedCount)
	}
	tr := findTree(t, rep.Retained, "aaa1")
	if tr.UntouchedKnown {
		t.Fatalf("age reported as known under a 1ns walk budget: %+v", tr)
	}
	if !strings.Contains(tr.UntouchedError, "too large to measure") {
		t.Errorf("UntouchedError = %q, want it to say the walk was abandoned for COST. "+
			"A tree reported as unlistable when it is merely enormous sends the reader to "+
			"fsck a repository that is fine.", tr.UntouchedError)
	}
	row := StreamedTree(tr)
	if !strings.Contains(row, "age unknown") || !strings.Contains(row, "COST, not damage") {
		t.Errorf("the rendered row must carry the distinction, not just the record:\n%s", row)
	}
	// The rest of the row is untouched: the files are what the listing exists
	// for, and they were read before the walk ever ran.
	if !strings.Contains(row, "only-copy.go") || !strings.Contains(row, "1 UNTRACKED") {
		t.Errorf("an over-budget tree must still report its files:\n%s", row)
	}
}

// TestWalkBudgetNeverReportsAPartialMaximum: an abandoned walk returns NO age,
// not the newest thing it happened to reach.
//
// newestWrite already applies this rule to an unreadable directory, and the
// reason carries over exactly. A maximum over the part of the tree that was
// visited would answer "untouched 30 days" about a tree whose recently written
// half was never reached — and this number is what a human clears a permanent
// pin on. Wrong-and-confident is the one output worse than absent.
func TestWalkBudgetNeverReportsAPartialMaximum(t *testing.T) {
	polecats, _, _ := threeRetained(t)
	tree := polecats + "/aaa1"

	when, err := newestWriteWithin(tree, time.Nanosecond)
	if !errors.Is(err, ErrWalkBudget) {
		t.Fatalf("want ErrWalkBudget, got %v", err)
	}
	if !when.IsZero() {
		t.Errorf("an abandoned walk returned a time (%v). A partial maximum is a confident wrong "+
			"answer about the one field an operator uses to decide a tree is dead.", when)
	}

	// GREEN control: unbounded, and generously bounded, both measure it.
	if _, err := newestWriteWithin(tree, 0); err != nil {
		t.Fatalf("control: an unbounded walk must succeed, got %v", err)
	}
	if _, err := newestWriteWithin(tree, time.Minute); err != nil {
		t.Fatalf("control: a generous budget must not fire, got %v", err)
	}
}
