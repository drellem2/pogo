package firstturn

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// crew builds a judgeable agent: spawned, addressed, never completed.
func crew(name string, started time.Time, delivered int) Agent {
	return Agent{Name: name, Identity: "crew-" + name, StartedAt: started, Delivered: delivered}
}

// TestDetect_FiresWhenNobodyEverCompletedATurn is the bar this package was
// written to clear. mg-3cbb: "your detector must be shown to FIRE on the
// condition, not merely to compile."
func TestDetect_FiresWhenNobodyEverCompletedATurn(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	now := spawn.Add(45 * time.Minute)
	snap := Snapshot{
		Now:      now,
		Scanned:  5,
		Lookback: DefaultLookback,
		Agents: []Agent{
			crew("mayor", spawn, 4), crew("pm-pogo", spawn, 4), crew("pm-onethird", spawn, 4),
			crew("architect", spawn, 4), crew("pa", spawn, 4),
		},
	}
	rep := Detect(snap, DefaultParams())
	if rep.State != StateDark {
		t.Fatalf("state = %s, want dark — five agents spawned and none ever acked is the whole condition", rep.State)
	}
	if !rep.Fleet {
		t.Error("Fleet = false; every judged agent is a finding, which is what changes the routing")
	}
	if len(rep.Findings) != 5 {
		t.Errorf("findings = %d, want 5", len(rep.Findings))
	}
	if rep.DarkFor != 45*time.Minute {
		t.Errorf("DarkFor = %s, want 45m", rep.DarkFor)
	}
}

// TestDetect_QuietWhenTheAgentActuallyAcked — the other half of a usable
// detector. A green that cannot be distinguished from a dead detector is what
// mg-3cbb is about, so this pins that a completed turn really does clear.
func TestDetect_QuietWhenTheAgentActuallyAcked(t *testing.T) {
	spawn := at("2026-08-11T19:20:37Z")
	now := spawn.Add(45 * time.Minute)
	a := crew("mayor", spawn, 4)
	a.FirstCompletion = spawn.Add(25 * time.Minute)
	rep := Detect(Snapshot{Now: now, Scanned: 1, Agents: []Agent{a}}, DefaultParams())
	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm", rep.State)
	}
	if len(rep.Judged) != 1 {
		t.Errorf("judged = %v, want the agent judged and cleared, not skipped", rep.Judged)
	}
}

// TestDetect_ACompletionBeforeTheSpawnDoesNotCount is the "a spawn is not a
// success" property stated as arithmetic. The 2026-08-11 fleet had completions
// in its events log — thousands of them — all from the incarnation that died at
// 22:20 the previous evening. A detector that matched on "this agent has acked
// at some point" would have read green through the entire outage.
func TestDetect_ACompletionBeforeTheSpawnDoesNotCount(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	a := crew("pm-pogo", spawn, 4)
	a.FirstCompletion = spawn.Add(-6 * time.Hour)
	rep := Detect(Snapshot{Now: spawn.Add(time.Hour), Scanned: 1, Agents: []Agent{a}}, DefaultParams())
	if rep.State != StateDark {
		t.Fatalf("state = %s, want dark — the ack predates this incarnation", rep.State)
	}
}

// TestDetect_TooFreshIsNotAFinding. A crew agent 30 minutes old is inside the
// healthy population's observed range (p50 12.6 min, max 33.7 min) and must not
// be alarmed on.
func TestDetect_TooFreshIsNotAFinding(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	rep := Detect(Snapshot{
		Now: spawn.Add(30 * time.Minute), Scanned: 1,
		Agents: []Agent{crew("mayor", spawn, 3)},
	}, DefaultParams())
	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm at 30m — under the 45m grace", rep.State)
	}
	if len(rep.TooFresh) != 1 {
		t.Errorf("TooFresh = %v, want the agent named as declined-to-judge; an unstated denominator reads as coverage (mg-7a20)", rep.TooFresh)
	}
	if len(rep.Judged) != 0 {
		t.Errorf("judged = %v, want empty — it was not judged", rep.Judged)
	}
}

// TestDetect_AnAgentNothingWasAskedOfIsNotBlamed. "Fires delivered and nothing
// completed" is a finding; "no fires delivered and nothing completed" is
// deaf-watch's finding (no mail loop) and a different remedy. Firing here would
// point the operator at the wrong component.
func TestDetect_AnAgentNothingWasAskedOfIsNotBlamed(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	rep := Detect(Snapshot{
		Now: spawn.Add(3 * time.Hour), Scanned: 1,
		Agents: []Agent{crew("architect", spawn, 1)},
	}, DefaultParams())
	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm — one delivery is not evidence it declined to answer", rep.State)
	}
	if len(rep.NeverAddressed) != 1 {
		t.Errorf("NeverAddressed = %v, want the agent recorded there", rep.NeverAddressed)
	}
}

// TestDetect_BeyondLookbackIsNotJudged. An agent that spawned before the
// evidence window cannot be shown to have completed nothing SINCE ITS SPAWN,
// only nothing recently — and "was alive, went dark" is ackwatch's blackout
// finding. Claiming it here would be asserting more than the read supports.
func TestDetect_BeyondLookbackIsNotJudged(t *testing.T) {
	now := at("2026-08-11T12:00:00Z")
	rep := Detect(Snapshot{
		Now: now, Scanned: 1, Lookback: 48 * time.Hour,
		Agents: []Agent{crew("pa", now.Add(-72*time.Hour), 400)},
	}, DefaultParams())
	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm", rep.State)
	}
	if len(rep.BeyondLookback) != 1 {
		t.Errorf("BeyondLookback = %v, want the agent recorded there", rep.BeyondLookback)
	}
}

// TestDetect_AFailedMeasurementIsBlindNotCalm. The founding error of this whole
// lineage, one level up: a detector that reads green because it could not look.
func TestDetect_AFailedMeasurementIsBlindNotCalm(t *testing.T) {
	rep := Detect(Snapshot{Now: at("2026-08-11T12:00:00Z"), Err: "events log unreadable"}, DefaultParams())
	if rep.State != StateBlind {
		t.Fatalf("state = %s, want blind", rep.State)
	}
	if rep.BlindReason == "" {
		t.Error("BlindReason empty; a blind sample that does not say why is indistinguishable from a quiet one")
	}
}

// TestDetect_OneDarkAgentAmongHealthyPeersIsNotAFleetClaim. Fleet changes the
// ROUTING (escalate immediately, outside the fleet), so the claim must be true
// before it changes who gets woken.
func TestDetect_OneDarkAgentAmongHealthyPeersIsNotAFleetClaim(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	now := spawn.Add(2 * time.Hour)
	healthy := crew("mayor", spawn, 12)
	healthy.FirstCompletion = spawn.Add(10 * time.Minute)
	rep := Detect(Snapshot{
		Now: now, Scanned: 2,
		Agents: []Agent{healthy, crew("pm-pogo", spawn, 12)},
	}, DefaultParams())
	if rep.State != StateDark {
		t.Fatalf("state = %s, want dark", rep.State)
	}
	if rep.Fleet {
		t.Error("Fleet = true with a healthy peer present; that is the per-agent case")
	}
}

// TestDetect_ASingleDarkAgentAloneIsNotAFleet. MinFleetAgents exists so a
// population of one cannot produce the sentence "no crew agent has completed a
// turn" as though it described a fleet.
func TestDetect_ASingleDarkAgentAloneIsNotAFleet(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	rep := Detect(Snapshot{
		Now: spawn.Add(2 * time.Hour), Scanned: 1,
		Agents: []Agent{crew("mayor", spawn, 12)},
	}, DefaultParams())
	if rep.State != StateDark {
		t.Fatalf("state = %s, want dark — it is still a finding, just not a fleet one", rep.State)
	}
	if rep.Fleet {
		t.Error("Fleet = true for a population of one; MinFleetAgents is 2 for a reason")
	}
}

// TestDetect_DarkForIsTheSHORTESTSpan. The fleet has been in this state only for
// as long as its most recently spawned member has; rounding that up would
// overstate the alarm's own evidence.
func TestDetect_DarkForIsTheShortestSpan(t *testing.T) {
	now := at("2026-08-11T12:00:00Z")
	rep := Detect(Snapshot{
		Now: now, Scanned: 2,
		Agents: []Agent{
			crew("mayor", now.Add(-10*time.Hour), 60),
			crew("pa", now.Add(-2*time.Hour), 12),
		},
	}, DefaultParams())
	if rep.DarkFor != 2*time.Hour {
		t.Errorf("DarkFor = %s, want 2h (the most recently spawned dark agent)", rep.DarkFor)
	}
}

// TestGrace_SitsInsideTheMeasuredEmptyBand pins the constant to the sweep that
// justifies it, so a future edit that moves it has to confront the measurement
// rather than the number. See the package header for the sweep.
func TestGrace_SitsInsideTheMeasuredEmptyBand(t *testing.T) {
	const (
		slowestHealthySpawn = 34 * time.Minute  // observed 33.7 min, 67 healthy spawns
		fastestRealOutage   = 150 * time.Minute // observed 150.8 min, 20 outage spawns
	)
	if DefaultGrace <= slowestHealthySpawn {
		t.Errorf("DefaultGrace = %s is at or below the slowest healthy crew spawn observed (%s); it will fire on healthy boots",
			DefaultGrace, slowestHealthySpawn)
	}
	if DefaultGrace >= fastestRealOutage {
		t.Errorf("DefaultGrace = %s is at or above the fastest real outage observed (%s); it would have missed one",
			DefaultGrace, fastestRealOutage)
	}
}
