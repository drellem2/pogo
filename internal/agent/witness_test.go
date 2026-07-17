package agent

import (
	"os"
	"os/exec"
	"path/filepath"
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
// boundary, so a process started after it returns has a different `ps lstart`
// reading than one started before.
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

	if err := RecordPolecatWitness("cat-alive", pid, "mg-13a3"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	if got := PolecatWitness("cat-alive"); got != WitnessAlive {
		t.Errorf("PolecatWitness(cat-alive) = %v, want %v — a running polecat whose pid AND start time "+
			"match the record is our process; the registry having forgotten it is not evidence of death (mg-13a3)", got, WitnessAlive)
	}
	// The event-identity form must resolve identically: schedules address
	// agents as cat-<name>, and a witness that only answers to one spelling
	// would be silently absent for real schedules.
	if got := PolecatWitness("cat-cat-alive"); got != WitnessAlive {
		t.Errorf("PolecatWitness(cat-cat-alive) = %v, want %v — event-identity form must resolve identically", got, WitnessAlive)
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

	// `ps lstart` resolves to whole seconds, so two processes started in the
	// same second are indistinguishable to the probe. Cross into the next
	// second before starting the stand-in, or this test would skip (or worse,
	// silently pass for the wrong reason) whenever both spawns landed in the
	// same tick — which is most runs. See the resolution caveat on
	// PolecatWitness: real pid recycling is separated by far more than a
	// second, but a TEST that only fails on a lucky clock boundary is the
	// control-that-cannot-fail defect this ticket exists to end.
	waitForNextSecond(t)
	otherPid := liveProcess(t)

	otherStart, ok := procStart(otherPid)
	if !ok {
		t.Fatalf("precondition: cannot read start time of pid %d", otherPid)
	}
	ourStart, ok := procStart(ourPid)
	if !ok {
		t.Fatalf("precondition: cannot read start time of pid %d", ourPid)
	}
	if otherStart.Equal(ourStart) {
		t.Fatalf("precondition: both probe processes report start time %v despite waiting for a second "+
			"boundary — the two identities must differ or the recycled-pid case below is untestable", ourStart)
	}

	// The control: with the TRUE identity recorded, this pid reads alive. If
	// this ever stops holding, the assertion below would pass for the wrong
	// reason — WitnessDead would be trivially correct and pid-reuse untested.
	if err := RecordPolecatWitness("cat-recycled", ourPid, ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	if got := PolecatWitness("cat-recycled"); got != WitnessAlive {
		t.Fatalf("control: PolecatWitness(cat-recycled) = %v, want %v — with the true start time recorded "+
			"this live pid must read ALIVE, or the recycled-pid assertion below proves nothing", got, WitnessAlive)
	}

	// Now the recycled case: same live pid, a start time that is not ours.
	writeWitnessForTest(t, witnessRecord{Name: "cat-recycled", PID: ourPid, StartTime: otherStart})

	if got := PolecatWitness("cat-recycled"); got != WitnessDead {
		t.Errorf("PolecatWitness(cat-recycled) = %v, want %v — the pid is alive but holds a process that "+
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
	if err := RecordPolecatWitness("cat-dead", pid, ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	// Control: alive while it is alive.
	if got := PolecatWitness("cat-dead"); got != WitnessAlive {
		t.Fatalf("control: PolecatWitness(cat-dead) = %v, want %v before the kill", got, WitnessAlive)
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait() // reap, so the pid stops answering signal 0

	if got := PolecatWitness("cat-dead"); got != WitnessDead {
		t.Errorf("PolecatWitness(cat-dead) = %v, want %v — the pid answers nothing; that is positive "+
			"evidence of death and the reap is correct (mg-13a3)", got, WitnessDead)
	}
}

// TestWitnessNoRecordForUnwitnessedAgent: no witness is NOT a verdict. Crew are
// never witnessed (auto_start is their second witness) and must fall through to
// the desired state rather than being answered for here.
func TestWitnessNoRecordForUnwitnessedAgent(t *testing.T) {
	sandboxWitness(t)

	if got := PolecatWitness("crew-pm-pogo"); got != WitnessNoRecord {
		t.Errorf("PolecatWitness(crew-pm-pogo) = %v, want %v — an agent with no witness must yield no "+
			"verdict, so the desired state still gets to speak for crew", got, WitnessNoRecord)
	}

	// A witness for a DIFFERENT polecat must not answer for this one.
	pid := liveProcess(t)
	if err := RecordPolecatWitness("cat-other", pid, ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	if got := PolecatWitness("cat-nobody"); got != WitnessNoRecord {
		t.Errorf("PolecatWitness(cat-nobody) = %v, want %v", got, WitnessNoRecord)
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

	if err := RecordPolecatWitness("cat-blind", pid, ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	prev := procStartFn
	procStartFn = func(int) (time.Time, bool) { return time.Time{}, false }
	t.Cleanup(func() { procStartFn = prev })

	if got := PolecatWitness("cat-blind"); got != WitnessUnreadable {
		t.Errorf("PolecatWitness(cat-blind) = %v, want %v — a live pid whose identity we cannot read is "+
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

	if err := RecordPolecatWitness("cat-noid", pid, ""); err == nil {
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

	if err := RecordPolecatWitness("cat-drop", pid, ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	if got := PolecatWitness("cat-drop"); got != WitnessAlive {
		t.Fatalf("control: PolecatWitness(cat-drop) = %v, want %v", got, WitnessAlive)
	}

	noteWitnessExit(&Agent{Name: "cat-drop", Type: TypePolecat, PID: pid})

	if got := PolecatWitness("cat-drop"); got != WitnessNoRecord {
		t.Errorf("PolecatWitness(cat-drop) = %v, want %v after exit — a witness for a process we watched "+
			"die must not survive to argue for it", got, WitnessNoRecord)
	}
}

// TestWitnessNotRecordedForCrew: crew already have an independent second
// witness (auto_start). Witnessing them too would put two sources in a
// position to disagree about the same agent for no gain.
func TestWitnessNotRecordedForCrew(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)

	noteWitnessStart(&Agent{Name: "pm-pogo", Type: TypeCrew, PID: pid})

	if got := PolecatWitness("pm-pogo"); got != WitnessNoRecord {
		t.Errorf("PolecatWitness(pm-pogo) = %v, want %v — crew must not be witnessed", got, WitnessNoRecord)
	}
}

// TestWitnessRecordReplacedOnRespawn: a name can be reused by a later polecat,
// and a probe must find the newest spawn — not a stale pid from a previous one
// that is now free to be recycled.
func TestWitnessRecordReplacedOnRespawn(t *testing.T) {
	sandboxWitness(t)
	first := liveProcess(t)
	second := liveProcess(t)

	if err := RecordPolecatWitness("cat-reused", first, ""); err != nil {
		t.Fatalf("RecordPolecatWitness(first): %v", err)
	}
	if err := RecordPolecatWitness("cat-reused", second, ""); err != nil {
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

	if got := PolecatWitness("cat-future"); got != WitnessUnreadable {
		t.Errorf("PolecatWitness against a future-version file = %v, want %v — an unreadable store is not "+
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

	if err := RecordPolecatWitness("cat-survivor", pid, "mg-13a3"); err != nil {
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
	if got := PolecatWitness("cat-survivor"); got != WitnessAlive {
		t.Errorf("PolecatWitness(cat-survivor) = %v, want %v — a successor pogod reading this file must "+
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
	recs, err := loadWitness()
	if err != nil {
		t.Fatalf("loadWitness: %v", err)
	}
	return recs
}
