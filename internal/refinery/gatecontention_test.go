package refinery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
)

// saturatedHost and idleHost are the two states this whole change is about.
// Neither is a state a test can create on the machine it runs on, which is
// precisely why the sampler is injectable.
func saturatedHost() hostload.Sample {
	return hostload.Sample{
		Cores: 10, FleetCores: 8.2, ExternalCores: 1.4, FleetProcs: 7,
		LoadAvg1: 118, Attributed: true, Window: time.Second,
	}
}

func idleHost() hostload.Sample {
	return hostload.Sample{
		Cores: 10, FleetCores: 0.4, ExternalCores: 0.9, FleetProcs: 6,
		LoadAvg1: 2.1, Attributed: true, Window: time.Second,
	}
}

// TestTimeoutOnASaturatedHostSaysSo is the arm that changes the outcome.
//
// The measured failure: a gate on track to exceed a 45-minute timeout while
// doing ~17 minutes of work, because another compute-heavy job held the host.
// The merge fails, and until now the failure said only "exceeded its timeout",
// which reads as a verdict on the branch. The branch was fine.
//
// What must change is the reading, not the kill. The assertions below hold
// both: the error still exists and still names the timeout, AND it states that
// a timeout reached under contention does not establish anything about the
// change.
func TestTimeoutOnASaturatedHostSaysSo(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	r.setLoadSampler(hostload.FixedSampler{Sample: saturatedHost()})
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo working; sleep 30\"]\ntimeout = \"250ms\"\n")

	mr := &MergeRequest{ID: "mr-contended", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("the gate must still fail: a bound that can be silenced by loading the host is not a bound")
	}
	var te *gateTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("expected a gateTimeoutError, got %T: %v", err, err)
	}
	if te.Contention.Samples == 0 {
		t.Fatal("the timeout error carries no contention record; the host was never sampled")
	}
	if !te.Contention.Contended() {
		t.Errorf("a host at %.1f of %d cores must read as contended, got %s",
			te.Contention.MeanUsedCores(), te.Contention.Cores, te.Contention.Report())
	}

	msg := err.Error()
	// Still a failure, still names the bound and the way to change it. The
	// phrasing of the bound became "KILLED at its ..." in mg-e565; the property
	// asserted here is unchanged.
	for _, want := range []string{"KILLED at its 250ms timeout", "raise [gates] timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the timeout error must still say %q; got: %s", want, msg)
		}
	}
	// And now says what it does and does not establish.
	for _, want := range []string{
		"TIMED OUT ON A CONTENDED HOST",
		"does NOT establish that this change is slow or broken",
		"does not establish that it is fine",
		"The kill stands",
		"Re-run on a quieter host",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the timeout error must say %q; got: %s", want, msg)
		}
	}
	// The numbers, not just the adjective — a reader has to be able to check.
	if !strings.Contains(msg, "of 10 cores") {
		t.Errorf("the error must carry the measurement, not only the verdict; got: %s", msg)
	}
}

// TestTimeoutOnAnIdleHostReadsAsBefore is the other arm, and it is the one
// that keeps the control able to fail. A gate that times out on a host with
// spare capacity has no excuse, and nothing in the message may offer it one.
func TestTimeoutOnAnIdleHostReadsAsBefore(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	r.setLoadSampler(hostload.FixedSampler{Sample: idleHost()})
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo working; sleep 30\"]\ntimeout = \"250ms\"\n")

	mr := &MergeRequest{ID: "mr-idle", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("expected the gate to time out")
	}
	msg := err.Error()
	if strings.Contains(msg, "CONTENDED HOST") {
		t.Errorf("an idle host must not produce a contention excuse; got: %s", msg)
	}
	if !strings.Contains(msg, "The host was not saturated while this ran") {
		t.Errorf("the error should state positively that the host was fine; got: %s", msg)
	}
	if !strings.Contains(msg, "elapsed time is about this gate") {
		t.Errorf("an uncontended timeout must point at the gate; got: %s", msg)
	}
}

// TestUnsampledTimeoutIsUnchanged: with sampling off, the error is exactly
// what it was before this existed. A host we could not measure must not
// acquire either an excuse or an accusation.
func TestUnsampledTimeoutIsUnchanged(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	r.setLoadSampler(nil)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo working; sleep 30\"]\ntimeout = \"250ms\"\n")

	mr := &MergeRequest{ID: "mr-unsampled", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("expected the gate to time out")
	}
	msg := err.Error()
	for _, unwanted := range []string{"CONTENDED", "was not saturated", "cores"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("an unsampled run must say nothing about the host; found %q in: %s", unwanted, msg)
		}
	}
	if mr.Progress != nil && mr.Progress.Contention != nil {
		t.Error("an unsampled run must leave no contention record")
	}
}

// TestSamplerFailureDoesNotFailTheGate: the contention record is
// observability. A gate that passed must not be failed by a broken `ps`.
func TestSamplerFailureDoesNotFailTheGate(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	r.setLoadSampler(hostload.FixedSampler{Err: errors.New("ps: exec format error")})
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo fine\"]\n")

	mr := &MergeRequest{ID: "mr-sampler-broken", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("a failing host sampler must not fail a passing gate: %v", err)
	}
	if mr.Progress != nil && mr.Progress.Contention != nil {
		t.Error("a failed sample must leave no contention record rather than a zero one")
	}
}

// TestContentionRecordLandsOnTheProgressRecord: `pogo refinery show` reads the
// MR's progress record, so the measurement has to reach it while the gate is
// still running — not only when it fails.
func TestContentionRecordLandsOnTheProgressRecord(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	r.setLoadSampler(hostload.FixedSampler{Sample: saturatedHost()})
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo hello; sleep 0.3\"]\n")

	mr := &MergeRequest{ID: "mr-record", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("gate: %v", err)
	}

	r.mu.Lock()
	c := mr.Progress.Contention
	r.mu.Unlock()
	if c == nil {
		t.Fatal("no contention record on the progress record")
	}
	if c.Samples == 0 {
		t.Fatal("contention record has no samples")
	}
	if c.Cores != 10 || c.PeakFleetCores < 8 {
		t.Errorf("contention record lost the measurement: %+v", *c)
	}
	if !c.Contended() {
		t.Errorf("expected a contended verdict, got %s", c.Report())
	}
}

// TestDiagnosisNamesContentionOnlyWhenItMatters: the slow-not-hung verdict
// gains a contention clause on a saturated host and stays silent on a quiet
// one. A note printed on every verdict is a note a reader learns to skip.
func TestDiagnosisNamesContentionOnlyWhenItMatters(t *testing.T) {
	now := time.Now()
	base := func() *StepProgress {
		return &StepProgress{
			Step: "quality-gates", StartTime: now.Add(-40 * time.Minute),
			Heartbeat: now, HeartbeatInterval: "30s", Beats: 80,
			OutputLines: 120, LastOutput: now.Add(-5 * time.Second),
		}
	}

	quiet := base()
	var qt hostload.Tracker
	qt.Add(idleHost())
	qs := qt.Summary()
	quiet.Contention = &qs
	if v := quiet.Diagnosis(now); strings.Contains(v, "HOST CONTENTION") {
		t.Errorf("a quiet host must not add a contention clause; got: %s", v)
	}

	loaded := base()
	var lt hostload.Tracker
	lt.Add(saturatedHost())
	ls := lt.Summary()
	loaded.Contention = &ls
	v := loaded.Diagnosis(now)
	if !strings.Contains(v, "ALIVE and working") {
		t.Errorf("the existing verdict must survive; got: %s", v)
	}
	for _, want := range []string{"HOST CONTENTION", "HOST SATURATED", "weaker evidence about this change"} {
		if !strings.Contains(v, want) {
			t.Errorf("a saturated host must add %q; got: %s", want, v)
		}
	}
}

// TestSilentAndIdleOnAFullHostIsNotCalledAStall is where the two measurements
// have to work together, and it is the interaction most likely to be got wrong.
//
// The subtree reading (mg-0c51) says "silent AND idle", which is the shape of a
// stall. But a saturated host STARVES a runnable process, and a starved process
// consumes almost no CPU — so a perfectly healthy gate measures as idle for
// exactly as long as the host stays full. Reading that as a stall on a full
// host is the wrong call, and the verdict has to say so or the two features
// combine into a confident wrong answer.
func TestSilentAndIdleOnAFullHostIsNotCalledAStall(t *testing.T) {
	now := time.Now()
	p := &StepProgress{
		Step: "quality-gates", StartTime: now.Add(-30 * time.Minute),
		Heartbeat: now, HeartbeatInterval: "30s", Beats: 60,
		OutputLines: 40, LastOutput: now.Add(-9 * time.Minute),
		GatePID: 4242, CPUSampledAt: now, CPUProcs: 3, CPUCores: 0.01,
		CPUWindow: "30s",
	}
	var tr hostload.Tracker
	tr.Add(saturatedHost())
	s := tr.Summary()
	p.Contention = &s

	if got := p.Subtree(); got != SubtreeIdle {
		t.Fatalf("fixture is not the silent-and-idle case: Subtree() = %v", got)
	}
	v := p.Diagnosis(now)
	if !strings.Contains(v, "SUSPECT") {
		t.Errorf("the existing subtree verdict must survive; got: %s", v)
	}
	if !strings.Contains(v, "HOST CONTENTION") {
		t.Errorf("a full host must be named on the idle verdict — it is the innocent explanation "+
			"for an idle subtree; got: %s", v)
	}
	if !strings.Contains(v, "starved rather than stalled") {
		t.Errorf("the verdict must offer starvation as the alternative reading; got: %s", v)
	}

	// Same fixture, quiet host: the starvation reading must NOT be offered,
	// or "silent and idle" acquires a permanent excuse and stops meaning
	// anything.
	var quiet hostload.Tracker
	quiet.Add(idleHost())
	qs := quiet.Summary()
	p.Contention = &qs
	if v := p.Diagnosis(now); strings.Contains(v, "starved rather than stalled") {
		t.Errorf("a quiet host must not offer starvation as an explanation; got: %s", v)
	}
}
