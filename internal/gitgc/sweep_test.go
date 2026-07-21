package gitgc

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testRepo is a throwaway git repository for exercising the git-touching
// helpers against real git rather than a mock.
type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	dir := t.TempDir()
	// Resolve symlinks so test-built paths match what `git worktree list`
	// reports — on macOS t.TempDir() lives under /var, a symlink to
	// /private/var, and git canonicalizes worktree paths.
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	r := &testRepo{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.name", "test")
	r.git("config", "user.email", "test@test")
	r.commit("seed.txt", "seed")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// commit writes a file and commits it on the current branch.
func (r *testRepo) commit(file, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, file), []byte(content), 0644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", file)
	r.git("commit", "-q", "-m", "commit "+file)
}

// branch creates branch name pointing at the current HEAD.
func (r *testRepo) branch(name string) {
	r.t.Helper()
	r.git("branch", name)
}

// worktree registers a worktree at <repo>/../wt-<name> for branch.
func (r *testRepo) worktree(branch string) string {
	r.t.Helper()
	path := filepath.Join(filepath.Dir(r.dir), "wt-"+branch)
	r.git("worktree", "add", "-q", path, branch)
	return path
}

func TestListPolecatBranchesAndWorktrees(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-aaaa")
	r.branch("polecat-bbbb")
	r.branch("feature-x") // not a polecat branch
	wtPath := r.worktree("polecat-aaaa")

	branches, err := ListPolecatBranches(r.dir)
	if err != nil {
		t.Fatalf("ListPolecatBranches: %v", err)
	}
	if len(branches) != 2 {
		t.Fatalf("got %d polecat branches, want 2: %v", len(branches), branches)
	}

	wts, err := ListWorktrees(r.dir)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("got %d worktrees, want 2 (main + polecat-aaaa): %v", len(wts), wts)
	}
	if !wts[0].Main {
		t.Error("first worktree should be flagged Main")
	}
	var found bool
	for _, w := range wts {
		if w.Path == wtPath {
			found = true
			if !w.IsPolecat() || w.Branch != "polecat-aaaa" {
				t.Errorf("worktree %+v not classified as polecat-aaaa", w)
			}
		}
	}
	if !found {
		t.Errorf("worktree %s not in list", wtPath)
	}

	checkedOut, err := CheckedOutBranches(r.dir)
	if err != nil {
		t.Fatalf("CheckedOutBranches: %v", err)
	}
	if !checkedOut["polecat-aaaa"] {
		t.Error("polecat-aaaa should be reported as checked out")
	}
	if checkedOut["polecat-bbbb"] {
		t.Error("polecat-bbbb has no worktree, should not be checked out")
	}
}

func TestBranchMerged(t *testing.T) {
	r := newTestRepo(t)
	// merged: a branch at HEAD is an ancestor of main.
	r.branch("polecat-merged")
	// unmerged: a branch with its own commit ahead of main.
	r.git("checkout", "-q", "-b", "polecat-unmerged")
	r.commit("extra.txt", "extra")
	r.git("checkout", "-q", "main")

	merged, err := BranchMerged(r.dir, "polecat-merged", "main")
	if err != nil {
		t.Fatalf("BranchMerged(merged): %v", err)
	}
	if !merged {
		t.Error("polecat-merged should be merged into main")
	}

	unmerged, err := BranchMerged(r.dir, "polecat-unmerged", "main")
	if err != nil {
		t.Fatalf("BranchMerged(unmerged): %v", err)
	}
	if unmerged {
		t.Error("polecat-unmerged should not be merged into main")
	}
}

func TestRemoveWorktreeAndPrune(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-gone")
	wtPath := r.worktree("polecat-gone")

	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree dir should exist: %v", err)
	}
	// The worktree is LINKED and its branch is checked out in it — the state
	// every polecat is now in at exit, since the submit-time unlink that used
	// to strip the registration was deleted (gh #88). This is the negative
	// control for that deletion: the exit/GC cleanup must still fully reclaim
	// a polecat whose merge SUCCEEDED.
	if err := RemoveWorktree(r.dir, wtPath, OwnerUnproven); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone, stat err = %v", err)
	}
	// The registration must go too, not just the directory. A leftover
	// registration is the gh #31 orphan shape.
	registered, err := ListWorktrees(r.dir)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	for _, wt := range registered {
		if wt.Path == wtPath {
			t.Errorf("worktree registration should be gone, still listed: %s", wt.Path)
		}
	}

	// RemoveWorktree is idempotent — a second call is a no-op success.
	if err := RemoveWorktree(r.dir, wtPath, OwnerUnproven); err != nil {
		t.Errorf("second RemoveWorktree should succeed: %v", err)
	}

	// A worktree whose directory vanished out from under git is reclaimed
	// by prune.
	wt2 := r.worktree("polecat-gone")
	if err := os.RemoveAll(wt2); err != nil {
		t.Fatal(err)
	}
	out, err := PruneWorktrees(r.dir, false)
	if err != nil {
		t.Fatalf("PruneWorktrees: %v", err)
	}
	_ = out
	wts, _ := ListWorktrees(r.dir)
	if len(wts) != 1 {
		t.Errorf("after prune want only main worktree, got %d: %v", len(wts), wts)
	}
}

// TestRemoveWorktreeFreesCheckedOutBranch is the negative control for the gh
// #88 deletion of the submit-time unlink. Without that hook a polecat's branch
// stays checked out in its worktree right up to exit, and `git branch -D`
// refuses a branch that is checked out somewhere. The cleanup is only safe
// because RemoveWorktree drops the registration first, which is exactly why
// Sweep processes worktrees before branches. If this ever regresses, every
// merged polecat leaks its branch.
func TestRemoveWorktreeFreesCheckedOutBranch(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-live")
	wtPath := r.worktree("polecat-live")

	// Precondition: while the worktree is live, the branch is pinned. This is
	// the state the deleted hook used to prevent — assert it, so the test
	// proves the ordering matters rather than assuming it.
	if err := DeleteBranch(r.dir, "polecat-live"); err == nil {
		t.Fatal("expected git to refuse deleting a branch checked out in a live worktree")
	}

	if err := RemoveWorktree(r.dir, wtPath, OwnerUnproven); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if err := DeleteBranch(r.dir, "polecat-live"); err != nil {
		t.Errorf("branch should be deletable once its worktree is removed: %v", err)
	}
}

func TestDeleteBranch(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-doomed")
	if err := DeleteBranch(r.dir, "polecat-doomed"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	branches, _ := ListPolecatBranches(r.dir)
	if len(branches) != 0 {
		t.Errorf("branch should be gone, got %v", branches)
	}
	if err := DeleteBranch(r.dir, "polecat-doomed"); err == nil {
		t.Error("deleting a nonexistent branch should error")
	}
}

// TestSweep is the end-to-end acceptance test for the package: it builds a
// repo mirroring the real spread of branch/worktree states and asserts the
// sweep deletes exactly the concluded, non-live, eligible items.
func TestSweep(t *testing.T) {
	r := newTestRepo(t)

	// Branches across every classification.
	r.branch("polecat-arch")       // archived       -> delete
	r.branch("polecat-live")       // archived, live -> keep
	r.branch("polecat-flight")     // in-flight      -> keep
	r.branch("polecat-unknown")    // unknown ticket -> keep
	r.branch("polecat-donemerged") // done + merged  -> delete
	r.git("checkout", "-q", "-b", "polecat-doneunmerged")
	r.commit("u.txt", "u") // done + unmerged -> keep (merge gate)
	r.git("checkout", "-q", "main")

	// Worktrees: one concluded (removed, frees its branch), one live (kept).
	archWT := r.worktree("polecat-arch")
	liveWT := r.worktree("polecat-live")

	tickets := TicketIndex{
		"mg-arch":         TicketArchived,
		"mg-live":         TicketArchived,
		"mg-flight":       TicketInFlight,
		"mg-donemerged":   TicketDone,
		"mg-doneunmerged": TicketDone,
		// mg-unknown intentionally absent
	}

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		LivePolecats: map[string]bool{"live": true},
		Tickets:      tickets,
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected sweep errors: %v", res.Errors)
	}

	deleted := branchSet(res.BranchesDeleted)
	wantDeleted := []string{"polecat-arch", "polecat-donemerged"}
	for _, b := range wantDeleted {
		if !deleted[b] {
			t.Errorf("expected branch %s to be deleted; deleted=%v", b, keys(deleted))
		}
	}
	wantKept := []string{"polecat-live", "polecat-flight", "polecat-unknown", "polecat-doneunmerged"}
	for _, b := range wantKept {
		if deleted[b] {
			t.Errorf("branch %s should have been kept", b)
		}
	}
	if len(res.BranchesDeleted) != 2 {
		t.Errorf("deleted %d branches, want 2: %v", len(res.BranchesDeleted), keys(deleted))
	}

	// Worktree assertions.
	if len(res.WorktreesRemoved) != 1 || res.WorktreesRemoved[0].Branch != "polecat-arch" {
		t.Errorf("want exactly polecat-arch worktree removed, got %+v", res.WorktreesRemoved)
	}
	if _, err := os.Stat(archWT); !os.IsNotExist(err) {
		t.Errorf("archived worktree dir should be gone: %v", err)
	}
	if _, err := os.Stat(liveWT); err != nil {
		t.Errorf("live worktree dir should remain: %v", err)
	}

	// Verify the on-disk end state matches the report.
	remaining, _ := ListPolecatBranches(r.dir)
	if len(remaining) != 4 {
		t.Errorf("want 4 branches remaining, got %d: %v", len(remaining), remaining)
	}
}

// TestSweepDryRun confirms a dry run reports the same deletions without
// touching the repository.
func TestSweepDryRun(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-arch")
	wt := r.worktree("polecat-arch")

	tickets := TicketIndex{"mg-arch": TicketArchived}
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets, DryRun: true})
	if err != nil {
		t.Fatalf("Sweep dry-run: %v", err)
	}
	if len(res.BranchesDeleted) != 1 || res.BranchesDeleted[0].Branch != "polecat-arch" {
		t.Errorf("dry run should report polecat-arch deletion, got %+v", res.BranchesDeleted)
	}
	if len(res.WorktreesRemoved) != 1 {
		t.Errorf("dry run should report 1 worktree removal, got %d", len(res.WorktreesRemoved))
	}
	// Nothing actually changed.
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("dry run must not remove the worktree dir: %v", err)
	}
	branches, _ := ListPolecatBranches(r.dir)
	if len(branches) != 1 {
		t.Errorf("dry run must not delete branches, got %v", branches)
	}
}

// TestSweepOrphanDirs covers gh #31: a polecat dir whose worktree was
// unlinked at submit time (no .git, no registration) and whose exit cleanup
// never ran is invisible to `git worktree list` — the PolecatsDir scan must
// reclaim it once the ticket concludes, generated files and all, while
// keeping live, in-flight, unclassifiable, and still-linked dirs.
func TestSweepOrphanDirs(t *testing.T) {
	r := newTestRepo(t)
	polecats := t.TempDir()

	// mkOrphan builds a dir shaped like the gh #31 leftovers: checked-out
	// files plus test-generated __pycache__, but no .git pointer.
	mkOrphan := func(name string) string {
		t.Helper()
		dir := filepath.Join(polecats, name)
		if err := os.MkdirAll(filepath.Join(dir, "__pycache__"), 0755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"test_foo.py", "__pycache__/test_foo.cpython-312.pyc"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	doneDir := mkOrphan("aaaa")    // done       -> removed
	archDir := mkOrphan("bbbb")    // archived   -> removed
	flightDir := mkOrphan("cccc")  // in-flight  -> kept
	unknownDir := mkOrphan("dddd") // unknown    -> kept
	liveDir := mkOrphan("eeee")    // live, done -> kept

	// A dir still carrying a .git pointer is a linked worktree of some
	// repo, not an orphan — untouched even with a concluded ticket.
	linkedDir := filepath.Join(polecats, "ffff")
	if err := os.MkdirAll(linkedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkedDir, ".git"), []byte("gitdir: /elsewhere/.git/worktrees/ffff"), 0644); err != nil {
		t.Fatal(err)
	}

	// A stray file directly under the polecats dir is ignored.
	if err := os.WriteFile(filepath.Join(polecats, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	tickets := TicketIndex{
		"mg-aaaa": TicketDone,
		"mg-bbbb": TicketArchived,
		"mg-cccc": TicketInFlight,
		// mg-dddd intentionally absent
		"mg-eeee": TicketDone,
		"mg-ffff": TicketArchived,
	}
	res, err := Sweep(Options{
		Repo:         r.dir,
		Tickets:      tickets,
		LivePolecats: map[string]bool{"eeee": true},
		PolecatsDir:  polecats,
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected sweep errors: %v", res.Errors)
	}

	for _, dir := range []string{doneDir, archDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("orphan dir %s should be gone, stat err = %v", dir, err)
		}
	}
	for _, dir := range []string{flightDir, unknownDir, liveDir, linkedDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("dir %s should remain: %v", dir, err)
		}
	}
	if len(res.WorktreesRemoved) != 2 {
		t.Errorf("want 2 orphan removals reported, got %+v", res.WorktreesRemoved)
	}
	if _, err := os.Stat(filepath.Join(polecats, "notes.txt")); err != nil {
		t.Errorf("stray file should be untouched: %v", err)
	}
}

// TestSweepOrphanDirsDryRun confirms the orphan scan reports without
// deleting, and that a missing polecats dir is not an error.
func TestSweepOrphanDirsDryRun(t *testing.T) {
	r := newTestRepo(t)
	polecats := t.TempDir()
	dir := filepath.Join(polecats, "aaaa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	tickets := TicketIndex{"mg-aaaa": TicketArchived}
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets, PolecatsDir: polecats, DryRun: true})
	if err != nil {
		t.Fatalf("Sweep dry-run: %v", err)
	}
	if len(res.WorktreesRemoved) != 1 || res.WorktreesRemoved[0].Path != dir {
		t.Errorf("dry run should report the orphan removal, got %+v", res.WorktreesRemoved)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dry run must not remove the orphan dir: %v", err)
	}

	// Nonexistent polecats dir: skipped silently.
	res, err = Sweep(Options{Repo: r.dir, Tickets: tickets, PolecatsDir: filepath.Join(polecats, "nope")})
	if err != nil {
		t.Fatalf("Sweep with missing polecats dir: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("missing polecats dir should not be an error: %v", res.Errors)
	}
}

// TestSweepOrphanDirsSkipsRegistered confirms a real registered worktree
// living under the polecats dir is owned by the registered-worktree scan,
// not double-handled by the orphan scan.
func TestSweepOrphanDirsSkipsRegistered(t *testing.T) {
	r := newTestRepo(t)
	polecats := t.TempDir()
	if real, err := filepath.EvalSymlinks(polecats); err == nil {
		polecats = real
	}
	r.branch("polecat-aaaa")
	wt := filepath.Join(polecats, "aaaa")
	r.git("worktree", "add", "-q", wt, "polecat-aaaa")

	tickets := TicketIndex{"mg-aaaa": TicketInFlight}
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets, PolecatsDir: polecats})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("in-flight registered worktree should remain: %v", err)
	}
	if len(res.WorktreesRemoved) != 0 {
		t.Errorf("nothing should be removed, got %+v", res.WorktreesRemoved)
	}
	// Exactly one kept entry — from the worktree scan, not doubled by the
	// orphan scan.
	if len(res.WorktreesKept) != 1 {
		t.Errorf("want 1 kept entry, got %+v", res.WorktreesKept)
	}
}

func branchSet(actions []BranchAction) map[string]bool {
	set := map[string]bool{}
	for _, a := range actions {
		set[a.Branch] = true
	}
	return set
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
