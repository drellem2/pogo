package gitgc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSameDeadOwnerGetsTheSameVerdict is mg-bdda, run as the reviewer ran it:
// one owner, one directory name, one set of ticket states, and the ONLY thing
// that differs between the two arms is whether git still holds the worktree
// registration.
//
// THE DEFECT. mg-dd92 moved worktree liveness to the owner but left ticket
// state split — phase 1 classified a registered worktree by the branch checked
// out inside it, phase 1b classified an orphan dir by the directory name.
// Owner 0047's ticket is archived and 0047 is dead; its tree is parked on
// foreign, still-in-flight polecat-a773. Before this fix:
//
//	registered -> KEPT    ("ticket in-flight")   <- inherited from a773
//	orphan     -> REMOVED ("orphan dir, ticket archived")
//
// A dead polecat's tree was pinned indefinitely by a foreign ticket that may
// never conclude, and a `git worktree prune` between two sweeps flipped the
// verdict on the same files. Both arms must now reach the owner's answer.
func TestSameDeadOwnerGetsTheSameVerdict(t *testing.T) {
	// Identical in both arms: 0047 is archived and dead, a773 is in flight.
	tickets := TicketIndex{"mg-0047": TicketArchived, "mg-a773": TicketInFlight}

	t.Run("registered worktree", func(t *testing.T) {
		r := newTestRepo(t)
		r.branch("polecat-0047")
		r.branch("polecat-a773")
		// 0047's tree, checked out on a773's branch — the review/QA shape.
		wt := r.worktreeOwnedBy("0047", "polecat-a773")

		res, err := Sweep(Options{Repo: r.dir, TargetBranch: "main", Tickets: tickets})
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if len(res.Errors) != 0 {
			t.Fatalf("unexpected sweep errors: %v", res.Errors)
		}

		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Errorf("dead owner 0047's tree should be reaped, not pinned by foreign in-flight a773; stat err = %v", err)
		}
		removed := findWorktreeAction(res.WorktreesRemoved, wt)
		if removed == nil {
			t.Fatalf("no removal reported for %s; kept=%+v", wt, res.WorktreesKept)
		}
		if !strings.Contains(removed.Reason, "owner's ticket archived") {
			t.Errorf("removal reason = %q, want it keyed on the owner", removed.Reason)
		}

		// NOTHING IS LOST by taking the directory. The foreign branch is
		// classified on its own name in phase 2, so it survives un-checked-out
		// with every commit still reachable — that is what makes reaping the
		// tree safe rather than merely more aggressive.
		remaining := branchList(t, r)
		if !remaining["polecat-a773"] {
			t.Error("the foreign in-flight branch was deleted; removing the tree must only un-check-it-out")
		}
		if kb := findBranchAction(res.BranchesKept, "polecat-a773"); kb == nil || kb.State != TicketInFlight {
			t.Errorf("polecat-a773 should be kept on its OWN in-flight ticket, got %+v", kb)
		}
		if remaining["polecat-0047"] {
			t.Error("0047's own archived branch should still be deleted")
		}
	})

	t.Run("orphan dir", func(t *testing.T) {
		r := newTestRepo(t)
		polecats := r.polecatsDir()
		dir := filepath.Join(polecats, "0047")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}

		res, err := Sweep(Options{Repo: r.dir, TargetBranch: "main", Tickets: tickets, PolecatsDir: polecats})
		if err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if len(res.Errors) != 0 {
			t.Fatalf("unexpected sweep errors: %v", res.Errors)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("orphan dir of archived owner 0047 should be reaped; stat err = %v", err)
		}
		removed := findWorktreeAction(res.WorktreesRemoved, dir)
		if removed == nil {
			t.Fatalf("no removal reported for %s; kept=%+v", dir, res.WorktreesKept)
		}
		if !strings.Contains(removed.Reason, "owner's ticket archived") {
			t.Errorf("removal reason = %q, want it keyed on the owner", removed.Reason)
		}
	})
}

// TestSweepKeepsTreeWhoseOwnerIsInFlight pins the direction the mg-bdda re-key
// makes MORE conservative, so it cannot be reverted by accident.
//
// Owner 0047 is in flight but not live — the shape of a polecat that died or
// has not been respawned — and its tree happens to be parked on a foreign,
// archived branch. Classified by the branch, the tree collected; classified by
// the owner, it is kept, because the work it was created for has not concluded
// and a respawn lands on that same path.
func TestSweepKeepsTreeWhoseOwnerIsInFlight(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-0047")
	r.branch("polecat-beef")
	wt := r.worktreeOwnedBy("0047", "polecat-beef")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-0047": TicketInFlight, "mg-beef": TicketArchived},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected sweep errors: %v", res.Errors)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("in-flight owner 0047's tree should be kept: %v", err)
	}
	kept := findWorktreeAction(res.WorktreesKept, wt)
	if kept == nil {
		t.Fatalf("no kept entry for %s; removed=%+v", wt, res.WorktreesRemoved)
	}
	if !strings.Contains(kept.Reason, "owner's ticket in-flight") {
		t.Errorf("kept reason = %q, want it keyed on the owner", kept.Reason)
	}
	// A kept tree pins its branch — the ref cannot be deleted while checked out.
	if kb := findBranchAction(res.BranchesKept, "polecat-beef"); kb == nil || kb.Reason != "checked out in a worktree" {
		t.Errorf("polecat-beef should be kept as checked out in a worktree, got %+v", kb)
	}
}

// TestUnresolvableOwnerFallsBackToBranch covers the symmetric defect that kept
// mg-dd92 from re-keying phase 1 in the first place: a worktree whose basename
// resolves to no work item — a legacy layout, a hand-made review tree — would
// classify TicketUnknown under an owner-only rule and never be reaped again.
//
// The branch answers only in that case, and the reason line says so, so a "why
// is this still here" question is answerable from the log rather than from
// reading candidateIDs. Both arms matter: the fallback has to be able to say
// "reap" (or it is just stranding with extra words) and to say "keep".
func TestUnresolvableOwnerFallsBackToBranch(t *testing.T) {
	cases := []struct {
		name        string
		branchState TicketState
		wantRemoved bool
	}{
		{"concluded branch reaps", TicketArchived, true},
		{"in-flight branch keeps", TicketInFlight, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newTestRepo(t)
			r.branch("polecat-aaaa")
			// "workshop" contains no 4-hex run, so no candidate ID can be
			// recovered from it — the unresolvable case, for real.
			if id, _ := (TicketIndex{"mg-aaaa": c.branchState}).OwnerState("workshop"); id != "" {
				t.Fatalf("setup is not the unresolvable case: owner resolved to %q", id)
			}
			wt := r.worktreeOwnedBy("workshop", "polecat-aaaa")

			res, err := Sweep(Options{
				Repo:         r.dir,
				TargetBranch: "main",
				Tickets:      TicketIndex{"mg-aaaa": c.branchState},
			})
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if len(res.Errors) != 0 {
				t.Fatalf("unexpected sweep errors: %v", res.Errors)
			}

			_, statErr := os.Stat(wt)
			gone := os.IsNotExist(statErr)
			if gone != c.wantRemoved {
				t.Errorf("worktree removed = %v, want %v (stat err = %v)", gone, c.wantRemoved, statErr)
			}

			acted := findWorktreeAction(res.WorktreesRemoved, wt)
			if acted == nil {
				acted = findWorktreeAction(res.WorktreesKept, wt)
			}
			if acted == nil {
				t.Fatalf("worktree %s was neither removed nor kept in the report", wt)
			}
			if !strings.Contains(acted.Reason, "branch's ticket") || !strings.Contains(acted.Reason, "no work item") {
				t.Errorf("reason = %q, want it to say the branch decided because the owner resolves to nothing", acted.Reason)
			}
		})
	}
}

// TestOwnerStateAndBranchStateAreOneResolver pins that the two keys differ only
// in WHICH string they read, never in how they read it. If they drifted apart,
// a naming spelling could resolve for a branch and not for the identically
// named directory — which is the split mg-bdda closed, reintroduced one level
// down.
func TestOwnerStateAndBranchStateAreOneResolver(t *testing.T) {
	idx := TicketIndex{
		"mg-30d5": TicketArchived,
		"mg-9cdc": TicketDone,
		"mg-a1d8": TicketInFlight,
		"mg-30eb": TicketArchived,
		"mg-06cb": TicketDone,
	}
	for _, name := range []string{
		"30d5", "mg-9cdc", "cat-mg-a1d8", "30eb-fix", "p06cb", "nosuchthing", "",
	} {
		wantID, wantState := idx.OwnerState(name)
		gotID, gotState := idx.BranchState(BranchPrefix + name)
		if gotID != wantID || gotState != wantState {
			t.Errorf("BranchState(%q) = (%q, %v), OwnerState(%q) = (%q, %v); the two must agree",
				BranchPrefix+name, gotID, gotState, name, wantID, wantState)
		}
	}
	// A non-polecat branch still resolves to nothing rather than being read as
	// a bare name.
	if id, state := idx.BranchState("main"); id != "" || state != TicketUnknown {
		t.Errorf(`BranchState("main") = (%q, %v), want ("", unknown)`, id, state)
	}
}
