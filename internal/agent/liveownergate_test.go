package agent

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for drellem2/pogo#167: a duplicate spawn-polecat dispatch onto a name or
// an item a live polecat already holds used to destroy that polecat's worktree.
//
// The four guards are tested one at a time and each is given a case only IT can
// answer, because they overlap in production on purpose (a live-owner gate that
// reads liveness, a destructor that reads the filesystem, and an ownership check
// that reads the worktree list all cover the incident). Overlapping guards make
// vacuous tests easy: a regression in any one of them hides behind the other
// two unless the test isolates it.

// TestLiveOwnerGate_RefusesADuplicateName. The name is not a label — the
// worktree directory and the branch are made from it — so a second polecat under
// a live one's name is aimed at the live one's tree by construction.
func TestLiveOwnerGate_RefusesADuplicateName(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.agents["82ad"] = livePolecat("82ad", "mg-82ad")

	refusal := reg.liveOwnerRefusal("82ad", "mg-other")
	if refusal == "" {
		t.Fatal("a dispatch reusing a live polecat's name was allowed: its worktree is the target")
	}
	for _, want := range []string{"82ad", "ALREADY RUNNING", "pogo agent stop"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal must contain %q, got: %s", want, refusal)
		}
	}
	assertNotOverridable(t, refusal)
}

// TestLiveOwnerGate_RefusesADuplicateItem. The item half is the one the
// stall-watch / priority-wake nag actually produces (drellem2/pogo#99 is its
// upstream): a second worker dispatched at claimed work, usually under a
// different name and on a different branch.
func TestLiveOwnerGate_RefusesADuplicateItem(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.agents["first"] = livePolecat("first", "mg-contested")

	refusal := reg.liveOwnerRefusal("second", "mg-contested")
	if refusal == "" {
		t.Fatal("a second polecat was allowed onto an item a live polecat is already working")
	}
	for _, want := range []string{"mg-contested", "first", "pogo agent list", "mg show mg-contested"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal must contain %q, got: %s", want, refusal)
		}
	}
	assertNotOverridable(t, refusal)
}

// TestLiveOwnerGate_SeesARestartSurvivor is why the gate reads the UNION rather
// than this pogod's registry. The in-memory registry is empty after a restart,
// permanently — it has no adopt path (mg-13a3) — and a polecat that outlived the
// pogod that spawned it is exactly the worker nobody remembers is running. A
// registry-only gate would be blind to the population it exists for.
func TestLiveOwnerGate_SeesARestartSurvivor(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t) // empty: this pogod spawned nothing
	if err := RecordPolecatWitness("survivor", liveProcess(t), "mg-survivor", "/repo"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	if refusal := reg.liveOwnerRefusal("survivor", "mg-unrelated"); refusal == "" {
		t.Error("a restart-surviving polecat's NAME was reused: the union is not being consulted")
	}
	if refusal := reg.liveOwnerRefusal("fresh-name", "mg-survivor"); refusal == "" {
		t.Error("a restart-surviving polecat's ITEM was re-dispatched: the union is not being consulted")
	}
}

// TestLiveOwnerGate_SurvivorRefusalOffersExitsThatWork pins round 1 of PR #172.
// For a WITNESS-evidence owner the registry-evidence exits are dead ends: the
// survivor is not in `pogo agent list` and `pogo agent stop <name>` answers 404
// (Registry.StopWithCause), because this pogod never spawned it. A refusal with
// no override whose only exits fail is a dead end, so both halves must name the
// pid, `pogo agent witness`, and a kill gated on the witness — and every
// command they offer must actually run. The ones that CAN run without a live
// pogod (git, ps) are executed here, not string-matched.
func TestLiveOwnerGate_SurvivorRefusalOffersExitsThatWork(t *testing.T) {
	sandboxWitness(t)
	t.Setenv("POGO_HOME", t.TempDir())
	pid := liveProcess(t)
	if err := RecordPolecatWitness("survivor", pid, "mg-survivor", "/repo"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	// The survivor's tree, as a real repo, so the `git -C` it is sent to runs.
	tree := polecatWorktreePath("survivor")
	if err := os.MkdirAll(tree, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, tree, "init", "-q")
	reg := newDrainTestRegistry(t) // empty: the survivor is known only from the witness

	for _, tc := range []struct{ half, name, item string }{
		{"name", "survivor", "mg-unrelated"},
		{"item", "fresh-name", "mg-survivor"},
	} {
		refusal := reg.liveOwnerRefusal(tc.name, tc.item)
		if refusal == "" {
			t.Fatalf("%s half: a restart survivor was not refused", tc.half)
		}
		pidStr := strconv.Itoa(pid)
		for _, want := range []string{
			"pid " + pidStr,
			"`pogo agent witness`",
			"404",
			WitnessAliveGrep("survivor", pid),
			"&& kill " + pidStr + " && mg unclaim mg-survivor",
		} {
			if !strings.Contains(refusal, want) {
				t.Errorf("%s half: survivor refusal must contain %q, got: %s", tc.half, want, refusal)
			}
		}
		assertOfferedCommandsRun(t, tc.half, refusal)
		assertNotOverridable(t, refusal)
	}
}

// TestLiveOwnerGate_UnreadableSurvivorIsNotSentToAGrepThatNeverFires. A
// survivor whose start time cannot be read still counts as live (LivePolecatSet),
// but `pogo agent witness` lists only CONFIRMED survivors — so the witness-gated
// kill line would be a command that silently never fires. The refusal must name
// the pid and a hand identity check instead.
func TestLiveOwnerGate_UnreadableSurvivorIsNotSentToAGrepThatNeverFires(t *testing.T) {
	sandboxWitness(t)
	t.Setenv("POGO_HOME", t.TempDir())
	pid := liveProcess(t)
	if err := RecordPolecatWitness("survivor", pid, "mg-survivor", "/repo"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	tree := polecatWorktreePath("survivor")
	if err := os.MkdirAll(tree, 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, tree, "init", "-q")
	prev := procStartFn
	procStartFn = func(int) (time.Time, bool) { return time.Time{}, false }
	t.Cleanup(func() { procStartFn = prev })
	reg := newDrainTestRegistry(t)

	refusal := reg.liveOwnerRefusal("fresh-name", "mg-survivor")
	if refusal == "" {
		t.Fatal("an unreadable-but-live survivor's item was re-dispatched")
	}
	pidStr := strconv.Itoa(pid)
	for _, want := range []string{"`ps -o lstart=,command= -p " + pidStr + "`", "`kill " + pidStr + " && mg unclaim mg-survivor`"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal must contain %q, got: %s", want, refusal)
		}
	}
	if strings.Contains(refusal, "grep -q") {
		t.Errorf("an unreadable survivor is not in `pogo agent witness`; a kill gated on it never fires: %s", refusal)
	}
	assertOfferedCommandsRun(t, "unreadable", refusal)
}

// assertOfferedCommandsRun holds every backticked span in a refusal to being a
// command: no prose placeholder inside it (round 1 shipped `git -C <path> (the
// path this dispatch would have targeted) status`), and never one of the exits
// that answers 404 for a survivor. The spans that need no pogod — git, ps — are
// executed and must exit 0.
func assertOfferedCommandsRun(t *testing.T, label, refusal string) {
	t.Helper()
	parts := strings.Split(refusal, "`")
	for i := 1; i < len(parts); i += 2 {
		span := parts[i]
		if strings.ContainsAny(span, "()<>") {
			t.Errorf("%s: offered command carries prose, not a command: `%s`", label, span)
		}
		if strings.HasPrefix(span, "pogo agent stop") || span == "pogo agent list" {
			t.Errorf("%s: offered `%s` for a survivor, which this pogod cannot see or stop", label, span)
		}
		if strings.HasPrefix(span, "git ") || strings.HasPrefix(span, "ps ") {
			if out, err := exec.Command("sh", "-c", span).CombinedOutput(); err != nil {
				t.Errorf("%s: offered command `%s` does not run: %v\n%s", label, span, err, out)
			}
		}
	}
}

// TestLiveOwnerGate_AllowsAnUncontestedDispatch is the control for all three
// above. A gate that refuses everything protects the fleet from doing any work,
// and this one is not overridable — so an over-broad refusal has no exit at all.
func TestLiveOwnerGate_AllowsAnUncontestedDispatch(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.agents["someone"] = livePolecat("someone", "mg-theirs")

	if refusal := reg.liveOwnerRefusal("nobody", "mg-mine"); refusal != "" {
		t.Fatalf("an uncontested dispatch was refused: %s", refusal)
	}
	// And with nothing running at all — the ordinary case, which must not
	// depend on a witness record existing.
	empty := newDrainTestRegistry(t)
	if refusal := empty.liveOwnerRefusal("nobody", "mg-mine"); refusal != "" {
		t.Fatalf("a dispatch into an idle fleet was refused: %s", refusal)
	}
}

// TestLiveOwnerGate_RefusesWhenTheWitnessCannotBeRead. This gate fails CLOSED,
// alone among the dispatch gates. An unreadable witness store is not an empty
// fleet — it is the only record of a restart-surviving polecat — and gitgc
// already SKIPS ITS SWEEP on the identical failure (mg-0130). Dispatching over
// it would make this gate strictly less careful than the reaper it covers for.
func TestLiveOwnerGate_RefusesWhenTheWitnessCannotBeRead(t *testing.T) {
	sandboxWitness(t)
	// A store that exists and is not JSON: "I could not look", not "nobody is
	// there". A missing file is the different, benign case — an idle fleet —
	// and TestLiveOwnerGate_AllowsAnUncontestedDispatch is its control.
	if err := os.WriteFile(WitnessPath(), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	reg := newDrainTestRegistry(t)

	refusal := reg.liveOwnerRefusal("anyone", "mg-anything")
	if refusal == "" {
		t.Fatal("dispatched over a witness store that could not be read: a restart-surviving " +
			"polecat is invisible in exactly this state")
	}
	if !strings.Contains(refusal, "cannot establish") {
		t.Errorf("the unreadable-instrument refusal must not read like an owner was found, got: %s", refusal)
	}
	if !strings.Contains(refusal, WitnessPath()) {
		t.Errorf("refusal must name the store so it can be repaired, got: %s", refusal)
	}
	assertNotOverridable(t, refusal)
}

// TestSpawnPolecat_LiveOwnerRefusalCreatesNothing is the mg-ef80 property for
// this gate, and here it is not tidiness: the side effects on the spawn path are
// what destroy the live worker's tree. The refusal has to land before any of
// them, so a refused duplicate leaves no worktree, no branch and no prompt file.
func TestSpawnPolecat_LiveOwnerRefusalCreatesNothing(t *testing.T) {
	sandboxWitness(t)
	workDir, _ := makeRepoWithOrigin(t)
	reg := newDrainTestRegistry(t)
	pogoHome := installPolecatTemplate(t)
	reg.agents["dup"] = livePolecat("dup", "mg-dup")

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "dup",
		Id:       "mg-dup",
		Repo:     workDir,
		Template: BuildWorkerTemplate,
	})

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a duplicate dispatch; body: %s", rr.Code, rr.Body.String())
	}
	if hasBranch(t, workDir, "polecat-dup") {
		t.Error("a refused duplicate dispatch created the branch polecat-dup")
	}
	if _, err := os.Stat(filepath.Join(pogoHome, "polecats", "dup")); !os.IsNotExist(err) {
		t.Error("a refused duplicate dispatch created a worktree directory at the live polecat's path")
	}
}

// TestSpawnPolecat_DoesNotDestroyALivePolecatsTree is the end-to-end statement of
// drellem2/pogo#167, driven through the real handler, with the live-owner gate
// deliberately blind so the WORKTREE guards are what is under test.
//
// Blind how, and why that is the realistic case rather than a contrivance: this
// pogod's registry is empty and the witness holds no record — the state after a
// restart (mg-13a3), which is precisely when a survivor is running and nothing
// in memory knows it. The victim is a polecat working a FOREIGN branch, so
// polecat-82ad is checked out nowhere and reads as an unowned leftover.
//
// The old chain: reclaim deletes polecat-82ad (no worktree has it checked out,
// nothing unmerged), `git worktree add` then fails on the occupied path, and the
// rollback for that failure force-removes the directory — taking an untracked
// file that is on no branch, in no stash and on no remote.
func TestSpawnPolecat_DoesNotDestroyALivePolecatsTree(t *testing.T) {
	sandboxWitness(t)
	workDir, _ := makeRepoWithOrigin(t)
	reg := newDrainTestRegistry(t)
	pogoHome := installPolecatTemplate(t)

	// The live polecat: its tree is at polecats/82ad and it has moved to
	// someone else's branch, as review and QA polecats are instructed to.
	wt := filepath.Join(pogoHome, "polecats", "82ad")
	if err := os.MkdirAll(filepath.Dir(wt), 0755); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "worktree", "add", wt, "-b", "polecat-82ad")
	runGit(t, wt, "checkout", "-q", "-b", "polecat-e400")
	live := filepath.Join(wt, "LIVE_WORK.txt")
	if err := os.WriteFile(live, []byte("the only copy\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// No --id: the item-keyed gates (dispatch, preserved, merged) have nothing
	// to say, which leaves the worktree guards as the only thing that can stop
	// this. That is the point of the test.
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "82ad",
		Repo:     workDir,
		Template: BuildWorkerTemplate,
	})

	if rr.Code == http.StatusNotFound {
		t.Fatalf("spawn failed before reaching worktree creation, so this test proves nothing: %s",
			rr.Body.String())
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("THE LIVE POLECAT'S ONLY COPY WAS DESTROYED (drellem2/pogo#167): %v", err)
	}
	if !hasBranch(t, workDir, "polecat-82ad") {
		t.Error("the live polecat's branch was deleted by the reclaim path")
	}
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 naming the live owner; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "still has a tree") {
		t.Errorf("the refusal must name the cause, got: %s", rr.Body.String())
	}
}

// TestSpawnPolecat_LeaksAnOccupiedDirectoryRatherThanRemovingIt isolates the
// destructor, which is the last guard and the only one that covers a directory
// no ref points at: an orphan left by a pogod that died mid-spawn (gh #31), or a
// tree whose registration was lost. There is no polecat-82ad branch here, so
// reclamation is a no-op and the ownership check has nothing to answer — the add
// fails on the occupied path and the rollback is all that stands between the
// files and --force.
//
// The leak is the deliberate trade (gh #31 is what --force was added for): a
// leaked directory is visible, recoverable and costs disk, and the alternative
// is unrecoverable and silent. The two are not comparable losses.
func TestSpawnPolecat_LeaksAnOccupiedDirectoryRatherThanRemovingIt(t *testing.T) {
	sandboxWitness(t)
	workDir, _ := makeRepoWithOrigin(t)
	reg := newDrainTestRegistry(t)
	pogoHome := installPolecatTemplate(t)

	dir := filepath.Join(pogoHome, "polecats", "82ad")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	orphaned := filepath.Join(dir, "WORK.txt")
	if err := os.WriteFile(orphaned, []byte("nowhere else\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name:     "82ad",
		Repo:     workDir,
		Template: BuildWorkerTemplate,
	})
	if rr.Code == http.StatusNotFound {
		t.Fatalf("spawn failed before reaching worktree creation, so this test proves nothing: %s",
			rr.Body.String())
	}
	if _, err := os.Stat(orphaned); err != nil {
		t.Fatalf("the failed spawn removed a directory it did not create: %v", err)
	}
}

// TestCleanupFailedPolecatSpawn_StillRemovesItsOwnWorktree is the control for
// the two tests above, and it guards the gh #27 / gh #31 regression the leak
// re-opens if it is applied too widely. A tree this spawn DID create must still
// be removed with its branch, or a failed spawn poisons every retry of the same
// work item with "branch already exists".
func TestCleanupFailedPolecatSpawn_StillRemovesItsOwnWorktree(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	wt := filepath.Join(t.TempDir(), "ours")
	runGit(t, workDir, "worktree", "add", wt, "-b", "polecat-ours")

	cleanupFailedPolecatSpawn(workDir, wt, "polecat-ours", worktreeDirIsOurs)

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("a worktree this spawn created was not removed: %v", err)
	}
	if hasBranch(t, workDir, "polecat-ours") {
		t.Error("the branch this spawn created survived: the next retry fails with 'already exists'")
	}
}

// TestCleanupFailedPolecatSpawn_LeavesAForeignDirectoryAlone is the unit
// statement of the same rule the handler test exercises end to end, kept
// separate because it pins the function's contract rather than one route to it.
func TestCleanupFailedPolecatSpawn_LeavesAForeignDirectoryAlone(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	dir := filepath.Join(t.TempDir(), "theirs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(keep, []byte("only copy\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cleanupFailedPolecatSpawn(workDir, dir, "", worktreeDirPreexisted)

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a directory this spawn did not create was destroyed: %v", err)
	}
}

// assertNotOverridable: every refusal from this gate must say there is no flag
// AND why, in the same breath. A reader told only "no override" reads it as an
// omission and goes looking — which is a reasonable expectation to have, given
// that the four gates below this one all have one.
func assertNotOverridable(t *testing.T, refusal string) {
	t.Helper()
	if !strings.Contains(refusal, "NO OVERRIDE") {
		t.Errorf("refusal must state that it cannot be overridden, got: %s", refusal)
	}
	if !strings.Contains(refusal, "no other copy") {
		t.Errorf("refusal must say WHY there is no override — a bare prohibition sends the "+
			"reader looking for the flag — got: %s", refusal)
	}
}

// installPolecatTemplate points POGO_HOME at a temp dir and writes a minimal
// polecat template under it. POGO_HOME must be set before the template is
// installed: it roots both TemplateDir and the polecats dir, so the handler and
// the test have to agree on one location.
func installPolecatTemplate(t *testing.T) string {
	t.Helper()
	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)
	tmplDir := filepath.Join(pogoHome, "agents", "templates")
	if err := os.MkdirAll(tmplDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("task {{.Id}}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return pogoHome
}
