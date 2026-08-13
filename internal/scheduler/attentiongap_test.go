package scheduler

import (
	"math"
	"testing"
	"time"
)

// The identity is the whole argument (mg-a14c, mg-ddf7). populations.go asserts
// it over EVENTS; these assert the same equality over the persisted COUNTERS,
// which is the surface `pogo schedule list` and `pogo schedule completion` read.
// A future edit that makes the ratio mean something other than an inverted
// attention gap should be found here, by a failing test, rather than in an alert
// nobody trusts.

func TestAttentionGap_IsExactlyTheInverseOfTheRatio(t *testing.T) {
	cases := []struct {
		name       string
		e          Entry
		wantGap    float64
		wantOutstd bool
	}{
		{
			// The ticket's own row. 302 fires, 103 acks: the agent's turns run
			// about three cadence periods, which is what 34% MEANS.
			name:    "pm-pogo as reported",
			e:       Entry{FiresDelivered: 302, FiresCompleted: 103, EverAcked: true},
			wantGap: 302.0 / 103.0,
		},
		{
			// A perfect agent on a cadence it can keep up with.
			name:    "acks every fire",
			e:       Entry{FiresDelivered: 40, FiresCompleted: 40, EverAcked: true},
			wantGap: 1.0,
		},
		{
			// The 2026-08-10 window pm-pogo reported: 13 fires, 8 acks, one in
			// flight. 13/(8+1) = 1.44, i.e. the agent is barely behind — not the
			// "62%, five missed" the ratio alone suggests.
			name:       "with a fire in flight",
			e:          Entry{FiresDelivered: 13, FiresCompleted: 8, EverAcked: true, PendingToken: "f39bae6e"},
			wantGap:    13.0 / 9.0,
			wantOutstd: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.Outstanding(); got != tc.wantOutstd {
				t.Errorf("Outstanding() = %v, want %v", got, tc.wantOutstd)
			}
			gap := tc.e.AttentionGap()
			if math.Abs(gap-tc.wantGap) > 1e-9 {
				t.Fatalf("AttentionGap() = %v, want %v", gap, tc.wantGap)
			}

			// rate == 1/gap - boundary/delivered, to zero error.
			boundary := 0.0
			if tc.e.Outstanding() {
				boundary = 1
			}
			rate := float64(tc.e.FiresCompleted) / float64(tc.e.FiresDelivered)
			resid := rate - (1/gap - boundary/float64(tc.e.FiresDelivered))
			if math.Abs(resid) > 1e-9 {
				t.Errorf("identity residual = %v, want 0 — the ratio has stopped being an inverted attention gap", resid)
			}
		})
	}
}

func TestAttentionGap_NoClosedCycleIsUnmeasured_NotZero(t *testing.T) {
	// EverAcked survives the re-registration that zeroes the counters (mg-00d6),
	// so this state is routine every morning after the nightly redeploy. A gap
	// of 0 must be read by callers as "nothing measured", never rendered as an
	// instantaneous one — which is why renderAckCell omits it.
	e := Entry{FiresDelivered: 5, FiresCompleted: 0, EverAcked: true}
	if got := e.AttentionGap(); got != 0 {
		t.Errorf("AttentionGap() = %v, want 0 for an unmeasured gap", got)
	}
	// Nothing delivered either: still 0, and still not a divide by zero.
	if got := (Entry{EverAcked: true}).AttentionGap(); got != 0 {
		t.Errorf("AttentionGap() on an empty entry = %v, want 0", got)
	}
}

func TestAttentionGap_CannotReach1WhenTurnsOutlastCadence(t *testing.T) {
	// The claim the old COMPLETED column made and could not support. Two fires
	// land inside every turn, so at most one of each pair is ever redeemable;
	// the schedule is pinned at 50% by the delivery interleaving, and no amount
	// of diligence moves it. This is mg-a14c's central fact, as arithmetic.
	perfectButBusy := Entry{FiresDelivered: 100, FiresCompleted: 50, EverAcked: true}
	if gap := perfectButBusy.AttentionGap(); gap != 2.0 {
		t.Fatalf("gap = %v, want 2.0", gap)
	}
	rate := float64(perfectButBusy.FiresCompleted) / float64(perfectButBusy.FiresDelivered)
	if rate >= 1.0 {
		t.Fatal("this fixture is meant to be structurally capped below 1")
	}
	if ceiling := 1 / perfectButBusy.AttentionGap(); math.Abs(ceiling-rate) > 1e-9 {
		t.Errorf("the achievable ceiling (%v) is not the observed rate (%v) — "+
			"the ratio is supposed to BE 1/gap, leaving no room for diligence", ceiling, rate)
	}
}

func TestCompletion_ReportsOutstandingAndMeanGap(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	now := time.Now()

	// Two tracked schedules, one holding a live token; one untracked schedule
	// that must not enter the arithmetic at all.
	s.mu.Lock()
	s.entries[entryKey{Agent: "a", ID: "one"}] = &Entry{
		Agent: "a", ID: "one", Cron: "*/10 * * * *", NextFire: now,
		FiresDelivered: 30, FiresCompleted: 10, EverAcked: true,
	}
	s.entries[entryKey{Agent: "b", ID: "two"}] = &Entry{
		Agent: "b", ID: "two", Cron: "*/10 * * * *", NextFire: now,
		FiresDelivered: 20, FiresCompleted: 4, EverAcked: true,
		PendingToken: "abcd1234", PendingSince: now,
	}
	s.entries[entryKey{Agent: "c", ID: "never"}] = &Entry{
		Agent: "c", ID: "never", Cron: "*/10 * * * *", NextFire: now,
		FiresDelivered: 9,
	}
	s.mu.Unlock()

	stats := s.Completion("", 0)

	if stats.Tracked != 2 {
		t.Fatalf("Tracked = %d, want 2 (the never-acked schedule is UNKNOWN, not failing)", stats.Tracked)
	}
	if stats.FiresDelivered != 50 || stats.FiresCompleted != 14 {
		t.Fatalf("delivered/completed = %d/%d, want 50/14", stats.FiresDelivered, stats.FiresCompleted)
	}
	if stats.Outstanding != 1 {
		t.Errorf("Outstanding = %d, want 1 — the boundary term is a property of when you looked", stats.Outstanding)
	}
	// 50 fires / (14 acks + 1 in flight) = 3.33
	if want := 50.0 / 15.0; math.Abs(stats.MeanGap-want) > 1e-9 {
		t.Errorf("MeanGap = %v, want %v", stats.MeanGap, want)
	}
	// And the same identity holds on the roll-up.
	resid := stats.Ratio - (1/stats.MeanGap - float64(stats.Outstanding)/float64(stats.FiresDelivered))
	if math.Abs(resid) > 1e-9 {
		t.Errorf("fleet identity residual = %v, want 0", resid)
	}
}

func TestCompletion_MeanGapUnmeasuredRatherThanZeroDivide(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	now := time.Now()

	// Tracked (EverAcked survived a re-registration) but no closed cycle and no
	// live token — the state the whole crew is in every morning.
	s.mu.Lock()
	s.entries[entryKey{Agent: "a", ID: "one"}] = &Entry{
		Agent: "a", ID: "one", Cron: "*/10 * * * *", NextFire: now,
		FiresDelivered: 6, FiresCompleted: 0, EverAcked: true,
	}
	s.mu.Unlock()

	stats := s.Completion("", 0)
	if stats.MeanGap != 0 {
		t.Errorf("MeanGap = %v, want 0 (unmeasured) rather than a fabricated gap", stats.MeanGap)
	}
	if stats.Outstanding != 0 {
		t.Errorf("Outstanding = %d, want 0", stats.Outstanding)
	}
}
