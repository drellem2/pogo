package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
	"github.com/drellem2/pogo/internal/turnlog"
)

// mg-7837. `pogo schedule list` told the mayor to act on ⚠ N unacked and gave
// it no way to tell which of three worlds it was in. These pin that the clause
// added next to the marker separates them, and — the part that matters more —
// that it refuses to guess when it cannot measure.
//
// The measured case that produced this: mayor's predeploy-quiesce-mayor read
// `1/7  ⚠ 6 unacked`; the newest fire was delivered 2026-08-19T01:30Z; mayor's
// turnlog records no completed turn between 2026-08-14T08:22Z and
// 2026-08-19T06:53Z. Nobody was there to ack it.

func unackedEntry() scheduler.Entry {
	return scheduler.Entry{
		ID: "predeploy-quiesce-mayor", Agent: "mayor", Cron: "30 2 * * *",
		FiresDelivered: 7, FiresCompleted: 1, EverAcked: true, UnackedStreak: 6,
		PendingToken: "54df645e",
		PendingSince: time.Date(2026, 8, 19, 1, 30, 10, 0, time.UTC),
	}
}

func TestAnnotate_NoTurnsAtAll_ReadsAsDeliveredIntoSilence(t *testing.T) {
	got := annotateUnacked(unackedEntry(), unackedTurnEvidence{
		Known: true, Settled: true, Grace: 3 * time.Hour,
	})
	low := strings.ToLower(got)
	if !strings.Contains(low, "no turn") || !strings.Contains(low, "silence") {
		t.Errorf("the clause does not say the fire went into silence: %q", got)
	}
	if strings.Contains(low, "did not ack") {
		t.Errorf("an absent agent must not be described as one that failed to ack: %q", got)
	}
}

func TestAnnotate_TurnsAroundTheFire_IsTheActionableReading(t *testing.T) {
	// The only one of the three readings that makes the streak causal: the
	// agent was completing turns in the window the token was redeemable in and
	// did not redeem it.
	got := annotateUnacked(unackedEntry(), unackedTurnEvidence{
		Known: true, Settled: true, AtFire: 14, Since: 40, Grace: 3 * time.Hour,
	})
	if !strings.Contains(got, "14 turn") {
		t.Errorf("the count that makes this actionable is missing: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "did not ack") {
		t.Errorf("the clause does not name the actionable reading: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "silence") {
		t.Errorf("a live agent's dropped ack must not read as silence: %q", got)
	}
}

func TestAnnotate_AgentReturnedAfterTheWindow_IsStillSilenceAtFireTime(t *testing.T) {
	// mayor's actual state on 2026-08-19 16:20Z: zero turns in the three hours
	// after the 01:30Z fire, and hundreds since it came back at 06:53Z. A clause
	// anchored only on "has this agent turned recently" would call this live and
	// re-tell the exact lie the marker already tells.
	got := annotateUnacked(unackedEntry(), unackedTurnEvidence{
		Known: true, Settled: true, AtFire: 0, Since: 218, Grace: 3 * time.Hour,
	})
	low := strings.ToLower(got)
	if !strings.Contains(low, "silence") {
		t.Errorf("an agent that was absent at fire time must still read as silence: %q", got)
	}
	if !strings.Contains(low, "has turned since") {
		t.Errorf("the clause hides that the agent has since returned, which is what a reader does next: %q", got)
	}
}

func TestAnnotate_NoTurnlog_SaysUnavailable_NeverAbsence(t *testing.T) {
	// Polecats write no turnlog at all. Rendering "no turns" over them would be
	// the could-not-look/looked-and-saw-nothing collapse, rebuilt one layer down
	// from the counter it is meant to disambiguate.
	got := annotateUnacked(unackedEntry(), unackedTurnEvidence{Grace: 3 * time.Hour})
	low := strings.ToLower(got)
	if !strings.Contains(low, "unavailable") {
		t.Errorf("a missing turnlog must read as unavailable: %q", got)
	}
	if strings.Contains(low, "silence") || strings.Contains(low, "did not ack") {
		t.Errorf("a missing instrument must not produce a verdict about the agent: %q", got)
	}
}

func TestAnnotate_UnsettledWindow_DoesNotAnnounceSilence(t *testing.T) {
	// A fire delivered ten minutes ago has zero turns after it because three
	// hours have not passed, not because nobody is there.
	got := annotateUnacked(unackedEntry(), unackedTurnEvidence{
		Known: true, Settled: false, Grace: 3 * time.Hour,
	})
	if strings.Contains(strings.ToLower(got), "silence") {
		t.Errorf("an unelapsed window announced silence: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "too early") {
		t.Errorf("the clause does not say why it is withholding a reading: %q", got)
	}
}

func TestAnnotate_NoOutstandingFire_SaysNothing(t *testing.T) {
	e := unackedEntry()
	e.PendingToken = ""
	if got := annotateUnacked(e, unackedTurnEvidence{Known: true, Settled: true}); got != "" {
		t.Errorf("with no fire to anchor a window to there is nothing to say, got %q", got)
	}
	e = unackedEntry()
	e.PendingSince = time.Time{}
	if got := annotateUnacked(e, unackedTurnEvidence{Known: true, Settled: true}); got != "" {
		t.Errorf("a record with no delivery time must not have one invented for it, got %q", got)
	}
}

func TestAnnotate_SpeaksAboutOneFire_NotTheWholeStreak(t *testing.T) {
	// The entry stores a delivery time for the newest fire only. Six unacked
	// fires with one measured window is one measurement, and the clause has to
	// say so or it is a broader claim than the data supports.
	e := unackedEntry()
	for _, ev := range []unackedTurnEvidence{
		{Known: true, Settled: true, Grace: 3 * time.Hour},
		{Known: true, Settled: true, AtFire: 5, Grace: 3 * time.Hour},
		{Known: true, Settled: true, Since: 9, Grace: 3 * time.Hour},
	} {
		got := annotateUnacked(e, ev)
		if !strings.Contains(got, "newest fire") {
			t.Errorf("clause does not scope itself to the newest fire: %q", got)
		}
		if strings.Contains(got, "6 fires") || strings.Contains(got, "these fires") {
			t.Errorf("clause claims the whole streak from one measured window: %q", got)
		}
	}
}

func TestAckCell_ClauseOnlyAppearsWithTheWarning(t *testing.T) {
	e := unackedEntry()
	e.UnackedStreak = 1
	got := renderAckCell(e, unackedTurnEvidence{Known: true, Settled: true, Grace: 3 * time.Hour})
	if strings.Contains(got, "silence") {
		t.Errorf("a streak below the stall threshold is a turn in progress and gets no clause: %q", got)
	}

	got = renderAckCell(unackedEntry(), unackedTurnEvidence{Known: true, Settled: true, Grace: 3 * time.Hour})
	if !strings.Contains(got, "⚠ 6 unacked") {
		t.Fatalf("the marker was lost: %q", got)
	}
	if strings.Index(got, "silence") < strings.Index(got, "⚠") {
		t.Errorf("the clause must follow the marker it qualifies: %q", got)
	}
}

// --- gatherTurnEvidence: the I/O half, over a stubbed window reader ---

func withStubWindow(t *testing.T, fn func(agent string, from, to time.Time) (int, error)) {
	t.Helper()
	prev := turnWindow
	turnWindow = fn
	t.Cleanup(func() { turnWindow = prev })
}

func TestGatherTurnEvidence_SplitsTheWindowAtTheGraceBoundary(t *testing.T) {
	e := unackedEntry()
	grace := turnlog.DefaultMaxAge
	end := e.PendingSince.Add(grace)
	now := e.PendingSince.Add(15 * time.Hour)

	var asked [][2]time.Time
	withStubWindow(t, func(agent string, from, to time.Time) (int, error) {
		asked = append(asked, [2]time.Time{from, to})
		if from.Equal(e.PendingSince) {
			return 0, nil
		}
		return 218, nil
	})

	ev := gatherTurnEvidence(e, now)
	if !ev.Known || !ev.Settled {
		t.Fatalf("ev = %+v, want a known, settled reading", ev)
	}
	if ev.AtFire != 0 || ev.Since != 218 {
		t.Errorf("ev = %+v, want AtFire 0 / Since 218", ev)
	}
	if len(asked) != 2 {
		t.Fatalf("asked %d windows, want 2", len(asked))
	}
	if !asked[0][1].Equal(end) {
		t.Errorf("first window ends at %s, want the grace boundary %s", asked[0][1], end)
	}
	// Disjoint: a turn landing exactly on the boundary must not be counted in
	// both halves. The turnlog is second-resolution, so the second window
	// starts one second later.
	if !asked[1][0].After(asked[0][1]) {
		t.Errorf("windows overlap: [%s,%s] then [%s,%s]", asked[0][0], asked[0][1], asked[1][0], asked[1][1])
	}
}

func TestGatherTurnEvidence_UnelapsedWindowIsNotSettled(t *testing.T) {
	e := unackedEntry()
	withStubWindow(t, func(string, time.Time, time.Time) (int, error) { return 0, nil })
	ev := gatherTurnEvidence(e, e.PendingSince.Add(10*time.Minute))
	if ev.Settled {
		t.Errorf("ev.Settled true ten minutes into a %s window: %+v", turnlog.DefaultMaxAge, ev)
	}
	if ev.Since != 0 {
		t.Errorf("a since-count was computed over an interval that has not begun: %+v", ev)
	}
}

func TestGatherTurnEvidence_ReadFailureLeavesItUnknown(t *testing.T) {
	e := unackedEntry()
	withStubWindow(t, func(string, time.Time, time.Time) (int, error) {
		return 0, errTurnlogMissingForTest
	})
	if ev := gatherTurnEvidence(e, e.PendingSince.Add(15*time.Hour)); ev.Known {
		t.Errorf("a failed read produced a known reading: %+v", ev)
	}
}

func TestGatherTurnEvidence_SkipsTheReadWhenThereIsNoWarningToQualify(t *testing.T) {
	// The clause only ever renders next to ⚠, so a row without one must not pay
	// for a turnlog read — this command lists every schedule on the host.
	e := unackedEntry()
	e.UnackedStreak = 1
	called := false
	withStubWindow(t, func(string, time.Time, time.Time) (int, error) {
		called = true
		return 0, nil
	})
	gatherTurnEvidence(e, e.PendingSince.Add(15*time.Hour))
	if called {
		t.Error("read a turnlog for a row that will not carry a clause")
	}
}

var errTurnlogMissingForTest = errStub("turnlog: no such file")

type errStub string

func (e errStub) Error() string { return string(e) }

// --- the help text that carried the false claim ---

func TestScheduleCompletionHelp_DoesNotClaimTheStreakSeparatesBusyFromDead(t *testing.T) {
	// The sentence mg-7837 was filed against, verbatim from the shipped help:
	// "the number that separates a busy agent from a dead one is the unacked
	// streak". It does not. Both worlds produce an unbounded streak.
	low := strings.ToLower(scheduleCompletionLong)
	if strings.Contains(low, "separates a busy agent from a dead one is the unacked") {
		t.Errorf("the false claim is back in the shipped help:\n%s", scheduleCompletionLong)
	}
	if !strings.Contains(scheduleCompletionLong, "THE STREAK DOES NOT SEPARATE A BUSY AGENT FROM A DEAD ONE") {
		t.Errorf("the correction is not stated where the claim was:\n%s", scheduleCompletionLong)
	}
	// The correction is only useful if it names where to look instead.
	if !strings.Contains(low, "turnlog") {
		t.Errorf("the correction withdraws a claim and offers no discriminator:\n%s", scheduleCompletionLong)
	}
}

func TestScheduleCompletionHelp_KeepsWhatWasAlreadyTrue(t *testing.T) {
	// A correction that quietly drops the surrounding reasoning is a second
	// defect. mg-a14c's ceiling and the ratio-is-a-turn-length reading are both
	// still correct and still the reason nobody should alarm on the percentage.
	for _, want := range []string{
		"WHAT THE RATIO IS NOT (mg-a14c)",
		"100% is not available",
		"reciprocal of the mean",
		"UNKNOWN, not failing",
	} {
		if !strings.Contains(scheduleCompletionLong, want) {
			t.Errorf("the correction dropped %q from the help text", want)
		}
	}
}
