package hostload

import (
	"strings"
	"sync"
	"testing"
)

func TestTrackerMeansAndPeaks(t *testing.T) {
	var tr Tracker
	tr.Add(Sample{Cores: 10, FleetCores: 2, ExternalCores: 1, FleetProcs: 3, LoadAvg1: 5, Attributed: true})
	tr.Add(Sample{Cores: 10, FleetCores: 6, ExternalCores: 3, FleetProcs: 9, LoadAvg1: 90, Attributed: true})

	s := tr.Summary()
	if s.Samples != 2 {
		t.Fatalf("Samples = %d", s.Samples)
	}
	if !near(s.MeanFleetCores, 4) {
		t.Errorf("MeanFleetCores = %.2f, want 4", s.MeanFleetCores)
	}
	if !near(s.MeanExternalCores, 2) {
		t.Errorf("MeanExternalCores = %.2f, want 2", s.MeanExternalCores)
	}
	if !near(s.PeakFleetCores, 6) {
		t.Errorf("PeakFleetCores = %.2f, want 6", s.PeakFleetCores)
	}
	if s.PeakFleetProcs != 9 {
		t.Errorf("PeakFleetProcs = %d, want 9", s.PeakFleetProcs)
	}
	if !near(s.PeakLoadAvg1, 90) {
		t.Errorf("PeakLoadAvg1 = %.2f, want 90", s.PeakLoadAvg1)
	}
	// Second sample used 9 of 10 cores, which is at/above SaturatedAt; the
	// first used 3 and is not.
	if s.SaturatedSamples != 1 {
		t.Errorf("SaturatedSamples = %d, want 1", s.SaturatedSamples)
	}
	if !near(s.FleetShare(), 4.0/6.0) {
		t.Errorf("FleetShare = %.3f, want %.3f", s.FleetShare(), 4.0/6.0)
	}
}

// TestContendedNeedsMostOfTheRun holds the claim Report is allowed to make. A
// single spike in a long run is not "the host was saturated"; a slow gate is
// only excusable by contention if the contention lasted.
func TestContendedNeedsMostOfTheRun(t *testing.T) {
	var brief Tracker
	for i := 0; i < 9; i++ {
		brief.Add(Sample{Cores: 10, FleetCores: 1, ExternalCores: 0.5, Attributed: true})
	}
	brief.Add(Sample{Cores: 10, FleetCores: 9, ExternalCores: 1, Attributed: true})
	if brief.Summary().Contended() {
		t.Error("one saturated sample in ten reads as contended")
	}

	var sustained Tracker
	for i := 0; i < 10; i++ {
		sustained.Add(Sample{Cores: 10, FleetCores: 8, ExternalCores: 1.5, Attributed: true})
	}
	if !sustained.Summary().Contended() {
		t.Error("ten saturated samples in ten do not read as contended")
	}
	if r := sustained.Summary().Report(); !strings.Contains(r, "HOST SATURATED") {
		t.Errorf("Report should name the saturation; got %q", r)
	}
}

// TestReportStatesItsOwnLimits: the failure this package exists to prevent is
// a reader taking a number for more than it says. Every report must carry what
// it cannot see.
func TestReportStatesItsOwnLimits(t *testing.T) {
	var tr Tracker
	tr.Add(Sample{Cores: 10, FleetCores: 8, ExternalCores: 1.5, LoadAvg1: 214, Attributed: true})
	r := tr.Summary().Report()
	for _, want := range []string{"cannot say how far past full", "blind to a step slowed by I/O", "not used in this verdict"} {
		if !strings.Contains(r, want) {
			t.Errorf("Report missing %q; got %q", want, r)
		}
	}
	// The load average appears, but only labelled as context.
	if !strings.Contains(r, "214") {
		t.Errorf("Report should carry the load average as context; got %q", r)
	}
}

func TestUnattributedSummaryDoesNotClaimAnIdleFleet(t *testing.T) {
	var tr Tracker
	tr.Add(Sample{Cores: 10, FleetCores: 0, ExternalCores: 7, Attributed: false})
	r := tr.Summary().Report()
	if !strings.Contains(r, "unattributed") || !strings.Contains(r, "not the same as the fleet being idle") {
		t.Errorf("Report must distinguish unattributed from idle; got %q", r)
	}
}

// TestAttributionSurvivesARootThatExits: a root that goes away partway through
// must not erase what the run was measured doing.
func TestAttributionSurvivesARootThatExits(t *testing.T) {
	var tr Tracker
	tr.Add(Sample{Cores: 10, FleetCores: 4, Attributed: true})
	tr.Add(Sample{Cores: 10, FleetCores: 0, Attributed: false})
	if !tr.Summary().Attributed {
		t.Error("Attributed went false after a root exited mid-run")
	}
}

func TestEmptyTracker(t *testing.T) {
	var tr Tracker
	s := tr.Summary()
	if s.Contended() {
		t.Error("an empty tracker reads as contended")
	}
	if r := s.Report(); !strings.Contains(r, "not sampled") {
		t.Errorf("Report = %q, want it to say nothing was sampled", r)
	}
	var nilT *Tracker
	nilT.Add(Sample{Cores: 4})
	if nilT.Summary().Samples != 0 {
		t.Error("nil tracker should be inert")
	}
}

func TestTrackerIsConcurrencySafe(t *testing.T) {
	var tr Tracker
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); tr.Add(Sample{Cores: 10, FleetCores: 1, Attributed: true}) }()
		go func() { defer wg.Done(); _ = tr.Summary().Report() }()
	}
	wg.Wait()
	if got := tr.Summary().Samples; got != 50 {
		t.Errorf("Samples = %d, want 50", got)
	}
}

// TestFleetHeavyIgnoresNonFleetLoad is the guard's most important property.
// The measured host had a VPN extension at ~0.9 cores and the system indexer
// at ~0.3, none of it the fleet's. A dispatch guard that counted those would
// hand an unrelated process a veto over the fleet's own work, and pausing the
// fleet would not give those cores back.
func TestFleetHeavyIgnoresNonFleetLoad(t *testing.T) {
	external := Sample{Cores: 10, FleetCores: 1, ExternalCores: 8.5, FleetProcs: 2, Attributed: true}
	if external.FleetHeavy() {
		t.Error("FleetHeavy fired on a host saturated by non-fleet work")
	}
	advice := external.DispatchAdvice()
	if !strings.HasPrefix(advice, "PROCEED") {
		t.Errorf("advice = %q, want PROCEED when the load is not ours", advice)
	}

	ours := Sample{Cores: 10, FleetCores: 6.2, ExternalCores: 1.3, FleetProcs: 5, Attributed: true}
	if !ours.FleetHeavy() {
		t.Error("FleetHeavy did not fire at 6.2 of 10 cores held by the fleet")
	}
	if a := ours.DispatchAdvice(); !strings.HasPrefix(a, "HOLD") {
		t.Errorf("advice = %q, want HOLD", a)
	}
}

// TestFleetHeavySeesOneAgentAsManyProcesses: the live instance was ONE polecat
// at ~5.7 cores. Nothing here may reduce to a count of agents.
func TestFleetHeavySeesOneAgentAsManyProcesses(t *testing.T) {
	oneBusyAgent := Sample{Cores: 10, FleetCores: 6.2, ExternalCores: 1.3, FleetProcs: 5, Attributed: true}
	fiveIdleAgents := Sample{Cores: 10, FleetCores: 0.5, ExternalCores: 1.3, FleetProcs: 12, Attributed: true}

	if !oneBusyAgent.FleetHeavy() {
		t.Error("one heavy agent must trip the guard")
	}
	if fiveIdleAgents.FleetHeavy() {
		t.Error("five idle agents must not trip the guard — this is the latency arm")
	}
}

// TestHoldAdviceNamesTheShareNotTheCount. The refusal used to say it cleared
// "when the work in flight finishes", which reads as "when an agent exits" —
// and a coordinator that acted on that reading burned two spawn attempts on
// 2026-08-13 (mg-eb47). It waited for the fleet to shrink, the fleet shrank
// from 6 agents to 5, and the core share ROSE from 6.1 to 7.0 of 10 because two
// refinery gates that self-parallelise outweighed the agents that left. The
// retry condition is the SHARE, and the advice has to say which one.
func TestHoldAdviceNamesTheShareNotTheCount(t *testing.T) {
	ours := Sample{Cores: 10, FleetCores: 7.0, ExternalCores: 0.7, FleetProcs: 21, Attributed: true}
	a := ours.DispatchAdvice()
	if !strings.HasPrefix(a, "HOLD") {
		t.Fatalf("advice = %q, want HOLD", a)
	}
	for _, want := range []string{
		"rather than the agent count",
		"core share falls below",
		"not the same",
		"fewer agents, MORE cores",
		"pogo host load",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("HOLD advice is missing %q; got:\n%s", want, a)
		}
	}
	// The retired sentence, named so a future edit cannot quietly restore the
	// reading that cost the retries.
	if strings.Contains(a, "clears when the work in flight finishes") {
		t.Errorf("the count-shaped retry condition is back; got:\n%s", a)
	}
}

// TestUnattributedDispatchAdviceProceeds: missing attribution must not stall
// dispatch. An unmeasurable fleet share is missing information, and refusing
// work on missing information starves the queue for a reason nobody can check.
func TestUnattributedDispatchAdviceProceeds(t *testing.T) {
	s := Sample{Cores: 10, ExternalCores: 9, Attributed: false}
	if s.FleetHeavy() {
		t.Error("FleetHeavy fired without attribution")
	}
	a := s.DispatchAdvice()
	if !strings.HasPrefix(a, "PROCEED (unattributed)") {
		t.Errorf("advice = %q", a)
	}
	if !strings.Contains(a, "not evidence of an idle fleet") {
		t.Errorf("advice must not let unattributed read as idle; got %q", a)
	}
}
