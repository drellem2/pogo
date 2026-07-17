package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/gitgc"
)

// The BAR for mg-0130, at the layer the defect lives in: livePolecatSet, the
// do-not-touch set that startGitGC's sweep is gated on.
//
// The scenario is a pogod RESTART with a polecat in its NORMAL end state. Under
// the polecat protocol every polecat, once merged, runs `mg done` and then
// STAYS ALIVE awaiting the mayor's stop. In that window its ticket is concluded
// while its process and worktree are still in use. If pogod restarts then, the
// in-memory registry comes back EMPTY (no adopt/reattach path), and worktree
// removal — unlike branch deletion — has NO merge gate: the live set is its
// sole guard. An empty live set therefore sweeps a running polecat's worktree
// out from under it. The persisted witness is the one piece of evidence that
// survives the restart, and unioning it back into the live set is the fix.

// polecatRepo is a throwaway git repository with a seed commit, for exercising
// the real gitgc.Sweep against the live set livePolecatSet builds.
type polecatRepo struct {
	t   *testing.T
	dir string
}

func newPolecatRepo(t *testing.T) *polecatRepo {
	t.Helper()
	dir := t.TempDir()
	// Resolve symlinks so test-built paths match what `git worktree list`
	// reports — on macOS t.TempDir() lives under /var, a symlink to /private/var.
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	r := &polecatRepo{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.name", "test")
	r.git("config", "user.email", "test@test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git("add", "seed.txt")
	r.git("commit", "-q", "-m", "seed")
	return r
}

func (r *polecatRepo) git(args ...string) string {
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

// addPolecatWorktree creates branch polecat-<name> and checks it out in a
// worktree, mirroring a live polecat's layout. The sweep keys its exclusion on
// the branch's suffix, which is <name>.
func (r *polecatRepo) addPolecatWorktree(name string) string {
	r.t.Helper()
	branch := gitgc.BranchPrefix + name
	path := filepath.Join(filepath.Dir(r.dir), "wt-"+name)
	r.git("branch", branch)
	r.git("worktree", "add", "-q", path, branch)
	return path
}

// TestLivePolecatSet_WitnessGuardsDoneButRunningWorktreeAcrossRestart is the
// mg-0130 acceptance test. It asserts BOTH directions, because a guard that
// cannot fail proves nothing: the RED half shows an empty (post-restart)
// registry set sweeping the worktree away, and the GREEN half shows the witness
// union restoring the guard. The ONLY difference between the two is the witness.
func TestLivePolecatSet_WitnessGuardsDoneButRunningWorktreeAcrossRestart(t *testing.T) {
	sandboxPogoHome(t)

	// The successor pogod after a restart: a real, EMPTY registry — the
	// registry is in-memory with no adopt path, so it has permanently forgotten
	// every polecat that survived the restart.
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	if len(reg.List()) != 0 {
		t.Fatalf("precondition: registry not empty (%d entries) — this test must model a post-restart "+
			"registry that has forgotten the polecat", len(reg.List()))
	}

	// Every polecat here is in its normal end state: ticket done, process still
	// running while it awaits the mayor's stop.
	tickets := gitgc.TicketIndex{
		"mg-0130": gitgc.TicketDone, // the survivor: witnessed alive
		"mg-0d0e": gitgc.TicketDone, // the control: no witness
	}

	// --- RED control: the registry ALONE cannot protect the worktree. -------
	// This is the pre-fix live set — registry only, which after a restart is
	// empty. With nothing marking the polecat live, its done-ticket worktree is
	// swept out from under the running process. If this did NOT happen, the
	// GREEN assertion below would prove nothing.
	{
		repo := newPolecatRepo(t)
		wt := repo.addPolecatWorktree("0d0e")
		res, err := gitgc.Sweep(gitgc.Options{
			Repo:         repo.dir,
			TargetBranch: "main",
			LivePolecats: map[string]bool{}, // empty registry, witness not consulted
			Tickets:      tickets,
		})
		if err != nil {
			t.Fatalf("control sweep: %v", err)
		}
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("control did not go RED: worktree %s survived an EMPTY live set. If the guard cannot "+
				"fail here, the GREEN assertion proves nothing (mg-0130)", wt)
		}
		if len(res.WorktreesRemoved) != 1 {
			t.Fatalf("control: want 1 worktree removed, got %d (%+v)", len(res.WorktreesRemoved), res.WorktreesRemoved)
		}
	}

	// --- GREEN: the witness restores the guard across the restart. ----------
	// A real, live process is the surviving polecat. Its witness was recorded
	// by the pogod that spawned it (exactly as Spawn does via noteWitnessStart)
	// and OUTLIVES that pogod — it is the evidence the empty registry lacks.
	repo := newPolecatRepo(t)
	wt := repo.addPolecatWorktree("0130")
	pid := liveProbeProcess(t)
	if err := agent.RecordPolecatWitness("0130", pid, "mg-0130"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	// The fix: livePolecatSet unions the witness into the (empty) registry set,
	// so the survivor's name reappears despite the restart.
	live, err := livePolecatSet(reg)
	if err != nil {
		t.Fatalf("livePolecatSet: %v", err)
	}
	if !live["0130"] {
		t.Fatalf("livePolecatSet did not mark the witnessed survivor live: %v — a restart empties the "+
			"registry, and without unioning the witness the survivor's SOLE worktree guard is gone (mg-0130)", live)
	}

	res, err := gitgc.Sweep(gitgc.Options{
		Repo:         repo.dir,
		TargetBranch: "main",
		LivePolecats: live,
		Tickets:      tickets,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("REGRESSION (mg-0130): the worktree of a restart-surviving, done-but-running polecat was "+
			"swept: %v. Worktree removal has no merge gate — the live set is its only guard, and the witness "+
			"is what keeps that guard non-empty across a pogod restart", err)
	}
	if n := len(res.WorktreesRemoved); n != 0 {
		t.Fatalf("want 0 worktrees removed, got %d (%+v)", n, res.WorktreesRemoved)
	}
	// Kept for the RIGHT reason — the live-polecat guard, not some unrelated
	// in-flight classification that would mask a broken guard.
	if len(res.WorktreesKept) != 1 || res.WorktreesKept[0].Reason != "live polecat" {
		t.Fatalf("want the worktree kept as a live polecat, got %+v", res.WorktreesKept)
	}
}
