package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// mgSandbox builds an isolated macguffin store and returns its root. Every test
// here points MGClaimReleaser.Root at one of these: the releaser's own default
// under a test binary is a throwaway temp store (see MGClaimReleaser.storeRoot),
// so a test that forgets this cannot reach the developer's live ~/.macguffin —
// but a test that means to exercise the real `mg unclaim` has to say where.
func mgSandbox(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("mg"); err != nil {
		t.Skip("mg not on PATH; the claim-release path needs the real macguffin CLI")
	}
	// Not t.TempDir(): mg writes a git snapshot repo under the root, and the
	// per-test cleanup of a deep temp path is noisy on failure. A short path
	// under the socket dir keeps the store beside the rest of the fixture.
	root := filepath.Join(shortSocketDir(t), "mgstore")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if out, err := exec.Command("mg", "--root", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("mg init: %v: %s", err, out)
	}
	return root
}

var mgCreatedID = regexp.MustCompile(`\b(mg-[0-9a-f]+)\b`)

// mgNewClaimed creates a work item in the sandbox store and claims it, leaving
// the store in the state a mid-flight polecat leaves it in: one claim file at
// work/claimed/<id>.md.<pid>, nothing in available/.
func mgNewClaimed(t *testing.T, root, title string) string {
	t.Helper()
	out, err := exec.Command("mg", "--root", root, "new", title).CombinedOutput()
	if err != nil {
		t.Fatalf("mg new: %v: %s", err, out)
	}
	m := mgCreatedID.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not parse work item id out of %q", out)
	}
	id := m[1]
	if out, err := exec.Command("mg", "--root", root, "claim", id).CombinedOutput(); err != nil {
		t.Fatalf("mg claim %s: %v: %s", id, err, out)
	}
	if !mgIsClaimed(t, root, id) {
		t.Fatalf("%s should be in claimed/ after mg claim", id)
	}
	return id
}

// mgIsClaimed reports whether a claim file for id sits in the store's claimed/
// directory. It looks at the file rather than asking `mg list` because the claim
// file IS the leak this test guards: work/claimed/<id>.md.<pid> outliving the pid.
func mgIsClaimed(t *testing.T, root, id string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "work", "claimed"))
	if err != nil {
		t.Fatalf("read claimed/: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id+".md") {
			return true
		}
	}
	return false
}

// mgStatus reports which status directory holds id, or "" if no directory does.
func mgStatus(t *testing.T, root, id string) string {
	t.Helper()
	for _, dir := range []string{"available", "claimed", "done", "pending", "shelved", "archive"} {
		entries, err := os.ReadDir(filepath.Join(root, "work", dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), id+".md") {
				return dir
			}
		}
	}
	return ""
}

// mgDispatchable reports whether mg itself considers id available — the
// downstream property that matters. A claim file stranded under a dead pid is
// invisible to dispatch; an item back in available/ is not.
func mgDispatchable(t *testing.T, root, id string) bool {
	t.Helper()
	out, err := exec.Command("mg", "--root", root, "list", "--status=available").CombinedOutput()
	if err != nil {
		t.Fatalf("mg list --status=available: %v: %s", err, out)
	}
	return strings.Contains(string(out), id)
}

// recordingReleaser is a ClaimReleaser that records the ids it was asked to
// release, for the tests that assert on TARGETING rather than on store state.
type recordingReleaser struct {
	mu   sync.Mutex
	ids  []string
	fail error
}

func (r *recordingReleaser) ReleaseClaim(id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	if r.fail != nil {
		return false, r.fail
	}
	return true, nil
}

func (r *recordingReleaser) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// mg-fb13 POSITIVE CONTROL: `pogo agent stop` on a MID-FLIGHT polecat — one that
// has claimed its work item and has NOT reached `mg done` — must return the item
// to available/. Before this fix Registry.Stop had no claim-release path at all,
// so the claim file survived the process and the item was stranded in claimed/
// under a dead pid: never dispatched, and invisible to stall-watch, which only
// scans available/.
func TestStopReleasesMidFlightPolecatClaim(t *testing.T) {
	root := mgSandbox(t)
	id := mgNewClaimed(t, root, "mid-flight item")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetClaimReleaser(MGClaimReleaser{Root: root})

	// `cat` blocks forever: the polecat is mid-flight, it never runs mg done.
	a, err := reg.Spawn(SpawnRequest{
		Name:       "midflight",
		Type:       TypePolecat,
		Command:    []string{"cat"},
		WorkItemID: id,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !a.alive() {
		t.Fatal("polecat should be alive before the stop")
	}

	if err := reg.Stop("midflight", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if mgIsClaimed(t, root, id) {
		t.Errorf("claim file for %s still in claimed/ after stopping the polecat that held it", id)
	}
	if got := mgStatus(t, root, id); got != "available" {
		t.Errorf("work item %s is in %q after a mid-flight stop, want %q", id, got, "available")
	}
	if !mgDispatchable(t, root, id) {
		t.Errorf("work item %s is not dispatchable (absent from mg list --status=available)", id)
	}
}

// mg-fb13: the same leak through the stale-registration path. The reported
// symptom was an item claimed by a pid that no longer existed, so the branch
// Stop takes when the process is ALREADY dead must release the claim too —
// otherwise clearing the wedged registration still strands the work.
func TestStopReleasesClaimOfDeadPolecat(t *testing.T) {
	root := mgSandbox(t)
	id := mgNewClaimed(t, root, "dead pid item")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetClaimReleaser(MGClaimReleaser{Root: root})

	a, err := reg.Spawn(SpawnRequest{
		Name:       "deadcat",
		Type:       TypePolecat,
		Command:    []string{"true"},
		WorkItemID: id,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-a.Done()
	if a.alive() {
		t.Fatal("polecat should be dead")
	}

	if err := reg.Stop("deadcat", 2*time.Second); err != nil {
		t.Fatalf("Stop on dead polecat: %v", err)
	}
	if got := mgStatus(t, root, id); got != "available" {
		t.Errorf("work item %s is in %q after stopping a dead polecat, want %q", id, got, "available")
	}
}

// mg-fb13: the normal done/unclaim route is unchanged. A polecat that reached
// `mg done` before being stopped (the gh #35 reap order — pogod records done,
// THEN stops) must not be double-released: Stop still succeeds, and the item
// stays done rather than being reopened or bounced back to available.
func TestStopAfterDoneLeavesWorkItemDone(t *testing.T) {
	root := mgSandbox(t)
	id := mgNewClaimed(t, root, "already done item")
	if out, err := exec.Command("mg", "--root", root, "done", id).CombinedOutput(); err != nil {
		t.Fatalf("mg done: %v: %s", err, out)
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetClaimReleaser(MGClaimReleaser{Root: root})

	if _, err := reg.Spawn(SpawnRequest{
		Name:       "donecat",
		Type:       TypePolecat,
		Command:    []string{"cat"},
		WorkItemID: id,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if err := reg.Stop("donecat", 2*time.Second); err != nil {
		t.Fatalf("Stop after mg done should succeed, got: %v", err)
	}
	if got := mgStatus(t, root, id); got != "done" {
		t.Errorf("work item %s is in %q after stopping an already-done polecat, want %q", id, got, "done")
	}
	if mgDispatchable(t, root, id) {
		t.Errorf("work item %s was returned to available/ — a done item must not be re-dispatched", id)
	}
}

// mg-fb13 guardrail: stop releases ONLY the claim held by the polecat being
// stopped. No blanket sweep of leaked claims — a second polecat's item stays
// claimed, as does an unrelated claim nobody in the registry owns.
func TestStopReleasesOnlyTheStoppedPolecatsClaim(t *testing.T) {
	root := mgSandbox(t)
	mine := mgNewClaimed(t, root, "the stopped polecat's item")
	sibling := mgNewClaimed(t, root, "a live sibling polecat's item")
	orphan := mgNewClaimed(t, root, "a leaked claim nobody in the registry owns")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetClaimReleaser(MGClaimReleaser{Root: root})

	for name, id := range map[string]string{"stopme": mine, "keepme": sibling} {
		if _, err := reg.Spawn(SpawnRequest{
			Name:       name,
			Type:       TypePolecat,
			Command:    []string{"cat"},
			WorkItemID: id,
		}); err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
	}

	if err := reg.Stop("stopme", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if mgIsClaimed(t, root, mine) {
		t.Errorf("stopped polecat's item %s is still claimed", mine)
	}
	if !mgIsClaimed(t, root, sibling) {
		t.Errorf("live sibling polecat's item %s was released; stop must not sweep other claims", sibling)
	}
	if !mgIsClaimed(t, root, orphan) {
		t.Errorf("unowned claim %s was released; stop must not sweep leaked claims", orphan)
	}
}

// mg-fb13: a polecat the supervisor is about to respawn keeps its claim. Stop
// leaves restart_on_crash=true agents in the registry precisely because the
// OnExit hook will bring them back on the same work item — releasing the claim
// there would hand the item to a second worker while the first one restarts.
func TestStopKeepsClaimForRespawningPolecat(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	rel := &recordingReleaser{}
	reg.SetClaimReleaser(rel)

	if _, err := reg.Spawn(SpawnRequest{
		Name:           "respawner",
		Type:           TypePolecat,
		Command:        []string{"cat"},
		WorkItemID:     "mg-keep",
		RestartOnCrash: true,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if err := reg.Stop("respawner", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if calls := rel.calls(); len(calls) != 0 {
		t.Errorf("claim released for a polecat awaiting respawn: %v", calls)
	}
}

// mg-fb13: crew agents and polecats with no recorded work item never reach the
// releaser at all, so stopping the mayor cannot touch the work store.
func TestStopDoesNotReleaseWithoutAWorkItem(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	rel := &recordingReleaser{}
	reg.SetClaimReleaser(rel)

	if _, err := reg.Spawn(SpawnRequest{Name: "crewman", Type: TypeCrew, Command: []string{"cat"}}); err != nil {
		t.Fatalf("Spawn crew: %v", err)
	}
	if _, err := reg.Spawn(SpawnRequest{Name: "noitem", Type: TypePolecat, Command: []string{"cat"}}); err != nil {
		t.Fatalf("Spawn polecat: %v", err)
	}

	for _, name := range []string{"crewman", "noitem"} {
		if err := reg.Stop(name, 2*time.Second); err != nil {
			t.Fatalf("Stop %s: %v", name, err)
		}
	}
	if calls := rel.calls(); len(calls) != 0 {
		t.Errorf("releaser called for agents with no work item: %v", calls)
	}
}

// mg-fb13: a releaser that fails does not fail the stop. The process is already
// gone by then; refusing to complete the teardown would trade a stranded work
// item for a stranded registry entry. The failure is logged loudly instead.
func TestStopSucceedsWhenClaimReleaseFails(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetClaimReleaser(&recordingReleaser{fail: fmt.Errorf("mg exploded")})

	if _, err := reg.Spawn(SpawnRequest{
		Name:       "failrelease",
		Type:       TypePolecat,
		Command:    []string{"cat"},
		WorkItemID: "mg-boom",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := reg.Stop("failrelease", 2*time.Second); err != nil {
		t.Fatalf("Stop should survive a claim-release failure, got: %v", err)
	}
	if reg.Get("failrelease") != nil {
		t.Error("registry entry should still be cleared when the claim release fails")
	}
}

// mg-fb13 / mg-da48: the releaser's default store under a test binary is a
// throwaway temp directory, never the live ~/.macguffin. A test-safe DEFAULT,
// not an opt-in helper — an opt-in guard is only ever remembered by the tests
// that least need it, and the blast radius here is releasing a live agent's claim.
func TestMGClaimReleaserDefaultRootIsNotTheLiveStore(t *testing.T) {
	got := MGClaimReleaser{}.storeRoot()
	if got == "" {
		t.Fatal("storeRoot returned empty; mg would fall back to the live store")
	}
	home, err := os.UserHomeDir()
	if err == nil && got == filepath.Join(home, ".macguffin") {
		t.Fatalf("storeRoot resolved to the live store %q under a test binary", got)
	}
	if root := (MGClaimReleaser{Root: "/explicit"}).storeRoot(); root != "/explicit" {
		t.Errorf("explicit Root = %q, want /explicit", root)
	}
}

// mg-fb13: releasing an item that is not claimed reports "nothing released"
// rather than an error, so the done path stays quiet and a genuine mg failure
// stays loud.
func TestMGClaimReleaserUnclaimedItemIsNotAnError(t *testing.T) {
	root := mgSandbox(t)
	rel := MGClaimReleaser{Root: root}

	released, err := rel.ReleaseClaim("mg-nope")
	if err != nil {
		t.Errorf("ReleaseClaim on an unknown item: got error %v, want nil", err)
	}
	if released {
		t.Error("ReleaseClaim reported a release for an item that was never claimed")
	}
}
