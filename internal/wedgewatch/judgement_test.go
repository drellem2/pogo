package wedgewatch

import (
	"errors"
	"testing"
	"time"
)

// errSourceFixture is the failure this file injects to prove that a detector
// which could not read its source does not advance its sample clock.
var errSourceFixture = errors.New("wedgewatch fixture: source unreadable")

// This file covers Judgement, the read path added by mg-d616 so that
// `wedge_watch_error` finally has a consumer (internal/blindwatch).
//
// It is driven through the RUNNER, not by setting the maps by hand. mg-20eb is
// the standing warning in this package: a fallback keyed on a field nothing
// assigned looked correct in a test that set the field itself, and was
// unreachable in production for four days. A consumer of blindness that is
// tested against blindness a test wrote is the same construction.

// TestJudgementExposesBlindnessFromTheProductionPath.
func TestJudgementExposesBlindnessFromTheProductionPath(t *testing.T) {
	rec := &recorder{}
	idx := staleIndex("cat-opaque", 6*time.Hour)
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		obs := []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), now)}
		return attachEventFallback(obs, fixedEvents(idx, nil)), validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})

	// Before any sample: nothing blind, and — the half that matters — nothing
	// sampled. A consumer that read only the first would call this clean.
	blind, sampledAt, examined := w.Judgement()
	if len(blind) != 0 {
		t.Fatalf("blind before the first sample = %d, want 0", len(blind))
	}
	if !sampledAt.IsZero() {
		t.Fatalf("SampledAt before the first sample = %s, want zero", sampledAt)
	}

	w.Check(t0)

	blind, sampledAt, examined = w.Judgement()
	if len(blind) != 1 {
		t.Fatalf("blind after a sample containing an unjudgeable agent = %d, want 1. "+
			"wedge_watch_error was emitted for 18 days with no consumer; this accessor is the consumer's "+
			"source, and if it does not carry the state the event carries, it has rebuilt the gap", len(blind))
	}
	if blind[0].Name != "opaque" {
		t.Errorf("blind target = %q, want opaque", blind[0].Name)
	}
	if !blind[0].Since.Equal(t0) {
		t.Errorf("blind since = %s, want %s", blind[0].Since, t0)
	}
	if blind[0].Why == "" {
		t.Error("blind target carries no reason; a consumer that cannot say WHY produces an unactionable notice")
	}
	if !sampledAt.Equal(t0) {
		t.Errorf("SampledAt = %s, want %s — a completed sample must advance the control", sampledAt, t0)
	}
	if examined != 1 {
		t.Errorf("examined = %d, want 1", examined)
	}
}

// TestRegainedSightLeavesTheBlindSet. An agent this detector can judge again
// must leave the state, or the consumer reports a blindness that ended weeks
// ago — and dates it from the wrong start.
func TestRegainedSightLeavesTheBlindSet(t *testing.T) {
	rec := &recorder{}
	idx := staleIndex("cat-opaque", 6*time.Hour)
	opaque := true
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		var obs []Observation
		if opaque {
			obs = []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), now)}
		} else {
			obs = []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, workingPTY("42s"), now)}
		}
		return attachEventFallback(obs, fixedEvents(idx, nil)), validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})
	w.Check(t0)
	if blind, _, _ := w.Judgement(); len(blind) != 1 {
		t.Fatalf("setup: blind = %d, want 1", len(blind))
	}

	opaque = false
	w.Check(t0.Add(2 * time.Minute))
	blind, sampledAt, _ := w.Judgement()
	if len(blind) != 0 {
		t.Fatalf("blind after sight was regained = %d, want 0", len(blind))
	}
	if !sampledAt.Equal(t0.Add(2 * time.Minute)) {
		t.Errorf("SampledAt = %s, want the newer sample", sampledAt)
	}
}

// TestASourceFailureDoesNotAdvanceTheSampleClock. This is the control that
// makes blindwatch's STOPPED arm mean anything: a detector that could not read
// its source has not sampled the fleet, and must not look like one that did.
func TestASourceFailureDoesNotAdvanceTheSampleClock(t *testing.T) {
	rec := &recorder{}
	fail := false
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{observedLikeProduction("ok", "cat-ok", 9*time.Hour, workingPTY("42s"), now)}, validCredAt(now)
	}}
	src := func(now time.Time) (Snapshot, error) {
		if fail {
			return Snapshot{}, errSourceFixture
		}
		return fleet.source(now)
	}
	w := New(Options{Enabled: true, Source: src, Emit: rec.emit, Interval: time.Minute})
	w.Check(t0)
	_, first, _ := w.Judgement()
	if !first.Equal(t0) {
		t.Fatalf("setup: SampledAt = %s, want %s", first, t0)
	}

	fail = true
	w.Check(t0.Add(2 * time.Minute))
	_, after, _ := w.Judgement()
	if !after.Equal(first) {
		t.Errorf("SampledAt advanced on a source failure (%s -> %s); a detector that could not read "+
			"its source has not sampled the fleet, and advancing the clock would hide a stopped detector",
			first, after)
	}
}

// TestJudgementOnANilWatcher — pogod holds a nil *Watcher when wedge-watch is
// off, and blindwatch's adapter must be able to ask it.
func TestJudgementOnANilWatcher(t *testing.T) {
	var w *Watcher
	blind, sampledAt, examined := w.Judgement()
	if len(blind) != 0 || !sampledAt.IsZero() || examined != 0 {
		t.Fatalf("Judgement on a nil watcher = %v %s %d, want empty", blind, sampledAt, examined)
	}
}
