package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Tests for the orphaned-polecat surface (mg-0b77): the population that is
// provably ALIVE and provably UNREACHABLE — a polecat that outlived the pogod
// that spawned it.
//
// The scenario every test here builds is a pogod RESTART, modelled exactly as
// it occurs rather than imitated: a REAL running process, a witness recording
// it (written by the production writer, RecordPolecatWitness), and a registry
// that has never heard of it. That last part is not a contrivance — the
// registry is in-memory with no adopt path, so an empty registry beside a live
// polecat is the ordinary, permanent state of a survivor (mg-46a4, mg-61a0).
//
// The controls matter more than the happy path here. "Is alive but unreachable"
// is a CONJUNCTION, and each half has a way of being wrong that reads as
// success: report a registered polecat and every redeploy screams about healthy
// agents; report a dead witness and a recycled pid resurrects a corpse into the
// alert (mg-8677). Both negative controls are pinned below.

// shiftRecordedStart rewrites one witness's recorded start time, making its
// (live) pid read as a process that is not ours — what the probe sees once a
// pid has been recycled. It goes through the store's real load/save path rather
// than hand-editing JSON, so the test builds the state production builds.
func shiftRecordedStart(t *testing.T, name string, delta time.Duration) {
	t.Helper()
	witnessMu.Lock()
	defer witnessMu.Unlock()
	recs, err := loadWitness()
	if err != nil {
		t.Fatalf("loadWitness: %v", err)
	}
	found := false
	for i := range recs {
		if recs[i].Name == name {
			recs[i].StartTime = recs[i].StartTime.Add(delta)
			found = true
		}
	}
	if !found {
		t.Fatalf("no witness record named %q to shift; the test cannot model a recycled pid", name)
	}
	if err := saveWitness(recs); err != nil {
		t.Fatalf("saveWitness: %v", err)
	}
}

// orphanNames flattens a survivor list to names for stable comparison.
func orphanNames(ps []OrphanedPolecat) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// TestOrphanedPolecats_SurvivorIsVisible is THE acceptance test for mg-0b77.
//
// Before this, a survivor resolved AgentUnknown (mg-13a3) and then nothing:
// UNKNOWN is consumed in exactly one place — GCStaleMailChecks' `== AgentGone`
// — so every non-GONE state means only "keep the schedule". Nothing enumerated
// these, so nothing could report them, and the polecat sat in limbo forever.
func TestOrphanedPolecats_SurvivorIsVisible(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	pid := liveProcess(t)

	// The polecat's own pogod recorded this before it died. It is the only
	// thing that survives the restart.
	if err := RecordPolecatWitness("cat-survivor", pid, "mg-0b77"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	// Precondition, asserted rather than assumed: the successor pogod's
	// registry is empty. Without this the test could pass while exercising
	// some other path.
	if len(reg.List()) != 0 {
		t.Fatalf("precondition: registry has %d entries; this test must model a post-restart "+
			"registry that has forgotten the polecat", len(reg.List()))
	}

	got, err := reg.OrphanedPolecats()
	if err != nil {
		t.Fatalf("OrphanedPolecats: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("OrphanedPolecats() = %v, want exactly [cat-survivor] — the polecat is ALIVE (its "+
			"witness matches a real process) and this registry cannot see it and never will. If it is "+
			"not reported, nothing in the tree looks at it: it holds a worktree and a claim, its "+
			"mail-check fires into a void, and a redeploy drain declares completion over it (mg-0b77)",
			orphanNames(got))
	}
	if got[0].Name != "cat-survivor" || got[0].PID != pid {
		t.Errorf("OrphanedPolecats()[0] = {Name:%q PID:%d}, want {Name:\"cat-survivor\" PID:%d}",
			got[0].Name, got[0].PID, pid)
	}
	if got[0].WorkItemID != "mg-0b77" {
		t.Errorf("OrphanedPolecats()[0].WorkItemID = %q, want %q — the work item is what makes the "+
			"alert actionable; without it a human cannot find what the survivor is holding",
			got[0].WorkItemID, "mg-0b77")
	}
}

// TestOrphanedPolecats_RegisteredPolecatIsNotOrphaned is the negative control
// that keeps the ALIVE half honest, and it is the one that proves this can go
// RED: drop the registry check from OrphanedPolecats and this fails.
//
// An ordinary, healthy, running polecat is witnessed too — the witness is
// written at spawn and dropped at exit, so witness-alive is TRUE for the entire
// normal life of every polecat on the box. Witness-alive alone therefore means
// nothing. What makes a survivor a survivor is that the registry ALSO cannot
// see it. Report on witness-alive alone and pogod mails the coordinator about
// every healthy agent in the fleet, forever — noise that would train its reader
// to ignore the one alert that matters.
func TestOrphanedPolecats_RegisteredPolecatIsNotOrphaned(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-healthy", pid, "mg-healthy"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	// The registry DOES know this one — an ordinary running polecat.
	reg.agents["cat-healthy"] = livePolecat("cat-healthy", "mg-healthy")

	// Precondition: the witness genuinely says ALIVE, so this test is
	// exercising the registry check and not a witness that happens to be
	// silent. Without this the assertion below would pass vacuously.
	if v := PolecatWitness("cat-healthy"); v != WitnessAlive {
		t.Fatalf("precondition: PolecatWitness(cat-healthy) = %v, want %v — if the witness does not "+
			"say ALIVE, this test is not testing the registry check at all", v, WitnessAlive)
	}

	got, err := reg.OrphanedPolecats()
	if err != nil {
		t.Fatalf("OrphanedPolecats: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("OrphanedPolecats() = %v, want [] — a polecat the registry can SEE is reachable and "+
			"is not orphaned. Every healthy polecat is witnessed-alive for its whole life; reporting on "+
			"the witness alone alerts on the entire fleet (mg-0b77)", orphanNames(got))
	}
}

// TestOrphanedPolecats_RecycledPidIsNotOrphaned is the negative control that
// keeps the UNREACHABLE half honest — and it is the constraint that stops this
// fix from re-entering mg-8677 through the back door.
//
// A dead polecat's witness can outlive it (its pogod died before it could drop
// the record). If the pid is later recycled by an unrelated process, a bare
// kill(pid,0) says "something is alive" and a naive sweep would resurrect a
// corpse into a permanent, unresolvable alert about an agent that has been dead
// for hours. The witness answers "is OUR process alive" — pid AND start time —
// and this pins that OrphanedPolecats routes through that check rather than
// re-deriving a weaker one.
func TestOrphanedPolecats_RecycledPidIsNotOrphaned(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-recycled", pid, "mg-recycled"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	// Precondition: without the shift this record reads ALIVE, so the
	// assertion below is not vacuous — it is the shift that does the work.
	if v := PolecatWitness("cat-recycled"); v != WitnessAlive {
		t.Fatalf("precondition: PolecatWitness(cat-recycled) = %v, want %v before the start-time shift",
			v, WitnessAlive)
	}

	// Model the recycle: the pid is alive, but it is NOT the process we
	// recorded — it started at a different time. Rewriting the recorded start
	// time is the same fact from the other side.
	shiftRecordedStart(t, "cat-recycled", -90*time.Second)

	got, err := reg.OrphanedPolecats()
	if err != nil {
		t.Fatalf("OrphanedPolecats: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("OrphanedPolecats() = %v, want [] — this pid holds an UNRELATED process; our polecat "+
			"is long dead. Alerting here would report a corpse as a leaked agent forever, and no human "+
			"action could ever clear it (mg-8677, mg-13a3)", orphanNames(got))
	}
}

// TestOrphanedPolecats_UnreadableWitnessIsNotZero is the mg-76e5 distinction
// applied to this layer: "I cannot read the witness" and "no polecats survived"
// are different facts, and collapsing them is the whole defect this ticket
// names. An unreadable store must surface an ERROR, never an empty list — an
// empty list is what a caller renders as "drain complete, nothing unreachable".
func TestOrphanedPolecats_UnreadableWitnessIsNotZero(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)

	if err := os.WriteFile(WitnessPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt witness: %v", err)
	}

	got, err := reg.OrphanedPolecats()
	if err == nil {
		t.Fatalf("OrphanedPolecats() = %v, nil — want an ERROR. An unreadable witness means we do not "+
			"know who is out there; returning an empty list lets every caller print 'none unreachable' "+
			"when it means 'cannot see' (mg-76e5, mg-0b77)", orphanNames(got))
	}
	if got != nil {
		t.Errorf("OrphanedPolecats() returned %v alongside its error; want nil so a caller that ignores "+
			"the error cannot silently read it as zero", orphanNames(got))
	}
}

// TestOrphanReporter_RepeatsOnCooldownNotEveryTick pins the alert cadence at
// both ends, and both ends are failures.
//
// pogod sweeps on every heartbeat tick (~30s). Alerting per tick would mail the
// coordinator thousands of times a day about one leaked agent — which is not
// "loud", it is a filter rule waiting to happen, and the next real alert dies
// in it. But alerting ONCE per pogod lifetime is the opposite failure: the leak
// is permanent and only a human ends it, so a single mail that is missed means
// the survivor is silently leaked again. The signal must persist until the
// fault does — that is de08's "stay noisy" corollary, with architect's mg-0b77
// ruling that loud means observable from OUTSIDE the thing that failed.
func TestOrphanReporter_RepeatsOnCooldownNotEveryTick(t *testing.T) {
	now := time.Now()
	var fired []string
	r := newOrphanReporter()
	r.now = func() time.Time { return now }
	r.alert = func(p OrphanedPolecat) { fired = append(fired, p.Name) }

	orphans := []OrphanedPolecat{{Name: "cat-leak", PID: 4242, WorkItemID: "mg-0b77"}}

	if n := r.report(orphans); n != 1 {
		t.Fatalf("first report fired %d alerts, want 1 — a newly seen survivor must be reported", n)
	}
	// Several heartbeat ticks inside the cooldown.
	for i := 0; i < 5; i++ {
		now = now.Add(30 * time.Second)
		if n := r.report(orphans); n != 0 {
			t.Fatalf("report at +%s fired %d alerts, want 0 — pogod sweeps every tick; alerting per "+
				"tick buries the signal under itself", now.Sub(now), n)
		}
	}
	// Past the cooldown the leak restates itself: it is still leaking.
	now = now.Add(orphanAlertCooldown)
	if n := r.report(orphans); n != 1 {
		t.Fatalf("report after the %s cooldown fired %d alerts, want 1 — the survivor is STILL alive "+
			"and unreachable, and only a human can end it. Firing once per pogod lifetime means one "+
			"missed mail silently leaks the agent forever (mg-0b77)", orphanAlertCooldown, n)
	}
	if len(fired) != 2 {
		t.Errorf("alert sink saw %v, want two fires for cat-leak", fired)
	}
}

// TestOrphanReporter_ResolvedSurvivorGoesQuietAndCanReturn pins the property
// that makes the repeating alert legitimate rather than spam: it STOPS when the
// fault stops. A survivor that is dealt with (killed, or allowed to finish)
// stops being witnessed-alive, and the mail stops with it. Noise that ends when
// the fault ends is the fault reporting itself.
//
// It also pins that the cooldown is forgotten on the way out, so a name reused
// by a later polecat that leaks again is reported afresh rather than silenced
// by a stale timestamp.
func TestOrphanReporter_ResolvedSurvivorGoesQuietAndCanReturn(t *testing.T) {
	now := time.Now()
	var fired []string
	r := newOrphanReporter()
	r.now = func() time.Time { return now }
	r.alert = func(p OrphanedPolecat) { fired = append(fired, p.Name) }

	orphans := []OrphanedPolecat{{Name: "cat-leak", PID: 4242}}
	r.report(orphans)

	// The human resolved it: the process is gone, so it is no longer witnessed
	// alive and no longer appears in the sweep.
	now = now.Add(time.Minute)
	if n := r.report(nil); n != 0 {
		t.Fatalf("report with no survivors fired %d alerts, want 0", n)
	}

	// The same name leaks again later, INSIDE what would have been the original
	// cooldown. It must be reported: this is a new fault, not a repeat.
	now = now.Add(time.Minute)
	if n := r.report(orphans); n != 1 {
		t.Errorf("a survivor that returned inside the cooldown fired %d alerts, want 1 — the cooldown "+
			"must be forgotten when a survivor is resolved, or a fresh leak is silenced by a stale "+
			"timestamp", n)
	}
	if len(fired) != 2 {
		t.Errorf("alert sink saw %v, want two fires", fired)
	}
}

// TestDrainStatus_ReportsUnreachableSurvivors is the original mg-0b77 scope,
// at the layer the false claim is actually assembled.
//
// drain_wait polls this endpoint's `count` to zero and the driver then logged
// "drain complete — 0 polecats active". `count` iterates the in-memory registry,
// so a survivor reads as 0 and the drain declares completion over live work —
// a claim about the REGISTRY asserted as a claim about the WORLD (mg-46a4 §5a).
//
// The survivor is deliberately NOT added to `count`: a survivor is not
// drainable, absence never heals, and blocking the drain on one would wedge
// every future redeploy forever — moving the lie rather than removing it. The
// drain still completes at 0. It simply stops claiming the survivor is not
// there.
func TestDrainStatus_ReportsUnreachableSurvivors(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-survivor", pid, "mg-0b77"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(reg.handleDrain))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /agents/drain: %v", err)
	}
	defer resp.Body.Close()

	var status DrainStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode DrainStatus: %v", err)
	}

	// The registry count is 0 and that is CORRECT — the survivor is not
	// drainable and must not block the drain. This is pinned so a later change
	// cannot "fix" the lie by wedging every redeploy instead.
	if status.Count != 0 {
		t.Errorf("DrainStatus.Count = %d, want 0 — a survivor is not drainable and must not be counted "+
			"into the readout drain_wait polls to zero; absence never heals, so it would block every "+
			"future redeploy forever", status.Count)
	}
	if status.UnreachableErr != "" {
		t.Fatalf("DrainStatus.UnreachableErr = %q, want empty", status.UnreachableErr)
	}
	if len(status.Unreachable) != 1 || status.Unreachable[0].Name != "cat-survivor" {
		t.Fatalf("DrainStatus.Unreachable = %v, want [cat-survivor] — without this the drain readout "+
			"says 0 and the driver prints 'drain complete — 0 polecats active' over a LIVE polecat, "+
			"silently: no snapshot, no cleanup, no mention (mg-0b77)", orphanNames(status.Unreachable))
	}
	if status.Unreachable[0].PID != pid {
		t.Errorf("DrainStatus.Unreachable[0].PID = %d, want %d — the pid is what makes the report "+
			"actionable", status.Unreachable[0].PID, pid)
	}
}

// TestDrainStatus_HealthyPolecatIsCountedNotOrphaned is the control that proves
// the drain readout still measures what it always measured. An ordinary running
// polecat is witnessed AND registered: it must land in Count (it is drainable
// and the drain must wait for it) and must NOT land in Unreachable. Getting
// this backwards would make every redeploy refuse to believe its own drain.
func TestDrainStatus_HealthyPolecatIsCountedNotOrphaned(t *testing.T) {
	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	pid := liveProcess(t)

	if err := RecordPolecatWitness("cat-healthy", pid, "mg-healthy"); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	reg.agents["cat-healthy"] = livePolecat("cat-healthy", "mg-healthy")

	srv := httptest.NewServer(http.HandlerFunc(reg.handleDrain))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /agents/drain: %v", err)
	}
	defer resp.Body.Close()

	var status DrainStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode DrainStatus: %v", err)
	}
	if status.Count != 1 {
		t.Errorf("DrainStatus.Count = %d, want 1 — a registered live polecat is drainable and the "+
			"drain must still wait for it", status.Count)
	}
	if len(status.Unreachable) != 0 {
		t.Errorf("DrainStatus.Unreachable = %v, want [] — this polecat is reachable; reporting it "+
			"would make every redeploy warn about a healthy fleet", orphanNames(status.Unreachable))
	}
}
