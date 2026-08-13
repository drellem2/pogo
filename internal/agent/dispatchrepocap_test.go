package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// The two repositories every test here contends over. Seven workers went into
// one of them on 2026-08-05 and the box reached a load average of 337; the
// second exists to hold the claim that a full repo says nothing about any other
// repo, which is the entire reason this cap is not the fleet-wide count it
// replaces.
const (
	goRepo    = "/Users/daniel/dev/pogo"
	otherRepo = "/Users/daniel/dev/other"
)

// capRegistry builds a registry with the cap armed at the shipped default and
// n live workers already in goRepo.
func capRegistry(t *testing.T, inGoRepo int) *Registry {
	t.Helper()
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())
	for i := 0; i < inGoRepo; i++ {
		name := string(rune('a'+i)) + "-cat"
		a := livePolecat(name, "mg-"+name)
		a.SourceRepo = goRepo
		reg.agents[name] = a
	}
	return reg
}

// spawnIntoRepo puts a spawn request for repo through the real handler.
func spawnIntoRepo(t *testing.T, reg *Registry, name, repo string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(SpawnPolecatAPIRequest{
		Name: name, Id: "mg-" + name, Repo: repo, Template: BuildWorkerTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body)))
	return rr
}

// TestSeventhWorkerIntoOneRepoIsRefused is the positive control: the gate CAN
// fail, on the exact input that produced the incident. Without this the cap
// would only ever be observed letting things through, which is what the
// pre-mg-3977 state also did.
func TestSeventhWorkerIntoOneRepoIsRefused(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo)

	rr := spawnIntoRepo(t, reg, "cat-seventh", goRepo)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — a full repo took another worker: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// Arguable, not adjectival: the refusal must carry the count, the cap, and
	// the names, because the reader has to be able to check it.
	for _, want := range []string{goRepo, "already has 3 worker(s)", "the cap is 3", "a-cat"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal must carry the count, missing %q; got: %s", want, body)
		}
	}
	// A "later", not a "no". A coordinator that reads this as a verdict on the
	// item abandons work that is fine.
	for _, want := range []string{"LATER", "nothing about the work item is wrong", "DIFFERENT repo"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal must read as retryable and repo-scoped, missing %q; got: %s", want, body)
		}
	}
	if a := reg.Get("cat-seventh"); a != nil {
		t.Error("a refused dispatch registered an agent anyway — the gate is below a side effect")
	}
}

// TestAnotherRepoIsUnaffected is the claim that makes this cap the right shape
// and the fleet-wide "3-5 concurrent" rule the wrong one. Five workers across
// five repos is fine; five in one Go repo is not, because they contend on one
// test suite.
func TestAnotherRepoIsUnaffected(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo+4) // seven, as measured

	occ := reg.RepoOccupancyFor(otherRepo)
	if occ.WouldRefuse {
		t.Fatalf("a full %s refused a dispatch into %s: %+v", goRepo, otherRepo, occ)
	}
	if occ.Count != 0 {
		t.Errorf("count for %s = %d, want 0 — workers were attributed to the wrong repo", otherRepo, occ.Count)
	}
	if rr := spawnIntoRepo(t, reg, "cat-elsewhere", otherRepo); rr.Code == http.StatusServiceUnavailable {
		t.Errorf("spawn into an empty repo was refused: %s", rr.Body.String())
	}
}

// TestUnderTheCapIsNotRefused. A throttle that fires early is a throttle
// nobody keeps armed.
func TestUnderTheCapIsNotRefused(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo-1)
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.WouldRefuse {
		t.Errorf("refused at %d of %d: %+v", occ.Count, occ.Cap, occ)
	}
}

// TestPathSpellingDoesNotUncapTheRepo. Two dispatchers writing the same repo
// with a trailing slash and without it must contend, or the cap is bypassed by
// a typo nobody would notice.
func TestPathSpellingDoesNotUncapTheRepo(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo)
	for _, spelling := range []string{goRepo + "/", goRepo + "/.", goRepo + "//"} {
		if occ := reg.RepoOccupancyFor(spelling); !occ.WouldRefuse {
			t.Errorf("%q read as a different repo from %q: count=%d", spelling, goRepo, occ.Count)
		}
	}
}

// TestNoRepoIsNotCapped: a --no-worktree dispatch runs no repository's test
// suite, so there is nothing for it to contend on. It must also not be pooled
// with other repo-less dispatches under one bucket, which would make unrelated
// in-place edits block each other.
func TestNoRepoIsNotCapped(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo)
	reg.agents["x-noworktree"] = &Agent{
		Name: "x-noworktree", Type: TypePolecat, PID: os.Getpid(),
		Status: StatusRunning, StartTime: time.Now(), done: make(chan struct{}),
	}
	occ := reg.RepoOccupancyFor("")
	if occ.WouldRefuse {
		t.Error("a repo-less dispatch was capped")
	}
	if len(occ.Unattributed) != 0 {
		t.Errorf("the repo-less path reported occupancy it cannot have: %+v", occ)
	}
}

// TestUnattributedWorkersAreReportedNotCounted. A live worker whose repo is
// unknown is a real state — a witness record written before the field existed,
// or a --no-worktree polecat. Counting it against a repo would refuse a correct
// dispatch on a guess; hiding it would let an undercount look exact.
func TestUnattributedWorkersAreReportedNotCounted(t *testing.T) {
	reg := capRegistry(t, 1)
	reg.agents["x-mystery"] = &Agent{
		Name: "x-mystery", Type: TypePolecat, PID: os.Getpid(),
		Status: StatusRunning, StartTime: time.Now(), done: make(chan struct{}),
	}
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Count != 1 {
		t.Errorf("count = %d, want 1 — an unattributable worker was counted against a repo it may not be in", occ.Count)
	}
	if len(occ.Unattributed) != 1 || occ.Unattributed[0] != "x-mystery" {
		t.Errorf("unattributed = %v, want [x-mystery] — an undercount that does not say so reads as exact", occ.Unattributed)
	}
}

// TestSurvivorsOfAPogodRestartStillCount is mg-0130's lesson applied here. The
// in-memory registry is EMPTY after a restart, permanently, and a redeploy is
// exactly when survivors exist — so a cap that read the registry alone would
// uncap itself on every restart.
func TestSurvivorsOfAPogodRestartStillCount(t *testing.T) {
	sandboxWitness(t)
	// A registry with no memory of anything, as after a restart.
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())

	pid := liveProcess(t)
	for _, name := range []string{"s-one", "s-two", "s-three"} {
		if err := RecordPolecatWitness(name, pid, "mg-"+name, goRepo); err != nil {
			t.Fatalf("RecordPolecatWitness: %v", err)
		}
	}

	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Count != 3 {
		t.Fatalf("count = %d, want 3 — survivors of a restart are invisible to the cap: %+v", occ.Count, occ)
	}
	if !occ.WouldRefuse {
		t.Error("an amnesiac registry uncapped a repo that already holds its allowance")
	}
}

// TestRegistryAndWitnessAgreeingCountsOnce. The two sources overlap in the
// normal case (pogod has run continuously AND wrote a witness), and double
// counting would refuse dispatch at half the configured cap.
func TestRegistryAndWitnessAgreeingCountsOnce(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())
	pid := liveProcess(t)
	a := livePolecat("dup-cat", "mg-dup")
	a.SourceRepo = goRepo
	a.PID = pid
	reg.agents["dup-cat"] = a
	if err := RecordPolecatWitness("dup-cat", pid, "mg-dup", goRepo); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	if occ := reg.RepoOccupancyFor(goRepo); occ.Count != 1 {
		t.Errorf("count = %d, want 1 — the same polecat was counted from both sources: %v", occ.Count, occ.Polecats)
	}
}

// TestDeadWitnessedPolecatsDoNotCount. A record whose process is provably gone
// must not hold a slot forever; that would be a cap that only ever tightens.
func TestDeadWitnessedPolecatsDoNotCount(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())
	// Witnessed while alive — the only way a record can be written, since a pid
	// with no readable start time has no identity to record — then killed and
	// REAPED, so it stops answering signal 0.
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	if err := RecordPolecatWitness("dead-cat", cmd.Process.Pid, "mg-dead", goRepo); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	if occ := reg.RepoOccupancyFor(goRepo); occ.Count != 0 {
		t.Errorf("count = %d, want 0 — a dead polecat is holding a slot: %v", occ.Count, occ.Polecats)
	}
}

// TestRefineryReservesASlot is the half of this ticket that is about the
// refinery being starved by the workers whose branches it merges. Three merges
// sat queued 24+ minutes on 2026-08-05 while the CPU went to builds of workers
// whose work was already submitted.
func TestRefineryReservesASlot(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo-1) // two of three
	if occ := reg.RepoOccupancyFor(goRepo); occ.WouldRefuse {
		t.Fatalf("refused with an idle refinery at %d of %d", occ.Count, occ.Cap)
	}

	reg.SetRefineryActivity(RefineryActivityFunc(func(repo string) (bool, bool) {
		return config.SameRepo(repo, goRepo), true
	}))

	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Cap != config.DefaultMaxPolecatsPerRepo-config.DefaultRefineryReserve {
		t.Errorf("cap = %d, want %d — the refinery's slot was not held back",
			occ.Cap, config.DefaultMaxPolecatsPerRepo-config.DefaultRefineryReserve)
	}
	if !occ.WouldRefuse {
		t.Fatal("the third worker was dispatched into the slot reserved for the merge it is waiting on")
	}
	if !strings.Contains(refusalFor(t, reg, goRepo), "RESERVED for the refinery") {
		t.Error("the refusal does not say the refinery is why — an unexplained refusal gets disarmed")
	}
	// And a repo the refinery has no work in keeps its whole budget.
	if occ := reg.RepoOccupancyFor(otherRepo); occ.Cap != config.DefaultMaxPolecatsPerRepo {
		t.Errorf("cap for %s = %d, want %d — the reserve leaked across repos",
			otherRepo, occ.Cap, config.DefaultMaxPolecatsPerRepo)
	}
}

// TestQueuedMergesReserveToo, not only in-flight ones. Reserving only while a
// gate is RUNNING would be nearly useless: the starvation is built by workers
// dispatched BEFORE the gate starts, and by then they cannot be taken back.
// This is the difference between preventing the incident and observing it.
func TestQueuedMergesReserveToo(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo-1)
	// Nothing processing; eight merge requests queued, which is the state the
	// refinery was actually in.
	reg.SetRefineryActivity(RefineryActivityFunc(func(repo string) (bool, bool) {
		return config.SameRepo(repo, goRepo), true
	}))
	if occ := reg.RepoOccupancyFor(goRepo); !occ.WouldRefuse {
		t.Error("a queue of merges reserved nothing; the reserve only helps if it precedes the gate")
	}
}

// TestUnaskableRefineryReservesNothing. "The refinery could not be asked" and
// "the refinery is idle" are different facts, and the gate must not tighten on
// the first — a reserve held for a refinery that may not exist is a slot lost
// for no reason.
func TestUnaskableRefineryReservesNothing(t *testing.T) {
	reg := capRegistry(t, config.DefaultMaxPolecatsPerRepo-1)
	reg.SetRefineryActivity(RefineryActivityFunc(func(string) (bool, bool) { return false, false }))
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Cap != config.DefaultMaxPolecatsPerRepo {
		t.Errorf("cap = %d, want %d — a refinery that could not be asked held a slot", occ.Cap, config.DefaultMaxPolecatsPerRepo)
	}
	if occ.RefineryKnown {
		t.Error("RefineryKnown must stay false so a reader can tell 'not asked' from 'idle'")
	}
}

// TestReserveNeverTakesTheLastSlot. A reserve >= the cap would refuse every
// dispatch into a repo whose refinery queue is non-empty — and on the fleet's
// main repo that queue is almost never empty, so the setting would be a wedge
// rather than a conservative choice.
func TestReserveNeverTakesTheLastSlot(t *testing.T) {
	cfg := config.DispatchCapConfig{MaxPolecatsPerRepo: 2, RefineryReserve: 5}
	if got := cfg.EffectiveCap(true); got != 1 {
		t.Errorf("EffectiveCap = %d, want 1 — an over-large reserve wedged the repo shut", got)
	}
	reg := capRegistry(t, 0)
	reg.SetDispatchCap(cfg)
	reg.SetRefineryActivity(RefineryActivityFunc(func(string) (bool, bool) { return true, true }))
	if occ := reg.RepoOccupancyFor(goRepo); occ.WouldRefuse {
		t.Error("an empty repo was refused because the reserve exceeded the cap")
	}
}

// TestDisarmedCapRefusesNothing. Zero is a value, not an absence: an operator
// who writes max_polecats_per_repo = 0 has turned the gate off and must get a
// daemon that refuses nothing.
func TestDisarmedCapRefusesNothing(t *testing.T) {
	reg := capRegistry(t, 20)
	reg.SetDispatchCap(config.DispatchCapConfig{MaxPolecatsPerRepo: 0, RefineryReserve: 1})
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.WouldRefuse {
		t.Errorf("a disarmed cap refused a dispatch: %+v", occ)
	}
	if occ.Count != 20 {
		t.Errorf("count = %d, want 20 — a disarmed cap must still COUNT, so `pogo host load --repo` "+
			"can report what it is not enforcing", occ.Count)
	}
}

// TestZeroValueRegistryIsDisarmed. Every existing caller constructs a Registry
// without calling SetDispatchCap, and this gate must not start refusing their
// spawns because a new field appeared. pogod arms it explicitly; nothing else
// does.
func TestZeroValueRegistryIsDisarmed(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	for i := 0; i < 9; i++ {
		name := string(rune('a'+i)) + "-unarmed"
		a := livePolecat(name, "mg-"+name)
		a.SourceRepo = goRepo
		reg.agents[name] = a
	}
	if occ := reg.RepoOccupancyFor(goRepo); occ.WouldRefuse {
		t.Error("a registry nobody armed refused a dispatch")
	}
}

// TestCapFailsOpenOnAnUnreadableWitness. Refusing on missing information halts
// dispatch into every repo for a reason the caller cannot check or clear. A
// throttle that jams shut on a bad read is worse than no throttle — the same
// argument loadGateRefusal makes next door.
func TestCapFailsOpenOnAnUnreadableWitness(t *testing.T) {
	sandboxWitness(t)
	// A witness file written by a NEWER pogod: loadWitnessAllTypes refuses it rather
	// than overwriting, so this is a real read failure rather than a simulated
	// one.
	if err := os.WriteFile(WitnessPath(), []byte(`{"version":9999,"polecats":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())

	occ := reg.RepoOccupancyFor(goRepo)
	if occ.WitnessErr == "" {
		t.Fatal("the witness read did not fail; this test is not exercising the fail-open path")
	}
	if occ.WouldRefuse {
		t.Error("an unreadable witness refused a dispatch instead of failing open")
	}
	// Failing open silently is the other half of the defect: the count is known
	// to be possibly low and has to say so.
	reg.agents["f-cat"] = func() *Agent { a := livePolecat("f-cat", "mg-f"); a.SourceRepo = goRepo; return a }()
	reg.agents["g-cat"] = func() *Agent { a := livePolecat("g-cat", "mg-g"); a.SourceRepo = goRepo; return a }()
	reg.agents["h-cat"] = func() *Agent { a := livePolecat("h-cat", "mg-h"); a.SourceRepo = goRepo; return a }()
	if !strings.Contains(refusalFor(t, reg, goRepo), "may be missing survivors") {
		t.Error("a refusal computed from a partial count did not say the count may be partial")
	}
}

// TestHostLoadEndpointServesTheSameCount. An advisory number that could drift
// from the enforced one lets a coordinator plan a batch against a repo pogod
// sees differently — the same defect HostLoadResponse.WouldRefuseDispatch was
// built to avoid one question over.
func TestHostLoadEndpointServesTheSameCount(t *testing.T) {
	for _, tc := range []struct{ live, want int }{{0, 0}, {2, 2}, {3, 3}} {
		reg := capRegistry(t, tc.live)
		rr := httptest.NewRecorder()
		reg.handleHostLoad(rr, httptest.NewRequest("GET", "/agents/hostload?repo="+goRepo, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		var resp HostLoadResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if resp.RepoOccupancy == nil {
			t.Fatal("the endpoint was given a repo and answered without one")
		}
		if resp.RepoOccupancy.Count != tc.want {
			t.Errorf("count = %d, want %d", resp.RepoOccupancy.Count, tc.want)
		}
		enforced := reg.RepoOccupancyFor(goRepo).WouldRefuse
		if resp.RepoOccupancy.WouldRefuse != enforced {
			t.Errorf("endpoint says would_refuse=%v, the gate says %v — advisory and enforced disagree",
				resp.RepoOccupancy.WouldRefuse, enforced)
		}
	}
}

// TestHostLoadOmitsOccupancyWhenNoRepoAsked keeps the endpoint's existing
// contract: callers that ask nothing about a repo get nothing back about one.
func TestHostLoadOmitsOccupancyWhenNoRepoAsked(t *testing.T) {
	reg := capRegistry(t, 3)
	rr := httptest.NewRecorder()
	reg.handleHostLoad(rr, httptest.NewRequest("GET", "/agents/hostload", nil))
	var resp HostLoadResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.RepoOccupancy != nil {
		t.Errorf("occupancy served for a request that named no repo: %+v", resp.RepoOccupancy)
	}
}

// refusalFor renders the refusal the spawn path would emit for repo.
func refusalFor(t *testing.T, reg *Registry, repo string) string {
	t.Helper()
	msg := reg.repoCapRefusal(repo)
	if msg == "" {
		t.Fatalf("expected a refusal for %s, got none", repo)
	}
	return msg
}
