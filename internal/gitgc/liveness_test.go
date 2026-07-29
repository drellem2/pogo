package gitgc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPolecatNameForWorktree pins the ownership rule the liveness gate reads:
// a worktree belongs to the polecat its path is NAMED after, whatever branch
// happens to be checked out inside it.
func TestPolecatNameForWorktree(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/Users/x/.pogo/polecats/caa65", "caa65"},
		{"/Users/x/.pogo/polecats/caa65/", "caa65"},
		{"/Users/x/.pogo/polecats/cat-mg-30d5", "cat-mg-30d5"},
		{"", ""},
	}
	for _, c := range cases {
		if got := PolecatNameForWorktree(c.path); got != c.want {
			t.Errorf("PolecatNameForWorktree(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestSweepKeepsLivePolecatOnForeignBranch is gh #94 / mg-dd92, run as the
// inversion of the reproduction that found it.
//
// THE DEFECT. `Sweep` decided a worktree was occupied from the polecat name
// embedded in the branch CHECKED OUT INSIDE it, never from the path — whose
// basename is the polecat that owns the tree. A live polecat working any
// branch but its own was therefore invisible to the liveness gate: it
// inherited the foreign, concluded ticket's state, its worktree was removed
// mid-task, and freeing its branch waived phase 2's guard so the ref went too.
// The agent kept running and `pogo agent list` kept calling it healthy. That
// is not exotic — two shipped roles (review, QA) instruct a foreign checkout,
// and were protected only by prompt conventions living in other files.
//
// THE CONTROL IS HALF THE TEST. A liveness gate that keeps everything is
// indistinguishable from a disabled one, and the symmetric defect — never
// reaping a dead polecat's tree — is just as real. So the same sweep also
// carries a concluded, non-live polecat sitting on its OWN branch, i.e. the
// normal end state of every polecat that ever exits, and that one must still
// collect: directory gone, branch deleted.
func TestSweepKeepsLivePolecatOnForeignBranch(t *testing.T) {
	r := newTestRepo(t)

	// caa65 is LIVE. Its own ticket is still in flight; it is working the
	// branch of dccb, a DIFFERENT and concluded ticket — the PR-conflict /
	// review / QA shape.
	r.branch("polecat-caa65")
	r.branch("polecat-dccb")
	// beef is the control: concluded, not live, on its own branch.
	r.branch("polecat-beef")

	liveWT := r.worktreeOwnedBy("caa65", "polecat-dccb")
	deadWT := r.worktreeOwnedBy("beef", "polecat-beef")

	tickets := TicketIndex{
		"mg-caa65": TicketInFlight,
		"mg-dccb":  TicketArchived,
		"mg-beef":  TicketArchived,
	}
	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		LivePolecats: map[string]bool{"caa65": true},
		Tickets:      tickets,
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected sweep errors: %v", res.Errors)
	}

	// --- The live polecat survives, tree and ref -------------------------
	if _, err := os.Stat(liveWT); err != nil {
		t.Errorf("LIVE polecat caa65's worktree was removed out from under it (gh #94): %v", err)
	}
	for _, w := range res.WorktreesRemoved {
		if w.Path == liveWT {
			t.Errorf("sweep reported removing the live polecat's worktree: %+v", w)
		}
	}
	kept := findWorktreeAction(res.WorktreesKept, liveWT)
	if kept == nil {
		t.Errorf("live polecat's worktree not reported kept at all; kept=%+v", res.WorktreesKept)
	} else {
		if kept.Owner != "caa65" {
			t.Errorf("kept worktree Owner = %q, want caa65 (the path's basename, not the branch's)", kept.Owner)
		}
		if !strings.Contains(kept.Reason, "live polecat") {
			t.Errorf("kept reason = %q, want it to name liveness", kept.Reason)
		}
	}
	// The branch it is working must survive too. Removing the tree is what
	// used to free the ref, so a kept tree has to keep the ref pinned.
	remaining := branchList(t, r)
	if !remaining["polecat-dccb"] {
		t.Error("the foreign branch the live polecat had checked out was deleted (gh #94 phase 2)")
	}
	if !remaining["polecat-caa65"] {
		t.Error("the live polecat's own branch was deleted")
	}
	if kb := findBranchAction(res.BranchesKept, "polecat-dccb"); kb == nil || kb.Reason != "checked out in a worktree" {
		t.Errorf("polecat-dccb should be kept as checked out in a worktree, got %+v", kb)
	}

	// --- The control still collects --------------------------------------
	if _, err := os.Stat(deadWT); !os.IsNotExist(err) {
		t.Errorf("concluded, non-live polecat beef's worktree should have been reaped, stat err = %v", err)
	}
	if removed := findWorktreeAction(res.WorktreesRemoved, deadWT); removed == nil {
		t.Errorf("sweep did not report removing beef's worktree; removed=%+v", res.WorktreesRemoved)
	} else if removed.Owner != "beef" {
		t.Errorf("removed worktree Owner = %q, want beef", removed.Owner)
	}
	if remaining["polecat-beef"] {
		t.Error("beef's branch should have been deleted once its worktree was freed")
	}
	if len(res.WorktreesRemoved) != 1 {
		t.Errorf("want exactly 1 worktree removed (the control), got %+v", res.WorktreesRemoved)
	}
}

// TestSweepLogsEveryAction is the positive control on diagnosability (gh #94).
// The sweep has always assembled path, owner, branch and reason per decision
// and thrown all of it away, logging counts only — so the one removal sitting
// in a 5.5MB log on the reporting host cannot be judged legitimate or not, and
// "did the GC ever fire on my worktree" is unanswerable for any past incident.
// Every destructive action must now name what it took and why.
func TestSweepLogsEveryAction(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-aaaa")
	wt := r.worktree("polecat-aaaa")

	// An orphan dir too — the other destructive path, which rm -rf's files
	// git cannot even see.
	polecats := r.polecatsDir()
	orphan := filepath.Join(polecats, "bbbb")
	if err := os.MkdirAll(orphan, 0755); err != nil {
		t.Fatal(err)
	}

	var lines []string
	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-aaaa": TicketArchived, "mg-bbbb": TicketArchived},
		PolecatsDir:  polecats,
		Logf: func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected sweep errors: %v", res.Errors)
	}
	joined := strings.Join(lines, "\n")

	// A REAL removal — not a dry run, not a compile — logs the tree it took,
	// whose it was, what was checked out in it, and the reason.
	wtLine := findLine(lines, "removed worktree ")
	if wtLine == "" {
		t.Fatalf("no worktree-removal line logged; log was:\n%s", joined)
	}
	for _, want := range []string{wt, "owner aaaa", "branch polecat-aaaa", "ticket archived"} {
		if !strings.Contains(wtLine, want) {
			t.Errorf("worktree-removal line missing %q: %s", want, wtLine)
		}
	}

	// The orphan-dir path is destructive too and gets the same treatment.
	orphanLine := findLine(lines, "removed orphan dir ")
	if orphanLine == "" {
		t.Fatalf("no orphan-dir removal line logged; log was:\n%s", joined)
	}
	for _, want := range []string{orphan, "owner bbbb", "ticket archived"} {
		if !strings.Contains(orphanLine, want) {
			t.Errorf("orphan-dir line missing %q: %s", want, orphanLine)
		}
	}

	// Branch deletion already logged; assert it still names the reason.
	brLine := findLine(lines, "deleted branch ")
	if brLine == "" || !strings.Contains(brLine, "polecat-aaaa") {
		t.Errorf("no branch-deletion line naming polecat-aaaa; log was:\n%s", joined)
	}
}

// TestSweepLogsPreservedDirtyWorktree covers the other decision an operator
// needs to find in a log: a tree the sweep deliberately did NOT take. Without
// it, a worktree that quietly pins its branch forever looks like a GC that
// never ran.
func TestSweepLogsPreservedDirtyWorktree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-aaaa")
	wt := r.worktree("polecat-aaaa")
	if err := os.WriteFile(filepath.Join(wt, "unmerged.txt"), []byte("work"), 0644); err != nil {
		t.Fatal(err)
	}

	var lines []string
	if _, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-aaaa": TicketArchived},
		Logf:         func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
	}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	line := findLine(lines, "kept worktree ")
	if line == "" {
		t.Fatalf("preserved dirty worktree logged nothing; log was:\n%s", strings.Join(lines, "\n"))
	}
	for _, want := range []string{wt, "owner aaaa", "uncommitted change"} {
		if !strings.Contains(line, want) {
			t.Errorf("dirty-keep line missing %q: %s", want, line)
		}
	}
}

// TestSweepFailedWorktreeRemovalKeepsBranchPinned covers the third part of the
// gh #94 packet, which the triage reached by CODE READ ONLY and never
// reproduced: `freed[wt.Branch]` was set BEFORE removal was attempted and
// survived a failure. Phase 2 then believed the branch was no longer checked
// out when it still was, so it went on to attempt a deletion git was always
// going to refuse — turning a correct "kept: checked out in a worktree" into a
// spurious error, and mis-reporting the sweep's own decision.
//
// The removal is faked because a real one losing to the filesystem needs both
// `git worktree remove --force` and os.RemoveAll to fail, which no portable
// fixture can arrange (and a chmod fixture silently passes as root).
func TestSweepFailedWorktreeRemovalKeepsBranchPinned(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-aaaa")
	wt := r.worktree("polecat-aaaa")

	orig := removeWorktreeFn
	t.Cleanup(func() { removeWorktreeFn = orig })
	removeWorktreeFn = func(sourceRepo, worktreeDir string) error {
		return fmt.Errorf("simulated removal failure")
	}

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-aaaa": TicketArchived},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// A failed removal is not a removal.
	if len(res.WorktreesRemoved) != 0 {
		t.Errorf("failed removal reported as removed: %+v", res.WorktreesRemoved)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree dir should still be there: %v", err)
	}

	// And the branch is still checked out in it, so that is what the sweep
	// must SAY — one error about the worktree, none about the branch.
	kept := findBranchAction(res.BranchesKept, "polecat-aaaa")
	if kept == nil || kept.Reason != "checked out in a worktree" {
		t.Errorf("branch should be kept as checked out in a worktree, got %+v (errors: %v)", kept, res.Errors)
	}
	if len(res.BranchesDeleted) != 0 {
		t.Errorf("no branch should have been deleted: %+v", res.BranchesDeleted)
	}
	if len(res.Errors) != 1 {
		t.Errorf("want exactly the one removal error, got %v", res.Errors)
	}
	if !branchList(t, r)["polecat-aaaa"] {
		t.Error("branch should still exist on disk")
	}
}

func findWorktreeAction(actions []WorktreeAction, path string) *WorktreeAction {
	for i := range actions {
		if actions[i].Path == path {
			return &actions[i]
		}
	}
	return nil
}

func findBranchAction(actions []BranchAction, branch string) *BranchAction {
	for i := range actions {
		if actions[i].Branch == branch {
			return &actions[i]
		}
	}
	return nil
}

func findLine(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

func branchList(t *testing.T, r *testRepo) map[string]bool {
	t.Helper()
	branches, err := ListPolecatBranches(r.dir)
	if err != nil {
		t.Fatalf("ListPolecatBranches: %v", err)
	}
	set := map[string]bool{}
	for _, b := range branches {
		set[b] = true
	}
	return set
}

// TestSummaryItemisesKeptWorktrees is the `pogo gc` half of diagnosability.
// The command's help promises a worktree holding uncommitted work is "KEPT and
// reported"; the summary reported it as a count. A preserved tree pins its
// branch until someone acts on it, so a count tells an operator a branch is
// stuck without telling them which tree to go rescue.
func TestSummaryItemisesKeptWorktrees(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-aaaa")
	wt := r.worktree("polecat-aaaa")
	if err := os.WriteFile(filepath.Join(wt, "unmerged.txt"), []byte("work"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-aaaa": TicketArchived},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	summary := res.Summary()
	for _, want := range []string{wt, "owner aaaa", "branch polecat-aaaa", "uncommitted change"} {
		if !strings.Contains(summary, want) {
			t.Errorf("gc summary missing %q — the operator cannot tell which tree to rescue.\n%s", want, summary)
		}
	}
}
