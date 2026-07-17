package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/scheduler"
)

// Tests for the polecat witness at the layer the defect actually shipped in:
// registryLiveness.AgentState, the classifier whose default arm reaped a live
// polecat's mail-check on the strength of two absences (mg-13a3).
//
// The scenario every test here reconstructs is a pogod RESTART, which is not a
// hypothetical: the registry is in-memory with no adopt/reattach path, so a
// successor pogod's registry is empty permanently, for every agent that
// survived. mg-61a0 reproduced the consequence end-to-end (live polecat pid
// 32471 → GONE → mail-check deleted from memory and disk → permanently dark).
// An empty registry + a live process is the ordinary state of a survivor, and
// it is modelled here exactly as it occurs: a real running process that a real
// registry has never heard of.

// liveProbeProcess starts a real, long-lived process and returns its pid,
// killed and reaped at test end. A real process is the point: the witness's
// whole job is telling OUR process from SOME process, and a fake pid could not
// exercise that. `sleep` is adequate — nothing here turns on signal
// disposition, so mg-61a0's SIGHUP-probe caveats do not apply.
func liveProbeProcess(t *testing.T) int {
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

// shiftWitnessStartTime rewrites the recorded start time of one witness on
// disk, making its pid read as a process that is not ours — what the probe
// sees once a pid has been recycled.
//
// It edits the FILE rather than calling a test hook in the agent package,
// which is both less invasive and more faithful: the classifier's whole
// premise is reading a file some other process wrote, so a test that writes
// that file is modelling production, not bypassing it.
func shiftWitnessStartTime(t *testing.T, name string, delta time.Duration) {
	t.Helper()
	data, err := os.ReadFile(agent.WitnessPath())
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	var disk struct {
		Version  int              `json:"version"`
		Polecats []map[string]any `json:"polecats"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatalf("parse witness: %v", err)
	}
	found := false
	for _, r := range disk.Polecats {
		if r["name"] != name {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, r["start_time"].(string))
		if err != nil {
			t.Fatalf("parse start_time: %v", err)
		}
		r["start_time"] = ts.Add(delta).Format(time.RFC3339Nano)
		found = true
	}
	if !found {
		t.Fatalf("no witness record named %q to shift; the test cannot model a recycled pid", name)
	}
	out, err := json.Marshal(disk)
	if err != nil {
		t.Fatalf("marshal witness: %v", err)
	}
	if err := os.WriteFile(agent.WitnessPath(), out, 0o644); err != nil {
		t.Fatalf("write witness: %v", err)
	}
}

// TestRegistryLiveness_WitnessedPolecatIsNotReaped is THE acceptance test for
// mg-13a3, and it is the exact case mg-61a0 reproduced on the live fleet.
//
// A polecat is RUNNING. The registry has no entry for it, because pogod
// restarted and the registry does not survive. The polecat has no prompt and
// no auto_start, so the desired state has nothing to say either. Before the
// witness, that pair of absences was the whole input and the classifier's
// default arm called it death:
//
//	registry: no entry        (absence)
//	desired state: not wanted (absence)
//	=> GONE => reap the mail-check => the polecat goes permanently dark
//
// Now there is a third thing to consult, and it is EVIDENCE rather than a
// second absence: the process itself.
func TestRegistryLiveness_WitnessedPolecatIsNotReaped(t *testing.T) {
	sandboxPogoHome(t)
	pid := liveProbeProcess(t)

	// The polecat's own pogod recorded this before it died. This is the only
	// thing that survives the restart.
	if err := agent.RecordPolecatWitness("cat-13a3", pid, "mg-13a3"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	// The successor pogod: a real, EMPTY registry — exactly what a restart
	// leaves behind.
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	l := registryLiveness{reg: reg}

	// Preconditions, asserted rather than assumed — without these the test
	// could pass while exercising some other path entirely.
	if len(reg.List()) != 0 {
		t.Fatalf("precondition: registry is not empty (%d entries); this test must model a post-restart "+
			"registry that has forgotten the polecat", len(reg.List()))
	}
	expected, err := agent.DesiredStateFor("cat-13a3")
	if err != nil || expected {
		t.Fatalf("precondition: DesiredStateFor(cat-13a3) = %v, %v; want false, nil — a polecat has no "+
			"prompt and no auto_start. If the desired state spoke for it, the double-absence path would "+
			"not be under test", expected, err)
	}

	if got := l.AgentState("cat-13a3"); got != scheduler.AgentUnknown {
		t.Errorf("AgentState(cat-13a3) = %v, want %v — the polecat is ALIVE and its witness says so. "+
			"Reaping here deletes a live agent's mail-check from memory and disk and it goes permanently "+
			"dark, unreachable by the mayor, with no signal anything is wrong (mg-13a3, reproduced by mg-61a0). "+
			"Registry-absent + OUR pid alive = UNKNOWN, never GONE", got, scheduler.AgentUnknown)
	}
}

// TestRegistryLiveness_RecycledPidIsGone is the constraint that keeps this fix
// from being a bug-swap, and it is not optional.
//
// A naive "registry-absent + pid alive = UNKNOWN" reintroduces mg-8677 through
// the very witness added to prevent mg-61a0: pids are reused. A dead polecat
// whose pid gets recycled by an unrelated process would read ALIVE → UNKNOWN →
// schedule kept forever → firing at a corpse, accumulating unbounded
// scheduler_fire_failed noise. That is mg-8677 exactly, re-entered through the
// fix for mg-61a0.
//
// So the witness must answer "is OUR process alive", never "is SOME process
// alive" — and a bare kill(pid,0) is precisely the instrument that cannot tell
// the difference. A pid whose start time disagrees is NOT our polecat and must
// resolve GONE.
func TestRegistryLiveness_RecycledPidIsGone(t *testing.T) {
	sandboxPogoHome(t)

	// A real process, started and then reaped: our polecat, now dead.
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	deadPid := cmd.Process.Pid
	if err := agent.RecordPolecatWitness("cat-recycled", deadPid, "mg-13a3"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	l := registryLiveness{reg: reg}

	// A dead polecat whose pid nothing has taken yet: GONE, and the reap is
	// correct. This is also the control for the assertion that follows — if a
	// plain dead witness did not reap, the recycled case below would prove
	// nothing about identity matching.
	if got := l.AgentState("cat-recycled"); got != scheduler.AgentGone {
		t.Fatalf("control: AgentState(cat-recycled) = %v, want %v — a witness whose pid holds nothing is "+
			"positive evidence of death", got, scheduler.AgentGone)
	}

	// Now the recycle: an unrelated live process takes the witnessed pid. We
	// cannot force the kernel to hand us a specific pid, so we do the
	// equivalent the probe cannot tell apart — re-point the witness at a LIVE
	// pid that is not the process we recorded. From the probe's side these are
	// identical: a witnessed pid that answers signals, holding a process whose
	// start time is not the one we wrote down. The process's history is not
	// something the probe can observe.
	otherPid := liveProbeProcess(t)
	if err := agent.RecordPolecatWitness("cat-recycled2", otherPid, ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	// Control: with its TRUE identity recorded, that live pid reads UNKNOWN.
	// This is what makes the next assertion meaningful — the ONLY difference
	// between these two cases is whether the recorded start time is ours.
	if got := l.AgentState("cat-recycled2"); got != scheduler.AgentUnknown {
		t.Fatalf("control: AgentState(cat-recycled2) = %v, want %v — a live pid with its true identity "+
			"recorded must NOT reap, or the recycled assertion below is vacuous", got, scheduler.AgentUnknown)
	}

	// Same live pid, an identity that is not ours: the pid was recycled.
	shiftWitnessStartTime(t, "cat-recycled2", -90*time.Second)

	if got := l.AgentState("cat-recycled2"); got != scheduler.AgentGone {
		t.Errorf("AgentState(cat-recycled2) = %v, want %v — the pid is ALIVE but holds a process that "+
			"started at a different time, so it is not our polecat. Answering UNKNOWN here keeps a dead "+
			"polecat's schedule alive forever, firing at a corpse: mg-8677, re-entered through the fix for "+
			"mg-61a0. A witness that cannot tell OUR process from SOME process is not a witness", got, scheduler.AgentGone)
	}
}

// TestRegistryLiveness_UnwitnessedPolecatStillGone: the witness ADDS evidence;
// it does not invent it. An agent with no witness and no desired state has
// nothing on this machine claiming it should exist or observing that it did,
// and still reaps. This is what preserves orphan-nudge prevention, and it is
// also the honest boundary of the fix — a polecat spawned by a pogod that
// could not record a witness is no better off than before, and no worse.
func TestRegistryLiveness_UnwitnessedPolecatStillGone(t *testing.T) {
	sandboxPogoHome(t)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	l := registryLiveness{reg: reg}

	if got := l.AgentState("cat-ghost"); got != scheduler.AgentGone {
		t.Errorf("AgentState(cat-ghost) = %v, want %v — an agent with no registry entry, no witness and "+
			"no desired state must still reap; the witness adds a source of evidence, it does not license "+
			"keeping schedules for agents nothing has ever seen", got, scheduler.AgentGone)
	}
}

// TestRegistryLiveness_RegistryStillBeatsWitness pins the precedence rule the
// witness must not loosen (mg-8677, architect's ruling):
//
//	Consult later sources ONLY when the registry yields NO evidence.
//	Evidence beats expectation, and a fresher look beats a staler one.
//
// A REGISTERED corpse is this pogod watching the process die. A witness saying
// "alive" alongside it can only be stale — the registry looked more recently
// and found a body. If the witness could override that, mg-8677 would be back:
// a mail-check firing at a corpse forever.
func TestRegistryLiveness_RegistryStillBeatsWitness(t *testing.T) {
	sandboxPogoHome(t)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	l := registryLiveness{reg: reg}

	// A registered, terminally-exited polecat: a body the registry holds.
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:    "cat-corpse",
		Type:    agent.TypePolecat,
		Command: []string{"sh", "-c", "exit 0"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("cat-corpse never exited")
	}

	// Plant a witness that CONTRADICTS the registry, pointing at a live pid.
	// This should not occur in production (the witness is dropped at exit),
	// which is exactly why the precedence must be asserted rather than assumed
	// to be unreachable — every unreachability argument in this area has been
	// wrong at least once.
	pid := liveProbeProcess(t)
	if err := agent.RecordPolecatWitness("cat-corpse", pid, ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	if got := agent.PolecatWitness("cat-corpse"); got != agent.WitnessAlive {
		t.Fatalf("precondition: witness says %v, want alive — the witness must be arguing for life or "+
			"this test does not exercise the conflict", got)
	}

	if got := l.AgentState("cat-corpse"); got != scheduler.AgentGone {
		t.Errorf("AgentState(cat-corpse) = %v, want %v — the registry LOOKED and found a body. A witness "+
			"must not resurrect a corpse the registry is holding: that is mg-8677's precedence rule, and "+
			"adding an evidence source must not weaken it", got, scheduler.AgentGone)
	}
}

// TestRegistryLiveness_CrewUnaffectedByWitness: crew are never witnessed —
// their prompt's auto_start already IS their independent second witness — so
// every crew classification must be exactly what it was before mg-13a3. This is
// the regression guard for mg-de08's population.
func TestRegistryLiveness_CrewUnaffectedByWitness(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPrompt(t, "pm-live", true)
	writeCrewPrompt(t, "pm-parked", false)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	l := registryLiveness{reg: reg}

	// mg-de08's invariant, unmoved: an unregistered auto_start crew agent
	// during the boot window is EXPECTED, not GONE.
	if got := l.AgentState("pm-live"); got != scheduler.AgentExpected {
		t.Errorf("REGRESSION (mg-de08): AgentState(pm-live) = %v, want %v — an unregistered auto_start "+
			"crew agent must survive the boot window", got, scheduler.AgentExpected)
	}
	// And a crew agent out of the desired state still reaps.
	if got := l.AgentState("pm-parked"); got != scheduler.AgentGone {
		t.Errorf("AgentState(pm-parked) = %v, want %v", got, scheduler.AgentGone)
	}
}

// TestRegistryLiveness_SpawnRecordsWitness closes the loop the other tests take
// on faith: that a real polecat spawned through the real Spawn path gets a
// witness without anyone asking, and that it is dropped when the process dies.
// Without this, every test above could pass while production recorded nothing.
func TestRegistryLiveness_SpawnRecordsWitness(t *testing.T) {
	sandboxPogoHome(t)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(5 * time.Second)

	a, err := reg.Spawn(agent.SpawnRequest{
		Name:       "cat-spawned",
		Type:       agent.TypePolecat,
		Command:    []string{"sleep", "600"},
		WorkItemID: "mg-13a3",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Spawn recorded it — a successor pogod would find this polecat alive.
	if got := agent.PolecatWitness("cat-spawned"); got != agent.WitnessAlive {
		t.Fatalf("PolecatWitness(cat-spawned) = %v, want %v — Spawn must record a polecat's witness, or "+
			"the classifier has nothing to consult after a restart (mg-13a3)", got, agent.WitnessAlive)
	}

	// And the live polecat is not reapable even though the classifier's
	// registry-absent path is what a restart would take. Prove the witness is
	// what saves it by asking a registry that never heard of this agent.
	fresh, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer fresh.StopAll(2 * time.Second)
	if got := (registryLiveness{reg: fresh}).AgentState("cat-spawned"); got != scheduler.AgentUnknown {
		t.Errorf("AgentState(cat-spawned) against a post-restart registry = %v, want %v", got, scheduler.AgentUnknown)
	}

	// Exit drops it: pogod watched the process die, so the record is known
	// false rather than stale, and the pid is free to be recycled.
	if err := reg.Stop("cat-spawned", 5*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("cat-spawned never exited")
	}
	if got := agent.PolecatWitness("cat-spawned"); got != agent.WitnessNoRecord {
		t.Errorf("PolecatWitness(cat-spawned) = %v, want %v after exit — a witness for a process we "+
			"watched die must not survive to argue for it", got, agent.WitnessNoRecord)
	}
}
