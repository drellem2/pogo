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
//
// Nothing here establishes that the owner is DEAD. Absence from LivePolecats is
// not that evidence: it is what a caller has when it did not look, or looked
// with an instrument that failed. So the sweep must keep.
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
	// cost of the refusal: a pin nothing reclaims automatically, cleared by a
	// human or by --force. A visible pin beats an invisible deletion.
	br := findBranchAction(res.BranchesKept, "polecat-damaged")
	if br == nil || br.Reason != "checked out in a worktree" {
		t.Errorf("branch of a kept worktree should be kept as checked out, got %+v", br)
	}
	if len(res.BranchesDeleted) != 0 {
		t.Errorf("no branch should have been deleted: %+v", res.BranchesDeleted)
	}
}

// TestSweepCannotTellOwnerVerdictSplit is the discriminator itself. The same
// tree, the same unreadable status, two verdicts, two outcomes — and the split
// is what keeps the fix from converting a data-loss bug into an unbounded leak.
//
// The OwnerGone tree is aged, because death evidence alone does not license the
// removal; see TestSweepCannotTellVetoedByRecentWrite for the other condition.
func TestSweepCannotTellOwnerVerdictSplit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verdicts map[string]WorktreeOwner
		removed  bool
	}{
		{"owner-gone", map[string]WorktreeOwner{"damaged": OwnerGone}, true},
		{"owner-unproven", map[string]WorktreeOwner{"damaged": OwnerUnproven}, false},
		// A name the caller said nothing about is the zero value, and the zero
		// value refuses. This is the arm that catches a caller which simply
		// never filled the map in — cmd/pogo, by construction.
		{"absent-from-map", map[string]WorktreeOwner{}, false},
		{"nil-map", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, wtPath, precious := sweptCannotTell(t, "damaged")
			ageTree(t, wtPath, 2*quietWindow)

			res, err := Sweep(Options{
				Repo:          r.dir,
				TargetBranch:  "main",
				Tickets:       archivedTickets("damaged"),
				OwnerVerdicts: tc.verdicts,
			})
			if err != nil {
				t.Fatalf("Sweep: %v", err)
			}

			_, statErr := os.Stat(precious)
			gone := os.IsNotExist(statErr)
			if gone != tc.removed {
				t.Fatalf("file removed = %v, want %v (removed=%+v kept=%+v)",
					gone, tc.removed, res.WorktreesRemoved, res.WorktreesKept)
			}
			if tc.removed && findWorktreeAction(res.WorktreesRemoved, wtPath) == nil {
				t.Errorf("removal not reported: %+v", res.WorktreesRemoved)
			}
			if !tc.removed && findWorktreeAction(res.WorktreesKept, wtPath) == nil {
				t.Errorf("refusal not reported: %+v", res.WorktreesKept)
			}
		})
	}
}

// TestSweepCannotTellVetoedByRecentWrite is the mtime VETO, and the asymmetry
// it rests on is the whole reason a timestamp appears in this package at all:
//
//	mtime RECENT -> something is writing   SOUND
//	mtime OLD    -> nobody holds it        UNSOUND
//
// So a timestamp may FORBID and may never AUTHORISE. Here it forbids: we hold
// positive evidence that the owner is dead, and something wrote to the tree
// anyway. That is a contradiction, and it resolves in favour of the files.
//
// The write is deliberately DEEP. The worktree root's own mtime does not move
// when pkg/deep/work.go is edited, so a veto that statted the root would be
// blind to exactly the live agent it exists to protect.
func TestSweepCannotTellVetoedByRecentWrite(t *testing.T) {
	r, wtPath, precious := sweptCannotTell(t, "damaged")
	deep := filepath.Join(wtPath, "pkg", "deep")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(deep, "work.go")
	if err := os.WriteFile(work, []byte("package deep // in flight\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Everything is old...
	ageTree(t, wtPath, 2*quietWindow)
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
		Repo:          r.dir,
		TargetBranch:  "main",
		Tickets:       archivedTickets("damaged"),
		OwnerVerdicts: map[string]WorktreeOwner{"damaged": OwnerGone},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("death evidence must NOT override a tree somebody is still writing to: %v", serr)
	}
	kept := findWorktreeAction(res.WorktreesKept, wtPath)
	if kept == nil {
		t.Fatalf("vetoed worktree missing from WorktreesKept: %+v", res.WorktreesKept)
	}
	if !strings.Contains(kept.Reason, "written to") {
		t.Errorf("veto reason must say the tree was written to recently, got: %s", kept.Reason)
	}
}

// TestSweepCannotTellVetoOverRefusesOnPointerDamage pins the HAZARD, and pins
// which DIRECTION it is allowed to fail in.
//
// The damage that makes `git status` fail can itself refresh mtime, unevenly:
// corrupting a worktree's .git pointer WRITES A FILE INSIDE THE TREE, so a
// freshly-damaged tree looks freshly worked-on, while chmod damage moves ctime
// and a truncated index lives outside the tree — both of those look cold. The
// signal is perturbed by the same event it is being asked to adjudicate.
//
// The veto therefore over-refuses here: an otherwise stone-cold tree is kept
// because its pointer was damaged a moment ago. That is the SAFE direction and
// it is asserted deliberately — a refreshed clock costs us a collection we could
// have made, never a file. The tempting "fix" is to exclude .git from the walk,
// which would trade this safe over-refusal for collecting a tree that looks
// live, so the test exists to make that trade fail loudly rather than silently.
func TestSweepCannotTellVetoOverRefusesOnPointerDamage(t *testing.T) {
	r, wtPath, precious := sweptCannotTell(t, "damaged")
	ageTree(t, wtPath, 30*24*time.Hour)

	// Re-touch ONLY .git, exactly as the damage event itself does. Every file
	// an agent could have written is 30 days old.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(wtPath, ".git"), now, now); err != nil {
		t.Fatal(err)
	}

	res, err := Sweep(Options{
		Repo:          r.dir,
		TargetBranch:  "main",
		Tickets:       archivedTickets("damaged"),
		OwnerVerdicts: map[string]WorktreeOwner{"damaged": OwnerGone},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("the veto must fail toward KEEPING: %v", serr)
	}
	if findWorktreeAction(res.WorktreesKept, wtPath) == nil {
		t.Errorf("over-refusal not reported as a keep: removed=%+v", res.WorktreesRemoved)
	}
}

// TestSweepCannotTellRefusalReportsAge pins what REPLACED the drain.
//
// A drain was proposed — reclaim once dead AND old AND previously refused — and
// withdrawn: age is not emptiness, and a 30-day-old irreplaceable.go is exactly
// as unrecoverable as a 30-second-old one. So the refusal is PERMANENT, and
// what the age buys is a human's decision instead of a machine's: the line has
// to say how long the tree has been untouched, or the operator holding a pinned
// worktree has no cheap way to judge it.
//
// The other half is asserted too — that the age DECIDES nothing here. A tree
// aged well past the veto window is still kept, because nothing established
// that its owner is dead.
func TestSweepCannotTellRefusalReportsAge(t *testing.T) {
	r, wtPath, precious := sweptCannotTell(t, "damaged")
	ageTree(t, wtPath, 30*24*time.Hour)

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      archivedTickets("damaged"),
		// No verdict: OwnerUnproven, the case carved out for human judgement.
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("age must not drain a refusal — the file is gone: %v", serr)
	}
	kept := findWorktreeAction(res.WorktreesKept, wtPath)
	if kept == nil {
		t.Fatalf("a 30-day-old unreadable tree must still be KEPT: removed=%+v", res.WorktreesRemoved)
	}
	if !strings.Contains(kept.Reason, "untouched 30 days") {
		t.Errorf("a permanent refusal must report the age so a human can act on it, got: %s", kept.Reason)
	}
}

// TestSweepCannotTellUnenumerableRefuses covers the one shape where the veto
// could fail open: the worktree root is stat-able but its contents cannot be
// enumerated, so there is no newest-file mtime to check.
//
// The alternative is to degrade to the ROOT directory's mtime, and that was
// measured to be blind to edits below it — a live agent editing pkg/deep/work.go
// leaves root mtime untouched (see TestSweepCannotTellVetoedByRecentWrite,
// which asserts exactly that). Deleting on a signal measured not to fire is
// worse than pinning one worktree in a rare shape, so a tree we cannot list is
// its own refusal: a cannot-tell about the cannot-tell.
func TestSweepCannotTellUnenumerableRefuses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unreadable")
	}
	r, wtPath, precious := sweptCannotTell(t, "damaged")
	ageTree(t, wtPath, 2*quietWindow)

	if err := os.Chmod(wtPath, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored before TempDir cleanup, which cannot remove an unreadable dir.
	t.Cleanup(func() { os.Chmod(wtPath, 0o755) })
	if _, err := os.ReadDir(wtPath); err == nil {
		t.Skip("filesystem does not enforce directory permissions here")
	}

	res, err := Sweep(Options{
		Repo:          r.dir,
		TargetBranch:  "main",
		Tickets:       archivedTickets("damaged"),
		OwnerVerdicts: map[string]WorktreeOwner{"damaged": OwnerGone},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	kept := findWorktreeAction(res.WorktreesKept, wtPath)
	if kept == nil {
		t.Fatalf("an unlistable tree must be kept, not removed (removed=%+v)", res.WorktreesRemoved)
	}
	if !strings.Contains(kept.Reason, "cannot be listed") {
		t.Errorf("reason must say the tree could not be listed, got: %s", kept.Reason)
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
// by omission: the refusal (which did not log at all) and the death-evidenced
// removal (which logged only the ticket state).
func TestSweepCannotTellLogNeverNamesAnInnocentReason(t *testing.T) {
	for _, tc := range []struct {
		name  string
		aged  bool
		want  string
		verb  string
		gone  bool
		verds map[string]WorktreeOwner
	}{
		{
			name: "refused", aged: false, verb: "kept worktree ", gone: false,
			want: "git status could not be read",
		},
		{
			name: "removed-anyway", aged: true, verb: "removed worktree ", gone: true,
			want:  "git status could not be read",
			verds: map[string]WorktreeOwner{"damaged": OwnerGone},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, wtPath, _ := sweptCannotTell(t, "damaged")
			if tc.aged {
				ageTree(t, wtPath, 2*quietWindow)
			}
			var lines []string
			if _, err := Sweep(Options{
				Repo:          r.dir,
				TargetBranch:  "main",
				Tickets:       archivedTickets("damaged"),
				OwnerVerdicts: tc.verds,
				Logf:          func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
			}); err != nil {
				t.Fatalf("Sweep: %v", err)
			}

			line := findLine(lines, tc.verb)
			if line == "" {
				t.Fatalf("no %q line; a cannot-tell decision must reach the log. log was:\n%s",
					tc.verb, strings.Join(lines, "\n"))
			}
			if !strings.Contains(line, tc.want) {
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

// TestSweepForceStillDiscardsCannotTell pins the escape hatch. An operator's
// explicit --force is a positive reason to discard, and it must not change:
// without it a refused tree pins its branch with no way for a human to say "yes
// I know, take it". It is also what bounds the leak this fix introduces.
//
// The log line still has to be honest about it, so --force names the status
// failure too — the operator gets what they asked for AND a true record.
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
