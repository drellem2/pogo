package gitgc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The coverage gap this file closes (gh #97). cannottell_test.go pins
// RemoveWorktree's guard well, and every one of its tests passed while the
// SWEEP walked around that guard entirely: phase 1 ran its own dirty check,
// discarded the error it returned, and called RemoveWorktreeForce. No test ever
// drove a status-failing worktree through Sweep, which is precisely why the
// bypass was invisible to a green suite. A guard is only as good as its
// narrowest caller, so the caller is what gets tested here.
//
// The rule these all pin is now singular: IF WE COULD NOT READ THE TREE, WE DO
// NOT ACT ON IT. No ownership arm, no clock, no exceptions but the operator's
// explicit --force. Two earlier designs put a condition on that — a drain, then
// an mtime veto — and both were withdrawn; see RemoveWorktree for why. mtime
// survives with exactly one job, which is to be REPORTED.

// sweptCannotTell builds a concluded, non-live polecat worktree holding an
// untracked file, then breaks `git status` inside it for real. It returns the
// repo, the worktree path, and the path of the file that must survive.
func sweptCannotTell(t *testing.T, name string) (*testRepo, string, string) {
	t.Helper()
	r := newTestRepo(t)
	branch := BranchPrefix + name
	r.branch(branch)
	wtPath := r.worktreeOwnedBy(name, branch)
	precious := dirty(t, wtPath, "irreplaceable.go", "package wip // the only copy\n")
	damageGitPointer(t, wtPath)
	return r, wtPath, precious
}

func archivedTickets(name string) TicketIndex {
	return TicketIndex{"mg-" + name: TicketArchived}
}

// TestSweepRefusesCannotTellWorktree is the red-on-1edecb6 control, in the
// shape the triage reproduced: an archived ticket, no live polecat, and a
// worktree whose `git status` errors. Before this fix the sweep force-removed
// it — the untracked file was destroyed, the directory went, and phase 2 then
// deleted the branch ref, so the work was unreachable by any route.
func TestSweepRefusesCannotTellWorktree(t *testing.T) {
	r, wtPath, precious := sweptCannotTell(t, "damaged")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      archivedTickets("damaged"),
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if len(res.WorktreesRemoved) != 0 {
		t.Errorf("a worktree whose status could not be read must not be removed: %+v", res.WorktreesRemoved)
	}
	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("THE WORK WAS DESTROYED — a tree the sweep could not read must survive: %v", serr)
	}
	if _, serr := os.Stat(wtPath); serr != nil {
		t.Fatalf("worktree dir should survive a refused removal: %v", serr)
	}

	kept := findWorktreeAction(res.WorktreesKept, wtPath)
	if kept == nil {
		t.Fatalf("refused worktree missing from WorktreesKept: %+v", res.WorktreesKept)
	}
	if !strings.Contains(kept.Reason, "git status could not be read") {
		t.Errorf("keep reason must name the status failure, got: %s", kept.Reason)
	}
	if strings.Contains(kept.Reason, "uncommitted change(s)") {
		t.Errorf("cannot-tell must NOT be reported as dirty — that claims knowledge we do not have: %s", kept.Reason)
	}

	// The branch is still checked out in the tree we kept, so it must be kept
	// too, and said to be kept for that reason. This is the honest, accepted
	// cost: a pin nothing reclaims automatically, cleared by a human or by
	// --force. A visible pin beats an invisible deletion.
	br := findBranchAction(res.BranchesKept, "polecat-damaged")
	if br == nil || br.Reason != "checked out in a worktree" {
		t.Errorf("branch of a kept worktree should be kept as checked out, got %+v", br)
	}
	if len(res.BranchesDeleted) != 0 {
		t.Errorf("no branch should have been deleted: %+v", res.BranchesDeleted)
	}
}

// TestSweepCannotTellRefusalReportsAgeAndNeverActsOnIt pins the two halves that
// replaced everything the age used to decide.
//
// A drain was proposed — reclaim once dead AND old AND previously refused — and
// withdrawn: age is not emptiness, and a 30-day-old irreplaceable.go is exactly
// as unrecoverable as a 30-second-old one. An mtime veto replaced it and was
// withdrawn in turn, on a subtler point: a veto that expires has not resolved
// the contradiction it was built for, it has stopped being able to see it.
//
// So a 30-day-old unreadable tree is KEPT — the age bought nothing — and the
// line SAYS "untouched 30 days", because that is the signal a human needs to
// clear a pin that nothing else will ever clear.
func TestSweepCannotTellRefusalReportsAgeAndNeverActsOnIt(t *testing.T) {
	r, wtPath, precious := sweptCannotTell(t, "damaged")
	ageTree(t, wtPath, 30*24*time.Hour)

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      archivedTickets("damaged"),
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("age must not license a removal — the file is gone: %v", serr)
	}
	kept := findWorktreeAction(res.WorktreesKept, wtPath)
	if kept == nil {
		t.Fatalf("a 30-day-old unreadable tree must still be KEPT: removed=%+v", res.WorktreesRemoved)
	}
	if !strings.Contains(kept.Reason, "untouched 30 days") {
		t.Errorf("a permanent refusal must report the age so a human can act on it, got: %s", kept.Reason)
	}
}

// TestSweepCannotTellAgeIsMeasuredByWalkingNotStattingTheRoot pins the one
// property of the age REPORT that is easy to lose to an optimisation.
//
// The worktree root's mtime is not a cheap approximation of the tree's; it is a
// different quantity answering a different question, and it was measured blind
// to the case that matters. A live agent editing pkg/deep/work.go leaves root
// mtime untouched, so an operator reading "untouched 30 days" off the root
// would be told a tree is abandoned while somebody is working in it — and would
// clear the pin on the strength of it.
//
// This mattered more when the age drove a veto. It still matters, because a
// report that a human acts on is not decoration.
func TestSweepCannotTellAgeIsMeasuredByWalkingNotStattingTheRoot(t *testing.T) {
	r, wtPath, _ := sweptCannotTell(t, "damaged")
	deep := filepath.Join(wtPath, "pkg", "deep")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(deep, "work.go")
	if err := os.WriteFile(work, []byte("package deep // in flight\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Everything is thirty days old...
	ageTree(t, wtPath, 30*24*time.Hour)
	rootBefore := modTime(t, wtPath)
	// ...except one file deep in the tree, which somebody just wrote.
	now := time.Now()
	if err := os.Chtimes(work, now, now); err != nil {
		t.Fatal(err)
	}
	if got := modTime(t, wtPath); !got.Equal(rootBefore) {
		t.Fatalf("fixture is not testing what it claims: root mtime moved (%s -> %s) on a deep edit", rootBefore, got)
	}

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      archivedTickets("damaged"),
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	kept := findWorktreeAction(res.WorktreesKept, wtPath)
	if kept == nil {
		t.Fatalf("worktree missing from WorktreesKept: %+v", res.WorktreesKept)
	}
	if strings.Contains(kept.Reason, "30 days") {
		t.Errorf("the age was read off the ROOT, which is blind to edits below it — "+
			"an operator would be told this abandoned while someone is working in it: %s", kept.Reason)
	}
	if !strings.Contains(kept.Reason, "untouched 0s") {
		t.Errorf("the age must come from the newest file in a WALK of the tree, got: %s", kept.Reason)
	}
}

// TestSweepCannotTellUnenumerableStillRefusesAndSaysSo covers the shape where
// the worktree root is stat-able but its contents cannot be listed.
//
// It needs no rule of its own any more: "we cannot list the files" is subsumed
// by "we cannot read the tree, so we do not act on it", and it was only ever a
// separate case because an earlier design had a veto that would have had
// nothing to adjudicate on. What is left is a REPORTING obligation — the line
// must say the age could not be measured, rather than omit it and read as
// though the tree were fresh.
func TestSweepCannotTellUnenumerableStillRefusesAndSaysSo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unreadable")
	}
	r, wtPath, precious := sweptCannotTell(t, "damaged")

	if err := os.Chmod(wtPath, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored before TempDir cleanup, which cannot remove an unreadable dir.
	t.Cleanup(func() { os.Chmod(wtPath, 0o755) })
	if _, err := os.ReadDir(wtPath); err == nil {
		t.Skip("filesystem does not enforce directory permissions here")
	}

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      archivedTickets("damaged"),
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	kept := findWorktreeAction(res.WorktreesKept, wtPath)
	if kept == nil {
		t.Fatalf("an unlistable tree must be kept, not removed (removed=%+v)", res.WorktreesRemoved)
	}
	if !strings.Contains(kept.Reason, "age unknown") {
		t.Errorf("a refusal that could not measure the age must SAY so rather than omit it, got: %s", kept.Reason)
	}
	os.Chmod(wtPath, 0o755)
	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("the file must survive a tree we could neither read nor list: %v", serr)
	}
}

// TestSweepCannotTellLogNeverNamesAnInnocentReason is the second half of gh #97
// and the part pm-pogo rated highest.
//
// The per-action GC log shipped as gh #94's remedy — the way to find out
// whether the GC took your worktree and why — confidently named an innocent
// reason on this exact path: `removed worktree <path> (owner damaged, branch
// polecat-damaged): ticket archived`, with the discarded status error appearing
// nowhere. A forensic log that names an innocent cause is worse than silence: a
// missing line prompts investigation, a plausible one ends it.
//
// Both outcomes are asserted, because both are reachable and both used to lie
// by omission: the refusal (which did not log at all) and the --force removal
// (which logged only the ticket state).
func TestSweepCannotTellLogNeverNamesAnInnocentReason(t *testing.T) {
	for _, tc := range []struct {
		name  string
		force bool
		verb  string
	}{
		{name: "refused", force: false, verb: "kept worktree "},
		{name: "removed-under-force", force: true, verb: "removed worktree "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := sweptCannotTell(t, "damaged")
			var lines []string
			if _, err := Sweep(Options{
				Repo:         r.dir,
				TargetBranch: "main",
				Tickets:      archivedTickets("damaged"),
				Force:        tc.force,
				Logf:         func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
			}); err != nil {
				t.Fatalf("Sweep: %v", err)
			}

			line := findLine(lines, tc.verb)
			if line == "" {
				t.Fatalf("no %q line; a cannot-tell decision must reach the log. log was:\n%s",
					tc.verb, strings.Join(lines, "\n"))
			}
			if !strings.Contains(line, "git status could not be read") {
				t.Errorf("line must say the status could not be read, got: %s", line)
			}
			// The killer: the reason must not be JUST the ticket state. That
			// string may still appear — the ticket really is archived, and it
			// is why the tree was a candidate — but it must not stand alone as
			// the explanation for what happened to the files.
			if strings.HasSuffix(strings.TrimSpace(line), "ticket archived") {
				t.Errorf("log names an innocent reason and nothing else: %s", line)
			}
		})
	}
}

// TestSweepForceStillDiscardsCannotTell pins the escape hatch, which is now the
// ONLY route by which an unreadable worktree is ever removed. An operator's
// explicit --force is a positive reason to discard, and without it a refused
// tree pins its branch with no way for a human to say "yes, I know, take it".
// It is also the whole of what bounds the leak this design accepts.
//
// The log line still has to be honest about it, so --force names the status
// failure too: the operator gets what they asked for AND a true record.
func TestSweepForceStillDiscardsCannotTell(t *testing.T) {
	r, wtPath, precious := sweptCannotTell(t, "damaged")

	var lines []string
	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      archivedTickets("damaged"),
		Force:        true,
		Logf:         func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, serr := os.Stat(precious); !os.IsNotExist(serr) {
		t.Fatalf("--force must still discard an unreadable tree; stat gave %v (kept=%+v)", serr, res.WorktreesKept)
	}
	if findWorktreeAction(res.WorktreesRemoved, wtPath) == nil {
		t.Errorf("forced removal not reported: %+v", res.WorktreesRemoved)
	}
	line := findLine(lines, "removed worktree ")
	if !strings.Contains(line, "--force was given") {
		t.Errorf("a forced removal of an unreadable tree must say so, got: %s", line)
	}
}

// TestSweepDryRunMatchesApplyOnCannotTell guards the property that makes a dry
// run worth running. `pogo gc` without --apply is how an operator finds out
// what a sweep would do; if the guard only ran on the apply path, a dry run
// would report a removal that an apply would then refuse — advertising a
// destruction that never happens, which is the same class of false report as
// the log reason above.
func TestSweepDryRunMatchesApplyOnCannotTell(t *testing.T) {
	r, wtPath, precious := sweptCannotTell(t, "damaged")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      archivedTickets("damaged"),
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.WorktreesRemoved) != 0 {
		t.Errorf("dry run says it WOULD remove a tree an apply refuses: %+v", res.WorktreesRemoved)
	}
	if findWorktreeAction(res.WorktreesKept, wtPath) == nil {
		t.Errorf("dry run must report the refusal: %+v", res.WorktreesKept)
	}
	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("a dry run must not touch anything: %v", serr)
	}
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

// TestReportedAgeAcrossCannotTellShapes pins the number an operator acts on,
// across every shape that makes `git status` fail.
//
// Nothing held these before. newestWrite's doc made a positive per-shape claim
// and no test reached it — a comment asserting behaviour nothing exercises is a
// mild version of what this whole ticket is about. They matter more since the
// veto was withdrawn: the age is no longer a second layer under a guard, it is
// the ONLY signal about a pin that nothing will ever clear automatically, so a
// wrong number here is a dead tree left pinned or a live one cleared.
//
// The pointer-damage row is the one with history. While .git was counted in the
// walk it reported `untouched 0s` for a tree abandoned for a month, because
// corrupting the pointer writes a file inside the tree — wrong in the direction
// that discourages reclamation, on the shape gh #97 was reproduced with.
func TestReportedAgeAcrossCannotTellShapes(t *testing.T) {
	const month = 30 * 24 * time.Hour
	for _, tc := range []struct {
		name      string
		damage    func(t *testing.T, wtPath string)
		wantInMsg string
		rootPerm  bool // damage makes the root unreadable; restore before asserting
	}{
		{
			name:      "damaged-git-pointer",
			damage:    func(t *testing.T, wt string) { damageGitPointer(t, wt) },
			wantInMsg: "untouched 30 days",
		},
		{
			name: "eacces-on-dotgit",
			damage: func(t *testing.T, wt string) {
				gitFile := filepath.Join(wt, ".git")
				if err := os.Chmod(gitFile, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(gitFile, 0o644) })
				if _, _, err := WorktreeDirty(wt); err == nil {
					t.Skip("filesystem does not enforce file permissions here")
				}
			},
			// chmod moves ctime, not mtime, so this shape reads correctly
			// whether or not .git is counted. It is the control that shows the
			// pointer-damage row is about the WRITE, not about permissions.
			wantInMsg: "untouched 30 days",
		},
		{
			name: "truncated-index",
			damage: func(t *testing.T, wt string) {
				// A linked worktree's index lives in the ADMIN dir named by the
				// .git pointer, not in the worktree — which is why this shape
				// reads cold: the damage never touches the tree at all.
				b, err := os.ReadFile(filepath.Join(wt, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				var gitdir string
				if _, err := fmt.Sscanf(string(b), "gitdir: %s", &gitdir); err != nil {
					t.Fatalf("parsing .git pointer %q: %v", b, err)
				}
				if err := os.WriteFile(filepath.Join(gitdir, "index"), []byte("GARBAGE"), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, _, err := WorktreeDirty(wt); err == nil {
					t.Skip("a truncated index did not make git status fail here")
				}
			},
			wantInMsg: "untouched 30 days",
		},
		{
			name: "eacces-on-worktree-root",
			damage: func(t *testing.T, wt string) {
				damageGitPointer(t, wt)
				if err := os.Chmod(wt, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(wt, 0o755) })
				if _, err := os.ReadDir(wt); err == nil {
					t.Skip("filesystem does not enforce directory permissions here")
				}
			},
			// The tree cannot be listed, so there is no age to report — and the
			// line must SAY that rather than omit it, which would read as fresh.
			wantInMsg: "age unknown",
			rootPerm:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("running as root: chmod cannot restrict access")
			}
			r := newTestRepo(t)
			r.branch("polecat-shape")
			wtPath := r.worktreeOwnedBy("shape", "polecat-shape")
			precious := dirty(t, wtPath, "irreplaceable.go", "package wip\n")

			// Age the CONTENTS first, so every shape starts from a tree that has
			// genuinely been cold for a month; then break it.
			ageTree(t, wtPath, month)
			tc.damage(t, wtPath)

			res, err := Sweep(Options{
				Repo:         r.dir,
				TargetBranch: "main",
				Tickets:      TicketIndex{"mg-shape": TicketArchived},
			})
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}
			if tc.rootPerm {
				os.Chmod(wtPath, 0o755)
			}

			kept := findWorktreeAction(res.WorktreesKept, wtPath)
			if kept == nil {
				t.Fatalf("every cannot-tell shape must be KEPT; removed=%+v", res.WorktreesRemoved)
			}
			if !strings.Contains(kept.Reason, tc.wantInMsg) {
				t.Errorf("reported age is wrong for this shape:\n  want substring: %s\n  got reason:     %s",
					tc.wantInMsg, kept.Reason)
			}
			if _, serr := os.Stat(precious); serr != nil {
				t.Fatalf("THE WORK WAS DESTROYED: %v", serr)
			}
		})
	}
}
