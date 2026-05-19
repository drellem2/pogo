package claude

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// --- helpers ----------------------------------------------------------------

// testRig is the minimal harness for driving RunModalHook in tests. It plays
// the role of an Agent's PTY tee + dismissal sink: writeOutput feeds bytes
// into the watcher's scanner, dismissals are captured into the dismissed
// slice (one entry per write).
type testRig struct {
	mu              sync.Mutex
	scanner         io.Writer
	dismissed       [][]byte
	dismissCount    int32
	eventCount      int32
	pmNotifications []string
	tracker         *fakeTracker
	now             func() time.Time
}

func newTestRig(now func() time.Time) *testRig {
	return &testRig{
		tracker: &fakeTracker{lastSeen: map[string]time.Time{}},
		now:     now,
	}
}

func (r *testRig) writeOutput(b []byte) {
	r.mu.Lock()
	w := r.scanner
	r.mu.Unlock()
	if w == nil {
		return
	}
	_, _ = w.Write(b)
}

func (r *testRig) deps(agentID string) ModalHookDeps {
	return ModalHookDeps{
		AgentName: agentID,
		AgentID:   agentID,
		Subscribe: func(w io.Writer) func() {
			r.mu.Lock()
			r.scanner = w
			r.mu.Unlock()
			return func() {
				r.mu.Lock()
				r.scanner = nil
				r.mu.Unlock()
			}
		},
		Dismiss: func(payload []byte) error {
			cp := make([]byte, len(payload))
			copy(cp, payload)
			r.mu.Lock()
			r.dismissed = append(r.dismissed, cp)
			r.mu.Unlock()
			atomic.AddInt32(&r.dismissCount, 1)
			return nil
		},
		Tracker: r.tracker,
		Now:     r.now,
		EmitEvent: func(events.Event) {
			atomic.AddInt32(&r.eventCount, 1)
		},
		NotifyPM: func(agentID, matcherName string) {
			r.mu.Lock()
			r.pmNotifications = append(r.pmNotifications, agentID+":"+matcherName)
			r.mu.Unlock()
		},
	}
}

func (r *testRig) dismissals() int { return int(atomic.LoadInt32(&r.dismissCount)) }

func (r *testRig) pmCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pmNotifications)
}

// fakeTracker is an in-memory ActivityTracker with a controllable clock.
type fakeTracker struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func (t *fakeTracker) LastSeen(agentID string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastSeen[agentID]
}

func (t *fakeTracker) set(agentID string, when time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastSeen[agentID] = when
}

// fakeClock is an atomic time.Time accessed via testRig.now.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// waitFor polls fn every 5ms up to timeout; returns true if fn returned true.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fn()
}

// testMatchers gives all test cases the same shape — short timeouts so the
// suite runs fast without time.Sleep'ing 20 minutes.
func testMatchers() []ModalMatcher {
	return []ModalMatcher{
		{
			Name:       "rating-dialog",
			LineMarker: RatingDialogMarker,
			Dismissal:  []byte("0\n"),
			IdleGate: IdleGatePolicy{
				Mode:            ModeScannerIdle,
				IdleAfterMarker: 50 * time.Millisecond,
			},
		},
		{
			Name:       "rate-limit-options",
			LineMarker: RateLimitMarker,
			Dismissal:  []byte("1\n"),
			IdleGate: IdleGatePolicy{
				Mode:           ModeEventsStale,
				EventStaleness: 20 * time.Minute,
			},
		},
	}
}

// --- mg-5a3d §5 fixture cases ----------------------------------------------

// Case 1 (rating-dialog): marker present in stream → dismissal fires after
// IdleAfterMarker with no further chunks.
func TestModalHook_Case1_RatingDialogFires(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rig := newTestRig(clock.Now)
	rig.tracker.set("cat-test", clock.Now())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { RunModalHook(ctx, rig.deps("cat-test"), testMatchers()); close(done) }()
	// Wait for the watcher's Subscribe to be installed before writing.
	if !waitFor(t, time.Second, func() bool {
		rig.mu.Lock()
		ok := rig.scanner != nil
		rig.mu.Unlock()
		return ok
	}) {
		t.Fatalf("scanner never subscribed")
	}

	rig.writeOutput([]byte("Some preamble\n" + RatingDialogMarker + "\n"))

	if !waitFor(t, time.Second, func() bool { return rig.dismissals() >= 1 }) {
		t.Fatalf("expected dismissal to fire, got %d", rig.dismissals())
	}
	rig.mu.Lock()
	if string(rig.dismissed[0]) != "0\n" {
		t.Errorf("expected dismissal payload %q, got %q", "0\n", rig.dismissed[0])
	}
	rig.mu.Unlock()
}

// Case 2 (rating-dialog): marker appears once but more output keeps arriving
// (transcript mentions the phrase but no dialog) → no false-positive fire.
func TestModalHook_Case2_RatingDialogMentionedNoFalsePositive(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rig := newTestRig(clock.Now)
	rig.tracker.set("cat-test", clock.Now())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { RunModalHook(ctx, rig.deps("cat-test"), testMatchers()); close(done) }()
	if !waitFor(t, time.Second, func() bool {
		rig.mu.Lock()
		ok := rig.scanner != nil
		rig.mu.Unlock()
		return ok
	}) {
		t.Fatalf("scanner never subscribed")
	}

	// Drip output so that the marker scrolls off (more than scanBufBytes of
	// trailing content) before the idle gate would fire.
	rig.writeOutput([]byte("Transcript line referencing " + RatingDialogMarker + " as a string.\n"))
	stop := time.After(300 * time.Millisecond) // ~6× IdleAfterMarker
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	pad := make([]byte, 256)
	for i := range pad {
		pad[i] = 'x'
	}
	for {
		select {
		case <-stop:
			goto verify
		case <-tick.C:
			rig.writeOutput(pad)
		}
	}
verify:
	if rig.dismissals() != 0 {
		t.Errorf("expected no dismissal for in-transcript mention, got %d", rig.dismissals())
	}
}

// Case 3 (rate-limit): marker present + event log stale ≥ EventStaleness →
// dismissal fires.
func TestModalHook_Case3_RateLimitFiresOnEventsStale(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rig := newTestRig(clock.Now)
	// Set last-seen to ~21m ago so the gate trips immediately.
	rig.tracker.set("cat-test", clock.Now().Add(-21*time.Minute))

	// Override the events-stale poll interval to something fast for the test
	// (we can't mock the ticker without exposing internals — pull-knob: lower
	// the poll interval globally for tests).
	prev := setEventsStalePollIntervalForTest(20 * time.Millisecond)
	defer setEventsStalePollIntervalForTest(prev)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { RunModalHook(ctx, rig.deps("cat-test"), testMatchers()); close(done) }()
	if !waitFor(t, time.Second, func() bool {
		rig.mu.Lock()
		ok := rig.scanner != nil
		rig.mu.Unlock()
		return ok
	}) {
		t.Fatalf("scanner never subscribed")
	}

	rig.writeOutput([]byte("What do you want to do?\n  1: " + RateLimitMarker + "\n  2: ...\n"))

	if !waitFor(t, 2*time.Second, func() bool { return rig.dismissals() >= 1 }) {
		t.Fatalf("expected rate-limit dismissal, got %d", rig.dismissals())
	}
	rig.mu.Lock()
	if string(rig.dismissed[0]) != "1\n" {
		t.Errorf("expected dismissal payload %q, got %q", "1\n", rig.dismissed[0])
	}
	rig.mu.Unlock()
	if !waitFor(t, time.Second, func() bool { return rig.pmCount() >= 1 }) {
		t.Errorf("expected PM notification for rate-limit dismissal, got %d", rig.pmCount())
	}
}

// Case 4 (rate-limit): marker present + events.jsonl actively being written
// (events recent enough) → no dismissal within first 20m.
func TestModalHook_Case4_RateLimitNoFireWhenEventsRecent(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rig := newTestRig(clock.Now)
	rig.tracker.set("cat-test", clock.Now()) // events are fresh

	prev := setEventsStalePollIntervalForTest(20 * time.Millisecond)
	defer setEventsStalePollIntervalForTest(prev)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunModalHook(ctx, rig.deps("cat-test"), testMatchers())
	if !waitFor(t, time.Second, func() bool {
		rig.mu.Lock()
		ok := rig.scanner != nil
		rig.mu.Unlock()
		return ok
	}) {
		t.Fatalf("scanner never subscribed")
	}

	rig.writeOutput([]byte(RateLimitMarker + "\n"))
	// Keep emitting fresh activity for ~200ms, well past several poll cycles.
	for i := 0; i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		rig.tracker.set("cat-test", clock.Now())
	}
	if rig.dismissals() != 0 {
		t.Errorf("expected no rate-limit dismissal with fresh events, got %d", rig.dismissals())
	}
}

// Case 5 (rate-limit): user-invoked /rate-limit-options where the user picks
// an option within 30s — events.jsonl active throughout → no dismissal.
// Modeled identically to case 4 (the active-events condition is what
// disambiguates this case from a wedge), but with an explicit user-stop
// after a short observation to mirror the "user picks option" outcome.
func TestModalHook_Case5_UserInvokedNoFire(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rig := newTestRig(clock.Now)
	rig.tracker.set("cat-test", clock.Now())

	prev := setEventsStalePollIntervalForTest(20 * time.Millisecond)
	defer setEventsStalePollIntervalForTest(prev)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunModalHook(ctx, rig.deps("cat-test"), testMatchers())
	if !waitFor(t, time.Second, func() bool {
		rig.mu.Lock()
		ok := rig.scanner != nil
		rig.mu.Unlock()
		return ok
	}) {
		t.Fatalf("scanner never subscribed")
	}

	rig.writeOutput([]byte(RateLimitMarker + "\n"))
	// Simulate user inspecting the menu for 100ms while reasoning loop keeps
	// emitting events. Then user picks an option; marker remains in buffer
	// briefly but events stay fresh so the gate never trips.
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		rig.tracker.set("cat-test", clock.Now())
	}
	// User-pick: simulated by output that doesn't contain the marker anymore.
	rig.writeOutput(make([]byte, 4096)) // push 4 KiB to scroll marker out
	for i := 0; i < 3; i++ {
		time.Sleep(20 * time.Millisecond)
		rig.tracker.set("cat-test", clock.Now())
	}
	if rig.dismissals() != 0 {
		t.Errorf("expected no user-invoked dismissal, got %d", rig.dismissals())
	}
}

// Case 6 (rate-limit): marker text quoted in transcript while events.jsonl
// active → no dismissal (active events suppress the gate even if marker is
// in the visible buffer).
func TestModalHook_Case6_RateLimitQuotedNoFire(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	rig := newTestRig(clock.Now)
	rig.tracker.set("cat-test", clock.Now())

	prev := setEventsStalePollIntervalForTest(20 * time.Millisecond)
	defer setEventsStalePollIntervalForTest(prev)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunModalHook(ctx, rig.deps("cat-test"), testMatchers())
	if !waitFor(t, time.Second, func() bool {
		rig.mu.Lock()
		ok := rig.scanner != nil
		rig.mu.Unlock()
		return ok
	}) {
		t.Fatalf("scanner never subscribed")
	}

	rig.writeOutput([]byte("Agent narration: today I will think about \"" + RateLimitMarker + "\" as a phrase.\n"))
	for i := 0; i < 8; i++ {
		time.Sleep(20 * time.Millisecond)
		rig.tracker.set("cat-test", clock.Now())
	}
	if rig.dismissals() != 0 {
		t.Errorf("expected no dismissal for transcript-quoted marker with fresh events, got %d", rig.dismissals())
	}
}

// TestSplitDismissal verifies the two-write pattern decomposition used by
// defaultDismisser to dodge the mg-09b6 paste-detection bug class.
func TestSplitDismissal(t *testing.T) {
	cases := []struct {
		in         string
		wantBody   string
		wantSubmit string
	}{
		{"0\n", "0", "\n"},
		{"1\n", "1", "\n"},
		{"42\r", "42", "\r"},
		{"plain", "plain", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		body, submit := splitDismissal([]byte(c.in))
		if string(body) != c.wantBody || string(submit) != c.wantSubmit {
			t.Errorf("splitDismissal(%q) = (%q, %q), want (%q, %q)",
				c.in, body, submit, c.wantBody, c.wantSubmit)
		}
	}
}

// TestModalScannerANSIStripping verifies that markers embedded in ANSI-laden
// PTY output are still detected — Claude Code wraps menu items in color
// escapes, so the scanner must operate on the stripped form.
func TestModalScannerANSIStripping(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	scanner := newModalScanner(testMatchers(), clock.Now)
	// Marker bracketed in ANSI escape sequences (color + reset).
	chunk := []byte("\x1b[1;32m" + RatingDialogMarker + "\x1b[0m\n")
	if _, err := scanner.Write(chunk); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !scanner.MarkerVisible(0) {
		t.Errorf("expected marker visible after ANSI-bracketed write")
	}
}
