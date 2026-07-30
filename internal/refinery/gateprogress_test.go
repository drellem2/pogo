package refinery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProgressDiagnosisSeparatesSlowFromDead is the positive control mg-8595
// asks for, in its purest form: two records that were indistinguishable before
// this change — a gate ten minutes into its run, and a gate whose runner died
// ten minutes ago — must produce opposite verdicts.
//
// Both records have the same elapsed time. That is the whole point: elapsed
// time alone was never the discriminator, and the old log line ("step entered
// at T") carried nothing else. Only the heartbeat's age separates them, and
// the heartbeat is written by the runner, so a dead runner cannot forge it.
func TestProgressDiagnosisSeparatesSlowFromDead(t *testing.T) {
	now := time.Now()
	started := now.Add(-10 * time.Minute)

	alive := &StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		StartTime:         started,
		Heartbeat:         now.Add(-2 * time.Second), // runner beat 2s ago
		Beats:             20,
		HeartbeatInterval: (30 * time.Second).String(),
		OutputLines:       412,
		LastOutput:        now.Add(-3 * time.Second),
	}
	dead := &StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		StartTime:         started,
		Heartbeat:         started, // beat once at entry, then the runner died
		Beats:             1,
		HeartbeatInterval: (30 * time.Second).String(),
	}

	if !alive.RunnerAlive(now) {
		t.Error("a gate whose runner beat 2s ago must read as alive")
	}
	if dead.RunnerAlive(now) {
		t.Error("a gate whose runner has not beaten for 10 minutes must NOT read as alive")
	}

	// Same elapsed time in both — proving the verdict does not come from it.
	if alive.Elapsed(now).Round(time.Second) != dead.Elapsed(now).Round(time.Second) {
		t.Fatalf("test setup: both records must have equal elapsed time, got %s and %s",
			alive.Elapsed(now), dead.Elapsed(now))
	}

	aliveVerdict, deadVerdict := alive.Diagnosis(now), dead.Diagnosis(now)
	if aliveVerdict == deadVerdict {
		t.Fatal("the two states must not render the same verdict — that is the defect being fixed")
	}
	if !strings.Contains(aliveVerdict, "ALIVE") || !strings.Contains(aliveVerdict, "waiting is correct") {
		t.Errorf("a running gate's verdict should tell the operator to wait, got: %s", aliveVerdict)
	}
	if !strings.Contains(deadVerdict, "DEAD") || !strings.Contains(deadVerdict, "Waiting will not help") {
		t.Errorf("a dead runner's verdict should tell the operator waiting is futile, got: %s", deadVerdict)
	}
}

// TestProgressDiagnosisRefusesToGuessOnASilentGate covers the case the
// heartbeat genuinely cannot resolve: the runner is alive but the gate under
// it has produced no output. The verdict must say so rather than reporting
// "working" — an instrument that overclaims here would rebuild the exact
// ambiguity this ticket is about.
func TestProgressDiagnosisRefusesToGuessOnASilentGate(t *testing.T) {
	now := time.Now()
	silent := &StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		StartTime:         now.Add(-20 * time.Minute),
		Heartbeat:         now.Add(-1 * time.Second),
		Beats:             40,
		HeartbeatInterval: (30 * time.Second).String(),
		TimeoutAt:         now.Add(40 * time.Minute),
	}

	if !silent.RunnerAlive(now) {
		t.Fatal("a fresh heartbeat must read as an alive runner even when the gate is silent")
	}
	v := silent.Diagnosis(now)
	if !strings.Contains(v, "ALIVE") {
		t.Errorf("verdict must report the runner as alive, got: %s", v)
	}
	if !strings.Contains(v, "cannot be told from here") {
		t.Errorf("verdict must admit it cannot separate working from stuck, got: %s", v)
	}
	if !strings.Contains(v, "kills it in") {
		t.Errorf("verdict must bound the wait with the timeout, got: %s", v)
	}

	// With no timeout configured, the unresolvable case is unbounded and the
	// verdict has to say that too.
	silent.TimeoutAt = time.Time{}
	if v := silent.Diagnosis(now); !strings.Contains(v, "will not resolve on its own") {
		t.Errorf("with no timeout the verdict must say the wait is unbounded, got: %s", v)
	}
}

// TestProgressFinishedRecordClaimsNoLiveness guards against the finished
// record being read as a liveness signal. After the gates finish, the
// heartbeat necessarily goes stale; that must not render as "DEAD".
func TestProgressFinishedRecordClaimsNoLiveness(t *testing.T) {
	now := time.Now()
	done := &StepProgress{
		Step:              "quality-gates",
		StartTime:         now.Add(-30 * time.Minute),
		EndTime:           now.Add(-25 * time.Minute),
		Heartbeat:         now.Add(-25 * time.Minute),
		HeartbeatInterval: (30 * time.Second).String(),
	}
	if done.RunnerAlive(now) {
		t.Error("a finished record must not claim the runner is alive")
	}
	v := done.Diagnosis(now)
	if strings.Contains(v, "DEAD") {
		t.Errorf("a finished record must not read as a dead runner, got: %s", v)
	}
	if !strings.Contains(v, "finished after 5m0s") {
		t.Errorf("a finished record should report how long the step took, got: %s", v)
	}
}

// TestProgressStaleWindowComesFromTheRecord checks that the interval carried
// in the record drives staleness. A reader must be able to judge staleness
// from the record alone — a timestamp with no stated cadence cannot be called
// stale by anyone.
func TestProgressStaleWindowComesFromTheRecord(t *testing.T) {
	now := time.Now()
	p := &StepProgress{
		StartTime:         now.Add(-time.Minute),
		Heartbeat:         now.Add(-20 * time.Second),
		HeartbeatInterval: "1s",
	}
	if p.RunnerAlive(now) {
		t.Error("with a 1s interval, a 20s-old heartbeat is 20 missed beats — must read as dead")
	}
	p.HeartbeatInterval = "30s"
	if !p.RunnerAlive(now) {
		t.Error("with a 30s interval, a 20s-old heartbeat is within tolerance — must read as alive")
	}
	// An older state file with no interval falls back to the package default
	// rather than treating every heartbeat as instantly stale.
	p.HeartbeatInterval = ""
	if got, want := p.staleWindow(), heartbeatStaleAfter*gateHeartbeatInterval; got != want {
		t.Errorf("stale window with no recorded interval = %s, want the %s default", got, want)
	}
}

// TestGateWatchBeatsWhileGateRuns is the live half of the positive control:
// with a real gate running, the persisted record must change on a bounded
// interval. Before this change the step logged on entry and not again until it
// produced a result.
func TestGateWatchBeatsWhileGateRuns(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	mr := &MergeRequest{ID: "mr-beat", Branch: "b", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	wtDir := t.TempDir()
	// A gate that runs long enough for several beats and says nothing while
	// it does — the shape of a slow test suite.
	writeGateConfig(t, wtDir, `quality_gate = "sleep 0.3"`)
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("gate should have passed: %v", err)
	}

	p := mr.Progress
	if p == nil {
		t.Fatal("a run gate must leave a progress record")
	}
	if p.Beats < 2 {
		t.Errorf("expected the runner to beat repeatedly during a 300ms gate at a 20ms interval, got %d beats", p.Beats)
	}
	if p.EndTime.IsZero() {
		t.Error("a finished gate must seal its record with an end time")
	}
	if p.HeartbeatInterval != "20ms" {
		t.Errorf("record must carry the interval in force, got %q", p.HeartbeatInterval)
	}
	if p.Gate != "sleep 0.3" {
		t.Errorf("record must name the gate that ran, got %q", p.Gate)
	}
	if p.Elapsed(time.Now()) < 250*time.Millisecond {
		t.Errorf("elapsed should reflect the real run, got %s", p.Elapsed(time.Now()))
	}
}

// TestGateWatchCountsTheGatesOwnOutput checks the second signal: the gate's
// output. It is what separates "the runner is alive and the gate is working"
// from "the runner is alive and the gate it is waiting on is stuck" — a
// distinction the heartbeat alone cannot make, because the heartbeat keeps
// beating either way.
func TestGateWatchCountsTheGatesOwnOutput(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, `quality_gate = "for i in 1 2 3 4 5; do echo line-$i; sleep 0.05; done"`)

	mr := &MergeRequest{ID: "mr-talk", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	out, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err != nil {
		t.Fatalf("gate should have passed: %v", err)
	}

	if !strings.Contains(out, "line-5") {
		t.Errorf("the gate's full output must still be captured for the MR, got: %s", out)
	}
	p := mr.Progress
	if p.OutputLines != 5 {
		t.Errorf("expected 5 counted output lines, got %d", p.OutputLines)
	}
	if p.LastOutput.IsZero() {
		t.Error("a gate that produced output must have a last-output timestamp")
	}

	// A gate that talks reads as working, not merely alive.
	p.EndTime = time.Time{} // pretend it is still running
	p.Heartbeat = time.Now()
	if v := p.Diagnosis(time.Now()); !strings.Contains(v, "ALIVE and working") {
		t.Errorf("a gate producing output must read as working, got: %s", v)
	}
}

// TestSilentGateLeavesNoOutputEvidence is the negative half of the pair
// above: a gate that produces nothing must NOT accumulate an output
// timestamp. If it did, silence would be indistinguishable from progress and
// the second signal would be worthless.
func TestSilentGateLeavesNoOutputEvidence(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, `quality_gate = "sleep 0.15"`)

	mr := &MergeRequest{ID: "mr-quiet", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("gate should have passed: %v", err)
	}
	p := mr.Progress
	if p.OutputLines != 0 {
		t.Errorf("a silent gate must report 0 output lines, got %d", p.OutputLines)
	}
	if !p.LastOutput.IsZero() {
		t.Error("a silent gate must not carry a last-output timestamp")
	}
	if p.Beats < 1 {
		t.Error("a silent gate must still beat — silence from the gate is not silence from the runner")
	}
}

// TestGateWatchTracksEachGateSeparately checks the record follows the gate
// list, so "which gate are we in" is answerable while a multi-gate run is in
// flight.
func TestGateWatchTracksEachGateSeparately(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo first\", \"echo second\"]\n")

	mr := &MergeRequest{ID: "mr-multi", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	if _, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("gates should have passed: %v (ran %v)", err, ran)
	}
	p := mr.Progress
	if p.Gate != "echo second" || p.GateIndex != 2 || p.GateCount != 2 {
		t.Errorf("record should track the last gate as 2/2, got gate=%q %d/%d", p.Gate, p.GateIndex, p.GateCount)
	}
}

// TestProgressSurvivesStateRoundTrip matters because the dead-runner case is
// precisely the case where the writing process is gone: the only reader is
// whatever loads the state file afterwards. A record that did not persist
// would answer the question for every case except the one that needs it.
func TestProgressSurvivesStateRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mr := &MergeRequest{
		ID:     "mr-persist",
		Status: StatusProcessing,
		Progress: &StepProgress{
			Step:              "quality-gates",
			Gate:              "./build.sh",
			GateIndex:         1,
			GateCount:         2,
			StartTime:         now.Add(-10 * time.Minute),
			Heartbeat:         now.Add(-30 * time.Second),
			Beats:             19,
			HeartbeatInterval: "30s",
			OutputLines:       88,
			LastOutput:        now.Add(-35 * time.Second),
			TimeoutAt:         now.Add(50 * time.Minute),
		},
	}
	data, err := json.Marshal(mr)
	if err != nil {
		t.Fatal(err)
	}
	var back MergeRequest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Progress == nil {
		t.Fatal("progress record did not survive the round trip")
	}
	got, want := back.Progress, mr.Progress
	if got.Beats != want.Beats || got.OutputLines != want.OutputLines ||
		got.HeartbeatInterval != want.HeartbeatInterval ||
		!got.Heartbeat.Equal(want.Heartbeat) || !got.LastOutput.Equal(want.LastOutput) ||
		!got.TimeoutAt.Equal(want.TimeoutAt) || got.Gate != want.Gate {
		t.Errorf("round trip changed the record:\n got %+v\nwant %+v", got, want)
	}

	// An MR with no progress record must not gain an empty one in the JSON —
	// callers key off its presence.
	plain, err := json.Marshal(&MergeRequest{ID: "mr-plain"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "progress") {
		t.Errorf("an MR with no progress must omit the field, got %s", plain)
	}
}

// newProgressTestRefinery builds a refinery with a short heartbeat interval so
// beats are observable inside a test's runtime.
func newProgressTestRefinery(t *testing.T, interval time.Duration) *Refinery {
	t.Helper()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.heartbeatInterval = interval
	// Host sampling off by default: a gate test asserting on progress records
	// should not also be execing `ps` against whatever the machine running the
	// suite happens to be doing. Tests that are about contention inject a
	// known host with setLoadSampler.
	r.setLoadSampler(nil)
	return r
}

// writeGateConfig writes a .pogo/refinery.toml into a worktree.
func writeGateConfig(t *testing.T, wtDir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(wtDir, ".pogo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".pogo", "refinery.toml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
