package refinery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// markInFlight puts mr into a lane, as though the queue loop had just claimed
// it. Tests that used to write `r.processing = mr` use this instead.
func markInFlight(r *Refinery, mr *MergeRequest) *lane {
	r.mu.Lock()
	defer r.mu.Unlock()
	return markInFlightLocked(r, mr)
}

func markInFlightLocked(r *Refinery, mr *MergeRequest) *lane {
	r.byID[mr.ID] = mr
	return r.beginLaneLocked(laneKey(mr.RepoPath), mr)
}

// namedBareOrigin builds a bare origin at an explicitly NAMED directory.
//
// The name is not cosmetic. A lane is keyed on the repo's basename (see
// laneKey), so a test about lanes that used t.TempDir() would be asserting
// against directory names like "001" and "002" — which are distinct today by
// accident of how t.TempDir numbers its calls, not by anything the test states.
func namedBareOrigin(t *testing.T, root, name, branch string) string {
	t.Helper()
	originDir := filepath.Join(root, name)
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, originDir, "git", "init", "--bare", "-b", branch)
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", branch)
	return originDir
}

// rendezvousGate builds a gate command that announces itself and then blocks
// until every other named gate has announced itself too.
//
// This is what makes the concurrency test a real one. A gate that merely sleeps
// proves nothing: a serial refinery running two sleeps takes twice as long and
// still passes, so the assertion would be on the clock and would flake on a
// loaded machine. A gate that cannot COMPLETE unless another gate is running at
// the same moment can only pass under genuine overlap, and fails outright —
// on its own timeout — under serialisation.
func rendezvousGate(dir, self string, others ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "touch %s/%s.started; ", dir, self)
	for _, o := range others {
		fmt.Fprintf(&b, "while [ ! -f %s/%s.started ]; do sleep 0.05; done; ", dir, o)
	}
	b.WriteString("echo rendezvous-complete")
	return b.String()
}

// TestTwoReposMergeConcurrently is the headline property of mg-37ad, stated as
// the incident that unparked it: work for one repo must not wait on a gate
// belonging to a repo it shares nothing with.
//
// On 2026-08-05 twelve merge requests sat 70 minutes behind a single
// slot-holder, seven of them for a repo that was, from the refinery's point of
// view, idle. The two repos here share no working tree and no test suite, and
// the gates below can only both finish if they run at the same time.
//
// Its negative arm is TestSameRepoMergesStaySerial, which builds the SAME
// rendezvous gate for two merges that must not overlap and asserts it times
// out. The pair is what makes either one worth reading: one construction, two
// scheduling regimes, opposite outcomes. A passing test here alone would be
// consistent with a gate that always succeeds.
func TestTwoReposMergeConcurrently(t *testing.T) {
	root := t.TempDir()
	sync := t.TempDir()

	alpha := namedBareOrigin(t, root, "repo-alpha", "main")
	beta := namedBareOrigin(t, root, "repo-beta", "main")
	seedBranchWithGate(t, alpha, "polecat-alpha", gateToml(rendezvousGate(sync, "alpha", "beta"), "60s"))
	seedBranchWithGate(t, beta, "polecat-beta", gateToml(rendezvousGate(sync, "beta", "alpha"), "60s"))

	r := newProgressTestRefinery(t, 50*time.Millisecond)
	idAlpha, err := r.Submit(MergeRequest{RepoPath: alpha, Branch: "polecat-alpha", TargetRef: "main", Author: "cat-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	idBeta, err := r.Submit(MergeRequest{RepoPath: beta, Branch: "polecat-beta", TargetRef: "main", Author: "cat-beta"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go r.Start(ctx)
	defer func() { cancel(); r.Stop() }()

	waitForStatus(t, r, idAlpha, StatusMerged, 90*time.Second)
	waitForStatus(t, r, idBeta, StatusMerged, 90*time.Second)
}

// TestSameRepoMergesStaySerial is the constraint the change must NOT break, and
// it is not a matter of taste: two merges for one repo share the refinery's
// single clone of it (ensureWorktree names that clone after the repo), and each
// rebases onto a target ref the other is about to move. Running them together
// would corrupt the clone and race the fast-forward.
//
// The gate here blocks until a second gate for the SAME repo starts. Under the
// lane rule that never happens, so the first gate must hit its own timeout —
// which is the observable proof that the second one never ran alongside it.
func TestSameRepoMergesStaySerial(t *testing.T) {
	root := t.TempDir()
	sync := t.TempDir()

	repo := namedBareOrigin(t, root, "repo-solo", "main")
	seedBranchWithGate(t, repo, "polecat-first", gateToml(rendezvousGate(sync, "first", "second"), "3s"))
	seedBranchWithGate(t, repo, "polecat-second", gateToml(rendezvousGate(sync, "second", "first"), "3s"))

	r := newProgressTestRefinery(t, 50*time.Millisecond)
	idFirst, err := r.Submit(MergeRequest{RepoPath: repo, Branch: "polecat-first", TargetRef: "main", Author: "cat-first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Submit(MergeRequest{RepoPath: repo, Branch: "polecat-second", TargetRef: "main", Author: "cat-second"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go r.Start(ctx)
	defer func() { cancel(); r.Stop() }()

	// The first merge must fail on its gate timeout: nothing ever joined it.
	waitForStatus(t, r, idFirst, StatusFailed, 60*time.Second)

	// And while it was running, it must have been alone.
	if _, err := os.Stat(filepath.Join(sync, "second.started")); err == nil {
		t.Fatal("a second merge for the same repo started while the first was still in its gate — " +
			"the two share one clone and one target ref, and must never overlap")
	}
}

// TestOneLaneCapIsTheHistoricRefinery pins the rollback. Operators need a
// setting that means "go back to how it was", and it has to be exact: with a
// cap of one, two DIFFERENT repos must serialise again, just as they did before
// this change.
func TestOneLaneCapIsTheHistoricRefinery(t *testing.T) {
	r := newLaneTestRefinery(t, 1)
	r.queue = []*MergeRequest{
		{ID: "mr-a", RepoPath: "/repos/alpha", Status: StatusQueued},
		{ID: "mr-b", RepoPath: "/repos/beta", Status: StatusQueued},
	}

	ln, mr := r.claimLane(nil)
	if ln == nil || mr.ID != "mr-a" {
		t.Fatalf("first claim should take mr-a, got %v", mr)
	}
	if ln2, mr2 := r.claimLane(nil); ln2 != nil {
		t.Fatalf("with max_concurrent_merges=1 a second repo must wait, but %s was started", mr2.ID)
	}
}

// TestBusyLaneIsSkippedNotBlocking is the scheduling rule stated on its own:
// a merge whose repo is busy does not stop a merge for an idle repo behind it.
// This is the exact shape of the incident — seven pogo merge requests queued
// behind one onethird gate — reduced to the decision that produced it.
func TestBusyLaneIsSkippedNotBlocking(t *testing.T) {
	r := newLaneTestRefinery(t, 4)
	r.queue = []*MergeRequest{
		{ID: "mr-a1", RepoPath: "/repos/alpha", Status: StatusQueued},
		{ID: "mr-a2", RepoPath: "/repos/alpha", Status: StatusQueued},
		{ID: "mr-b1", RepoPath: "/repos/beta", Status: StatusQueued},
	}

	_, first := r.claimLane(nil)
	if first.ID != "mr-a1" {
		t.Fatalf("first claim should be the head of the queue, got %s", first.ID)
	}
	_, second := r.claimLane(nil)
	if second == nil {
		t.Fatal("beta's merge must start while alpha is busy — that is the whole change")
	}
	if second.ID != "mr-b1" {
		t.Fatalf("second claim should skip alpha's queued merge and take beta's, got %s", second.ID)
	}
	// alpha's second merge still waits, in submit order, for alpha's lane.
	if _, third := r.claimLane(nil); third != nil {
		t.Fatalf("a repo's second merge must wait for its own lane, but %s started", third.ID)
	}
	if got := len(r.Queue()); got != 1 || r.Queue()[0].ID != "mr-a2" {
		t.Fatalf("the queue should hold exactly alpha's second merge, got %+v", r.Queue())
	}
}

// TestCancelReachesOnlyItsOwnLane is the defect a shared cancel handle would
// have introduced, and it would have been silent: every killed merge reports
// itself as cancelled by an operator, so a cancel that took out three merges
// would look exactly like three operators cancelling.
func TestCancelReachesOnlyItsOwnLane(t *testing.T) {
	r := newLaneTestRefinery(t, 4)
	alpha := &MergeRequest{ID: "mr-alpha", RepoPath: "/repos/alpha", Status: StatusProcessing}
	beta := &MergeRequest{ID: "mr-beta", RepoPath: "/repos/beta", Status: StatusProcessing}
	lnAlpha := markInFlight(r, alpha)
	lnBeta := markInFlight(r, beta)

	if _, err := r.Cancel("mr-alpha"); err != nil {
		t.Fatal(err)
	}

	if !r.cancelWasRequested(alpha) {
		t.Error("the cancelled merge does not see its own cancel")
	}
	if r.cancelWasRequested(beta) {
		t.Error("cancelling one repo's merge also cancelled another repo's")
	}
	select {
	case <-lnAlpha.ctx.Done():
	default:
		t.Error("the cancelled lane's gate context is still live")
	}
	select {
	case <-lnBeta.ctx.Done():
		t.Error("an unrelated lane's gate was killed by a cancel aimed elsewhere")
	default:
	}
}

// TestEveryInFlightMergeIsPersisted covers the hazard named in this ticket's
// own brief: do not leave the refinery in a state where in-flight merge
// requests are lost. With one persisted slot and three merges running, a
// restart would have kept one and dropped two.
func TestEveryInFlightMergeIsPersisted(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	r := newLaneTestRefineryAt(t, 4, statePath)

	for _, repo := range []string{"alpha", "beta", "gamma"} {
		mr := &MergeRequest{ID: "mr-" + repo, RepoPath: "/repos/" + repo, Branch: "b-" + repo, Status: StatusProcessing}
		markInFlight(r, mr)
	}
	r.mu.Lock()
	r.saveStateLocked()
	r.mu.Unlock()
	// saveStateLocked snapshots and hands off; flushState is where the bytes
	// become durable, and it runs with r.mu released on purpose (mg-538e).
	// A test that reads the file is subject to the same contract as Submit.
	r.flushState()

	st, err := (&store{path: statePath}).load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ProcessingLanes) != 3 {
		t.Fatalf("state persisted %d in-flight merges, want 3", len(st.ProcessingLanes))
	}

	// Reloading must restore all three as recovered, and must NOT also queue
	// the compatibility mirror as fresh work.
	r2 := newLaneTestRefineryAt(t, 4, statePath)
	if len(r2.recovered) != 3 {
		t.Fatalf("reload recovered %d in-flight merges, want 3", len(r2.recovered))
	}
	if len(r2.queue) != 0 {
		t.Fatalf("reload queued %d duplicates of the in-flight merges, want 0: %+v", len(r2.queue), r2.queue)
	}
}

// TestOlderPogodStillFindsEveryInFlightMerge is the DOWNGRADE direction, and it
// is the one that decides whether this change can be rolled back safely.
//
// A pogod predating lanes reads only `processing` (one slot) and `queue`. This
// asserts against exactly that reader — a decode into the old shape — because
// the question is not what our loader does with our file, it is what THEIR
// loader does with it.
func TestOlderPogodStillFindsEveryInFlightMerge(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	r := newLaneTestRefineryAt(t, 4, statePath)

	for _, repo := range []string{"alpha", "beta"} {
		markInFlight(r, &MergeRequest{ID: "mr-" + repo, RepoPath: "/repos/" + repo, Branch: "b-" + repo, Status: StatusProcessing})
	}
	r.mu.Lock()
	r.queue = append(r.queue, &MergeRequest{ID: "mr-pending", RepoPath: "/repos/alpha", Status: StatusQueued})
	r.saveStateLocked()
	r.mu.Unlock()
	r.flushState()

	// The pre-lanes wire format, verbatim: no processing_lanes field.
	var old struct {
		Version    int             `json:"version"`
		Queue      []*MergeRequest `json:"queue"`
		Processing *MergeRequest   `json:"processing"`
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatalf("a pogod predating lanes cannot even parse the state file: %v", err)
	}
	if old.Version > StateVersion {
		t.Fatalf("state version %d would make an older pogod REFUSE to start its refinery", old.Version)
	}

	seen := map[string]MergeStatus{}
	for _, mr := range old.Queue {
		seen[mr.ID] = mr.Status
	}
	if old.Processing != nil {
		seen[old.Processing.ID] = old.Processing.Status
	}
	for _, want := range []string{"mr-alpha", "mr-beta", "mr-pending"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("%s is invisible to a pogod predating lanes — a rollback would lose it", want)
		}
	}
	// The mirrored rows must read as queued: an older pogod that took them for
	// in-flight would report merges as running that nothing is running.
	for _, id := range []string{"mr-alpha", "mr-beta"} {
		if seen[id] != StatusQueued {
			t.Errorf("%s is mirrored with status %q; an older pogod must see it as queued work to re-run", id, seen[id])
		}
	}
}

// TestUpgradeCarriesTheSingleProcessingSlot is the UPGRADE direction: a state
// file written by the pogod running right now, loaded by this one. Its
// in-flight merge must survive into the recovery probe, not vanish because the
// field it was written under is no longer the field we write.
func TestUpgradeCarriesTheSingleProcessingSlot(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	legacy := &MergeRequest{ID: "mr-legacy", RepoPath: "/repos/alpha", Branch: "b-legacy", Status: StatusProcessing}
	if err := (&store{path: statePath}).save(&persistedState{
		Processing: legacy,
		Queue:      []*MergeRequest{{ID: "mr-after", RepoPath: "/repos/beta", Status: StatusQueued}},
	}); err != nil {
		t.Fatal(err)
	}

	r := newLaneTestRefineryAt(t, 4, statePath)
	if len(r.recovered) != 1 || r.recovered[0].ID != "mr-legacy" {
		t.Fatalf("the pre-lanes in-flight merge was not carried across the upgrade, recovered=%+v", r.recovered)
	}
	if len(r.queue) != 1 || r.queue[0].ID != "mr-after" {
		t.Fatalf("queue = %+v, want just mr-after", r.queue)
	}
}

// TestStopWaitsForInFlightLanes guards the restart path. pogod builds a
// REPLACEMENT Refinery from the state file the outgoing one flushes, so a Stop
// that returned while lanes were still pushing would put two refineries on the
// same clone and the same branches.
func TestStopWaitsForInFlightLanes(t *testing.T) {
	r := newLaneTestRefinery(t, 4)

	release := make(chan struct{})
	finished := make(chan struct{})
	r.laneWG.Add(1)
	go func() {
		defer r.laneWG.Done()
		<-release
		close(finished)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() { close(started); r.Start(ctx) }()
	<-started
	time.Sleep(50 * time.Millisecond)

	cancel()
	stopped := make(chan struct{})
	go func() { r.Stop(); close(stopped) }()

	select {
	case <-stopped:
		t.Fatal("Stop returned while a merge was still in flight — a replacement refinery would " +
			"start on the same clone while this one is still pushing")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned after the in-flight merge finished")
	}
	<-finished
}

// TestWakeIfActionableIgnoresABusyLane is a liveness guard on the loop itself.
// Dispatch no longer blocks, so re-arming the wake for work that cannot start
// would spin the loop: dispatch starts nothing, re-arms, and runs again
// immediately, forever, burning a core while it does.
func TestWakeIfActionableIgnoresABusyLane(t *testing.T) {
	r := newLaneTestRefinery(t, 4)
	markInFlight(r, &MergeRequest{ID: "mr-a1", RepoPath: "/repos/alpha", Status: StatusProcessing})
	r.mu.Lock()
	r.queue = []*MergeRequest{{ID: "mr-a2", RepoPath: "/repos/alpha", Status: StatusQueued}}
	r.mu.Unlock()

	r.wakeIfActionable()
	select {
	case <-r.wakeCh:
		t.Fatal("a queue holding only work for a BUSY repo must not re-arm the wake — the loop would spin")
	default:
	}

	// A different repo's merge is dispatchable, so it must wake.
	r.mu.Lock()
	r.queue = append(r.queue, &MergeRequest{ID: "mr-b1", RepoPath: "/repos/beta", Status: StatusQueued})
	r.mu.Unlock()
	r.wakeIfActionable()
	select {
	case <-r.wakeCh:
	default:
		t.Fatal("expected a wake for a merge whose lane is free")
	}
}

// TestLaneKeyFollowsTheClone states why the key is the repo BASENAME. The
// refinery's private clone is named after the basename, so two repos sharing
// one must share a lane; giving them separate lanes would put two merges into a
// directory only one of them can own.
func TestLaneKeyFollowsTheClone(t *testing.T) {
	if laneKey("/Users/daniel/dev/pogo") != laneKey("/Users/daniel/other/pogo") {
		t.Error("two checkouts that share a clone directory must share a lane")
	}
	if laneKey("/repos/alpha") == laneKey("/repos/beta") {
		t.Error("distinct repos must get distinct lanes")
	}
	if laneKey("/repos/alpha/") != laneKey("/repos/alpha") {
		t.Error("a trailing separator must not create a second lane for one repo")
	}
}

// TestQueueViewShowsEveryRunningMerge is the reporting half of mg-37ad. Twelve
// merge requests waited on one gate and no view said whose gate it was; a
// pipeline view that shows only one running row would leave that exactly as it
// was, with the extra rows hidden instead of the one.
func TestQueueViewShowsEveryRunningMerge(t *testing.T) {
	r := newLaneTestRefinery(t, 4)
	now := time.Now()
	markInFlight(r, &MergeRequest{ID: "mr-old", RepoPath: "/repos/alpha", Branch: "b-old", Status: StatusProcessing, StartTime: now.Add(-time.Hour)})
	markInFlight(r, &MergeRequest{ID: "mr-new", RepoPath: "/repos/beta", Branch: "b-new", Status: StatusProcessing, StartTime: now.Add(-time.Minute)})
	r.mu.Lock()
	r.queue = []*MergeRequest{{ID: "mr-pending", RepoPath: "/repos/alpha", Status: StatusQueued}}
	r.mu.Unlock()

	full := r.QueueWithProcessing()
	if len(full) != 3 {
		t.Fatalf("pipeline view has %d rows, want 3", len(full))
	}
	if full[0].ID != "mr-old" || full[1].ID != "mr-new" {
		t.Errorf("running merges must lead, longest-running first, got %s then %s", full[0].ID, full[1].ID)
	}

	st := r.GetStatus()
	if st.ProcessingCount != 2 {
		t.Errorf("ProcessingCount = %d, want 2", st.ProcessingCount)
	}
	if len(st.InFlight) != 2 {
		t.Fatalf("status reports %d in-flight rows, want 2", len(st.InFlight))
	}
	if st.InFlight[0].Repo != "alpha" || st.InFlight[1].Repo != "beta" {
		t.Errorf("each in-flight row must name the repo holding the lane, got %q and %q",
			st.InFlight[0].Repo, st.InFlight[1].Repo)
	}
	// The legacy single-slot field keeps meaning "one of them", so a client
	// older than lanes still reports a busy refinery as busy.
	if st.Processing != "mr-old" {
		t.Errorf("Status.Processing = %q, want the longest-running merge", st.Processing)
	}
	if st.MaxConcurrentMerges != 4 {
		t.Errorf("MaxConcurrentMerges = %d, want 4", st.MaxConcurrentMerges)
	}
}

// TestQAHoldReleasesTheLane checks the one path that returns a claimed merge
// request to the queue. A hold that kept the lane would block every other merge
// for that repo behind a branch that is not even running — the serialisation
// this change removes, reintroduced one repo at a time.
func TestQAHoldReleasesTheLane(t *testing.T) {
	r := newLaneTestRefinery(t, 4)
	mr := &MergeRequest{ID: "mr-held", RepoPath: "/repos/alpha", Status: StatusProcessing, StartTime: time.Now()}
	markInFlight(r, mr)

	r.holdMergeRequest(mr, "mg-qa")

	if r.laneCount() != 0 {
		t.Fatal("a held merge request is still holding its repo's lane")
	}
	if !mr.StartTime.IsZero() {
		t.Error("a re-queued request must not keep its in-flight stamp")
	}
	if got := r.GetStatus().ProcessingCount; got != 0 {
		t.Errorf("ProcessingCount = %d after a hold, want 0", got)
	}
}

// TestConfiguredLaneCapIsHonoured proves the knob reaches the scheduler, and
// that an out-of-range value is refused at construction rather than silently
// reinterpreted.
func TestConfiguredLaneCapIsHonoured(t *testing.T) {
	r := newLaneTestRefinery(t, 2)
	for _, repo := range []string{"alpha", "beta", "gamma"} {
		r.queue = append(r.queue, &MergeRequest{ID: "mr-" + repo, RepoPath: "/repos/" + repo, Status: StatusQueued})
	}
	for i := 0; i < 2; i++ {
		if ln, _ := r.claimLane(nil); ln == nil {
			t.Fatalf("claim %d should have started a merge", i+1)
		}
	}
	if ln, mr := r.claimLane(nil); ln != nil {
		t.Fatalf("max_concurrent_merges=2 must stop a third merge, but %s started", mr.ID)
	}

	if _, err := New(Config{Enabled: true, WorktreeDir: t.TempDir(), MaxConcurrentMerges: -1}); err == nil {
		t.Error("a negative lane cap must be refused, not reinterpreted")
	}

	def, err := New(Config{Enabled: true, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if def.maxLanes() != DefaultMaxConcurrentMerges {
		t.Errorf("unset lane cap = %d, want the package default %d", def.maxLanes(), DefaultMaxConcurrentMerges)
	}
}

func newLaneTestRefinery(t *testing.T, maxConcurrent int) *Refinery {
	t.Helper()
	return newLaneTestRefineryAt(t, maxConcurrent, "")
}

func newLaneTestRefineryAt(t *testing.T, maxConcurrent int, statePath string) *Refinery {
	t.Helper()
	r, err := New(Config{
		Enabled:             true,
		PollInterval:        time.Hour,
		WorktreeDir:         t.TempDir(),
		StatePath:           statePath,
		MaxConcurrentMerges: maxConcurrent,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.setLoadSampler(nil)
	return r
}

// gateToml renders a .pogo/refinery.toml running one gate command under a
// timeout, with retries off so a serialised run fails once instead of seven
// times over.
func gateToml(command, timeout string) string {
	return fmt.Sprintf("[gates]\ncommands = [%q]\ntimeout = %q\nmax_attempts = 1\n", command, timeout)
}

// waitForStatus blocks until the merge request reaches want, failing with what
// it actually reached.
func waitForStatus(t *testing.T, r *Refinery, id string, want MergeStatus, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last MergeStatus
	for time.Now().Before(deadline) {
		if mr := r.Get(id); mr != nil {
			last = mr.Status
			if last == want {
				return
			}
			if isTerminal(last) && last != want {
				t.Fatalf("MR %s resolved as %q, want %q: %s", id, last, want, mr.Error)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("MR %s never reached %q (last status %q)", id, want, last)
}

func isTerminal(s MergeStatus) bool {
	switch s {
	case StatusMerged, StatusFailed, StatusCancelled, StatusLost:
		return true
	}
	return false
}

// TestQueueEndpointUnderALiveGate is a probe, not a feature test: it serves the
// pipeline view while a gate is beating, which is the pair of goroutines most
// likely to race now that several gates can beat at once. Run under -race.
func TestQueueEndpointUnderALiveGate(t *testing.T) {
	root := t.TempDir()
	repo := namedBareOrigin(t, root, "repo-probe", "main")
	seedBranchWithGate(t, repo, "polecat-probe", gateToml("echo working; sleep 3", "60s"))

	r := newProgressTestRefinery(t, 10*time.Millisecond)
	id, err := r.Submit(MergeRequest{RepoPath: repo, Branch: "polecat-probe", TargetRef: "main", Author: "cat-probe"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go r.Start(ctx)
	defer func() { cancel(); r.Stop() }()

	waitForBeat(t, r, id)
	// Counted, not assumed. A probe that reads the view AFTER the gate has
	// finished races nothing and passes for free, which would make it a guard
	// against nothing at all.
	sawRunningGate := 0
	for i := 0; i < 200; i++ {
		for _, mr := range r.QueueWithProcessing() {
			if mr.Status == StatusProcessing && mr.Progress != nil && mr.Progress.EndTime.IsZero() {
				sawRunningGate++
				_ = mr.Progress.Beats + mr.Progress.OutputLines
			}
		}
		_ = r.GetStatus()
		time.Sleep(time.Millisecond)
	}
	if sawRunningGate == 0 {
		t.Fatal("the view was never read while a gate was running — this probe proved nothing")
	}
}
