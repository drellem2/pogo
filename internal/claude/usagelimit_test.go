package claude

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// mailSink captures coordinator mails for assertions.
type mailSink struct {
	mu    sync.Mutex
	mails []sentMail
}

type sentMail struct {
	to, from, subject, body string
}

func (s *mailSink) send(to, from, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mails = append(s.mails, sentMail{to, from, subject, body})
	return nil
}

func (s *mailSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.mails)
}

func (s *mailSink) at(i int) sentMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mails[i]
}

func fixedNow() func() time.Time {
	t := time.Unix(1_700_000_000, 0).UTC()
	return func() time.Time { return t }
}

// fakeTimer is a test double for the coordinator's hold-down timer. It records
// the callback and whether it was stopped, so a test can decide the hold-down
// "elapsed" by calling fire().
type fakeTimer struct {
	mu      sync.Mutex
	fn      func()
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (t *fakeTimer) stoppedOK() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

// timerHarness supplies the coordinator's `after` factory and lets the test
// fire the currently-armed hold-down timer on demand. Only one hold-down timer
// is armed per open episode, so tracking the most recent is sufficient.
type timerHarness struct {
	mu    sync.Mutex
	armed *fakeTimer
}

func (h *timerHarness) after(_ time.Duration, f func()) stoppableTimer {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := &fakeTimer{fn: f}
	h.armed = t
	return t
}

// fire simulates the hold-down elapsing: it invokes the armed timer's callback
// unless it was already stopped (i.e. the episode cleared first).
func (h *timerHarness) fire() {
	h.mu.Lock()
	t := h.armed
	h.mu.Unlock()
	if t != nil && !t.stoppedOK() {
		t.fn()
	}
}

// newHeldCoordinator builds a coordinator with a controllable hold-down timer.
// The hold-down duration is irrelevant to the fake (the test drives firing
// explicitly), so any non-zero value is fine.
func newHeldCoordinator(send func(to, from, subject, body string) error) (*usageLimitCoordinator, *timerHarness) {
	h := &timerHarness{}
	c := newUsageLimitCoordinatorWithHoldDown(send, fixedNow(), 45*time.Second, h.after)
	return c, h
}

// A single agent hitting and clearing produces exactly one hit + one clear
// mail, both to human.
func TestUsageLimitCoordinator_SingleAgentHitAndClear(t *testing.T) {
	sink := &mailSink{}
	c, h := newHeldCoordinator(sink.send)
	now := fixedNow()()

	c.OnHit("cat-mg-7ffa", "mg-7ffa", now)
	if sink.count() != 0 {
		t.Fatalf("hit mail must not fire before the hold-down elapses, got %d", sink.count())
	}
	h.fire() // hold-down elapses with the episode still open
	if sink.count() != 1 {
		t.Fatalf("expected 1 hit mail, got %d", sink.count())
	}
	hit := sink.at(0)
	if hit.to != "human" {
		t.Errorf("hit mail to = %q, want human", hit.to)
	}
	if !strings.Contains(hit.subject, "hit") {
		t.Errorf("hit subject = %q, want it to mention hit", hit.subject)
	}
	if !strings.Contains(hit.body, "cat-mg-7ffa") {
		t.Errorf("hit body should name the agent, got %q", hit.body)
	}

	c.OnClear("cat-mg-7ffa", now.Add(time.Hour))
	if sink.count() != 2 {
		t.Fatalf("expected 2 mails after clear, got %d", sink.count())
	}
	clear := sink.at(1)
	if clear.to != "human" {
		t.Errorf("clear mail to = %q, want human", clear.to)
	}
	if !strings.Contains(clear.subject, "cleared") {
		t.Errorf("clear subject = %q, want it to mention cleared", clear.subject)
	}
	// Resume checklist must name the agent + a recovery command.
	if !strings.Contains(clear.body, "cat-mg-7ffa") ||
		!strings.Contains(clear.body, "pogo nudge mg-7ffa") {
		t.Errorf("clear body missing resume checklist entry, got:\n%s", clear.body)
	}
}

// Two agents hitting the same episode produce ONE hit mail (not two), and one
// clear mail that lists BOTH agents in the roster.
func TestUsageLimitCoordinator_CoalescesFleetEpisode(t *testing.T) {
	sink := &mailSink{}
	c, h := newHeldCoordinator(sink.send)
	now := fixedNow()()

	c.OnHit("cat-mg-aaaa", "mg-aaaa", now)
	c.OnHit("cat-mg-bbbb", "mg-bbbb", now.Add(time.Minute))
	c.OnHit("cat-mg-aaaa", "mg-aaaa", now.Add(2*time.Minute)) // duplicate, no-op

	h.fire() // hold-down elapses; both agents still active, so one hit mail
	if sink.count() != 1 {
		t.Fatalf("expected exactly 1 coalesced hit mail for the episode, got %d", sink.count())
	}

	// First agent clears — episode still open (second agent active), no mail.
	c.OnClear("cat-mg-aaaa", now.Add(time.Hour))
	if sink.count() != 1 {
		t.Fatalf("clearing one of two agents should not send a mail, got %d", sink.count())
	}

	// Second agent clears — episode closes, one clear mail listing both.
	c.OnClear("cat-mg-bbbb", now.Add(2*time.Hour))
	if sink.count() != 2 {
		t.Fatalf("expected clear mail after last agent clears, got %d", sink.count())
	}
	clear := sink.at(1)
	if !strings.Contains(clear.body, "cat-mg-aaaa") || !strings.Contains(clear.body, "cat-mg-bbbb") {
		t.Errorf("clear roster should list both agents, got:\n%s", clear.body)
	}
	if !strings.Contains(clear.subject, "2 agent") {
		t.Errorf("clear subject should report 2 agents, got %q", clear.subject)
	}
}

// A second, separate episode after the first fully cleared gets its own hit
// mail — the roster is reset between episodes.
func TestUsageLimitCoordinator_SecondEpisodeAfterClear(t *testing.T) {
	sink := &mailSink{}
	c, h := newHeldCoordinator(sink.send)
	now := fixedNow()()

	c.OnHit("cat-a", "", now)
	h.fire()                               // episode 1 outlives the hold-down: hit mail
	c.OnClear("cat-a", now.Add(time.Hour)) // episode 1 closes (2 mails)
	c.OnHit("cat-b", "", now.Add(2*time.Hour))
	h.fire() // episode 2 outlives the hold-down: hit mail

	if sink.count() != 3 {
		t.Fatalf("expected hit+clear+hit = 3 mails, got %d", sink.count())
	}
	// The second episode's clear mail must NOT still carry agent a.
	c.OnClear("cat-b", now.Add(3*time.Hour))
	clear := sink.at(3)
	if strings.Contains(clear.body, "cat-a") {
		t.Errorf("episode 2 clear should not list episode 1's agent, got:\n%s", clear.body)
	}
	if !strings.Contains(clear.body, "cat-b") {
		t.Errorf("episode 2 clear should list cat-b, got:\n%s", clear.body)
	}
}

// Clearing an agent that was never hit is a no-op (no mail, no panic).
func TestUsageLimitCoordinator_ClearUnknownAgentNoop(t *testing.T) {
	sink := &mailSink{}
	c := newUsageLimitCoordinator(sink.send, fixedNow())
	c.OnClear("cat-ghost", fixedNow()())
	if sink.count() != 0 {
		t.Fatalf("clearing an unknown agent should send no mail, got %d", sink.count())
	}
}

// The failing case that motivated mg-4904: replay the real 07-22 flap sequence
// — three episodes at 23:21, 23:47, 23:54 that each opened and resolved within
// ~1s, all well inside the hold-down. Because none of them outlives the timer,
// fire() is never reached for any (the episode clears first, stopping the
// timer), and the fleet operator gets ZERO mails instead of the six it got on
// 07-22. This is the case a sustained-only test cannot prove: it passes today.
func TestUsageLimitCoordinator_SubSecondFlapEmitsNoMail(t *testing.T) {
	sink := &mailSink{}
	c, h := newHeldCoordinator(sink.send)
	base := fixedNow()()

	// gaps mirror the observed 23:21 / 23:47 / 23:54 spacing; the exact offsets
	// don't matter, only that each hit/clear pair is separated by ~1s.
	for i, gap := range []time.Duration{0, 26 * time.Minute, 33 * time.Minute} {
		agent := fmt.Sprintf("cat-flap-%d", i)
		hit := base.Add(gap)
		c.OnHit(agent, "mg-flap", hit)
		// The hold-down (45s) has NOT elapsed — the episode clears 1s later.
		c.OnClear(agent, hit.Add(time.Second))
	}

	if sink.count() != 0 {
		t.Fatalf("three sub-second flaps must emit zero human mails, got %d:\n%+v", sink.count(), sink.mails)
	}
	// The armed timer must have been cancelled by the final OnClear, not left
	// dangling to fire a late orphan hit.
	if h.armed != nil && !h.armed.stoppedOK() {
		t.Errorf("hold-down timer should be stopped after the flap clears")
	}
}

// A sustained episode (outlives the hold-down, as the real 23h weekly-limit
// episode did) pages exactly once: one hit when the hold-down elapses, one
// clear when it resolves — unchanged from pre-mg-4904 behavior. Paired with the
// flap test above, this proves the hold-down suppresses noise without
// suppressing a page that should fire.
func TestUsageLimitCoordinator_SustainedEpisodePagesOnce(t *testing.T) {
	sink := &mailSink{}
	c, h := newHeldCoordinator(sink.send)
	now := fixedNow()()

	c.OnHit("cat-real", "mg-real", now)
	if sink.count() != 0 {
		t.Fatalf("no page until the hold-down elapses, got %d", sink.count())
	}

	h.fire() // hold-down elapses; episode still open -> one hit mail
	if sink.count() != 1 {
		t.Fatalf("sustained episode should page once at hit, got %d", sink.count())
	}
	if !strings.Contains(sink.at(0).subject, "hit") {
		t.Errorf("first mail should be the hit, got subject %q", sink.at(0).subject)
	}

	// ~23h later the limit resets and the agent clears.
	c.OnClear("cat-real", now.Add(23*time.Hour))
	if sink.count() != 2 {
		t.Fatalf("sustained episode should send exactly one clear, got %d", sink.count())
	}
	if !strings.Contains(sink.at(1).subject, "cleared") {
		t.Errorf("second mail should be the clear, got subject %q", sink.at(1).subject)
	}
}

// A flap (no hit mail) followed by a genuine sustained episode: the flap is
// silent, then the real episode pages normally. Guards against the hold-down
// state (hitSent / opener / timer) leaking between episodes.
func TestUsageLimitCoordinator_FlapThenSustained(t *testing.T) {
	sink := &mailSink{}
	c, h := newHeldCoordinator(sink.send)
	now := fixedNow()()

	// Flap: opens and closes inside the hold-down -> zero mails.
	c.OnHit("cat-flap", "mg-flap", now)
	c.OnClear("cat-flap", now.Add(time.Second))
	if sink.count() != 0 {
		t.Fatalf("flap should be silent, got %d", sink.count())
	}

	// Genuine episode a while later -> pages once, hit then clear.
	c.OnHit("cat-real", "mg-real", now.Add(time.Hour))
	h.fire()
	c.OnClear("cat-real", now.Add(2*time.Hour))
	if sink.count() != 2 {
		t.Fatalf("sustained episode after a flap should page once (hit+clear), got %d", sink.count())
	}
	// The clear roster must name the real agent and not the flapped one.
	clear := sink.at(1)
	if !strings.Contains(clear.body, "cat-real") {
		t.Errorf("clear should name the sustained agent, got:\n%s", clear.body)
	}
	if strings.Contains(clear.body, "cat-flap") {
		t.Errorf("clear must not carry the earlier flapped agent, got:\n%s", clear.body)
	}
}

func TestAgentNameFromID(t *testing.T) {
	cases := map[string]string{
		"cat-mg-7ffa": "mg-7ffa",
		"crew-arch":   "arch",
		"mayor":       "mayor",
		"human":       "human",
	}
	for in, want := range cases {
		if got := agentNameFromID(in); got != want {
			t.Errorf("agentNameFromID(%q) = %q, want %q", in, got, want)
		}
	}
}
