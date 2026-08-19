package strandedwork

import (
	"testing"
)

// The mg-aed4 marker tests. See RescuePrefix for the convention, its observed
// population (one rescue event, five branches, one author) and why the match is
// deliberately WIDER than the pre-registration one — the asymmetry runs the
// other way, so a miss is the expensive direction here.

// The five subjects below are the whole live population, transcribed from the
// mg-51bf rescue's branches on 2026-08-19. They are the only evidence this
// convention exists, so they are the test.
var liveRescueSubjects = []string{
	"RESCUE(mg-1d05): core-budget prompt clause covering self-parallelising libraries, " +
		"recovered from preserved worktree p1d05 — UNREVIEWED, not this committer's work (mg-51bf)",
	"RESCUE(mg-516e): fleet-progress detector recovered from preserved worktree p516e — " +
		"UNREVIEWED, not this committer's work (mg-51bf)",
	"RESCUE(mg-9d4e): merged-work dispatch gate + declares-remainder close path recovered " +
		"from preserved worktree p9d4e — UNREVIEWED, not this committer's work (mg-51bf)",
	"RESCUE(mg-fbaf): `pogo agent env` + injected-env catalogue recovered from preserved " +
		"worktree pfbaf — UNREVIEWED, not this committer's work (mg-51bf)",
	"RESCUE(mg-e7ff): pogo-side depends dispatch gate recovered from preserved worktree " +
		"pe7ff — UNREVIEWED, not this committer's work (mg-51bf)",
}

// The SECOND spelling, and the majority one: 27 of the 32 rescue commits on this
// box came from the mg-11fa rescue and parenthesise the AGENT whose worktree the
// work came out of, not a work item. Only the prefix is common to both forms —
// which is why the predicate is the prefix and nothing else.
var liveRescueSubjectsMG11FA = []string{
	"RESCUE(p6b2d): working-tree snapshot of a worktree mid-rebase (mg-11fa)",
	"RESCUE(ca397): 13 uncommitted path(s) from a retained worktree (mg-11fa)",
	"RESCUE(p6476): 2 uncommitted path(s) from a retained worktree (mg-11fa)",
	"RESCUE(75f0): 10 uncommitted path(s) from a retained worktree (mg-11fa)",
	"RESCUE(z48d8): 1 uncommitted path(s) from a retained worktree (mg-11fa)",
}

func TestIsRescueMatchesTheLivePopulation(t *testing.T) {
	for _, s := range append(append([]string{}, liveRescueSubjects...), liveRescueSubjectsMG11FA...) {
		if !(Commit{Subject: s}).IsRescue() {
			t.Errorf("IsRescue(%q) = false; this is a live rescue subject, and a miss prints a "+
				"paste-ready submit for work that has never been built", s)
		}
	}
}

// TestBothSpellingsYieldTheRescueTracker. The two forms disagree about what goes
// in the parentheses and agree about the trailing id, which is the one this
// report prints — so a rule that read the payload would have covered one event
// and silently missed the other, and the missed one is 27 of the 32.
func TestBothSpellingsYieldTheRescueTracker(t *testing.T) {
	for _, s := range liveRescueSubjectsMG11FA {
		c := Commit{Subject: s}
		if got := c.RescueTracker(); got != "mg-11fa" {
			t.Errorf("RescueTracker(%q) = %q, want mg-11fa", s, got)
		}
		if got := c.RescuedItem(); got != "" {
			t.Errorf("RescuedItem(%q) = %q; this form parenthesises an AGENT NAME, not a work "+
				"item, and reporting one as the other is worse than reporting none", s, got)
		}
	}
}

func TestIsRescueMatching(t *testing.T) {
	for _, tc := range []struct {
		subject string
		want    bool
	}{
		{"RESCUE(mg-51bf): recovered from a preserved worktree", true},
		{"rescue(mg-51bf): same, lowercased", true},
		{"  RESCUE(mg-51bf): leading space", true},
		{"RESCUE: no tracker named", true},
		{"rescue: no tracker, lowercased", true},

		// The marker has to BEGIN the subject. Anything else is a commit that
		// merely talks about a rescue, and taking its remedy away costs a reader a
		// build they did not need on a branch that was gated normally.
		{"fix(refinery): rescue the queue from a wedged worker (mg-1111)", false},
		{"feat(gitgc): preserved-worktree rescue path (mg-2222)", false},
		{"docs: rescued 1026 lines by hand (mg-3333)", false},
		{"RESCUE", false},
		{"RESCUED(mg-51bf): a different word", false},
		{"", false},
	} {
		if got := (Commit{Subject: tc.subject}).IsRescue(); got != tc.want {
			t.Errorf("IsRescue(%q) = %v, want %v", tc.subject, got, tc.want)
		}
	}
}

// TestTheTwoIdsInARescueSubjectAreKeptApart. `RESCUE(mg-516e): … (mg-51bf)`
// names TWO different items — the one whose work was recovered, and the rescue
// that recovered it — and a report joining branches to items already knows the
// first, because it is the row's own subject. The first draft of RescueTracker
// returned that redundant one, which is exactly how the mistake survived a test
// that passed: both are ids, both look right, and only one tells a reader
// anything they did not have.
func TestTheTwoIdsInARescueSubjectAreKeptApart(t *testing.T) {
	c := Commit{Subject: liveRescueSubjects[1]}
	if got := c.RescuedItem(); got != "mg-516e" {
		t.Errorf("RescuedItem() = %q, want mg-516e", got)
	}
	if got := c.RescueTracker(); got != "mg-51bf" {
		t.Errorf("RescueTracker() = %q, want mg-51bf — the rescue, not the item it recovered", got)
	}
	for _, s := range liveRescueSubjects {
		if got := (Commit{Subject: s}).RescueTracker(); got != "mg-51bf" {
			t.Errorf("RescueTracker(%q) = %q, want mg-51bf; all five came from one rescue", s, got)
		}
	}
	if got := (Commit{Subject: "RESCUE: no tracker named"}).RescueTracker(); got != "" {
		t.Errorf("RescueTracker() = %q on a subject naming no id, want \"\"", got)
	}
	if got := (Commit{Subject: "RESCUE: recovered by hand (mg-51bf)"}).RescueTracker(); got != "mg-51bf" {
		t.Errorf("RescueTracker() = %q on the bare-colon form, want mg-51bf", got)
	}
	if got := (Commit{Subject: "feat: ordinary work (mg-1234)"}).RescueTracker(); got != "" {
		t.Errorf("RescueTracker() = %q on a NON-rescue subject; every commit in this repo has a "+
			"trailing id and none of them is a rescue tracker", got)
	}
}

// TestInspectPopulatesRescueWithoutChangingTheDisposition is the load-bearing
// half. A rescue branch IS stranded — its commits really are absent from the
// target — so the disposition every caller switches on must not move, or the
// spawn-time guard stops refusing a dispatch it has always refused. What the
// field adds is the information a REMEDY needs.
func TestInspectPopulatesRescueWithoutChangingTheDisposition(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commit("fleetprogress.go", liveRescueSubjects[1])
	r.push("polecat-p516e")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-p516e", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Disposition != DispositionResubmit {
		t.Errorf("Disposition = %q, want %q — a rescue branch is still stranded, and the "+
			"dispatch gate reads this field", f.Disposition, DispositionResubmit)
	}
	if f.Rescue == nil {
		t.Fatal("Rescue is nil on a branch whose unmerged commit carries the marker")
	}
	if f.Rescue.Subject != liveRescueSubjects[1] {
		t.Errorf("Rescue.Subject = %q", f.Rescue.Subject)
	}
}

// TestMergedRescueCommitIsNotFlagged is the predicate control: only an UNMERGED
// rescue commit is evidence of unbuilt work outside the target. One that landed
// was built by whatever gate merged it, and a permanent label on the branch
// would be a permanent false refusal.
func TestMergedRescueCommitIsNotFlagged(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commit("fleetprogress.go", liveRescueSubjects[1])
	r.push("polecat-p516e")
	r.checkout("main")
	r.git("merge", "-q", "--ff-only", "polecat-p516e")
	r.push("main")

	f, err := Inspect(r.dir, "polecat-p516e", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Rescue != nil {
		t.Errorf("Rescue = %+v on a branch with no unmerged commits", f.Rescue)
	}
}

// TestOrdinaryBranchHasNoRescue is the false-positive control on Inspect itself.
func TestOrdinaryBranchHasNoRescue(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery, all five cases caught (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-q9a19", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Rescue != nil {
		t.Errorf("Rescue = %+v on an ordinary branch", f.Rescue)
	}
}
