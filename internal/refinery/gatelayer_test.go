package refinery

import (
	"strings"
	"testing"
	"time"
)

// This file is about ATTRIBUTION: which layer each signal belongs to, and
// whether the verdict can be shown to depend on the right one.
//
// mg-48d8 was filed against this output, observed live at 2026-08-05 19:20Z:
//
//	Heartbeat: 33s ago — beat 22, every 30s
//	Gate says: 124 lines, last 26m0s ago
//	Verdict:   ALIVE and working: runner heartbeat is 33s old, ... Slow, not
//	           hung — waiting is correct.
//
// The heartbeat is written by the RUNNER — the pogod-side goroutine supervising
// the gate. It proves the supervisor is alive and nothing else; it beats at the
// same 30-second cadence whether the gate under it is computing at four cores
// or deadlocked on a mutex. So "waiting is correct", an instruction not to
// intervene, was being issued on evidence about a different process than the
// one at issue.
//
// The tests below are the two halves of proving that is fixed: the verdict must
// SAY which layer it judges, and it must be demonstrably DRIVEN by that layer.

// incidentRecord reproduces the 2026-08-05 19:20Z reading exactly: a runner
// beating on schedule over a gate that has said nothing for 26 minutes.
func incidentRecord(now time.Time) *StepProgress {
	return &StepProgress{
		Step:              "quality-gates",
		Gate:              "./scripts/refinery_gate.sh",
		StartTime:         now.Add(-29*time.Minute - 3*time.Second),
		Heartbeat:         now.Add(-33 * time.Second),
		Beats:             22,
		HeartbeatInterval: (30 * time.Second).String(),
		OutputLines:       124,
		LastOutput:        now.Add(-26 * time.Minute),
		TimeoutAt:         now.Add(61 * time.Minute),
	}
}

// TestSignalsAttributeEachReadingToItsLayer is the shape the ticket asked for:
// the runner's aliveness and the gate's silence as two rows that disagree,
// rather than one sentence that resolves the disagreement without evidence.
func TestSignalsAttributeEachReadingToItsLayer(t *testing.T) {
	now := time.Now()
	p := incidentRecord(now)
	// The gate is busy, so the runner and the gate's stdout point opposite ways
	// while the gate is in fact healthy — the case where a collapsed verdict is
	// most tempting and most misleading.
	p.CPUSampledAt = now.Add(-2 * time.Second)
	p.CPUCores = 3.9
	p.CPUProcs = 4
	p.CPUWindow = "30s"

	byName := map[string]Signal{}
	for _, s := range p.Signals(now) {
		byName[s.Name] = s
	}

	beat, ok := byName["heartbeat"]
	if !ok {
		t.Fatal("no heartbeat signal")
	}
	if beat.Layer != LayerRunner {
		t.Errorf("the heartbeat must be attributed to the RUNNER — it is written by the "+
			"supervising goroutine and proves only that the supervisor exists; got layer %q", beat.Layer)
	}
	if beat.State != "alive" {
		t.Errorf("heartbeat state = %q, want alive", beat.State)
	}

	out, ok := byName["stdout"]
	if !ok {
		t.Fatal("no stdout signal")
	}
	if out.Layer != LayerGate {
		t.Errorf("stdout must be attributed to the GATE; got layer %q", out.Layer)
	}
	if out.State != "SILENT" {
		t.Errorf("a gate that has said nothing for 26 minutes must read SILENT, got %q", out.State)
	}
	if !strings.Contains(out.Detail, "26m0s") {
		t.Errorf("the stdout row must carry the silence it was judged on, got %q", out.Detail)
	}

	cpu, ok := byName["process subtree"]
	if !ok {
		t.Fatal("no process subtree signal")
	}
	if cpu.Layer != LayerGate {
		t.Errorf("the subtree measurement is rooted at the gate's own pid and must be attributed "+
			"to the GATE; got layer %q", cpu.Layer)
	}

	// The point of the whole record: two signals, opposite readings, both
	// visible. If the rows ever agree by construction the view has collapsed
	// back into one signal wearing two labels.
	if beat.State == out.State {
		t.Fatalf("the runner row and the gate row read identically (%q) — this fixture was chosen "+
			"BECAUSE they disagree, so identical states mean the attribution is not real", beat.State)
	}
}

// TestVerdictNamesTheLayerItIsJudging guards the sentence itself. The defect
// was not a wrong verdict — in the observed instance the gate really was slow
// rather than hung — it was a verdict that did not say what it was about, and
// so could not be checked.
func TestVerdictNamesTheLayerItIsJudging(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name  string
		setup func(*StepProgress)
		// wantSubject is the leading token: the verdict must open by naming
		// what it is a verdict about.
		wantSubject string
	}{
		{"talking", func(p *StepProgress) { p.LastOutput = now.Add(-2 * time.Second) }, "GATE"},
		{"silent and computing", func(p *StepProgress) {
			p.CPUSampledAt, p.CPUCores, p.CPUProcs, p.CPUWindow = now.Add(-2*time.Second), 3.9, 4, "30s"
		}, "GATE"},
		{"silent and idle", func(p *StepProgress) {
			p.CPUSampledAt, p.CPUCores, p.CPUProcs, p.CPUWindow = now.Add(-2*time.Second), 0, 1, "30s"
		}, "GATE"},
		{"silent and gone", func(p *StepProgress) {
			p.CPUSampledAt, p.CPUProcs, p.GatePID = now.Add(-2*time.Second), 0, 4321
		}, "GATE"},
		{"unmeasurable", func(p *StepProgress) { p.CPUUnavailable = "reading the process table failed" }, "GATE"},
		{"runner dead", func(p *StepProgress) { p.Heartbeat = now.Add(-20 * time.Minute) }, "RUNNER"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := incidentRecord(now)
			tc.setup(p)
			v := p.Diagnosis(now)
			if !strings.HasPrefix(v, tc.wantSubject) {
				t.Errorf("verdict must open by naming its subject %q, got: %s", tc.wantSubject, v)
			}
		})
	}
}

// TestVerdictDoesNotRestOnTheRunnersHeartbeat is the discriminating control.
//
// Two records with an IDENTICAL runner layer — same heartbeat age, same beat
// count, same cadence — and identical gate STDOUT, differing only in what the
// gate's processes are doing. If the verdict were being derived from the
// heartbeat, as the reported one was, these would render the same sentence.
// They must not.
//
// This is the test that would have failed against the reported output, and it
// is the reason the fixtures are held equal everywhere except the one signal
// under test: a pair that differs in several places proves nothing about which
// difference the verdict responded to.
func TestVerdictDoesNotRestOnTheRunnersHeartbeat(t *testing.T) {
	now := time.Now()

	computing := incidentRecord(now)
	computing.CPUSampledAt, computing.CPUCores, computing.CPUProcs, computing.CPUWindow =
		now.Add(-2*time.Second), 3.9, 4, "30s"

	deadlocked := incidentRecord(now)
	deadlocked.CPUSampledAt, deadlocked.CPUCores, deadlocked.CPUProcs, deadlocked.CPUWindow =
		now.Add(-2*time.Second), 0, 1, "30s"

	// Held constant, and asserted so rather than assumed: the runner reads
	// exactly the same in both.
	if a, b := computing.runnerSignal(now), deadlocked.runnerSignal(now); a != b {
		t.Fatalf("test setup: the runner layer must be identical in both fixtures, got %+v and %+v", a, b)
	}
	if a, b := computing.stdoutSignal(now), deadlocked.stdoutSignal(now); a != b {
		t.Fatalf("test setup: the stdout layer must be identical in both fixtures, got %+v and %+v", a, b)
	}

	vComputing, vDeadlocked := computing.Diagnosis(now), deadlocked.Diagnosis(now)
	if vComputing == vDeadlocked {
		t.Fatal("a computing gate and a stalled one rendered the same verdict from the same " +
			"heartbeat — that is exactly the defect: the sentence is the runner's liveness " +
			"dressed as the gate's")
	}
	if !strings.Contains(vComputing, "Waiting is correct") {
		t.Errorf("a gate whose subtree is burning cores should tell the operator to wait, got: %s", vComputing)
	}
	if strings.Contains(vDeadlocked, "waiting is correct") || strings.Contains(vDeadlocked, "Waiting is correct") {
		t.Errorf("a silent gate with an idle subtree must NOT be told to wait on the strength of a "+
			"fresh heartbeat, got: %s", vDeadlocked)
	}
	// And the heartbeat, where it appears at all, is labelled as the runner's
	// and disclaimed rather than counted.
	for name, v := range map[string]string{"computing": vComputing, "deadlocked": vDeadlocked} {
		if !strings.Contains(v, "RUNNER") {
			t.Errorf("%s verdict mentions the heartbeat without naming the runner: %s", name, v)
		}
		if !strings.Contains(v, "not evidence here") {
			t.Errorf("%s verdict must state that the heartbeat carries no weight about the gate: %s", name, v)
		}
	}
}

// TestFinishedRecordDropsTheSubtreeRow keeps the signal list honest at the
// other end: once the gate has finished there are no processes to measure, and
// a row reporting on them would be a measurement of nothing rendered in the
// shape of a measurement.
func TestFinishedRecordDropsTheSubtreeRow(t *testing.T) {
	now := time.Now()
	p := incidentRecord(now)
	p.EndTime = now.Add(-1 * time.Minute)
	for _, s := range p.Signals(now) {
		if s.Name == "process subtree" {
			t.Errorf("a finished record must not carry a subtree row, got %+v", s)
		}
		if s.Layer == LayerRunner && s.State != "n/a" {
			t.Errorf("a finished record's heartbeat necessarily goes stale and must claim nothing, got %+v", s)
		}
	}
}
