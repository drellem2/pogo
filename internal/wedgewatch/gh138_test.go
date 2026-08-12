package wedgewatch

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// This file is drellem2/pogo#138, which is two defects that share one sentence:
// something that fires BECAUSE an agent is in trouble was being read as
// evidence that it is not.
//
//  1. The headline, and NOT the defect the issue reported. mg-20eb wired the
//     event-log fallback so `established` could be true without a counter.
//     inspect's blind branch keyed on `established` alone, so an agent with an
//     unreadable counter and no marker walked past it into the healthy default
//     — at ANY staleness. The answer space went from {healthy, stalled, blind}
//     to {healthy, stalled}, and "I cannot judge this" collapsed into
//     "healthy". No modal, no dismissal, no rescue required.
//
//  2. The reported one. pogod's `modal_dismissed` is written under the
//     DISMISSED AGENT's identity, so it landed in the same last-seen index that
//     the stall clock reads. dismissalCooldown (5m) regenerates that line at
//     twice the rate MarkerHoldDown (10m) needs to survive, so a modal the
//     dismissal cannot clear suppressed the finding about it forever.
//
// A note for whoever reads a wedge_watch_error burst after this lands: that is
// the instrument working. "Zero wedge_watch_error since 2026-08-10" was
// GUARANTEED by defect (1) — the blind branch was unreachable — and an
// instrument that cannot go red is not evidence of green.

// staleIndex builds a readable fallback index putting one identity's newest
// line `age` in the past.
func staleIndex(identity string, age time.Duration) EventsIndex {
	return EventsIndex{Readable: true, LastSeen: map[string]time.Time{identity: t0.Add(-age)}}
}

// --- (1) the blind state ----------------------------------------------------

// TestAnUnjudgeableAgentCannotReadHealthy is the headline regression, driven
// through the runner because that is where the verdict is consumed.
//
// The condition needs nothing exotic: a harness whose status line this package
// cannot parse, nothing on screen from the marker table, and an event log whose
// newest line for the identity is six hours old. That agent cannot be judged.
// Before this fix it came out of every pass as HEALTHY — silently, with no
// event of any kind, which is the worst of the three possible wrong answers.
func TestAnUnjudgeableAgentCannotReadHealthy(t *testing.T) {
	rec := &recorder{}
	idx := staleIndex("cat-opaque", 6*time.Hour)
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		obs := []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), now)}
		return attachEventFallback(obs, fixedEvents(idx, nil)), validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})
	w.Check(t0)

	errs := rec.ofType(EventError)
	if len(errs) != 1 {
		t.Fatalf("wedge_watch_error events = %d, want 1. An agent whose counter cannot be parsed and "+
			"which shows no dead-end marker is UNJUDGEABLE; reading it as healthy is drellem2/pogo#138, "+
			"and it needs no modal and no dismissal to happen", len(errs))
	}
	if got := errs[0].Details["target"]; got != "opaque" {
		t.Errorf("error target = %v, want opaque", got)
	}
	if got := errs[0].Details["phase"]; got != "judge" {
		t.Errorf("error phase = %v, want judge", got)
	}
	why, _ := errs[0].Details["error"].(string)
	if !strings.Contains(why, "not the same as healthy") {
		t.Errorf("the blind message does not say what it is refusing to conclude: %q", why)
	}
	// The age is the one fact that WAS established, and an operator deciding
	// whether to go and look needs it.
	if !strings.Contains(why, "6h0m0s") {
		t.Errorf("the blind message omits the event-log age it did establish: %q", why)
	}
	// And it must not present that age as a verdict — see blindOnFallbackAlone.
	if !strings.Contains(why, "cannot separate a wedged agent from an idle one") {
		t.Errorf("the blind message presents event-log age as if it were a verdict: %q", why)
	}

	if fired := rec.ofType(EventFired); len(fired) != 0 {
		t.Errorf("an unjudgeable agent produced %d findings; blind is not stalled either", len(fired))
	}
}

// TestStalenessCannotRescueAnUnjudgeableAgent pins that the collapse was
// unconditional rather than a threshold being too high.
//
// Every one of these ages read HEALTHY before the fix, including six hours.
// That is what makes it the headline: there is no staleness at which the old
// code would have told you anything.
func TestStalenessCannotRescueAnUnjudgeableAgent(t *testing.T) {
	for _, age := range []time.Duration{time.Minute, time.Hour, 6 * time.Hour, 72 * time.Hour} {
		obs := attachEventFallback(
			[]Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), t0)},
			fixedEvents(staleIndex("cat-opaque", age), nil))

		w := New(Options{Enabled: true, Thresholds: Thresholds{}})
		_, state := w.inspect(obs[0], validCredAt(t0), roomyHost, t0)
		if state.kind != stateBlind {
			t.Errorf("age %s: state = %d, want stateBlind (%d)", age, state.kind, stateBlind)
		}
	}
}

// TestTheEventFallbackKeepsTheJobItCanActuallyDo is the other half of the
// ruling, and the reason this is not a revert of mg-20eb.
//
// The fallback stays wired and stays useful — it TIMES a marker's hold-down,
// which is a job it can do, because the marker supplies the evidence and the
// clock supplies only the age. What it may not do is produce a verdict on its
// own. Deleting it would put back mg-20eb's 40 judgements and zero verdicts.
func TestTheEventFallbackKeepsTheJobItCanActuallyDo(t *testing.T) {
	withMarker := func(age time.Duration) (Finding, inspectState) {
		pty := append(noCounterPTY(), []byte("Please run /login\r\n")...)
		obs := attachEventFallback(
			[]Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, pty, t0)},
			fixedEvents(staleIndex("cat-opaque", age), nil))
		w := New(Options{Enabled: true})
		return w.inspect(obs[0], validCredAt(t0), roomyHost, t0)
	}

	if f, state := withMarker(6 * time.Hour); state.kind != stateConfirmed {
		t.Errorf("marker + 6h of event-log silence = %d, want stateConfirmed (%d) — the fallback must "+
			"still age a marker past its hold-down, or mg-20eb regresses", state.kind, stateConfirmed)
	} else if f.StallSource != "events_stale" {
		t.Errorf("stall_source = %q, want events_stale", f.StallSource)
	}

	if _, state := withMarker(time.Minute); state.kind != statePending {
		t.Errorf("marker + 1m of event-log silence = %d, want statePending (%d) — the hold-down is what "+
			"stops an agent that merely WROTE a marker string being reported", state.kind, statePending)
	}
}

// TestAReadableCounterIsStillJudgedOnItsOwnEvidence is the negative control for
// the blind restore: the fix must not make a healthy fleet noisy.
//
// A parseable counter that has just changed is positive evidence of progress —
// the agent's own, not an inference from an event log — and stays HEALTHY with
// no event emitted at all. Today every crew agent on this box is in this state,
// which is why the expected wedge_watch_error volume from this change is zero.
func TestAReadableCounterIsStillJudgedOnItsOwnEvidence(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		declared := (2*time.Minute + now.Sub(t0)).Round(time.Second).String()
		return []Observation{observedLikeProduction("working", "cat-working", time.Hour,
			currentSpinnerPTY(declared, "cerebrating"), now)}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})
	run(t, w, 30*time.Minute, time.Minute)

	if errs := rec.ofType(EventError); len(errs) != 0 {
		t.Errorf("a working agent produced %d wedge_watch_error events: %v", len(errs), errs[0].Details)
	}
	if fired := rec.ofType(EventFired); len(fired) != 0 {
		t.Errorf("a working agent produced %d findings", len(fired))
	}
}

// --- (2) the dismissal refresh ----------------------------------------------

// TestPogodsModalDismissalCannotRefreshTheStallClock drives the PRODUCTION
// reader against the log shape this box actually produced.
//
// crew-architect on 2026-08-12: an agent_spawned line hours old, then pogod's
// own modal_dismissed thirty seconds ago written UNDER THE AGENT'S IDENTITY.
// The clock must read from the spawn line. Reading it from the dismissal is the
// reported defect, and it is self-sustaining rather than transient:
// dismissalCooldown is 5m against a 10m MarkerHoldDown, so the refresh
// regenerates at twice the rate the hold-down needs to survive.
func TestPogodsModalDismissalCannotRefreshTheStallClock(t *testing.T) {
	spawn := t0.Add(-6 * time.Hour)
	writeFixtureLog(t, []events.Event{
		ev("cat-architect", "agent_spawned", spawn),
		ev("cat-architect", "modal_dismissed", t0.Add(-30*time.Second)),
		ev("cat-architect", "rating_dialog_dismissed", t0.Add(-30*time.Second)),
	})

	idx := SystemEvents()
	if !idx.Readable {
		t.Fatalf("SystemEvents unreadable: %s", idx.Reason)
	}
	if got := idx.LastSeen["cat-architect"]; !got.Equal(spawn) {
		t.Errorf("cat-architect last seen = %s, want its %s spawn line. pogod's own modal dismissal was "+
			"credited to the agent it was performed ON — so the act that PROVES the agent is behind a "+
			"modal is also the act that makes it look freshly alive, and with a 5m cooldown against a "+
			"10m hold-down the finding is suppressed indefinitely", got, spawn)
	}
}

// TestSyntheticFailureCannotRefreshTheStallClock is the same inversion with a
// different author, and the reason this is an exclusion LIST rather than a
// two-name special case for the modal path.
//
// internal/synthwatch records synthetic_failure_detected under the failing
// agent's identity. It is the most frequent crew event on this host, so fixing
// only the dismissal would have left the defect live under the trigger that
// fires most often.
func TestSyntheticFailureCannotRefreshTheStallClock(t *testing.T) {
	spawn := t0.Add(-6 * time.Hour)
	writeFixtureLog(t, []events.Event{
		ev("cat-failing", "agent_spawned", spawn),
		ev("cat-failing", "synthetic_failure_detected", t0.Add(-time.Minute)),
	})

	idx := SystemEvents()
	if !idx.Readable {
		t.Fatalf("SystemEvents unreadable: %s", idx.Reason)
	}
	if got := idx.LastSeen["cat-failing"]; !got.Equal(spawn) {
		t.Errorf("cat-failing last seen = %s, want %s. A record that the agent's turns are FAILING "+
			"was read as evidence that the agent is working", got, spawn)
	}
}

// TestAnAgentsOwnEventsStillRefreshTheStallClock is the control for the two
// above: the exclusion must remove pogod's interventions and nothing else.
//
// Without this, an over-broad exclusion would empty the index and report the
// whole fleet unjudgeable — which reads as a fixed detector right up until
// somebody notices it has stopped answering.
func TestAnAgentsOwnEventsStillRefreshTheStallClock(t *testing.T) {
	writeFixtureLog(t, []events.Event{
		ev("cat-live", "agent_spawned", t0.Add(-6*time.Hour)),
		ev("cat-live", "modal_dismissed", t0.Add(-90*time.Minute)),
		ev("cat-live", "work_item_claimed", t0.Add(-20*time.Minute)),
	})

	idx := SystemEvents()
	if !idx.Readable {
		t.Fatalf("SystemEvents unreadable: %s", idx.Reason)
	}
	if got := idx.LastSeen["cat-live"]; !got.Equal(t0.Add(-20 * time.Minute)) {
		t.Errorf("cat-live last seen = %s, want its own work_item_claimed at %s", got, t0.Add(-20*time.Minute))
	}
}

// --- the case that actually bites -------------------------------------------

// TestTheSuccessfulRescueWithAnUnreadableCounterAndALingeringMarker is the
// three-way conjunction the triage named as the one branch that needed a test
// rather than an assurance, and it is where this fix COSTS something.
//
// The conjunction: pogod dismisses a modal, the dismissal WORKS, and the agent
// resumes — but its counter cannot be parsed, and the dead-end marker is still
// sitting in the PTY ring. Both halves of that were measured on this box:
// crew-mayor still showed a marker 67s after its 2026-08-10 dismissal, and
// crew-architect's next completed turn landed 9m55s after its 2026-08-12 one,
// against a 10m0s hold-down.
//
// Before the exclusion, the dismissal itself reset the clock, so a marker that
// outlived a successful rescue stayed PENDING. After it, the clock runs from the
// agent's last own event — which is EARLIER than the dismissal — so the agent
// reports CONFIRMED for the window between a successful rescue and its first
// event afterwards. That is a real, transient false positive and this test pins
// it as the accepted cost rather than hiding it:
//
//   - It is transient and self-correcting: the resumed agent's first own event
//     clears it, and recordCleared emits the all-clear.
//   - The alternative is to treat the dismissal as proof the rescue worked. It
//     is not: at the instant of dismissal nothing knows whether the modal
//     cleared, and inferring recovery from an intervention is exactly the
//     absence-as-evidence error this package refuses elsewhere.
//   - The five-second margin the old behaviour ran on was a coincidence, not a
//     safety property. Had MarkerHoldDown been 9m, or had architect resumed six
//     seconds later, the observed case flips. Trading a transient false alarm
//     for a permanent false silence is the right side of that.
func TestTheSuccessfulRescueWithAnUnreadableCounterAndALingeringMarker(t *testing.T) {
	const (
		lastOwnEvent = 35 * time.Minute // before the dismissal — the agent's own newest line
		ringLingers  = 67 * time.Second // measured on crew-mayor, 2026-08-10
	)
	// t0 is the moment pogod dismissed the modal, and the dismissal WORKED.
	ownEventAt := t0.Add(-lastOwnEvent)
	resumedEventAt := t0.Add(3 * time.Minute) // crew-architect's was 9m55s

	// The log goes through the PRODUCTION reader, so the exclusion is what
	// decides the clock rather than a hand-built index. Before this fix
	// SystemEvents returned t0 here — pogod's own dismissal, 30 seconds old at
	// the first sample — and the finding read PENDING and was never emitted.
	writeFixtureLog(t, []events.Event{
		ev("cat-rescued", "agent_spawned", t0.Add(-9*time.Hour)),
		ev("cat-rescued", "work_item_claimed", ownEventAt),
		ev("cat-rescued", "modal_dismissed", t0),
	})

	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		pty := noCounterPTY()
		if now.Sub(t0) < ringLingers {
			// The rescue succeeded, but the modal's text has not scrolled out
			// of the ring yet.
			pty = append(pty, []byte("Stop and wait for limit to reset\r\n")...)
		}
		obs := []Observation{observedLikeProduction("rescued", "cat-rescued", 9*time.Hour, pty, now)}
		return attachEventFallback(obs, SystemEvents), validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 30 * time.Second})

	// One sample inside the lingering-marker window, before the resumed agent
	// has produced anything of its own.
	w.Check(t0.Add(30 * time.Second))
	fired := rec.ofType(EventFired)
	if len(fired) != 1 {
		t.Fatalf("findings during the lingering-marker window = %d, want 1. The clock now runs from the "+
			"agent's last OWN event, %s before pogod's dismissal, so this window IS reported. That is "+
			"the accepted cost of the exclusion — and the alternative is what it replaces: the dismissal "+
			"resetting the clock to 30s, holding the finding below the %s hold-down, and regenerating "+
			"every 5m for as long as the modal survives", len(fired), lastOwnEvent, DefaultMarkerHoldDown)
	}
	latest, _ := w.Latest()
	if len(latest) != 1 || latest[0].StallSource != "events_stale" {
		t.Fatalf("findings = %+v, want one with stall_source=events_stale — the fallback clock is what "+
			"aged this, and the whole question is which line it started from", latest)
	}
	if got := latest[0].StalledFor; got < lastOwnEvent {
		t.Errorf("stalled_for = %s, want at least %s (the age of the agent's own newest line). A shorter "+
			"value means pogod's dismissal is still supplying the clock", got, lastOwnEvent)
	}

	// It is not permanent. Once the marker has scrolled out and the resumed
	// agent has written a line of its own, the finding clears.
	writeFixtureLog(t, []events.Event{
		ev("cat-rescued", "agent_spawned", t0.Add(-9*time.Hour)),
		ev("cat-rescued", "work_item_claimed", ownEventAt),
		ev("cat-rescued", "modal_dismissed", t0),
		ev("cat-rescued", "work_item_claimed", resumedEventAt),
	})
	w.Check(t0.Add(4 * time.Minute))
	cleared := rec.ofType(EventCleared)
	if len(cleared) != 1 {
		t.Fatalf("wedge_watch_cleared events = %d, want 1. A transient false positive is only acceptable "+
			"BECAUSE it clears itself; an alarm with no all-clear leaves the reader holding an open "+
			"incident forever", len(cleared))
	}
	if got := cleared[0].Details["target"]; got != "rescued" {
		t.Errorf("cleared target = %v, want rescued", got)
	}

	// And the counterpart that costs nothing: with a READABLE counter, the
	// rescue is visible in the agent's own evidence and no finding is produced
	// at all. This is the case both measured incidents were actually in, which
	// is why zero verdicts have been wrongly suppressed to date — and it is why
	// the event log is not even opened here (attachEventFallback is lazy), so
	// the exclusion cannot affect this path either way.
	recOK := &recorder{}
	okFleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		declared := (10*time.Second + now.Sub(t0)).Round(time.Second).String()
		pty := []byte(string(currentWorkedForPTY(declared)) + "Stop and wait for limit to reset\r\n")
		obs := []Observation{observedLikeProduction("rescued", "cat-rescued", 9*time.Hour, pty, now)}
		return attachEventFallback(obs, SystemEvents), validCredAt(now)
	}}
	wOK := New(Options{Enabled: true, Source: okFleet.source, Emit: recOK.emit, Interval: 30 * time.Second})
	run(t, wOK, 5*time.Minute, 30*time.Second)
	if fired := recOK.ofType(EventFired); len(fired) != 0 {
		t.Errorf("a rescued agent with a READABLE counter produced %d findings; its own advancing "+
			"counter is positive evidence of progress and must settle this without the event log",
			len(fired))
	}
}

// TestTheTwoFixesAreIndependent is the ordering claim from the ticket, checked
// rather than asserted: the blind restore needs no dismissal to matter, and the
// exclusion needs no unreadable counter to matter.
//
// They were filed as one issue and they are two defects. Fixing only the
// reported one (the dismissal refresh) would have left an unjudgeable agent
// reading HEALTHY, which is the larger of the two.
func TestTheTwoFixesAreIndependent(t *testing.T) {
	// Blind restore, with a log containing nothing but the agent's own events:
	// no dismissal anywhere, still unjudgeable.
	obs := attachEventFallback(
		[]Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), t0)},
		fixedEvents(staleIndex("cat-opaque", 6*time.Hour), nil))
	w := New(Options{Enabled: true})
	if _, state := w.inspect(obs[0], validCredAt(t0), roomyHost, t0); state.kind != stateBlind {
		t.Errorf("state = %d, want stateBlind — defect (1) needs no modal and no dismissal", state.kind)
	}

	// Exclusion, with a counter that parses fine: the index is still wrong, and
	// it is wrong for every consumer of it, not only this package's stall clock.
	if events.CountsAsAgentActivity("modal_dismissed") {
		t.Error("modal_dismissed still counts as agent activity")
	}
}

// TestBlindAgentsAreNotCountedAsClearedFindings guards a seam between the two
// states restored here: an agent that goes from CONFIRMED to BLIND has not
// recovered, and must not produce an all-clear.
//
// recordCleared keys on the confirmed set, and blind is not in it — so the
// transition emits a cleared event. That is the correct reading of the code as
// it stands, and this test exists so the next person changing recordCleared
// finds the question already asked: the all-clear is honest here because the
// blind ERROR is emitted in the same sample, so the pair reads as "we stopped
// being able to see it", not as "it recovered".
func TestBlindAgentsAreNotCountedAsClearedFindings(t *testing.T) {
	rec := &recorder{}
	blind := false
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		pty := append(noCounterPTY(), []byte("Please run /login\r\n")...)
		if blind {
			pty = noCounterPTY()
		}
		obs := []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, pty, now)}
		return attachEventFallback(obs, fixedEvents(staleIndex("cat-opaque", 6*time.Hour), nil)), validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})
	w.Check(t0)
	if len(rec.ofType(EventFired)) != 1 {
		t.Fatalf("setup: want 1 finding before the marker leaves the ring, got %d", len(rec.ofType(EventFired)))
	}

	blind = true
	w.Check(t0.Add(time.Minute))
	if n := len(rec.ofType(EventError)); n != 1 {
		t.Errorf("wedge_watch_error events = %d, want 1 — losing sight of a reported agent must be "+
			"recorded as blindness, not left implicit in an all-clear", n)
	}
	if n := len(rec.ofType(EventCleared)); n != 1 {
		t.Errorf("wedge_watch_cleared events = %d, want 1 alongside the blind error", n)
	}
}
