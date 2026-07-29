package gitgc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The regression suite for mg-3b7c: a LIVE polecat whose worktree has a branch
// other than its own polecat-<name> checked out.
//
// This is not a hypothetical shape. It is what every ticket of the form "fix
// the conflicts on PR #N" produces, because updating an existing pull request
// in place requires checking out that PR's head branch — there is no other
// way to do it, so the platform has to support it. On 2026-07-29 the sweep
// removed polecat caa65's worktree and deleted the branch ref five minutes
// after the worktree was created, while the agent was still running and still
// reported healthy by `pogo agent list`. The work survived only because the
// commit was still loose in the shared object store.
//
// The names below mirror that incident with in-convention 4-hex codes: `aa65`
// is the live polecat (worktree <polecats>/aa65), `dccb` is the other
// polecat's PR head branch it had to check out, and mg-dccb is long since
// archived — which is precisely what made the tree look reclaimable.

// polecatWorktree registers a worktree for branch at <polecatsDir>/<name>,
// exactly as internal/agent lays one out at spawn.
func (r *testRepo) polecatWorktree(polecatsDir, name, branch string) string {
	r.t.Helper()
	path := filepath.Join(polecatsDir, name)
	r.git("worktree", "add", "-q", path, branch)
	return path
}

// tempPolecatsDir returns a symlink-resolved temp dir to stand in for
// ~/.pogo/polecats, so paths built here match what git reports.
func tempPolecatsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

func hasWorktreeKept(res Result, path string) (WorktreeAction, bool) {
	for _, w := range res.WorktreesKept {
		if w.Path == path {
			return w, true
		}
	}
	return WorktreeAction{}, false
}

// TestSweepKeepsLiveWorktreeOnForeignBranch is the incident itself: the sweep
// must keep both the tree and the ref when a live agent owns the directory,
// regardless of which branch is checked out inside it.
func TestSweepKeepsLiveWorktreeOnForeignBranch(t *testing.T) {
	r := newTestRepo(t)
	polecats := tempPolecatsDir(t)

	r.branch("polecat-dccb") // another polecat's PR head, ticket long archived
	wt := r.polecatWorktree(polecats, "aa65", "polecat-dccb")

	tickets := TicketIndex{
		"mg-dccb": TicketArchived,
		"mg-aa65": TicketInFlight,
	}
	res, err := Sweep(Options{
		Repo:          r.dir,
		TargetBranch:  "main",
		Tickets:       tickets,
		PolecatsDir:   polecats,
		LivePolecats:  map[string]bool{"aa65": true},
		LiveWorktrees: map[string]bool{wt: true},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected sweep errors: %v", res.Errors)
	}

	if _, err := os.Stat(wt); err != nil {
		t.Errorf("live polecat's worktree must survive the sweep: %v", err)
	}
	if len(res.WorktreesRemoved) != 0 {
		t.Errorf("nothing should have been removed, got %+v", res.WorktreesRemoved)
	}
	if len(res.BranchesDeleted) != 0 {
		t.Errorf("no branch should have been deleted, got %+v", res.BranchesDeleted)
	}

	// The branch ref is what made the incident near-unrecoverable: deleting it
	// left the commit reachable only as a dangling object.
	remaining, _ := ListPolecatBranches(r.dir)
	var found bool
	for _, b := range remaining {
		if b == "polecat-dccb" {
			found = true
		}
	}
	if !found {
		t.Errorf("polecat-dccb ref must survive; remaining branches = %v", remaining)
	}

	// The kept reason has to name the situation, not just say "kept" — the
	// original diagnosis needed a log grep plus the polecat's own report.
	kept, ok := hasWorktreeKept(res, wt)
	if !ok {
		t.Fatalf("worktree %s should appear in WorktreesKept, got %+v", wt, res.WorktreesKept)
	}
	if !strings.Contains(kept.Reason, "live polecat") || !strings.Contains(kept.Reason, "polecat-dccb") {
		t.Errorf("kept reason should name the live owner and the foreign branch, got %q", kept.Reason)
	}
}

// TestSweepKeepsLiveWorktreeOnForeignBranchAfterRestart covers the same shape
// with the registry empty — the state pogod is in after a restart, when the
// only evidence is the on-disk witness, which records names and not paths.
// gitgc must re-derive <PolecatsDir>/<name> rather than fall back to the
// branch, or the fix would not survive the very restart that widened mg-0130.
func TestSweepKeepsLiveWorktreeOnForeignBranchAfterRestart(t *testing.T) {
	r := newTestRepo(t)
	polecats := tempPolecatsDir(t)

	r.branch("polecat-dccb")
	wt := r.polecatWorktree(polecats, "aa65", "polecat-dccb")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-dccb": TicketArchived},
		PolecatsDir:  polecats,
		LivePolecats: map[string]bool{"aa65": true}, // witness-only: no path
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree must survive on the name-derived path alone: %v", err)
	}
	if len(res.WorktreesRemoved) != 0 || len(res.BranchesDeleted) != 0 {
		t.Errorf("nothing should have been destroyed: removed=%+v deleted=%+v",
			res.WorktreesRemoved, res.BranchesDeleted)
	}
}

// TestSweepKeepsLiveWorktreeByBasename covers the `pogo gc` CLI path, where
// the live set arrives from pogod's HTTP agent list as bare names and no
// PolecatsDir was resolvable. A polecat worktree is named for its polecat by
// construction, so the basename is still direct evidence of ownership.
func TestSweepKeepsLiveWorktreeByBasename(t *testing.T) {
	r := newTestRepo(t)
	elsewhere := tempPolecatsDir(t)

	r.branch("polecat-dccb")
	wt := r.polecatWorktree(elsewhere, "aa65", "polecat-dccb")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-dccb": TicketArchived},
		LivePolecats: map[string]bool{"aa65": true},
		// No PolecatsDir, no LiveWorktrees.
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree must survive on the basename match alone: %v", err)
	}
	if len(res.WorktreesRemoved) != 0 || len(res.BranchesDeleted) != 0 {
		t.Errorf("nothing should have been destroyed: removed=%+v deleted=%+v",
			res.WorktreesRemoved, res.BranchesDeleted)
	}
}

// TestSweepLiveWorktreeMatchesThroughSymlink guards the path comparison. git
// canonicalizes the worktree paths it reports; a path handed to us from the
// agent registry has not been, and on macOS ~/.pogo under /var vs /private/var
// is exactly that mismatch. A comparison that missed it would silently drop
// the whole protection.
func TestSweepLiveWorktreeMatchesThroughSymlink(t *testing.T) {
	r := newTestRepo(t)
	polecats := tempPolecatsDir(t)

	r.branch("polecat-dccb")
	wt := r.polecatWorktree(polecats, "aa65", "polecat-dccb")

	// A second name for the same directory, as the registry might hold.
	link := filepath.Join(t.TempDir(), "polecats-link")
	if err := os.Symlink(polecats, link); err != nil {
		t.Skipf("cannot create symlink on this filesystem: %v", err)
	}
	viaLink := filepath.Join(link, "aa65")

	res, err := Sweep(Options{
		Repo:          r.dir,
		TargetBranch:  "main",
		Tickets:       TicketIndex{"mg-dccb": TicketArchived},
		LiveWorktrees: map[string]bool{viaLink: true},
		// Deliberately no LivePolecats: the path must carry it alone.
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree named through a symlink must still be recognised: %v", err)
	}
	if len(res.WorktreesRemoved) != 0 || len(res.BranchesDeleted) != 0 {
		t.Errorf("nothing should have been destroyed: removed=%+v deleted=%+v",
			res.WorktreesRemoved, res.BranchesDeleted)
	}
}

// TestSweepKeepsForeignBranchWorktreeOfDeadPolecat is the other half of the
// same conflation. Even with nobody live, another ticket's conclusion says
// nothing about whether THIS directory is finished with, so the tree is kept
// and reported rather than reclaimed on borrowed evidence.
func TestSweepKeepsForeignBranchWorktreeOfDeadPolecat(t *testing.T) {
	r := newTestRepo(t)
	polecats := tempPolecatsDir(t)

	r.branch("polecat-dccb")
	wt := r.polecatWorktree(polecats, "aa65", "polecat-dccb")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-dccb": TicketArchived, "mg-aa65": TicketInFlight},
		PolecatsDir:  polecats,
		// No live polecats at all.
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree should be kept, not reclaimed on another ticket's state: %v", err)
	}
	kept, ok := hasWorktreeKept(res, wt)
	if !ok {
		t.Fatalf("worktree %s should be reported as kept, got %+v", wt, res.WorktreesKept)
	}
	if !strings.Contains(kept.Reason, "another polecat's branch") {
		t.Errorf("kept reason should explain the mismatch, got %q", kept.Reason)
	}
}

// TestSweepStillReclaimsConcludedPolecatWorktree pins the behaviour the fix
// must NOT cost: a dead polecat sitting on its OWN concluded branch is still
// swept, tree and ref together. Without this, the guards above could be
// satisfied by a GC that simply stopped collecting.
func TestSweepStillReclaimsConcludedPolecatWorktree(t *testing.T) {
	r := newTestRepo(t)
	polecats := tempPolecatsDir(t)

	r.branch("polecat-aa65")
	wt := r.polecatWorktree(polecats, "aa65", "polecat-aa65")

	res, err := Sweep(Options{
		Repo:         r.dir,
		TargetBranch: "main",
		Tickets:      TicketIndex{"mg-aa65": TicketArchived},
		PolecatsDir:  polecats,
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected sweep errors: %v", res.Errors)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("concluded polecat's worktree should be gone, stat err = %v", err)
	}
	if len(res.WorktreesRemoved) != 1 {
		t.Errorf("want the worktree removed, got %+v", res.WorktreesRemoved)
	}
	if deleted := branchSet(res.BranchesDeleted); !deleted["polecat-aa65"] {
		t.Errorf("want polecat-aa65 deleted, got %v", keys(deleted))
	}
}

// TestSweepNeverDeletesBranchCheckedOutInSurvivingWorktree pins the rule
// directly: whatever else the sweep concludes, a ref that a still-present
// worktree has checked out is not deletable. `freed` used to be set before the
// removal was even attempted, so a removal that FAILED still told the branch
// phase the ref was loose.
func TestSweepNeverDeletesBranchCheckedOutInSurvivingWorktree(t *testing.T) {
	r := newTestRepo(t)
	polecats := tempPolecatsDir(t)

	// Concluded and merged: every gate except the checkout is open.
	r.branch("polecat-aa65")
	wt := r.polecatWorktree(polecats, "aa65", "polecat-aa65")

	res, err := Sweep(Options{
		Repo:          r.dir,
		TargetBranch:  "main",
		Tickets:       TicketIndex{"mg-aa65": TicketArchived},
		PolecatsDir:   polecats,
		LiveWorktrees: map[string]bool{wt: true}, // owner is live -> tree stays
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("precondition: worktree should have been kept: %v", err)
	}
	if deleted := branchSet(res.BranchesDeleted); deleted["polecat-aa65"] {
		t.Errorf("branch checked out in a surviving worktree must not be deleted")
	}
	remaining, _ := ListPolecatBranches(r.dir)
	if len(remaining) != 1 || remaining[0] != "polecat-aa65" {
		t.Errorf("polecat-aa65 should still exist, got %v", remaining)
	}
}
