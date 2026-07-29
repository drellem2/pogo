package agent

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// setLastWriteForTest backdates (or forward-dates) the buffer's last-write
// stamp. The wake policy's whole question is WHEN the agent last spoke relative
// to when we last woke it, and real writes can only ever land at time.Now() —
// so without this seam every rule-1 case would have to be built out of sleeps.
func (r *RingBuffer) setLastWriteForTest(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastWrite = at
}

// newPolicyAgent builds the smallest Agent the wake policy can be evaluated
// against: a name, a ring buffer, and a nudge profile. No PTY — evaluateWake
// never touches one, which is the point of keeping the decision separable from
// the delivery.
func newPolicyAgent(t *testing.T) *Agent {
	t.Helper()
	return &Agent{
		Name:      "policy-test",
		Type:      TypePolecat,
		outputBuf: NewRingBuffer(1024),
		nudge:     DefaultNudgeProfile,
		done:      make(chan struct{}),
	}
}

// withLimitEpisodeQuery installs a query for the duration of one test and
// restores the absent state afterwards. The seam is package-level, so leaking
// one into a sibling test would make an unrelated wake suppress.
func withLimitEpisodeQuery(t *testing.T, q LimitEpisodeQuery) {
	t.Helper()
	SetLimitEpisodeQuery(q)
	t.Cleanup(func() { SetLimitEpisodeQuery(nil) })
}

// TestWakePolicyPassesWhenNothingSuppresses is the baseline control. A fresh
// agent with no limit state and no prior wake must be woken — a policy observed
// only suppressing has not been observed deciding.
func TestWakePolicyPassesWhenNothingSuppresses(t *testing.T) {
	a := newPolicyAgent(t)
	if d := a.evaluateWake(time.Now()); d.suppressed() {
		t.Fatalf("fresh agent should be wakeable, got suppression %q: %s", d.Rule, d.Detail)
	}
}

// TestWakePolicyRule2FleetEpisode proves rule 2 in BOTH directions against the
// fleet query: it suppresses while an episode is open, and it declines to
// suppress the moment the query says the episode is not open.
func TestWakePolicyRule2FleetEpisode(t *testing.T) {
	a := newPolicyAgent(t)

	open := true
	withLimitEpisodeQuery(t, func() (bool, string) {
		return open, "usage-limit episode ep-1 open since 2026-07-29T00:00:00Z (3 agent(s) rate-limited)"
	})

	// Suppress case.
	d := a.evaluateWake(time.Now())
	if !d.suppressed() {
		t.Fatal("wake inside an open fleet limit episode should be suppressed")
	}
	if d.Rule != RuleLimitEpisode {
		t.Errorf("rule = %q, want %q", d.Rule, RuleLimitEpisode)
	}
	if !strings.Contains(d.Detail, "ep-1") {
		t.Errorf("detail should name the episode, got %q", d.Detail)
	}

	// Pass-through case: same agent, same query function, episode closed.
	open = false
	if d := a.evaluateWake(time.Now()); d.suppressed() {
		t.Fatalf("wake outside an episode should not be suppressed, got %q: %s", d.Rule, d.Detail)
	}
}

// TestWakePolicyRule2NoQueryInstalled pins the safe default for a process that
// never installed the query (a bare registry, a library caller): absent
// knowledge, behave exactly as before the policy existed.
func TestWakePolicyRule2NoQueryInstalled(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	a := newPolicyAgent(t)
	if d := a.evaluateWake(time.Now()); d.suppressed() {
		t.Fatalf("no query installed must never suppress, got %q: %s", d.Rule, d.Detail)
	}
}

// TestWakePolicyRule2PerAgentFlag proves rule 2 against the per-agent condition
// the modal watcher already maintains, in both directions. Reading it here is
// the pull: SetRateLimited records a condition ON the agent, and the wake
// decision asks for it.
func TestWakePolicyRule2PerAgentFlag(t *testing.T) {
	a := newPolicyAgent(t)

	a.SetRateLimited(true)
	d := a.evaluateWake(time.Now())
	if !d.suppressed() {
		t.Fatal("wake into a rate-limited agent should be suppressed")
	}
	if d.Rule != RuleLimitEpisode {
		t.Errorf("rule = %q, want %q", d.Rule, RuleLimitEpisode)
	}

	a.SetRateLimited(false)
	if d := a.evaluateWake(time.Now()); d.suppressed() {
		t.Fatalf("cleared agent should be wakeable, got %q: %s", d.Rule, d.Detail)
	}
}

// TestWakePolicyRule1SuppressesSecondWakeOfSameSilence is rule 1's suppress
// case: the agent was woken, produced nothing but the echo of that wake, and
// so is still in the silence we already spoke into.
func TestWakePolicyRule1SuppressesSecondWakeOfSameSilence(t *testing.T) {
	a := newPolicyAgent(t)
	now := time.Now()

	// A wake was delivered a minute ago, and the only output since is the
	// echo it produced — well inside the settle window.
	a.recordWake(now.Add(-time.Minute))
	a.outputBuf.setLastWriteForTest(now.Add(-time.Minute).Add(10 * time.Millisecond))

	d := a.evaluateWake(now)
	if !d.suppressed() {
		t.Fatal("second wake of the same unbroken silence should be suppressed")
	}
	if d.Rule != RuleWakeSilenceOnce {
		t.Errorf("rule = %q, want %q", d.Rule, RuleWakeSilenceOnce)
	}
	if !strings.Contains(d.Detail, "no PTY output") {
		t.Errorf("detail should say why, got %q", d.Detail)
	}
}

// TestWakePolicyRule1PassesWhenTheSilenceBroke is rule 1's pass-through case:
// the agent answered the last wake with a real turn, so the silence it is in
// now is a DIFFERENT one and is owed its own wake.
func TestWakePolicyRule1PassesWhenTheSilenceBroke(t *testing.T) {
	a := newPolicyAgent(t)
	now := time.Now()

	a.recordWake(now.Add(-time.Minute))
	// Output well after the wake settled: the agent ran a turn.
	a.outputBuf.setLastWriteForTest(now.Add(-10 * time.Second))

	if d := a.evaluateWake(now); d.suppressed() {
		t.Fatalf("a broken silence is a new silence and must be wakeable, got %q: %s", d.Rule, d.Detail)
	}
}

// TestWakePolicyRule1PermitsTheFirstWake pins the half of rule 1 that is easy
// to lose: "wake each silence ONCE" is a permission as well as a limit.
func TestWakePolicyRule1PermitsTheFirstWake(t *testing.T) {
	a := newPolicyAgent(t)
	// Silent for an hour, never woken by us.
	a.outputBuf.setLastWriteForTest(time.Now().Add(-time.Hour))
	if d := a.evaluateWake(time.Now()); d.suppressed() {
		t.Fatalf("the first wake of a silence must fire, got %q: %s", d.Rule, d.Detail)
	}
}

// TestWakePolicyEchoDoesNotBreakTheSilence is the regression guard for the one
// mechanism that would quietly make rule 1 vacuous. Bytes written to the PTY
// master come back through readOutput into outputBuf, so a naive "has it
// written anything since we woke it?" answers YES for every wake — including a
// wake that landed on a wedged harness. If someone drops the settle window,
// this test fails and the comment on sameSilenceAsLastWake explains why.
func TestWakePolicyEchoDoesNotBreakTheSilence(t *testing.T) {
	a := newPolicyAgent(t)
	now := time.Now()
	woke := now.Add(-30 * time.Second)

	a.recordWake(woke)
	// Echo lands a hair AFTER the wake — strictly later than lastWakeAt, but
	// inside the IdleThreshold settle window.
	a.outputBuf.setLastWriteForTest(woke.Add(a.nudge.IdleThreshold / 2))

	if d := a.evaluateWake(now); !d.suppressed() {
		t.Fatal("the wake's own echo must not count as the agent breaking its silence")
	}
}

// TestWakePolicyRule2BeatsRule1 pins the reporting order. When both rules hold,
// the recorded reason is the limit episode: "it cannot act" is a stronger and
// more actionable statement than "we already spoke into this silence".
func TestWakePolicyRule2BeatsRule1(t *testing.T) {
	a := newPolicyAgent(t)
	now := time.Now()
	a.recordWake(now.Add(-time.Minute))
	a.outputBuf.setLastWriteForTest(now.Add(-time.Minute))
	a.SetRateLimited(true)

	d := a.evaluateWake(now)
	if d.Rule != RuleLimitEpisode {
		t.Errorf("rule = %q, want the limit-episode rule to be reported when both hold", d.Rule)
	}
}

// TestNudgeWakeSuppressionIsAnError pins the contract callers depend on: a
// suppressed wake wrote nothing, so it must NOT report success. Reporting
// delivery for an undelivered message is its own defect, and the stall nudger
// and scheduler deliverer both key their durable-mail fallback off this error.
func TestNudgeWakeSuppressionIsAnError(t *testing.T) {
	a := newPolicyAgent(t)
	withLimitEpisodeQuery(t, func() (bool, string) { return true, "episode ep-9 open" })

	err := a.NudgeWake("wake up", NudgeImmediate, time.Second, "tok-1")
	if err == nil {
		t.Fatal("a suppressed wake must return an error, not nil")
	}
	if !errors.Is(err, ErrWakeSuppressed) {
		t.Fatalf("error should wrap ErrWakeSuppressed, got %v", err)
	}
	if !strings.Contains(err.Error(), RuleLimitEpisode) {
		t.Errorf("error should name the rule, got %v", err)
	}
	// And it stopped before the PTY: this agent has no master, so a wake that
	// reached delivery would have failed with a different error entirely.
	if strings.Contains(err.Error(), "no PTY") {
		t.Errorf("suppression should decide before touching the PTY, got %v", err)
	}
}

// TestNudgeWakeEndToEnd drives a real spawned agent through both halves of rule
// 1 on a live PTY: the first wake lands, an immediate second wake of the same
// silence is declined, and a wake after the agent has spoken again lands.
//
// The agent is `cat`, so everything written to the PTY comes straight back —
// the echo behaviour the settle window exists to see through.
func TestNudgeWakeEndToEnd(t *testing.T) {
	SetLimitEpisodeQuery(nil)

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "wake-policy-e2e",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// First wake of the silence: delivered.
	if err := a.NudgeWake("first wake", NudgeImmediate, 5*time.Second, ""); err != nil {
		t.Fatalf("first wake should be delivered: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if out := string(a.RecentOutput(4096)); !strings.Contains(out, "first wake") {
		t.Errorf("first wake did not reach the PTY, output: %q", out)
	}

	// Second wake of the SAME silence: the only output since the first wake is
	// its echo, so the policy declines.
	err = a.NudgeWake("second wake", NudgeImmediate, 5*time.Second, "")
	if !errors.Is(err, ErrWakeSuppressed) {
		t.Fatalf("second wake of the same silence should be suppressed, got %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if out := string(a.RecentOutput(4096)); strings.Contains(out, "second wake") {
		t.Error("a suppressed wake must not have been written to the PTY")
	}

	// The agent speaks again, after the settle window: a new silence begins.
	time.Sleep(a.nudge.IdleThreshold + 300*time.Millisecond)
	if err := a.SendRaw("agent output\r"); err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if err := a.NudgeWake("third wake", NudgeImmediate, 5*time.Second, ""); err != nil {
		t.Fatalf("wake of a new silence should be delivered: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if out := string(a.RecentOutput(4096)); !strings.Contains(out, "third wake") {
		t.Errorf("third wake did not reach the PTY, output: %q", out)
	}
}

// TestNudgeWakeFailureLeavesTheSilenceUnwoken pins the other half of the
// bookkeeping: only a DELIVERED wake counts. A wake that failed (busy PTY, dead
// agent) must not suppress the next attempt, or one transient error would turn
// into permanent silence.
func TestNudgeWakeFailureLeavesTheSilenceUnwoken(t *testing.T) {
	SetLimitEpisodeQuery(nil)
	a := newPolicyAgent(t) // no PTY: every delivery fails

	if err := a.NudgeWake("first", NudgeImmediate, time.Second, ""); err == nil {
		t.Fatal("wake to an agent with no PTY should fail")
	} else if errors.Is(err, ErrWakeSuppressed) {
		t.Fatalf("that failure is a delivery failure, not a suppression: %v", err)
	}

	// The failed attempt woke nothing, so the next attempt is judged fresh.
	if d := a.evaluateWake(time.Now()); d.suppressed() {
		t.Fatalf("a failed wake must not suppress the retry, got %q: %s", d.Rule, d.Detail)
	}
}
