package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for mg-d22a: a failed polecat spawn used to leave a branch
// with no worktree (poisoning every retry of that work item with a misleading
// "branch already exists"), and it emitted no event at all — so a gap in the
// spawn record was indistinguishable from a spawn that was never attempted.
//
// The two halves are tested separately on purpose. A reviewer who checks only
// the HTTP status leaves the record just as unreadable as it was: the status
// tells the live caller, the event tells everyone who reads the log afterwards.

// spawnPolecat drives the handler directly and returns the recorder.
func spawnPolecat(t *testing.T, reg *Registry, req SpawnPolecatAPIRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, r)
	return rr
}

// branchNames returns the repo's local branches.
func branchNames(t *testing.T, repo string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "branch", "--format=%(refname:short)").Output()
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

func hasBranch(t *testing.T, repo, branch string) bool {
	t.Helper()
	for _, b := range branchNames(t, repo) {
		if b == branch {
			return true
		}
	}
	return false
}

// TestSpawnPolecat_FailureEmitsSpawnFailedEvent is the (b) half of mg-d22a's
// acceptance: a failed spawn must put its cause on the record. Before this fix
// pogod emitted nothing on any failure path — 34,090 agent_spawned events in
// the live log and not one failure counterpart — so a reader reconstructing a
// gap between spawn records had to guess a mechanism. One did, guessed
// "throttled by the dispatch cap", and wrote the guess into a ticket as a
// finding. It was wrong: the spawn had failed.
func TestSpawnPolecat_FailureEmitsSpawnFailedEvent(t *testing.T) {
	logPath := useTempEventLog(t)
	reg := newDrainTestRegistry(t)

	// An unresolvable template fails the spawn early, with no git side effects.
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "cat-d22a",
		Template: "no-such-template-d22a",
		Id:       "mg-d22a",
		Repo:     "/some/repo",
	})
	if rr.Code == http.StatusCreated {
		t.Fatalf("spawn with a bogus template unexpectedly succeeded")
	}

	evs := readEventLines(t, logPath)
	ev := findEvent(evs, "agent_spawn_failed", "cat-cat-d22a")
	if ev == nil {
		var seen []string
		for _, e := range evs {
			seen = append(seen, fmt.Sprintf("%v/%v", e["event_type"], e["agent"]))
		}
		t.Fatalf("no agent_spawn_failed event emitted for cat-cat-d22a; saw %v", seen)
	}

	// The event must carry enough to identify what failed and why, without the
	// reader supplying any of it.
	if ev["work_item_id"] != "mg-d22a" {
		t.Errorf("event work_item_id = %v, want mg-d22a", ev["work_item_id"])
	}
	if ev["repo"] != "/some/repo" {
		t.Errorf("event repo = %v, want /some/repo", ev["repo"])
	}
	details, _ := ev["details"].(map[string]any)
	if reason, _ := details["reason"].(string); reason == "" {
		t.Error("event details.reason is empty: the cause must be on the record")
	}
	if name, _ := details["agent_name"].(string); name != "cat-d22a" {
		t.Errorf("event details.agent_name = %v, want cat-d22a", details["agent_name"])
	}
}

// TestSpawnPolecat_SuccessEmitsNoSpawnFailedEvent keeps the failure event
// honest: it must fire on failure only, or it is just noise that makes the log
// less readable rather than more.
func TestSpawnPolecat_SuccessEmitsNoSpawnFailedEvent(t *testing.T) {
	logPath := useTempEventLog(t)
	reg := newDrainTestRegistry(t)

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "cat-ok-d22a", Id: "mg-d22a", Template: BuildWorkerTemplate})
	if rr.Code != http.StatusCreated {
		t.Skipf("spawn did not succeed in this environment (status %d); "+
			"the negative assertion needs a successful spawn to be meaningful", rr.Code)
	}
	if ev := findEvent(readEventLines(t, logPath), "agent_spawn_failed", "cat-cat-ok-d22a"); ev != nil {
		t.Errorf("successful spawn emitted agent_spawn_failed: %+v", ev)
	}
}

// TestSpawnPolecat_DrainRefusalIsNamedOnTheRecord covers the exact ambiguity
// that produced the false finding: a throttle and a failure must not emit the
// identical nothing. The drain refusal is a throttle — it must say so, and say
// which item it refused.
func TestSpawnPolecat_DrainRefusalIsNamedOnTheRecord(t *testing.T) {
	logPath := useTempEventLog(t)
	reg := newDrainTestRegistry(t)
	reg.SetDraining(true)

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{Name: "cat-drain-d22a", Id: "mg-d22a"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("spawn while draining = %d, want 503", rr.Code)
	}
	ev := findEvent(readEventLines(t, logPath), "agent_spawn_failed", "cat-cat-drain-d22a")
	if ev == nil {
		t.Fatal("drain refusal emitted no event: a throttled spawn and a failed " +
			"spawn are then indistinguishable on the record")
	}
	if ev["work_item_id"] != "mg-d22a" {
		t.Errorf("drain refusal event work_item_id = %v, want mg-d22a", ev["work_item_id"])
	}
	details, _ := ev["details"].(map[string]any)
	if reason, _ := details["reason"].(string); !strings.Contains(reason, "draining") {
		t.Errorf("drain refusal reason = %v, want it to name the drain", details["reason"])
	}
}

// TestSpawnPolecat_FailedWorktreeAddRollsBackItsBranch covers the second
// measured defect. `git worktree add -b <branch>` creates the branch and *then*
// checks it out, so a failure in the checkout leaves the branch behind with no
// worktree — verified directly: a blocked target dir fails the add and the
// branch survives. Every other failure path in the handler already called
// cleanupFailedPolecatSpawn; this one did not, so a partial failure poisoned
// every subsequent retry of that work item with a *different* and misleading
// error ("a branch named X already exists") that names nothing about the
// original cause (mg-d22a).
//
// This needs no concurrency: blocking the worktree target reproduces it
// deterministically. The same-repo-race hypothesis this ticket carries is a
// separate, still-unproven question — this defect and its fix do not depend on
// it either way.
func TestSpawnPolecat_FailedWorktreeAddRollsBackItsBranch(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	reg := newDrainTestRegistry(t)

	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)
	tmplDir := filepath.Join(pogoHome, "agents", "templates")
	if err := os.MkdirAll(tmplDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("task {{.Id}}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Occupy the worktree target with a non-empty directory so `git worktree
	// add` fails *after* creating the branch — the exact partial failure.
	wtDir := filepath.Join(pogoHome, "polecats", "cat-rollback")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "occupied.txt"), []byte("in the way\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-rollback",
		Id:   "mg-d22a",
		Repo: workDir,
		// Explicit, so the type→template router (mg-9a04) is not what stops
		// this spawn: the subject here is worktree rollback, and the spawn has
		// to reach worktree creation for the test to prove anything.
		Template: BuildWorkerTemplate,
	})
	if rr.Code == http.StatusCreated {
		t.Fatal("spawn unexpectedly succeeded; the worktree target was supposed to be blocked")
	}
	if !strings.Contains(rr.Body.String(), "worktree creation failed") {
		t.Fatalf("expected a worktree-creation failure, got: %s", rr.Body.String())
	}

	// The property: the failure left nothing that blocks the next attempt.
	if hasBranch(t, workDir, "polecat-cat-rollback") {
		t.Error("failed spawn left branch polecat-cat-rollback behind with no worktree: " +
			"every retry of this work item is now permanently blocked")
	}
}

// TestSpawnPolecat_FailedWorktreeAddKeepsAPreexistingBranch is the limit on the
// rollback above. Rolling back means deleting a branch, and the handler must
// only ever delete the branch *it* created. If a branch appeared underneath us
// between the reclamation check and the add — a concurrent spawn of the same
// name, which is exactly what the unproven same-repo-race hypothesis would
// imply — deleting it would destroy that spawn's work. Here the branch carries
// unmerged commits, so reclamation refuses and the branch must survive intact.
func TestSpawnPolecat_FailedWorktreeAddKeepsAPreexistingBranch(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	reg := newDrainTestRegistry(t)

	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)
	tmplDir := filepath.Join(pogoHome, "agents", "templates")
	if err := os.MkdirAll(tmplDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("task {{.Id}}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// A branch with real, unmerged work and no worktree.
	runGit(t, workDir, "checkout", "-q", "-b", "polecat-cat-precious")
	if err := os.WriteFile(filepath.Join(workDir, "work.txt"), []byte("precious\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "work.txt")
	runGit(t, workDir, "commit", "-m", "unmerged work")
	runGit(t, workDir, "checkout", "-q", "main")

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "cat-precious",
		Id:       "mg-d22a",
		Repo:     workDir,
		Template: BuildWorkerTemplate, // reach worktree creation; see cat-rollback
	})
	if rr.Code == http.StatusCreated {
		t.Fatal("spawn succeeded onto a branch holding unmerged work")
	}
	if !hasBranch(t, workDir, "polecat-cat-precious") {
		t.Fatal("a branch with unmerged commits was deleted by the spawn failure path")
	}
	// And the refusal explains itself, rather than repeating git's misleading
	// "a branch named X already exists".
	if !strings.Contains(rr.Body.String(), "unmerged") {
		t.Errorf("refusal must name the cause on the wire, got: %s", rr.Body.String())
	}
}

// TestReclaimStalePolecatBranch_ClearsSpentOrphan is the dispatch-blocker fix.
// A polecat-<name> branch with no worktree, carrying nothing that isn't already
// in the base ref, is spent — it must not block the next dispatch of that id.
// These branches are not exotic: they accumulate from ordinary successful
// merged work (55 existed in one repo when this was written), and each one was
// a permanent landmine for its id.
func TestReclaimStalePolecatBranch_ClearsSpentOrphan(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	// A branch at the same commit as main: no worktree, nothing unmerged.
	runGit(t, workDir, "branch", "polecat-spent", "main")

	if err := reclaimStalePolecatBranch(workDir, "polecat-spent", "main"); err != nil {
		t.Fatalf("reclaim of a spent orphan branch failed: %v", err)
	}
	if hasBranch(t, workDir, "polecat-spent") {
		t.Error("spent orphan branch survived reclamation; it still blocks re-dispatch")
	}
}

// TestReclaimStalePolecatBranch_AbsentIsNoOp: the common path (no leftover) must
// be silent and successful.
func TestReclaimStalePolecatBranch_AbsentIsNoOp(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	if err := reclaimStalePolecatBranch(workDir, "polecat-nonexistent", "main"); err != nil {
		t.Fatalf("reclaim of an absent branch should be a no-op, got: %v", err)
	}
}

// TestReclaimStalePolecatBranch_RefusesUnmergedWork is the safety property that
// makes automatic reclamation defensible. Reclamation deletes branches; a
// branch carrying commits nobody has merged is real work, and deleting it would
// destroy it. Refusing is the only correct answer — and the refusal must name
// the cause, unlike git's "a branch named X already exists".
func TestReclaimStalePolecatBranch_RefusesUnmergedWork(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	runGit(t, workDir, "checkout", "-q", "-b", "polecat-unmerged")
	if err := os.WriteFile(filepath.Join(workDir, "work.txt"), []byte("precious\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "work.txt")
	runGit(t, workDir, "commit", "-m", "unmerged work")
	runGit(t, workDir, "checkout", "-q", "main")

	err := reclaimStalePolecatBranch(workDir, "polecat-unmerged", "main")
	if err == nil {
		t.Fatal("reclaim deleted a branch with unmerged commits: work would be lost")
	}
	if !strings.Contains(err.Error(), "unmerged") {
		t.Errorf("refusal must name the cause, got: %v", err)
	}
	if !hasBranch(t, workDir, "polecat-unmerged") {
		t.Fatal("branch with unmerged work was deleted despite the refusal")
	}
}

// TestReclaimStalePolecatBranch_RefusesLiveWorktree: a branch checked out in a
// worktree belongs to a running polecat. Reclaiming it would pull the floor out
// from under live work.
//
// The worktree is deliberately at a path whose basename is NOT the polecat name
// (drellem2/pogo#167). Both liveness tests would fire on the natural
// polecats/<name> layout, and this test's subject is the checked-out one, so it
// has to be the only one that can answer — otherwise a regression in it would
// hide behind its sibling. TestReclaimStalePolecatBranch_RefusesOwnerOnForeignBranch
// is the mirror image, isolating the other.
func TestReclaimStalePolecatBranch_RefusesLiveWorktree(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	wt := filepath.Join(t.TempDir(), "somewhere-else")
	runGit(t, workDir, "worktree", "add", wt, "-b", "polecat-live")

	err := reclaimStalePolecatBranch(workDir, "polecat-live", "main")
	if err == nil {
		t.Fatal("reclaim cleared a branch checked out by a live worktree")
	}
	if !strings.Contains(err.Error(), "still live") {
		t.Errorf("refusal must name the live polecat, got: %v", err)
	}
	// Matched on the basename, not the full path: git reports a worktree at the
	// location it resolved when it was added, which on macOS is the /private
	// form of a /var temp dir. Asserting the string the test happens to hold
	// measures the platform's symlink layout, not the refusal.
	if !strings.Contains(err.Error(), "somewhere-else") {
		t.Errorf("refusal must name the worktree holding the branch, got: %v", err)
	}
	if !hasBranch(t, workDir, "polecat-live") {
		t.Fatal("live polecat's branch was deleted")
	}
}

// TestReclaimStalePolecatBranch_RefusesOwnerOnForeignBranch is the regression
// for drellem2/pogo#167, and it is the case the old checked-out-branch proxy got
// wrong in the destructive direction.
//
// A polecat is working a FOREIGN branch — a review or QA polecat reading someone
// else's work, which our own shipped roles instruct — so nothing anywhere has
// polecat-<name> checked out. Under the old lookup that made the branch read as
// an unowned leftover: no worktree, nothing unmerged against the base, so it was
// deleted. That deletion is only the first half; `git worktree add` then fails
// on the occupied path and the rollback for THAT failure removed the live tree
// with --force.
//
// The tree here is named after the polecat and holds an UNTRACKED file, which is
// what the incident actually costs: a path on no branch, in no stash and on no
// remote, so the tree is its only copy anywhere on the machine.
func TestReclaimStalePolecatBranch_RefusesOwnerOnForeignBranch(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	polecats := t.TempDir()
	wt := filepath.Join(polecats, "82ad")
	runGit(t, workDir, "worktree", "add", wt, "-b", "polecat-82ad")
	// The polecat moves to someone else's branch and starts working.
	runGit(t, wt, "checkout", "-q", "-b", "polecat-e400")
	live := filepath.Join(wt, "LIVE_WORK.txt")
	if err := os.WriteFile(live, []byte("the only copy\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Positive control for the instrument: the branch really is invisible to
	// the checked-out test now, so a pass below is the ownership check working
	// and not the situation failing to reproduce.
	holder, err := polecatBranchHolders(workDir, "polecat-82ad")
	if err != nil {
		t.Fatalf("polecatBranchHolders: %v", err)
	}
	if holder.CheckedOutAt != "" {
		t.Fatalf("setup did not reproduce the defect: polecat-82ad is checked out at %s, so the "+
			"old proxy would have caught it and this test proves nothing", holder.CheckedOutAt)
	}
	// Compared on the basename: git reports the location it resolved at add
	// time, which on macOS is the /private form of a /var temp dir, and
	// ownership is defined on the basename anyway
	// (gitgc.PolecatNameForWorktree).
	if filepath.Base(holder.OwnerTree) != "82ad" {
		t.Fatalf("OwnerTree = %q, want the tree named 82ad — ownership must follow the directory name",
			holder.OwnerTree)
	}

	err = reclaimStalePolecatBranch(workDir, "polecat-82ad", "main")
	if err == nil {
		t.Fatal("reclaim deleted the branch of a live polecat working a foreign branch " +
			"(drellem2/pogo#167): the spawn that follows destroys its worktree")
	}
	if !strings.Contains(err.Error(), "still live") {
		t.Errorf("refusal must name the live polecat, got: %v", err)
	}
	if !strings.Contains(err.Error(), holder.OwnerTree) {
		t.Errorf("refusal must name the owning worktree so the operator can read it, got: %v", err)
	}
	if !hasBranch(t, workDir, "polecat-82ad") {
		t.Fatal("live polecat's branch was deleted")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the live polecat's untracked file is gone: %v", err)
	}
}

// TestReclaimStalePolecatBranch_StillReclaimsASpentBranch is the control for the
// two refusals above: the ownership check must not have turned reclamation off.
// A spent branch whose polecat is gone — the mg-d22a state, and by far the
// common one — is still cleared, or every re-dispatch of a reused work item id
// is permanently blocked.
func TestReclaimStalePolecatBranch_StillReclaimsASpentBranch(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	runGit(t, workDir, "branch", "polecat-gone", "main")

	if err := reclaimStalePolecatBranch(workDir, "polecat-gone", "main"); err != nil {
		t.Fatalf("reclaim refused a spent branch with no worktree at all: %v", err)
	}
	if hasBranch(t, workDir, "polecat-gone") {
		t.Fatal("spent branch survived reclamation: the next re-dispatch of this item is blocked")
	}
}

// TestPolecatBranchHoldersFailsClosedOnAnUnreadableRepo. The caller deletes a
// branch on the strength of this answer, so "I could not look" must be an error
// rather than an empty holder — an empty holder is spendable as "nobody is
// there", which is the one reading that destroys work.
func TestPolecatBranchHoldersFailsClosedOnAnUnreadableRepo(t *testing.T) {
	notARepo := t.TempDir()
	if _, err := polecatBranchHolders(notARepo, "polecat-x"); err == nil {
		t.Fatal("polecatBranchHolders returned a clean empty answer for a directory that is not a " +
			"git repo: an unreadable worktree list must not read as an empty one")
	}
}

// TestSpawnPolecat_StaleOrphanBranchNoLongerBlocksDispatch is the end-to-end
// statement of the blocker, driven through the real handler. This is the
// scenario that broke the recovery procedure documented in the mayor's own
// prompt (stop the agent → mg unclaim → re-dispatch): the re-dispatch failed on
// the surviving branch until a human ran `git branch -D` by hand.
func TestSpawnPolecat_StaleOrphanBranchNoLongerBlocksDispatch(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	reg := newDrainTestRegistry(t)

	// POGO_HOME must be set before the template is installed under it: it roots
	// both TemplateDir (where the handler resolves "polecat") and the polecats
	// dir (where the worktree lands).
	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)
	tmplDir := filepath.Join(pogoHome, "agents", "templates")
	if err := os.MkdirAll(tmplDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("task {{.Id}}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// The exact poisoned state: branch exists, no worktree, nothing unmerged.
	const branch = "polecat-cat-d22a"
	runGit(t, workDir, "branch", branch, "main")

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "cat-d22a",
		Id:       "mg-d22a",
		Repo:     workDir,
		Template: BuildWorkerTemplate, // reach worktree creation; see cat-rollback
	})

	// Guard against a vacuous pass: if the spawn died before it ever tried the
	// worktree, this test would "pass" while proving nothing. The handler
	// resolves the template first, so a 404 means we never reached the code
	// under test.
	if rr.Code == http.StatusNotFound {
		t.Fatalf("spawn failed before reaching worktree creation, so this test "+
			"proves nothing: %s", rr.Body.String())
	}

	// The blocker itself: git's "a branch named X already exists" is the
	// distinctive symptom of the poisoned state.
	if strings.Contains(rr.Body.String(), "already exists") {
		t.Fatalf("stale orphan branch still blocks dispatch: %s", rr.Body.String())
	}

	// And the positive statement: the spent branch was actually reclaimed and
	// the worktree got made on top of it. The spawn may still fail *after* this
	// point (no harness binary in a test env), and that failure path rolls the
	// worktree back — so assert on the reclamation having happened, which is
	// what unblocks re-dispatch.
	holder, err := polecatBranchHolders(workDir, branch)
	if err != nil {
		t.Fatalf("polecatBranchHolders: %v", err)
	}
	if hasBranch(t, workDir, branch) && holder.OwnerTree == "" && holder.CheckedOutAt == "" {
		t.Errorf("branch %s is still an orphan after dispatch: the next "+
			"re-dispatch of this work item is still blocked", branch)
	}
}
