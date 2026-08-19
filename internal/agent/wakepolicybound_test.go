package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The BOUND on the wake-cycle policy (mg-3a8a).
//
// These tests are written against evaluateWake with a SYNTHETIC now rather than
// against the clock, because the behaviour being pinned is measured in hours:
// the incident that filed this ran 143 consecutive suppressions across 106
// hours. A test that waited for its own bound could only ever pin a bound small
// enough to wait for, which is the one size the defect never had.

// newBoundedPolicyAgent is newPolicyAgent with an explicit bound, so a test can
// state the ceiling it is proving instead of inheriting the production value.
func newBoundedPolicyAgent(t *testing.T, bound time.Duration) *Agent {
	t.Helper()
	a := newPolicyAgent(t)
	a.wakeSuppressionBound = bound
	return a
}

// silentSince puts the agent in the state the incident was in: a wake was
// delivered at `woke`, the only output since is that wake's own echo, and
// nothing has happened since. Rule 1 suppresses every wake from here, forever,
// unless something bounds it.
func silentSince(a *Agent, woke time.Time) {
	a.recordWake(woke)
	a.outputBuf.setLastWriteForTest(woke.Add(10 * time.Millisecond))
}

// TestWakeSuppressionBoundReleasesTheSilenceLatch is the regression test for
// mg-3a8a proper. Rule 1's suppressing condition — "no PTY output since we woke
// it" — is exactly the condition a wake exists to break, so without a bound the
// first suppression is permanent. Here the run must release.
func TestWakeSuppressionBoundReleasesTheSilenceLatch(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	const bound = 15 * time.Minute
	a := newBoundedPolicyAgent(t, bound)

	t0 := time.Now()
	silentSince(a, t0)

	// Fires arrive on the `*/10` mail-check cadence, as crew-pa's did.
	d1 := a.evaluateWake(t0.Add(10 * time.Minute))
	if !d1.suppressed() || d1.Rule != RuleWakeSilenceOnce {
		t.Fatalf("first fire of the run should be suppressed by rule 1, got %+v", d1)
	}
	if d1.Count != 1 || d1.Age != 0 {
		t.Errorf("first suppression of a run: count/age = %d/%s, want 1/0s", d1.Count, d1.Age)
	}

	// Still inside the bound: the debounce is doing its job.
	d2 := a.evaluateWake(t0.Add(20 * time.Minute))
	if !d2.suppressed() {
		t.Fatalf("a run 10m old must still be suppressed under a %s bound, got %+v", bound, d2)
	}
	if d2.Count != 2 {
		t.Errorf("consecutive count = %d, want 2", d2.Count)
	}

	// Past the bound: the wake goes out, over rule 1's decline.
	d3 := a.evaluateWake(t0.Add(30 * time.Minute))
	if d3.suppressed() {
		t.Fatalf("a run older than the %s bound must release the wake, got %+v", bound, d3)
	}
	if !d3.Released {
		t.Error("the release must be marked as such — it is the only record the bound fired")
	}
	if d3.Rule != RuleWakeSilenceOnce {
		t.Errorf("a release must still name the rule it overrode, got %q", d3.Rule)
	}
	if !strings.Contains(d3.Detail, "released by the wake-suppression bound") {
		t.Errorf("detail should say the bound released it, got %q", d3.Detail)
	}

	// And the shape the incident actually had: the run does not merely release
	// once at 20 minutes and re-latch. At 106 hours it is still releasing.
	d4 := a.evaluateWake(t0.Add(106 * time.Hour))
	if d4.suppressed() {
		t.Fatalf("106h into a silence the wake must not be suppressed, got %+v", d4)
	}
}

// TestWakeSuppressionBoundReleasesTheLimitEpisode is the sibling-rule half of
// mg-3a8a, which asked explicitly whether the same unbounded shape exists in the
// other wake rules. It does: rule 2's per-agent flag is cleared by
// internal/claude's modal hook only when the agent's event log ADVANCES, so an
// agent that never speaks again holds the flag — and the flag suppresses the
// wakes that would make it speak. The bound sits above both rules, so this is
// covered by construction rather than by a second fix.
func TestWakeSuppressionBoundReleasesTheLimitEpisode(t *testing.T) {
	const bound = 15 * time.Minute
	a := newBoundedPolicyAgent(t, bound)
	withLimitEpisodeQuery(t, func() (bool, string) {
		return true, "usage-limit episode ep-1 open since 2026-08-14T08:22:00Z"
	})

	t0 := time.Now()
	if d := a.evaluateWake(t0); !d.suppressed() || d.Rule != RuleLimitEpisode {
		t.Fatalf("an open episode should suppress the first wake, got %+v", d)
	}
	// The episode never closes and the agent never speaks.
	d := a.evaluateWake(t0.Add(20 * time.Minute))
	if d.suppressed() {
		t.Fatalf("an episode that outlives the bound must not keep suppressing, got %+v", d)
	}
	if !d.Released || d.Rule != RuleLimitEpisode {
		t.Errorf("released decision should name rule 2, got %+v", d)
	}
}

// TestWakeSuppressionBoundNeedsNoPriorWake pins the reason the run has its own
// clock instead of reusing lastWakeAt. Rule 2 declines wakes for an agent this
// process has NEVER woken, so a bound keyed on the last delivery would read as
// "zero time has passed" forever — or release instantly, which is worse.
func TestWakeSuppressionBoundNeedsNoPriorWake(t *testing.T) {
	a := newBoundedPolicyAgent(t, 15*time.Minute)
	withLimitEpisodeQuery(t, func() (bool, string) { return true, "episode open" })

	t0 := time.Now()
	a.wakeMu.Lock()
	neverWoken := a.lastWakeAt.IsZero()
	a.wakeMu.Unlock()
	if !neverWoken {
		t.Fatal("precondition: this agent must never have been woken")
	}
	if d := a.evaluateWake(t0); !d.suppressed() {
		t.Fatalf("the first decline of a run must decline, got %+v", d)
	}
	if d := a.evaluateWake(t0.Add(16 * time.Minute)); !d.Released {
		t.Fatalf("a run with no prior wake must still be bounded, got %+v", d)
	}
}

// TestWakeSuppressionBoundKeepsTheDebounce is the control, and it is the
// measured one: over the same window that crew-pa accumulated 143 suppressions
// at 100h+, crew-mayor accumulated its own with the age reading "0s" every time
// — two scheduler fires landing in one wake cycle, which is precisely what rule
// 1 is for. The bound must not touch that case.
func TestWakeSuppressionBoundKeepsTheDebounce(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	a := newBoundedPolicyAgent(t, 15*time.Minute)

	t0 := time.Now()
	silentSince(a, t0)

	// Two fires in the same instant, and a third a minute later.
	for i, at := range []time.Time{t0, t0, t0.Add(time.Minute)} {
		if d := a.evaluateWake(at); !d.suppressed() {
			t.Fatalf("fire %d inside the debounce window must be suppressed, got %+v", i, d)
		}
	}
}

// TestWakeSuppressionRunEndsOnlyOnDelivery pins the half that keeps the bound
// from cancelling itself. Once past the bound, EVERY attempt is released until
// one actually lands: a wake that failed on a busy PTY wrote nothing, so
// treating the release as spent would buy another full bound period of silence
// — the defect in miniature.
func TestWakeSuppressionRunEndsOnlyOnDelivery(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	a := newBoundedPolicyAgent(t, 15*time.Minute)

	t0 := time.Now()
	silentSince(a, t0)

	if d := a.evaluateWake(t0.Add(time.Minute)); !d.suppressed() {
		t.Fatalf("run should open with a suppression, got %+v", d)
	}
	if d := a.evaluateWake(t0.Add(30 * time.Minute)); !d.Released {
		t.Fatalf("run past the bound should release, got %+v", d)
	}
	// That release was not delivered (busy PTY): nothing called recordWake.
	if d := a.evaluateWake(t0.Add(31 * time.Minute)); !d.Released {
		t.Fatalf("an undelivered release must be re-offered, not re-latched, got %+v", d)
	}

	// Now one lands. The run is over, and the debounce starts again from zero —
	// so a permanently silent agent gets at most one wake per bound period, not
	// a wake per fire.
	a.recordWake(t0.Add(31 * time.Minute))
	a.outputBuf.setLastWriteForTest(t0.Add(31 * time.Minute).Add(10 * time.Millisecond))

	d := a.evaluateWake(t0.Add(35 * time.Minute))
	if !d.suppressed() {
		t.Fatalf("a delivered wake restarts the debounce, got %+v", d)
	}
	if d.Count != 1 || d.Age != 0 {
		t.Errorf("delivery must reset the run: count/age = %d/%s, want 1/0s", d.Count, d.Age)
	}
}

// TestWakeSuppressionRunEndsWhenTheRulesStopDeclining covers the other way a run
// ends: the agent spoke, so rule 1 permits the wake on its own merits and the
// accumulated run must not be carried into the next silence.
func TestWakeSuppressionRunEndsWhenTheRulesStopDeclining(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	a := newBoundedPolicyAgent(t, 15*time.Minute)

	t0 := time.Now()
	silentSince(a, t0)
	if d := a.evaluateWake(t0.Add(time.Minute)); !d.suppressed() {
		t.Fatalf("expected a suppression to open the run, got %+v", d)
	}

	// The agent ran a turn, well after the wake settled.
	a.outputBuf.setLastWriteForTest(t0.Add(5 * time.Minute))
	if d := a.evaluateWake(t0.Add(6 * time.Minute)); d.suppressed() || d.Released {
		t.Fatalf("a broken silence is wakeable on its own merits, got %+v", d)
	}

	// Next silence: a fresh run, not a continuation of the old one.
	a.recordWake(t0.Add(6 * time.Minute))
	a.outputBuf.setLastWriteForTest(t0.Add(6 * time.Minute).Add(10 * time.Millisecond))
	d := a.evaluateWake(t0.Add(7 * time.Minute))
	if !d.suppressed() {
		t.Fatalf("new silence should debounce, got %+v", d)
	}
	if d.Count != 1 {
		t.Errorf("run count carried across a broken silence: %d, want 1", d.Count)
	}
}

// TestWakeSuppressionBoundSurvivesABackwardsClock guards the one input the bound
// does not control. now comes from time.Now() at the call site, and a wall-clock
// step backwards (NTP, host wake) must not read as a negative age — which would
// compare below every bound and extend a run for as long as the step lasted.
func TestWakeSuppressionBoundSurvivesABackwardsClock(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	a := newBoundedPolicyAgent(t, 15*time.Minute)

	t0 := time.Now()
	silentSince(a, t0)
	if d := a.evaluateWake(t0.Add(time.Hour)); !d.suppressed() {
		t.Fatalf("expected the run to open, got %+v", d)
	}
	// The clock steps back an hour.
	d := a.evaluateWake(t0)
	if !d.suppressed() {
		t.Fatalf("a backwards step should not release early, got %+v", d)
	}
	if d.Age < 0 {
		t.Errorf("age must never be negative, got %s", d.Age)
	}
	// And forward progress from the stepped clock still reaches the bound.
	if d := a.evaluateWake(t0.Add(2 * time.Hour)); !d.Released {
		t.Fatalf("the run must still be bounded after a clock step, got %+v", d)
	}
}

// TestNudgeSuppressedCarriesTheRunFields pins the observability half. mg-3a8a's
// load-bearing number was "143 consecutive over 106h", and reading it required
// regexing an age out of an English sentence — which is how a run past 100 hours
// stayed something you had to already suspect in order to see.
func TestNudgeSuppressedCarriesTheRunFields(t *testing.T) {
	logPath := useTempEventLog(t)
	a := newBoundedPolicyAgent(t, 15*time.Minute)
	withLimitEpisodeQuery(t, func() (bool, string) { return true, "episode ep-1 open" })

	if err := a.NudgeWake("wake up", NudgeImmediate, time.Second, "tok-1"); !errors.Is(err, ErrWakeSuppressed) {
		t.Fatalf("expected a suppression, got %v", err)
	}
	ev := findEvent(readEventLines(t, logPath), EventNudgeSuppressed, "pogod")
	if ev == nil {
		t.Fatal("no nudge_suppressed event recorded")
	}
	details, _ := ev["details"].(map[string]any)
	if details == nil {
		t.Fatal("event has no details")
	}
	if got, ok := details["consecutive"].(float64); !ok || int(got) != 1 {
		t.Errorf("details.consecutive = %v, want 1", details["consecutive"])
	}
	if _, ok := details["suppressed_for_seconds"].(float64); !ok {
		t.Errorf("details.suppressed_for_seconds missing or not a number: %v", details["suppressed_for_seconds"])
	}
}

// TestNudgeWakeReleaseReachesThePTY drives the whole thing on a live PTY: a
// wake the bound released must actually be WRITTEN, and must leave a
// wake_suppression_released record. A release that only changed a return value
// would leave the bound exactly where the defect was — a mechanism whose
// operation cannot be observed, and so is indistinguishable from one that was
// never wired up.
func TestNudgeWakeReleaseReachesThePTY(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	logPath := useTempEventLog(t)

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "wake-bound-e2e",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// A bound short enough to cross between two calls, so the test measures the
	// release rather than sleeping out the production ceiling.
	a.wakeMu.Lock()
	a.wakeSuppressionBound = 20 * time.Millisecond
	a.wakeMu.Unlock()

	if err := a.NudgeWake("first wake", NudgeImmediate, 5*time.Second, ""); err != nil {
		t.Fatalf("first wake should be delivered: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Same silence (only the echo came back), so rule 1 declines — and this is
	// the run's first suppression, which always declines.
	if err := a.NudgeWake("second wake", NudgeImmediate, 5*time.Second, ""); !errors.Is(err, ErrWakeSuppressed) {
		t.Fatalf("second wake of the same silence should be suppressed, got %v", err)
	}

	// The run is now older than the bound: the next wake is released.
	time.Sleep(50 * time.Millisecond)
	if err := a.NudgeWake("released wake", NudgeImmediate, 5*time.Second, ""); err != nil {
		t.Fatalf("a wake past the bound should be delivered, got %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if out := string(a.RecentOutput(8192)); !strings.Contains(out, "released wake") {
		t.Errorf("the released wake never reached the PTY, output: %q", out)
	}

	ev := waitForEvent(t, logPath, EventWakeSuppressionReleased, "pogod", 2*time.Second)
	if ev == nil {
		t.Fatal("no wake_suppression_released event recorded")
	}
	details, _ := ev["details"].(map[string]any)
	if details["rule"] != RuleWakeSilenceOnce {
		t.Errorf("release should name the overridden rule, got %v", details["rule"])
	}
	if _, ok := details["bound_seconds"].(float64); !ok {
		t.Errorf("release should record the bound it crossed, got %v", details["bound_seconds"])
	}

	// A delivered wake ends the run, so the wake right after it is debounced
	// again: the bound releases at most one wake per period.
	if err := a.NudgeWake("fourth wake", NudgeImmediate, 5*time.Second, ""); !errors.Is(err, ErrWakeSuppressed) {
		t.Fatalf("the wake after a release should be debounced again, got %v", err)
	}
}

// TestWakeSuppressionReleasedIsDocumented. The catalog in docs/event-log.md is
// the only place a reader who finds one of these lines can look it up, and this
// event is the sole outward sign the bound exists — an undocumented one puts it
// back in the position the defect was in. Same guard as internal/synthwatch's
// TestNewEventTypesAreDocumented.
func TestWakeSuppressionReleasedIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "event-log.md"))
	if err != nil {
		t.Skipf("event-log.md not readable from here: %v", err)
	}
	for _, want := range []string{
		EventWakeSuppressionReleased,
		"bound_seconds",
		"consecutive",
		"suppressed_for_seconds",
	} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("%q is emitted but absent from docs/event-log.md", want)
		}
	}
}
