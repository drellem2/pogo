package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

// This file is the acceptance test for mg-0c51. The criterion is not "the
// fields are present" but:
//
//	from `pogo refinery` output alone, with no access to pogod.log, can a
//	coordinator distinguish a gate that is running slowly from one that has
//	stopped producing output?
//
// So every case below renders the actual operator-facing text and asserts on
// what a reader would conclude — and the healthy and unhealthy fixtures are
// built to be as similar as possible, so the output cannot be separating them
// on anything but the signals that matter.

const testHeartbeat = "30s"

// silentGate builds the fixture the ticket was filed about: an MR mid-gate
// whose output has been frozen for 8m31s of a 10m run. Everything about it is
// identical across the healthy and suspect readings EXCEPT the process-subtree
// measurement — which is the point.
func silentGate(now time.Time, apply func(*refinery.StepProgress)) []refinery.MergeRequest {
	p := &refinery.StepProgress{
		Step:              "quality-gates",
		Gate:              "./scripts/refinery_gate.sh",
		GateIndex:         2,
		GateCount:         2,
		StartTime:         now.Add(-10 * time.Minute),
		Heartbeat:         now.Add(-3 * time.Second),
		Beats:             20,
		HeartbeatInterval: testHeartbeat,
		OutputLines:       114,
		LastOutput:        now.Add(-8*time.Minute - 31*time.Second),
		TimeoutAt:         now.Add(50 * time.Minute),
		GatePID:           4856,
	}
	apply(p)
	return []refinery.MergeRequest{
		{
			ID: "mr-active", Branch: "polecat-4f9b", Author: "mg-4f9b",
			Status: refinery.StatusProcessing, SubmitTime: now.Add(-12 * time.Minute),
			StartTime: now.Add(-10 * time.Minute), Progress: p,
		},
		{
			ID: "mr-behind", Branch: "polecat-0c51", Author: "mg-0c51",
			Status: refinery.StatusQueued, SubmitTime: now.Add(-30 * time.Minute),
		},
	}
}

// TestQueueSeparatesASlowGateFromAStoppedOne is the acceptance criterion,
// stated as a test. The two fixtures differ ONLY in the CPU measurement of the
// gate's process subtree: same elapsed time, same 114 lines, same 8m31s of
// silence, same heartbeat. If the rendering cannot separate them, the whole
// view is back to being unable to tell slow from stopped.
func TestQueueSeparatesASlowGateFromAStoppedOne(t *testing.T) {
	now := time.Now()

	// READING ONE — healthy. This is the real observation from the ticket: a
	// gate silent for 8m31s while a descendant burned ~3.9 cores. Output
	// staleness alone calls this stopped; it was not.
	healthy := formatQueue(silentGate(now, func(p *refinery.StepProgress) {
		p.CPUSampledAt = now.Add(-2 * time.Second)
		p.CPUCores = 3.9
		p.CPUProcs = 2
		p.CPUWindow = "30s"
	}), now)

	// READING TWO — suspect. Byte-identical inputs except the subtree did no
	// work at all.
	suspect := formatQueue(silentGate(now, func(p *refinery.StepProgress) {
		p.CPUSampledAt = now.Add(-2 * time.Second)
		p.CPUCores = 0
		p.CPUProcs = 1
		p.CPUWindow = "30s"
	}), now)

	t.Logf("HEALTHY:\n%s", healthy)
	t.Logf("SUSPECT:\n%s", suspect)

	if healthy == suspect {
		t.Fatal("a computing gate and an idle one rendered identically — that is the defect")
	}
	if !strings.Contains(healthy, "ALIVE and computing") {
		t.Errorf("a silent gate burning cores must read as computing, got:\n%s", healthy)
	}
	if !strings.Contains(healthy, "Waiting is correct") {
		t.Errorf("the healthy reading must tell the operator to wait, got:\n%s", healthy)
	}
	if strings.Contains(healthy, "SUSPECT") {
		t.Errorf("a healthy gate must not be flagged — an alert that fires on healthy work "+
			"trains its reader to ignore it, got:\n%s", healthy)
	}
	if !strings.Contains(suspect, "SUSPECT") {
		t.Errorf("a silent AND idle gate must be flagged, got:\n%s", suspect)
	}
	// The asymmetry from the ticket: an idle subtree is grounds for a closer
	// look, never for automatic intervention.
	if !strings.Contains(suspect, "not proof") || !strings.Contains(suspect, "before intervening") {
		t.Errorf("the suspect reading must stop short of authorising a kill, got:\n%s", suspect)
	}
	// Both readings must carry the raw evidence, not just the verdict.
	for name, out := range map[string]string{"healthy": healthy, "suspect": suspect} {
		if !strings.Contains(out, "114 lines") || !strings.Contains(out, "8m31s ago") {
			t.Errorf("%s reading must show the output evidence it judged on, got:\n%s", name, out)
		}
		if !strings.Contains(out, "cpu:") {
			t.Errorf("%s reading must show the CPU evidence it judged on, got:\n%s", name, out)
		}
	}
}

// TestQueueShowsTheInFlightMergeRequest is the omission itself: the row that
// was moving was the one row `pogo refinery queue` did not list.
func TestQueueShowsTheInFlightMergeRequest(t *testing.T) {
	now := time.Now()
	out := formatQueue(silentGate(now, func(p *refinery.StepProgress) {
		p.CPUSampledAt = now.Add(-2 * time.Second)
		p.CPUCores = 3.9
		p.CPUProcs = 2
		p.CPUWindow = "30s"
	}), now)

	if !strings.Contains(out, "mr-active") {
		t.Errorf("the in-flight merge request must be listed, got:\n%s", out)
	}
	if !strings.Contains(out, "status=processing") {
		t.Errorf("the in-flight row must be distinguishable by status, got:\n%s", out)
	}
	if !strings.Contains(out, "step=quality-gates") || !strings.Contains(out, "(2/2)") {
		t.Errorf("the in-flight row must say which gate it is on, got:\n%s", out)
	}
	// A queued row that has been waiting 30 minutes must read as waiting
	// behind something, not as ignored.
	if !strings.Contains(out, "1 merge request ahead") {
		t.Errorf("a pending row must state its position in line, got:\n%s", out)
	}
	if strings.Contains(out, "NOTHING IN FLIGHT") {
		t.Errorf("a busy refinery must not be described as idle, got:\n%s", out)
	}
}

// TestQueueNamesTheGenuinelyFrozenCase is the other half of the pair. Pending
// rows with nothing in flight is the arrangement that used to render exactly
// like a busy refinery — because in both cases the output was a static list of
// queued rows.
func TestQueueNamesTheGenuinelyFrozenCase(t *testing.T) {
	now := time.Now()
	frozen := formatQueue([]refinery.MergeRequest{
		{ID: "mr-a", Branch: "b-a", Author: "mg-a", Status: refinery.StatusQueued, SubmitTime: now.Add(-30 * time.Minute)},
		{ID: "mr-b", Branch: "b-b", Author: "mg-b", Status: refinery.StatusQueued, SubmitTime: now.Add(-30 * time.Minute)},
	}, now)
	t.Logf("FROZEN:\n%s", frozen)

	busy := formatQueue(silentGate(now, func(p *refinery.StepProgress) {
		p.CPUSampledAt = now.Add(-2 * time.Second)
		p.CPUCores = 3.9
		p.CPUProcs = 2
		p.CPUWindow = "30s"
	}), now)

	if !strings.Contains(frozen, "NOTHING IN FLIGHT") {
		t.Errorf("pending rows with nothing being processed must say so, got:\n%s", frozen)
	}
	if strings.Contains(busy, "NOTHING IN FLIGHT") {
		t.Error("the busy rendering must not carry the frozen banner")
	}
	if !strings.Contains(frozen, "next up") {
		t.Errorf("the head of the queue should be named, got:\n%s", frozen)
	}
}

// TestQueueEmpty checks the honest empty case is not confused with the frozen
// one: no pending requests and nothing in flight is a refinery with no work.
func TestQueueEmpty(t *testing.T) {
	out := formatQueue(nil, time.Now())
	if strings.Contains(out, "NOTHING IN FLIGHT") {
		t.Errorf("an empty queue must not raise the frozen banner, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing pending") {
		t.Errorf("an empty queue should say so plainly, got:\n%s", out)
	}
}

// TestQueueInFlightBetweenGates covers the in-flight request that is NOT
// inside a quality gate — setup, push, deploy. Reprinting the previous gate's
// finished numbers as if they were live would be a fabricated liveness claim.
func TestQueueInFlightBetweenGates(t *testing.T) {
	now := time.Now()
	out := formatQueue([]refinery.MergeRequest{{
		ID: "mr-push", Branch: "b", Author: "mg-x", Status: refinery.StatusProcessing,
		SubmitTime: now.Add(-5 * time.Minute), StartTime: now.Add(-4 * time.Minute),
		Progress: &refinery.StepProgress{
			Step: "quality-gates", Gate: "./build.sh",
			StartTime: now.Add(-4 * time.Minute), EndTime: now.Add(-30 * time.Second),
			HeartbeatInterval: testHeartbeat, OutputLines: 900, LastOutput: now.Add(-31 * time.Second),
		},
	}}, now)
	t.Logf("BETWEEN STEPS:\n%s", out)

	if !strings.Contains(out, "in flight for 4m0s") {
		t.Errorf("the in-flight row must state how long it has been going, got:\n%s", out)
	}
	if !strings.Contains(out, "not inside a quality gate right now") {
		t.Errorf("a finished gate record must not be presented as a live one, got:\n%s", out)
	}
	if strings.Contains(out, "ALIVE") || strings.Contains(out, "SUSPECT") {
		t.Errorf("no liveness verdict should be drawn from a sealed gate record, got:\n%s", out)
	}
}

// TestQueueInFlightWithNoRecordYet covers the gap between dequeue and the
// first gate. A processing row with nothing under it is the ambiguity this
// whole change removes, so the gap has to be named.
func TestQueueInFlightWithNoRecordYet(t *testing.T) {
	now := time.Now()
	out := formatQueue([]refinery.MergeRequest{{
		ID: "mr-new", Branch: "b", Author: "mg-x", Status: refinery.StatusProcessing,
		SubmitTime: now.Add(-1 * time.Minute), StartTime: now.Add(-2 * time.Second),
	}}, now)
	if !strings.Contains(out, "not inside a quality gate right now") {
		t.Errorf("a just-dequeued request must say what it is doing, got:\n%s", out)
	}
	if !strings.Contains(out, "in flight for 2s") {
		t.Errorf("a just-dequeued request must state its age, got:\n%s", out)
	}
}

// TestQueuePositionExplainsAWaitingRequest covers the `pogo refinery show`
// half: a queued request must read as "waiting behind a live merge" rather
// than as "ignored for 30 minutes".
func TestQueuePositionExplainsAWaitingRequest(t *testing.T) {
	now := time.Now()
	queue := silentGate(now, func(p *refinery.StepProgress) {
		p.CPUSampledAt = now.Add(-2 * time.Second)
		p.CPUCores = 3.9
		p.CPUProcs = 2
		p.CPUWindow = "30s"
	})
	out := formatQueuePositionFrom(queue, "mr-behind", now)
	t.Logf("POSITION:\n%s", out)

	if !strings.Contains(out, "1 merge request ahead") {
		t.Errorf("position must be stated, got:\n%s", out)
	}
	if !strings.Contains(out, "mr-active") {
		t.Errorf("the request being waited on must be named, got:\n%s", out)
	}
	if !strings.Contains(out, "ALIVE and computing") {
		t.Errorf("the wait must be justified with the blocker's liveness, got:\n%s", out)
	}
}

// TestQueuePositionWithNothingInFlight is the negative half: the same queued
// request, but nothing is being processed. That is not a normal wait and must
// not read like one.
func TestQueuePositionWithNothingInFlight(t *testing.T) {
	now := time.Now()
	out := formatQueuePositionFrom([]refinery.MergeRequest{
		{ID: "mr-behind", Branch: "b", Status: refinery.StatusQueued, SubmitTime: now.Add(-30 * time.Minute)},
	}, "mr-behind", now)
	if !strings.Contains(out, "NOTHING IS IN FLIGHT") {
		t.Errorf("a queued request with no active merge must say so, got:\n%s", out)
	}
	if strings.Contains(out, "Waiting behind") {
		t.Errorf("there is nothing to wait behind, got:\n%s", out)
	}
}

// TestQueuePositionUnreadableSaysSo guards the degradation path: a failed
// lookup must be reported, not swallowed. Silence there is what made a normal
// wait look like neglect in the first place.
func TestQueuePositionUnreadableSaysSo(t *testing.T) {
	prev := getRefineryQueue
	getRefineryQueue = func() ([]refinery.MergeRequest, error) { return nil, errors.New("connection refused") }
	t.Cleanup(func() { getRefineryQueue = prev })

	out := formatQueuePosition("mr-behind", time.Now())
	if !strings.Contains(out, "connection refused") || !strings.Contains(out, "UNKNOWN") {
		t.Errorf("an unreadable pipeline must be reported as unknown, got:\n%s", out)
	}
}
