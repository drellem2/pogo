package gitgc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// A project whose record names a backend other than git is not swept as git
// (mg-0ff9c). pogod's sweep set is every repo a polecat was cut from, which
// includes enth repos, and sweeping one as git failed "not a git repository" on
// every tick — a degraded condition raised that nothing could ever clear.

// TestSweepSkipsGitPhasesForAnEnthProjectWithNoGit is the reported case: an
// enth-born project directory with no .git. The control is the same directory
// swept as git, which must still fail the way the incident did — otherwise the
// enth assertion could pass because the directory happened to be sweepable.
func TestSweepSkipsGitPhasesForAnEnthProjectWithNoGit(t *testing.T) {
	fakeEnthOnPath(t)
	repo := t.TempDir()
	enthTree(t, repo)
	if _, err := os.Lstat(filepath.Join(repo, ".git")); !os.IsNotExist(err) {
		t.Fatalf("precondition: the enth project must have no .git (lstat err = %v)", err)
	}

	// Positive control: as git, this is exactly the incident's error.
	_, err := Sweep(Options{Repo: repo, Tickets: TicketIndex{}})
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("control: sweeping a .git-less dir as git = %v; want the incident's \"not a git repository\"", err)
	}

	// The polecats dir still gets judged on an enth sweep: a concluded clean
	// enth checkout goes, a dirty one stays.
	polecats := t.TempDir()
	cleanCo := filepath.Join(polecats, "aaaa")
	enthTree(t, cleanCo)
	dirtyCo := filepath.Join(polecats, "bbbb")
	enthTree(t, dirtyCo, `{"path":"new.go","op":"put","blob":"z","mode":"file"}`)

	res, err := Sweep(Options{
		Repo:        repo,
		VCS:         vcs.Enth,
		Tickets:     TicketIndex{"mg-aaaa": TicketDone, "mg-bbbb": TicketDone},
		PolecatsDir: polecats,
	})
	if err != nil {
		t.Fatalf("enth project swept with VCS=enth: %v — this is the permanent false degraded condition", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("sweep errors: %v", res.Errors)
	}
	if res.GitSkipped == "" || !strings.Contains(res.GitSkipped, "vcs=enth") {
		t.Errorf("GitSkipped = %q; the skip must be stated, naming the backend", res.GitSkipped)
	}
	if sum := res.Summary(); !strings.Contains(sum, "coverage:") || !strings.Contains(sum, "vcs=enth") {
		t.Errorf("Summary does not carry the coverage line:\n%s", sum)
	}
	if _, err := os.Stat(cleanCo); !os.IsNotExist(err) {
		t.Errorf("a clean, concluded enth checkout was not reaped on the enth repo's sweep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirtyCo, "a.txt")); err != nil {
		t.Errorf("a DIRTY enth checkout was removed: %v", err)
	}
}

// TestSweepLeavesGitStateOfACutOverEnthProjectAlone is the riskier half the
// ticket names: a repo cut over to enth that still has its .git. Sweeping it
// as git raises no error at all, so nothing would say its git-era branches and
// worktree registrations were being judged. The control is the same repo swept
// as git, which does delete the concluded branch and remove the worktree.
func TestSweepLeavesGitStateOfACutOverEnthProjectAlone(t *testing.T) {
	setup := func() (*testRepo, string, TicketIndex) {
		r := newTestRepo(t)
		r.branch("polecat-arch")
		wt := r.worktree("polecat-arch")
		return r, wt, TicketIndex{"mg-arch": TicketArchived}
	}

	// Control: as git, both go.
	r, wt, tickets := setup()
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets})
	if err != nil {
		t.Fatal(err)
	}
	if !branchSet(res.BranchesDeleted)["polecat-arch"] {
		t.Fatalf("control: the git sweep did not delete the concluded branch: %+v", res)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("control: the git sweep did not remove the concluded worktree: %v", err)
	}

	// As enth, neither is touched.
	r, wt, tickets = setup()
	res, err = Sweep(Options{Repo: r.dir, VCS: vcs.Enth, Tickets: tickets})
	if err != nil {
		t.Fatal(err)
	}
	if res.GitSkipped == "" {
		t.Errorf("GitSkipped empty on an enth sweep")
	}
	if len(res.BranchesDeleted)+len(res.WorktreesRemoved) != 0 {
		t.Errorf("an enth sweep acted on git state: %+v", res)
	}
	if out := r.git("branch", "--list", "polecat-arch"); !strings.Contains(out, "polecat-arch") {
		t.Errorf("an enth sweep deleted the git branch")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("an enth sweep removed the git worktree: %v", err)
	}
}

// TestSweepTreatsExplicitGitLikeUnset: VCS "git" is the git sweep, as empty is.
func TestSweepTreatsExplicitGitLikeUnset(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-arch")
	res, err := Sweep(Options{Repo: r.dir, VCS: vcs.Git, Tickets: TicketIndex{"mg-arch": TicketArchived}})
	if err != nil {
		t.Fatal(err)
	}
	if res.GitSkipped != "" || !branchSet(res.BranchesDeleted)["polecat-arch"] {
		t.Errorf("VCS=git did not run the git sweep: GitSkipped=%q deleted=%+v", res.GitSkipped, res.BranchesDeleted)
	}
}
