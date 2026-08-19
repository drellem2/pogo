package agent

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
)

// stubPreservedGate answers with a fixed set of trees, and records what it was
// asked, so the handler wiring can be tested without a polecats directory.
type stubPreservedGate struct {
	trees   []gitgc.PreservedTree
	err     error
	askedID []string
}

func (s *stubPreservedGate) PreservedWorktrees(workItemID, repo string) ([]gitgc.PreservedTree, error) {
	s.askedID = append(s.askedID, workItemID)
	return s.trees, s.err
}

// preservedTree is the shape of the 2026-08-19 finding: a tree holding both a
// tracked edit and an untracked file, on an open item's branch.
func preservedTree(path, branch string) gitgc.PreservedTree {
	return gitgc.PreservedTree{
		Path: path, Owner: filepath.Base(path), Branch: branch,
		Outcome: "preserved", Total: 16, Modified: 14, Untracked: 2,
		Files: []string{" M cmd/pogo/main.go", "?? cmd/pogo/checkprogress.go"},
	}
}

// TestSpawnRefusedForAnItemWithAPreservedWorktree is the whole finding, at the
// moment it costs something.
//
// mg-516e read `available`, priority-wake advertised it as unclaimed and ready,
// and sixteen uncommitted files — including a whole new command and its tests —
// sat in ~/.pogo/polecats/p516e. The dispatch that morning was stopped only by
// accident: `git worktree add` failed because the branch was still checked out.
// That error names a different reason, and its obvious remedy destroys the files.
func TestSpawnRefusedForAnItemWithAPreservedWorktree(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetPreservedWorktreeGate(&stubPreservedGate{
		trees: []gitgc.PreservedTree{preservedTree("/Users/x/.pogo/polecats/p516e", "polecat-p516e")},
	})

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "q516e", Id: "mg-516e", Repo: "/repo", Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn onto an item whose work sits UNCOMMITTED in a retained worktree: status = %d, "+
			"want 409. Detection, preservation, the mail and the standing list all already worked; "+
			"nothing gated dispatch on any of it (mg-836c)", rr.Code)
	}
	body := rr.Body.String()
	// The path is the load-bearing half. A refusal that does not name the tree
	// cannot stop the reflex remedy, which is to delete it.
	for _, want := range []string{"p516e", "polecat-p516e", "14 modified", "2 untracked"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not mention %q — a reader cannot act on it; got: %s", want, body)
		}
	}
	if !strings.Contains(body, "DO NOT remove the worktree") {
		t.Errorf("the refusal must forbid the reflex remedy explicitly: the near-miss on 2026-08-19 "+
			"was one `git worktree remove` away from destroying the only copy; got: %s", body)
	}
	if !strings.Contains(body, "--preserved-override") {
		t.Errorf("the refusal must name its way out, or it gets disarmed rather than overridden; got: %s", body)
	}
	// It must NOT prescribe the stranded gate's remedy. There is nothing to
	// submit until somebody commits, and the refinery refuses a branch that is
	// not on origin.
	if strings.Contains(body, "refinery submit") {
		t.Errorf("the refusal prescribes `refinery submit`, which cannot run here — nothing is "+
			"committed, let alone pushed; got: %s", body)
	}
}

// TestSpawnAllowedWithPreservedOverride pins the escape hatch, and that using it
// is never silent. Attribution is a name match, so this gate can be wrong — and
// a gate that can be wrong with no way past it gets disarmed rather than
// overridden.
func TestSpawnAllowedWithPreservedOverride(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetPreservedWorktreeGate(&stubPreservedGate{
		trees: []gitgc.PreservedTree{preservedTree("/Users/x/.pogo/polecats/p516e", "polecat-p516e")},
	})

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "q516e", Id: "mg-516e", Repo: "/repo", Branch: "main", Template: BuildWorkerTemplate,
		PreservedOverride: "read every file; all 16 are regenerated suite output",
	})
	if rr.Code == http.StatusConflict {
		t.Fatalf("--preserved-override did not clear the preserved-worktree gate: %s", rr.Body.String())
	}
	// A blank override is not an override: the reason IS the deliverable.
	rr2 := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "q516e", Id: "mg-516e", Repo: "/repo", Branch: "main", Template: BuildWorkerTemplate,
		PreservedOverride: "   ",
	})
	if rr2.Code != http.StatusConflict {
		t.Fatalf("a whitespace-only override cleared the gate (status %d); it must not — a bare "+
			"--force records that someone overrode the gate and loses the only thing a later reader "+
			"needs", rr2.Code)
	}
}

// TestPreservedRefusalOnATreeThatCouldNotBeRead. The failure direction is OPEN
// at the edges and CLOSED at the centre: a tree that was FOUND and could not be
// READ still refuses, and the message must not claim uncommitted work was
// positively seen. gc already refuses to reclaim such a tree; a gate that
// dispatched over one would be less careful than the reaper it covers for.
func TestPreservedRefusalOnATreeThatCouldNotBeRead(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetPreservedWorktreeGate(&stubPreservedGate{
		trees: []gitgc.PreservedTree{{
			Path: "/Users/x/.pogo/polecats/pfbaf", Owner: "pfbaf",
			Outcome: "undetermined", StatusError: "status: exit status 128: fatal: not a git repository",
		}},
	})
	refusal := reg.preservedWorktreeRefusal("mg-fbaf", "/repo")
	if refusal == "" {
		t.Fatal("a retained tree that could not be read must still refuse: what we failed to " +
			"establish is whether it holds the only copy of somebody's work")
	}
	if !strings.Contains(refusal, "could NOT be read") {
		t.Errorf("the refusal must say the tree was unread, not assert uncommitted work it never "+
			"saw; got: %s", refusal)
	}
	if strings.Contains(refusal, "already has UNCOMMITTED work") {
		t.Errorf("the refusal asserts uncommitted work for a tree nobody could read. It is still a "+
			"refusal, but the claim has to match what was established, or a reader who opens the "+
			"tree and finds nothing learns that this gate overstates; got: %s", refusal)
	}
}

// TestPreservedGateFailsOpenOnError. A guard that halts the fleet over one bad
// path gets disarmed rather than fixed, and `--id` is optional by design.
func TestPreservedGateFailsOpenOnError(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetPreservedWorktreeGate(&stubPreservedGate{err: errors.New("read polecats dir: EACCES")})
	if refusal := reg.preservedWorktreeRefusal("mg-516e", "/repo"); refusal != "" {
		t.Fatalf("the gate refused on an unanswerable question: %s", refusal)
	}
	// And no id is not a question at all.
	stub := &stubPreservedGate{}
	reg.SetPreservedWorktreeGate(stub)
	if refusal := reg.preservedWorktreeRefusal("", "/repo"); refusal != "" {
		t.Fatalf("the gate refused a spawn with no work item id: %s", refusal)
	}
}

// TestGitPreservedWorktreeGateReadsRealTrees drives the production gate against
// a real polecats directory, so the wiring between the gate and gitgc is not
// only asserted through a stub.
func TestGitPreservedWorktreeGateReadsRealTrees(t *testing.T) {
	repo := strandedRepo(t)
	polecats := filepath.Join(filepath.Dir(repo), "polecats")
	if err := os.MkdirAll(polecats, 0755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(polecats, "p9d4e")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "polecat-p9d4e", wt)
	if err := os.WriteFile(filepath.Join(wt, "rescue-me.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	g := GitPreservedWorktreeGate{PolecatsDir: polecats}
	trees, err := g.PreservedWorktrees("mg-9d4e", repo)
	if err != nil {
		t.Fatalf("PreservedWorktrees: %v", err)
	}
	if len(trees) != 1 {
		t.Fatalf("want 1 tree for mg-9d4e, got %d (%+v)", len(trees), trees)
	}
	if trees[0].Untracked != 1 {
		t.Errorf("Untracked = %d, want 1 — an untracked path is the case this whole guard exists "+
			"for: it is on no branch, in no stash and on no remote", trees[0].Untracked)
	}

	// A different item gets no refusal from the same directory.
	other, err := g.PreservedWorktrees("mg-0000", repo)
	if err != nil {
		t.Fatalf("PreservedWorktrees: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("the gate attributed another item's tree to mg-0000: %+v", other)
	}
}

// TestPreservedGateConsultsNoLiveness pins the property that is easiest to lose
// to a plausible optimisation. "The polecat is running, so its tree is in use,
// so skip the check" is one line and reads as obviously safe — and it is
// StrandedWorkGate's documented mistake: a running polecat is the PRECONDITION
// for the harm, not evidence against it. The gate's inputs are the id and the
// repo, full stop.
func TestPreservedGateConsultsNoLiveness(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.agents["p516e"] = livePolecat("p516e", "mg-516e")
	reg.SetPreservedWorktreeGate(&stubPreservedGate{
		trees: []gitgc.PreservedTree{preservedTree("/Users/x/.pogo/polecats/p516e", "polecat-p516e")},
	})
	if refusal := reg.preservedWorktreeRefusal("mg-516e", "/repo"); refusal == "" {
		t.Fatal("the gate went quiet because a polecat was live on the item. A live worker is why " +
			"the tree is dirty; it is not a reason to dispatch a second one")
	}
}
