package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for the polecat witness (mg-13a3).
//
// These use REAL processes and the REAL `ps` probe wherever the thing under
// test is "can we tell our process from some process". Faking the probe would
// make the tests measure the fake — and an instrument that cannot distinguish
// our process from some process is the exact defect this store exists to
// prevent, so it is the one thing that must not be mocked here. procStartFn is
// overridden only in the two cases whose subject IS an unreadable probe.

// sandboxWitness points the witness store at a temp file for one test.
func sandboxWitness(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prev := witnessPathOverride
	witnessPathOverride = filepath.Join(dir, "polecat-witness.json")
	t.Cleanup(func() { witnessPathOverride = prev })
}

// liveProcess starts a real, long-lived process and returns its pid. It is
// killed and reaped when the test ends. `sleep` is adequate here: nothing in
// this file depends on the process's signal disposition — only on it having a
// pid and a kernel start time — so mg-61a0's SIGHUP-probe caveats do not
// apply.
func liveProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// waitForNextSecond blocks until the wall clock crosses a whole-second
// boundary. TestWitnessDeadWhenPidRecycled uses it to make forward progress
// between spawn retries: after it returns, a freshly spawned process reads a
// LATER `ps lstart` second than one started before the call, so the retry loop
// converges. It is not a standalone guarantee that two spawns differ — that is
// the loop's job (mg-9c14) — only that each retry advances past the prior
// second.
func waitForNextSecond(t *testing.T) {
	t.Helper()
	now := time.Now()
	time.Sleep(now.Truncate(time.Second).Add(time.Second + 50*time.Millisecond).Sub(now))
}

// TestWitnessAliveWhenOurProcessRuns is THE acceptance test for mg-13a3.
//
// A polecat is running and the registry has no entry for it — the state of
// every polecat that survives a pogod restart, because the registry is
// in-memory with no adopt path. Before the witness existed this agent was
// classified from two absences and reaped. Now there is something to look at.
func TestWitnessAliveWhenOurProcessRuns(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-alive", pid, "mg-13a3", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	if got := AgentWitness("cat-alive"); got != WitnessAlive {
		t.Errorf("AgentWitness(cat-alive) = %v, want %v — a running polecat whose pid AND start time "+
			"match the record is our process; the registry having forgotten it is not evidence of death (mg-13a3)", got, WitnessAlive)
	}
	// The event-identity form must resolve identically: schedules address
	// agents as cat-<name>, and a witness that only answers to one spelling
	// would be silently absent for real schedules.
	if got := AgentWitness("cat-cat-alive"); got != WitnessAlive {
		t.Errorf("AgentWitness(cat-cat-alive) = %v, want %v — event-identity form must resolve identically", got, WitnessAlive)
	}
}

// TestWitnessDeadWhenPidRecycled is the constraint that makes the witness a
// witness, and the case that turns this fix from a bug-swap into a repair.
//
// Naive "registry-absent + pid alive = UNKNOWN" reintroduces mg-8677 through
// the very witness added to prevent mg-61a0: pids are reused. A dead polecat
// whose pid is recycled by an unrelated process reads ALIVE, so its schedule
// is kept forever and fires at a corpse, accumulating unbounded
// scheduler_fire_failed noise.
//
// The witness must therefore answer "is OUR process alive", never "is SOME
// process alive". Both timestamps here come from real processes via the real
// `ps` probe; we record process A's pid against process B's start time, which
// is precisely what the probe sees after a recycle — a live pid whose start
// time is not the one we wrote down. (True pid recycling cannot be forced
// deterministically; the process's *history* is unobservable to the probe, so
// crossing two real identities models exactly what it can observe.)
func TestWitnessDeadWhenPidRecycled(t *testing.T) {
	sandboxWitness(t)
	ourPid := liveProcess(t)

	ourStart, ok := procStart(ourPid)
	if !ok {
		t.Fatalf("precondition: cannot read start time of pid %d", ourPid)
	}

	// The recycled case needs a stand-in whose kernel start time the probe
	// reads as DIFFERENT from ourPid's — that difference is exactly what the
	// probe sees after a pid is reused. `ps lstart` resolves only to whole
	// seconds, so two processes born in the same second are indistinguishable
	// to it.
	//
	// A single wait across one second boundary does NOT durably separate two
	// spawns: under CI load the stand-in could still land in ourPid's second,
	// and then this test's own precondition Fatalf'd and flapped main CI
	// (mg-9c14) — a control-that-cannot-fail defect. So don't gamble on one
	// boundary crossing. Spawn the stand-in, RE-READ both real start times, and
	// retry past the next boundary until the probe itself confirms they differ.
	// The loop's exit condition IS the precondition, so setup cannot proceed on
	// two indistinguishable identities, and it makes forward progress every
	// iteration (each waitForNextSecond permanently advances past ourPid's
	// second), so it terminates. Both times stay genuine `ps` readings of real
	// processes — the probe is never mocked here, because an instrument that
	// cannot tell our process from some process is the exact defect this store
	// prevents.
	var otherStart time.Time
	for {
		standIn := liveProcess(t)
		s, ok := procStart(standIn)
		if !ok {
			t.Fatalf("precondition: cannot read start time of pid %d", standIn)
		}
		if !s.Equal(ourStart) {
			otherStart = s
			break
		}
		waitForNextSecond(t)
	}

	// Last-resort assert. The loop above establishes this invariant
	// constructively, so on a healthy machine it can never fire; it stays as a
	// tripwire in case the setup is ever weakened back toward a timing gamble.
	if otherStart.Equal(ourStart) {
		t.Fatalf("precondition: the stand-in's start time %v equals ourPid's despite looping until the probe "+
			"reported them distinct — the two identities must differ or the recycled-pid case below is untestable", ourStart)
	}

	// The control: with the TRUE identity recorded, this pid reads alive. If
	// this ever stops holding, the assertion below would pass for the wrong
	// reason — WitnessDead would be trivially correct and pid-reuse untested.
	if err := RecordPolecatWitness("cat-recycled", ourPid, "", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	if got := AgentWitness("cat-recycled"); got != WitnessAlive {
		t.Fatalf("control: AgentWitness(cat-recycled) = %v, want %v — with the true start time recorded "+
			"this live pid must read ALIVE, or the recycled-pid assertion below proves nothing", got, WitnessAlive)
	}

	// Now the recycled case: same live pid, a start time that is not ours.
	writeWitnessForTest(t, witnessRecord{Name: "cat-recycled", PID: ourPid, StartTime: otherStart})

	if got := AgentWitness("cat-recycled"); got != WitnessDead {
		t.Errorf("AgentWitness(cat-recycled) = %v, want %v — the pid is alive but holds a process that "+
			"started at a different time, so it is NOT our polecat. Answering anything but GONE here keeps a "+
			"dead polecat's mail-check firing at a corpse forever — mg-8677, re-entered through the fix for mg-61a0", got, WitnessDead)
	}
}

// TestWitnessDeadWhenProcessGone: the pid holds nothing at all. Positive
// evidence of death — safe to reap, and the ordinary path for a polecat whose
// pogod died before it could drop the witness.
func TestWitnessDeadWhenProcessGone(t *testing.T) {
	sandboxWitness(t)

	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	if err := RecordPolecatWitness("cat-dead", pid, "", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	// Control: alive while it is alive.
	if got := AgentWitness("cat-dead"); got != WitnessAlive {
		t.Fatalf("control: AgentWitness(cat-dead) = %v, want %v before the kill", got, WitnessAlive)
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait() // reap, so the pid stops answering signal 0

	if got := AgentWitness("cat-dead"); got != WitnessDead {
		t.Errorf("AgentWitness(cat-dead) = %v, want %v — the pid answers nothing; that is positive "+
			"evidence of death and the reap is correct (mg-13a3)", got, WitnessDead)
	}
}

// TestWitnessNoRecordForUnwitnessedAgent: no witness is NOT a verdict. Crew are
// never witnessed (auto_start is their second witness) and must fall through to
// the desired state rather than being answered for here.
func TestWitnessNoRecordForUnwitnessedAgent(t *testing.T) {
	sandboxWitness(t)

	if got := AgentWitness("crew-pm-pogo"); got != WitnessNoRecord {
		t.Errorf("AgentWitness(crew-pm-pogo) = %v, want %v — an agent with no witness must yield no "+
			"verdict, so the desired state still gets to speak for crew", got, WitnessNoRecord)
	}

	// A witness for a DIFFERENT polecat must not answer for this one.
	pid := liveProcess(t)
	if err := RecordPolecatWitness("cat-other", pid, "", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	if got := AgentWitness("cat-nobody"); got != WitnessNoRecord {
		t.Errorf("AgentWitness(cat-nobody) = %v, want %v", got, WitnessNoRecord)
	}
}

// TestWitnessUnreadableWhenIdentityUnreadable: the pid answers signal 0 but we
// cannot read its start time, so we know something is alive and do not know
// that it is ours. That difference is the entire subject of this file, and the
// honest answer is "cannot tell" — never a reap.
//
// This is one of the two places procStartFn is faked, because an unreadable
// probe IS the subject.
func TestWitnessUnreadableWhenIdentityUnreadable(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-blind", pid, "", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	prev := procStartFn
	procStartFn = func(int) (time.Time, bool) { return time.Time{}, false }
	t.Cleanup(func() { procStartFn = prev })

	if got := AgentWitness("cat-blind"); got != WitnessUnreadable {
		t.Errorf("AgentWitness(cat-blind) = %v, want %v — a live pid whose identity we cannot read is "+
			"not evidence of death; calling it dead would reap on an inability to measure (mg-de08)", got, WitnessUnreadable)
	}
}

// TestRecordRefusesPidWithoutIdentity: if the start time cannot be read at
// spawn, we write NOTHING. A pid-only record is a false witness — it could not
// tell our polecat from a recycled pid, and would answer UNKNOWN at a corpse
// forever. No record is strictly better than an untrustworthy one: it leaves
// the classifier exactly as it was before this store existed.
func TestRecordRefusesPidWithoutIdentity(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	prev := procStartFn
	procStartFn = func(int) (time.Time, bool) { return time.Time{}, false }
	t.Cleanup(func() { procStartFn = prev })

	if err := RecordPolecatWitness("cat-noid", pid, "", ""); err == nil {
		t.Error("RecordPolecatWitness with an unreadable start time returned nil; want an error — " +
			"recording a pid without an identity creates the false witness this store exists to avoid")
	}
	if _, err := os.Stat(WitnessPath()); !os.IsNotExist(err) {
		t.Errorf("witness file exists after a refused record (stat err = %v); want no file written", err)
	}
}

// TestWitnessDropRemovesRecord: pogod watched the process die, so the record is
// known false, not merely stale. Leaving it would strand a record whose pid is
// free to be recycled.
func TestWitnessDropRemovesRecord(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-drop", pid, "", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	if got := AgentWitness("cat-drop"); got != WitnessAlive {
		t.Fatalf("control: AgentWitness(cat-drop) = %v, want %v", got, WitnessAlive)
	}

	noteWitnessExit(&Agent{Name: "cat-drop", Type: TypePolecat, PID: pid})

	if got := AgentWitness("cat-drop"); got != WitnessNoRecord {
		t.Errorf("AgentWitness(cat-drop) = %v, want %v after exit — a witness for a process we watched "+
			"die must not survive to argue for it", got, WitnessNoRecord)
	}
}

// TestWitnessRecordedForCrew REPLACES TestWitnessNotRecordedForCrew, which
// asserted the opposite and was the defect (mg-f9e8).
//
// The old test's rationale — "crew already have an independent second witness
// (auto_start)" — is true only while auto_start is true. The prompt-side witness
// IS auto_start. Turn it off and the agent has no process witness (not a
// polecat) and no desired-state witness (not expected), and the mail-check
// classifier reaps it on the strength of those two absences while it is running.
//
// This asserts on TYPE rather than on the classifier because the classifier's
// answer for a live crew agent depends on both this record AND a prompt on disk;
// the pogod-side test pins that. Here the claim is narrower and is the one the
// old test denied: pogod writes a record when it starts a crew agent.
func TestWitnessRecordedForCrew(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	noteWitnessStart(&Agent{Name: "pm-pogo", Type: TypeCrew, PID: pid})

	if got := AgentWitness("pm-pogo"); got != WitnessAlive {
		t.Errorf("AgentWitness(pm-pogo) = %v, want %v — a crew agent pogod started and can still see must "+
			"carry evidence of life, or an auto_start=false one is classified from two absences (mg-f9e8)",
			got, WitnessAlive)
	}

	// ...and the event-identity spelling resolves identically. This is not a
	// formality: crew mail-checks are registered under BOTH spellings on this
	// fleet (mailcheck_gc_restart_test.go has one under "crew-pm-pogo"), and a
	// probe that only stripped "cat-" would have fixed the agents whose schedule
	// used one spelling and left the identical agent broken under the other.
	if got := AgentWitness("crew-pm-pogo"); got != WitnessAlive {
		t.Errorf("AgentWitness(crew-pm-pogo) = %v, want %v — a crew schedule addressed by event identity "+
			"must resolve to the same evidence as one addressed by bare name (mg-f9e8)", got, WitnessAlive)
	}
}

// TestCrewWitnessDroppedOnExit is the negative half of the one above, and the
// halves are not interchangeable: a guard observed only KEEPING things alive is
// not known to work. pogod watched this crew process die, so its record must go
// — leaving it would let a recycled pid argue for an agent we know is dead,
// which is mg-8677 re-entered through mg-f9e8's fix.
func TestCrewWitnessDroppedOnExit(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	noteWitnessStart(&Agent{Name: "pm-transient", Type: TypeCrew, PID: pid})
	if got := AgentWitness("pm-transient"); got != WitnessAlive {
		t.Fatalf("control: AgentWitness(pm-transient) = %v, want %v", got, WitnessAlive)
	}

	noteWitnessExit(&Agent{Name: "pm-transient", Type: TypeCrew, PID: pid})

	if got := AgentWitness("pm-transient"); got != WitnessNoRecord {
		t.Errorf("AgentWitness(pm-transient) = %v, want %v after exit — the drop must cover every type the "+
			"write covers, or a crew record outlives its process forever", got, WitnessNoRecord)
	}
}

// TestCrewWitnessIsInvisibleToThePolecatReaders is the blast-radius guard for
// mg-f9e8, and it is the assertion that made the "just widen the writer"
// suggestion the wrong shape.
//
// The store is read by five things. ONE of them (AgentWitness) asks about a
// single named agent and must see crew — that is the fix. The other four
// enumerate "the polecats" and mean it literally:
//
//   - WitnessedAlivePolecats feeds the redeploy drain, which waits for the count
//     to reach zero. Crew never exit, so the drain would never drain.
//   - ...and the orphan alert, which mails the coordinator `kill <pid>` for
//     every row. Crew survive restarts; the mail would be a standing kill order
//     for the fleet, addressed to a member of it.
//   - WitnessedPolecatVerdicts feeds gitgc's live set, matched against polecat
//     branch names.
//   - WitnessedPolecatRepos feeds the per-repo dispatch cap, which would refuse
//     correct dispatches.
//   - WitnessedPolecatWorkItems feeds stall-watch's in-flight set.
//   - UnadoptablePolecats checks polecat-<name> for unmerged work.
//
// A live crew agent must appear in NONE of them.
func TestCrewWitnessIsInvisibleToThePolecatReaders(t *testing.T) {
	sandboxWitness(t)
	crewPID := liveProcess(t)
	catPID := liveProcess(t)

	noteWitnessStart(&Agent{Name: "pm-doctor", Type: TypeCrew, PID: crewPID, WorkItemID: "mg-crew", SourceRepo: "/repo"})
	// A polecat in the same store is the positive control: without it, every
	// assertion below would also pass against a reader that returns nothing.
	noteWitnessStart(&Agent{Name: "cat-live", Type: TypePolecat, PID: catPID, WorkItemID: "mg-cat", SourceRepo: "/repo"})

	alive, err := WitnessedAlivePolecats()
	if err != nil {
		t.Fatalf("WitnessedAlivePolecats: %v", err)
	}
	names := map[string]bool{}
	for _, r := range alive {
		names[r.Name] = true
	}
	if !names["cat-live"] {
		t.Fatalf("control: WitnessedAlivePolecats did not report the live polecat (%v); every assertion "+
			"below would pass vacuously", names)
	}
	if names["pm-doctor"] {
		t.Error("WitnessedAlivePolecats reported a CREW agent — the drain would never reach zero and the " +
			"orphan alert would mail the coordinator a kill order for the fleet (mg-f9e8)")
	}

	verdicts, err := WitnessedPolecatVerdicts()
	if err != nil {
		t.Fatalf("WitnessedPolecatVerdicts: %v", err)
	}
	if _, ok := verdicts["cat-live"]; !ok {
		t.Fatalf("control: WitnessedPolecatVerdicts lost the polecat: %v", verdicts)
	}
	if _, ok := verdicts["pm-doctor"]; ok {
		t.Error("WitnessedPolecatVerdicts reported a CREW agent; gitgc's live set is matched against " +
			"polecat branch names and would carry a key that protects nothing")
	}

	repos, unattributed, err := WitnessedPolecatRepos()
	if err != nil {
		t.Fatalf("WitnessedPolecatRepos: %v", err)
	}
	if repos["cat-live"] != "/repo" {
		t.Fatalf("control: WitnessedPolecatRepos lost the polecat: %v / %v", repos, unattributed)
	}
	if _, ok := repos["pm-doctor"]; ok {
		t.Error("WitnessedPolecatRepos counted a CREW agent against a repo; the dispatch cap would refuse " +
			"correct dispatches, and refuse more of them the healthier the fleet was")
	}

	items, err := WitnessedPolecatWorkItems()
	if err != nil {
		t.Fatalf("WitnessedPolecatWorkItems: %v", err)
	}
	if items["cat-live"] != "mg-cat" {
		t.Fatalf("control: WitnessedPolecatWorkItems lost the polecat: %v", items)
	}
	if _, ok := items["pm-doctor"]; ok {
		t.Error("WitnessedPolecatWorkItems reported a CREW agent; stall-watch would call an item worked " +
			"that no polecat is working")
	}

	// The gitgc live set is the reader whose mistake is unrecoverable (a worktree
	// removed under a running polecat), so assert it through its real entry point
	// rather than only through the function it calls.
	live, err := LivePolecatSet(nil)
	if err != nil {
		t.Fatalf("LivePolecatSet: %v", err)
	}
	if !live["cat-live"] {
		t.Fatalf("control: LivePolecatSet lost the polecat: %v", live)
	}
	if live["pm-doctor"] {
		t.Error("LivePolecatSet included a CREW agent (mg-f9e8)")
	}

	// UnadoptablePolecats reads the store directly rather than through any of the
	// above, so it needs its own assertion. An empty registry is the restart
	// state, which is exactly when this sweep runs.
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	cands, err := reg.UnadoptablePolecats()
	if err != nil {
		t.Fatalf("UnadoptablePolecats: %v", err)
	}
	sawCat, sawCrew := false, false
	for _, c := range cands {
		switch c.Name {
		case "cat-live":
			sawCat = true
		case "pm-doctor":
			sawCrew = true
		}
	}
	if !sawCat {
		t.Fatalf("control: UnadoptablePolecats lost the polecat: %+v", cands)
	}
	if sawCrew {
		t.Error("UnadoptablePolecats reported a CREW agent; the startup sweep would look for a " +
			"polecat-pm-doctor branch that cannot exist and report on a population it cannot judge")
	}
}

// TestAThirdWitnessTypeIsInvisibleToThePolecatReaders is the same assertion as
// TestCrewWitnessIsInvisibleToThePolecatReaders made against a type that does not
// exist yet (mg-ef7d).
//
// WHY IT IS NOT A DUPLICATE. The crew test proves the readers exclude CREW, and a
// reader that filtered by spelling `r.Type != TypeCrew` would pass it while
// admitting every future type — which is the shape of the defect, not a
// hypothetical: the store's population was widened once already, by one line, and
// the readers inherited the change silently. This pins the polarity that matters:
// the readers admit POLECATS, they do not merely reject crew. A third type is
// excluded on the day it is added, before anyone writes a reader for it.
//
// It also re-pins the migration property in the same store, so the two facts are
// asserted against each other rather than in separate files: a typeless record
// (written by a pogod predating mg-f9e8) is a POLECAT and stays in the readers,
// while an unrecognised type is not. "Unknown" and "absent" are different facts —
// this package's whole subject.
func TestAThirdWitnessTypeIsInvisibleToThePolecatReaders(t *testing.T) {
	sandboxWitness(t)
	futurePID := liveProcess(t)
	catPID := liveProcess(t)
	legacyPID := liveProcess(t)

	// An agent type nobody has written a reader for. TypeCrew is deliberately not
	// used: the point is a value these readers have never been shown.
	const typeFuture = AgentType("reviewer")
	if err := RecordAgentWitness("rev-future", futurePID, typeFuture, "mg-future", "/repo"); err != nil {
		t.Fatalf("RecordAgentWitness(future type): %v", err)
	}
	// Positive control, same as the crew test: without a real polecat in the store
	// every assertion below passes against a reader that returns nothing.
	if err := RecordPolecatWitness("cat-live", catPID, "mg-cat", "/repo"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	// The migration control: a record from before the Type field existed.
	appendTypelessWitnessRecord(t, "cat-legacy", legacyPID, "mg-legacy", "/repo")

	// Every polecat-only reader, through its own entry point. The list is the same
	// six mg-f9e8 enumerated; what makes it more than a second hand-enumeration is
	// TestWitnessAllTypesReadersAreDeclared next door, which fails when a SEVENTH
	// appears without answering the type question at all.
	alive, err := WitnessedAlivePolecats()
	if err != nil {
		t.Fatalf("WitnessedAlivePolecats: %v", err)
	}
	aliveNames := map[string]bool{}
	for _, r := range alive {
		aliveNames[r.Name] = true
	}
	assertPolecatPopulation(t, "WitnessedAlivePolecats", aliveNames)

	verdicts, err := WitnessedPolecatVerdicts()
	if err != nil {
		t.Fatalf("WitnessedPolecatVerdicts: %v", err)
	}
	assertPolecatPopulation(t, "WitnessedPolecatVerdicts", keySet(verdicts))

	repos, _, err := WitnessedPolecatRepos()
	if err != nil {
		t.Fatalf("WitnessedPolecatRepos: %v", err)
	}
	assertPolecatPopulation(t, "WitnessedPolecatRepos", keySet(repos))

	items, err := WitnessedPolecatWorkItems()
	if err != nil {
		t.Fatalf("WitnessedPolecatWorkItems: %v", err)
	}
	assertPolecatPopulation(t, "WitnessedPolecatWorkItems", keySet(items))

	live, err := LivePolecatSet(nil)
	if err != nil {
		t.Fatalf("LivePolecatSet: %v", err)
	}
	assertPolecatPopulation(t, "LivePolecatSet", live)

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	cands, err := reg.UnadoptablePolecats()
	if err != nil {
		t.Fatalf("UnadoptablePolecats: %v", err)
	}
	candNames := map[string]bool{}
	for _, c := range cands {
		candNames[c.Name] = true
	}
	assertPolecatPopulation(t, "UnadoptablePolecats", candNames)

	// The counterpart, and the reason the store is allowed to hold other types at
	// all: the mail-check classifier must still see the record, or an agent of the
	// new type is reaped while alive — mg-f9e8's defect, one type over.
	if got := AgentWitness("rev-future"); got != WitnessAlive {
		t.Errorf("AgentWitness(rev-future) = %v, want %v — a record of an unrecognised type must still "+
			"answer for the ONE-agent probe, or the new type's agents go dark exactly the way "+
			"auto_start=false crew did (mg-f9e8)", got, WitnessAlive)
	}
}

// assertPolecatPopulation checks one reader's answer against the fixture built by
// TestAThirdWitnessTypeIsInvisibleToThePolecatReaders: both polecats present (the
// typed one and the typeless legacy one), the unrecognised type absent.
func assertPolecatPopulation(t *testing.T, reader string, got map[string]bool) {
	t.Helper()
	if !got["cat-live"] {
		t.Fatalf("control: %s dropped the live polecat (%v) — every assertion about it would pass "+
			"vacuously", reader, sortedNameSet(got))
	}
	if !got["cat-legacy"] {
		t.Errorf("%s dropped the TYPELESS record (%v). Every record written before mg-f9e8 has no type "+
			"key and all of them are polecats; dropping them removes a redeploy survivor's worktree "+
			"guard while it is still running", reader, sortedNameSet(got))
	}
	if got["rev-future"] {
		t.Errorf("%s reported an agent of an unrecognised type (%v). These readers must admit POLECATS, "+
			"not merely reject crew — otherwise the next type added to this store re-enters mg-f9e8's "+
			"blast radius silently", reader, sortedNameSet(got))
	}
}

// appendTypelessWitnessRecord adds a record in the exact on-disk shape a
// pre-mg-f9e8 pogod wrote: no "type" key at all. RecordAgentWitness cannot emit
// one, so this goes through the store's own load/save path with the field left
// unset rather than hand-rolling the whole file — the other records in the
// fixture have to survive it.
func appendTypelessWitnessRecord(t *testing.T, name string, pid int, workItem, repo string) {
	t.Helper()
	start, ok := procStart(pid)
	if !ok {
		t.Fatalf("cannot read start time for pid %d", pid)
	}
	witnessMu.Lock()
	defer witnessMu.Unlock()
	recs, err := loadWitnessAllTypes()
	if err != nil {
		t.Fatalf("loadWitnessAllTypes: %v", err)
	}
	recs = append(recs, witnessRecord{
		Name: name, PID: pid, StartTime: start, WorkItemID: workItem, SourceRepo: repo,
	})
	if err := saveWitness(recs); err != nil {
		t.Fatalf("saveWitness: %v", err)
	}
	// The `type` key must genuinely be absent, not present-and-empty: the readers
	// are being asked about the shape an OLD pogod left behind, and a test that
	// wrote `"type":""` would be asking about a shape nothing ever produced.
	data, err := os.ReadFile(WitnessPath())
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	if strings.Contains(string(data), `"type"`) && strings.Contains(string(data), `"type": ""`) {
		t.Fatalf("fixture emitted an empty type key; wanted the key absent:\n%s", data)
	}
}

func keySet[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedNameSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestWitnessRecordWithoutTypeIsAPolecat pins the compatibility half of the type
// field. Every record a pre-mg-f9e8 pogod left on disk has no "type" key, and
// all of them are polecats. Reading a missing type as "not a polecat" would drop
// exactly the population the store exists for — the survivors of a redeploy —
// out of gitgc's live set, and worktree removal is gated on that set ALONE.
//
// It writes the file by hand because that is the only way to produce the shape
// the old pogod produced; RecordAgentWitness cannot emit a typeless record.
func TestWitnessRecordWithoutTypeIsAPolecat(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)
	start, ok := procStart(pid)
	if !ok {
		t.Fatalf("cannot read start time for pid %d", pid)
	}

	// Exactly the on-disk shape of a pre-mg-f9e8 record: no "type" key at all.
	legacy := `{"version":1,"polecats":[{"name":"cat-legacy","pid":` + strconv.Itoa(pid) +
		`,"start_time":"` + start.Format(time.RFC3339Nano) + `","work_item_id":"mg-legacy"}]}`
	if err := os.WriteFile(WitnessPath(), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy witness: %v", err)
	}

	verdicts, err := WitnessedPolecatVerdicts()
	if err != nil {
		t.Fatalf("WitnessedPolecatVerdicts: %v", err)
	}
	if got := verdicts["cat-legacy"]; got != WitnessAlive {
		t.Errorf("a typeless (pre-mg-f9e8) record read as %v, want %v — every such record is a POLECAT, and "+
			"dropping it removes a running polecat's worktree guard", got, WitnessAlive)
	}
}

// TestWitnessRecordReplacedOnRespawn: a name can be reused by a later polecat,
// and a probe must find the newest spawn — not a stale pid from a previous one
// that is now free to be recycled.
func TestWitnessRecordReplacedOnRespawn(t *testing.T) {
	sandboxWitness(t)
	first := liveProcess(t)
	second := liveProcess(t)

	if err := RecordPolecatWitness("cat-reused", first, "", ""); err != nil {
		t.Fatalf("RecordPolecatWitness(first): %v", err)
	}
	if err := RecordPolecatWitness("cat-reused", second, "", ""); err != nil {
		t.Fatalf("RecordPolecatWitness(second): %v", err)
	}

	recs := readWitnessForTest(t)
	if len(recs) != 1 {
		t.Fatalf("witness holds %d records for one name, want 1 — a re-spawn must replace, not stack", len(recs))
	}
	if recs[0].PID != second {
		t.Errorf("witness pid = %d, want %d (the newest spawn)", recs[0].PID, second)
	}
}

// TestWitnessRefusesFutureVersion: a file written by a NEWER pogod may carry
// fields we would silently drop on our next write. Refuse it, and — because a
// refusal is an inability to read, not evidence of death — never reap on it.
func TestWitnessRefusesFutureVersion(t *testing.T) {
	sandboxWitness(t)

	body := `{"version": 99, "polecats": [{"name": "cat-future", "pid": 1, "start_time": "2026-07-17T08:00:00Z"}]}`
	if err := os.WriteFile(WitnessPath(), []byte(body), 0o644); err != nil {
		t.Fatalf("write witness: %v", err)
	}

	if got := AgentWitness("cat-future"); got != WitnessUnreadable {
		t.Errorf("AgentWitness against a future-version file = %v, want %v — an unreadable store is not "+
			"evidence of death; a parse error must not reap the fleet", got, WitnessUnreadable)
	}
}

// TestWitnessSurvivesProcessRestart is the point of persisting at all: a
// witness written by one pogod must be readable by a successor that never
// spawned the process and holds no memory of it. This is the property the
// in-memory registry cannot have, and its absence is what made every
// post-restart polecat look dead.
func TestWitnessSurvivesProcessRestart(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-survivor", pid, "mg-13a3", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	// Model the restart the only way that is honest at this layer: drop every
	// scrap of in-process state and re-read from disk. The store keeps nothing
	// in memory between calls, so a fresh read IS what a successor pogod does.
	recs := readWitnessForTest(t)
	if len(recs) != 1 || recs[0].Name != "cat-survivor" || recs[0].PID != pid {
		t.Fatalf("witness on disk = %+v, want one record for cat-survivor pid=%d", recs, pid)
	}
	if recs[0].StartTime.IsZero() {
		t.Error("persisted start_time is zero — a record without an identity is a false witness")
	}
	if got := AgentWitness("cat-survivor"); got != WitnessAlive {
		t.Errorf("AgentWitness(cat-survivor) = %v, want %v — a successor pogod reading this file must "+
			"find the polecat alive (mg-13a3)", got, WitnessAlive)
	}
}

// TestParsePsLstart pins the timestamp format ps actually emits, including the
// space-padded day-of-month, which is the form that bites.
func TestParsePsLstart(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Wed Jul 10 15:50:52 2026", true},
		{"Wed Jul  2 15:50:52 2026", true}, // space-padded day
		{"  Wed Jul 10 15:50:52 2026\n", true},
		{"", false},
		{"not a timestamp", false},
	}
	for _, c := range cases {
		if _, ok := parsePsLstart(c.in); ok != c.want {
			t.Errorf("parsePsLstart(%q) ok = %v, want %v", c.in, ok, c.want)
		}
	}
}

// TestProcStartMatchesRealProcess: the probe reads a plausible start time for a
// process we just started, and reports not-ok for a pid that holds nothing.
func TestProcStartMatchesRealProcess(t *testing.T) {
	pid := liveProcess(t)

	start, ok := procStart(pid)
	if !ok {
		t.Fatalf("procStart(%d) not ok for a process we just started", pid)
	}
	if d := time.Since(start); d < -5*time.Second || d > time.Minute {
		t.Errorf("procStart(%d) = %v, which is %v ago — implausible for a just-started process", pid, start, d)
	}
	// The probe is stable: the same process must read the same start time
	// every time, or the identity match would be a coin flip.
	again, ok := procStart(pid)
	if !ok || !again.Equal(start) {
		t.Errorf("procStart(%d) second read = %v (ok=%v), want a stable %v", pid, again, ok, start)
	}
}

// writeWitnessForTest replaces the witness file with exactly these records.
func writeWitnessForTest(t *testing.T, recs ...witnessRecord) {
	t.Helper()
	witnessMu.Lock()
	defer witnessMu.Unlock()
	if err := saveWitness(recs); err != nil {
		t.Fatalf("saveWitness: %v", err)
	}
}

// readWitnessForTest returns the records currently on disk.
func readWitnessForTest(t *testing.T) []witnessRecord {
	t.Helper()
	witnessMu.Lock()
	defer witnessMu.Unlock()
	recs, err := loadWitnessAllTypes()
	if err != nil {
		t.Fatalf("loadWitnessAllTypes: %v", err)
	}
	return recs
}

// TestWitnessStoreExistsSeparatesAbsenceFromZero is the mg-65b2 acceptance test.
//
// The drain gate must tell "pogod looked and there are no polecats" from "nobody
// ever wrote a witness here", because it ACTS on the answer: the first is an idle
// fleet and it may bounce; the second is an absence and it must refuse. Every
// other reader of this store maps a missing file to "no record" and declines to
// act, so the distinction never had to exist. It does now.
func TestWitnessStoreExistsSeparatesAbsenceFromZero(t *testing.T) {
	sandboxWitness(t)

	// No file: an ABSENCE. WitnessedAlivePolecats reports zero alive here too,
	// with a nil error — which is right for the reaper and is exactly why the
	// drain cannot rely on it alone.
	present, err := WitnessStoreExists()
	if err != nil {
		t.Fatalf("WitnessStoreExists on a missing file: unexpected error %v", err)
	}
	if present {
		t.Fatal("WitnessStoreExists reported a witness that does not exist")
	}
	alive, err := WitnessedAlivePolecats()
	if err != nil || len(alive) != 0 {
		t.Fatalf("precondition: absent store should read as 0 alive, nil err; got %d, %v", len(alive), err)
	}

	// An idle fleet leaves a present-and-EMPTY file: saveWitness writes
	// "polecats":[] rather than removing it. This is the state that means a
	// genuine ZERO, and it must be distinguishable from the one above — the two
	// agree on alive_count and disagree on everything that matters.
	pid := liveProcess(t)
	if err := RecordPolecatWitness("cat-gone", pid, "mg-x", ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := saveWitness(nil); err != nil {
		t.Fatalf("saveWitness(nil): %v", err)
	}
	present, err = WitnessStoreExists()
	if err != nil {
		t.Fatalf("WitnessStoreExists on an empty store: unexpected error %v", err)
	}
	if !present {
		t.Fatal("an emptied witness store must still EXIST — an idle fleet is a zero, not an absence")
	}
	alive, err = WitnessedAlivePolecats()
	if err != nil || len(alive) != 0 {
		t.Fatalf("emptied store should read as 0 alive, nil err; got %d, %v", len(alive), err)
	}
}

// TestWitnessStoreExistsWithLivePolecat pins the positive population the drain
// gate refuses on: a witnessed, running polecat, seen with no pogod involved.
func TestWitnessStoreExistsWithLivePolecat(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)
	if err := RecordPolecatWitness("cat-live", pid, "mg-live", ""); err != nil {
		t.Fatalf("record: %v", err)
	}

	present, err := WitnessStoreExists()
	if err != nil || !present {
		t.Fatalf("WitnessStoreExists after a record: present=%v err=%v", present, err)
	}
	alive, err := WitnessedAlivePolecats()
	if err != nil {
		t.Fatalf("WitnessedAlivePolecats: %v", err)
	}
	if len(alive) != 1 || alive[0].Name != "cat-live" || alive[0].PID != pid {
		t.Fatalf("expected the live polecat to be witnessed; got %+v", alive)
	}
}

// TestWitnessStoreExistsDoesNotHideAReadError: "I could not look" is not "it is
// not there". A stat error other than not-exist must reach the caller, because
// the caller's whole job is refusing to act on states it cannot establish.
func TestWitnessStoreExistsDoesNotHideAReadError(t *testing.T) {
	sandboxWitness(t)
	// A path whose PARENT is a file, not a directory: stat yields ENOTDIR, which
	// is neither "exists" nor "does not exist".
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	prev := witnessPathOverride
	witnessPathOverride = filepath.Join(blocker, "polecat-witness.json")
	t.Cleanup(func() { witnessPathOverride = prev })

	present, err := WitnessStoreExists()
	if err == nil {
		t.Fatal("a stat error was reported as a clean absence — 'cannot look' must never render as 'not there'")
	}
	if present {
		t.Fatal("WitnessStoreExists reported present alongside an error")
	}
}

// TestWitnessedPolecatVerdicts_ReportsEachRecordsVerdict pins the contract
// gitgc's sweep depends on (mg-0130): the raw per-polecat verdict, including
// WitnessUnreadable, is handed to the caller rather than pre-collapsed the way
// WitnessedAlivePolecats collapses it. A reaper that only ever saw "provably
// alive" would delete on the strength of "not provably alive", which is exactly
// what deletes the work an unmeasurable-but-live polecat is doing.
func TestWitnessedPolecatVerdicts_ReportsEachRecordsVerdict(t *testing.T) {
	sandboxWitness(t)

	// A live process whose identity we can read: Alive.
	alivePid := liveProcess(t)
	if err := RecordPolecatWitness("alive", alivePid, "mg-0130", ""); err != nil {
		t.Fatalf("record alive: %v", err)
	}
	// A process started and then reaped: its pid holds nothing → Dead.
	dead := exec.Command("sleep", "600")
	if err := dead.Start(); err != nil {
		t.Fatalf("start dead: %v", err)
	}
	deadPid := dead.Process.Pid
	if err := RecordPolecatWitness("dead", deadPid, "mg-0130", ""); err != nil {
		t.Fatalf("record dead: %v", err)
	}
	_ = dead.Process.Kill()
	_, _ = dead.Process.Wait()

	verdicts, err := WitnessedPolecatVerdicts()
	if err != nil {
		t.Fatalf("WitnessedPolecatVerdicts: %v", err)
	}
	if verdicts["alive"] != WitnessAlive {
		t.Errorf("verdicts[alive] = %v, want %v", verdicts["alive"], WitnessAlive)
	}
	if verdicts["dead"] != WitnessDead {
		t.Errorf("verdicts[dead] = %v, want %v", verdicts["dead"], WitnessDead)
	}

	// Now blind the identity probe: the alive pid still answers signals but its
	// start time is unreadable. It must surface as Unreadable, NOT dropped and
	// NOT called dead — "cannot tell" is never "safe to delete". The dead pid,
	// which holds nothing, stays Dead: pidAlive settles it before the probe.
	prev := procStartFn
	procStartFn = func(int) (time.Time, bool) { return time.Time{}, false }
	t.Cleanup(func() { procStartFn = prev })

	blind, err := WitnessedPolecatVerdicts()
	if err != nil {
		t.Fatalf("WitnessedPolecatVerdicts (blind): %v", err)
	}
	if blind["alive"] != WitnessUnreadable {
		t.Errorf("with an unreadable probe, verdicts[alive] = %v, want %v — a live pid whose identity we "+
			"cannot confirm is UNREADABLE; a reaper must keep it, not delete it (mg-0130)", blind["alive"], WitnessUnreadable)
	}
	if blind["dead"] != WitnessDead {
		t.Errorf("verdicts[dead] = %v, want %v — a pid that holds nothing is dead regardless of the probe", blind["dead"], WitnessDead)
	}
}

// TestWitnessedPolecatReposRoundTrip is the persistence half of the per-repo
// dispatch cap (mg-3977). The cap counts polecats that outlived the pogod being
// asked to dispatch, and this store is the only place that survivor's repo can
// come from — the in-memory registry is EMPTY after a restart, permanently.
func TestWitnessedPolecatReposRoundTrip(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)
	if err := RecordPolecatWitness("cat-pogo", pid, "mg-3977", "/Users/daniel/dev/pogo"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	// A record written before this field existed, or by a --no-worktree polecat
	// that has no repository at all. Its absence is a THIRD state, not a repo
	// named "": counting it against some repo would refuse a correct dispatch.
	if err := RecordPolecatWitness("cat-legacy", pid, "mg-old", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	repos, unattributed, err := WitnessedPolecatRepos()
	if err != nil {
		t.Fatalf("WitnessedPolecatRepos: %v", err)
	}
	if got := repos["cat-pogo"]; got != "/Users/daniel/dev/pogo" {
		t.Errorf("repos[cat-pogo] = %q, want the recorded repo — a survivor the cap cannot attribute "+
			"is a survivor it will not count", got)
	}
	if _, ok := repos["cat-legacy"]; ok {
		t.Error("a record with no repo was given one; an absent field must not become a repo named \"\"")
	}
	if len(unattributed) != 1 || unattributed[0] != "cat-legacy" {
		t.Errorf("unattributed = %v, want [cat-legacy] — an undercount that does not say so reads as exact",
			unattributed)
	}
}

// TestWitnessedPolecatReposPropagatesReadError: an unreadable store is not an
// empty fleet, and the cap's caller must be able to tell the difference — it
// fails open and says the count may be missing survivors, which it cannot do if
// the error is swallowed here.
func TestWitnessedPolecatReposPropagatesReadError(t *testing.T) {
	sandboxWitness(t)
	if err := os.WriteFile(WitnessPath(), []byte(`{"version":9999,"polecats":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	repos, unattributed, err := WitnessedPolecatRepos()
	if err == nil {
		t.Fatal("a store written by a newer pogod read as an empty fleet")
	}
	if repos != nil || unattributed != nil {
		t.Errorf("a failed read returned data: repos=%v unattributed=%v", repos, unattributed)
	}
}
