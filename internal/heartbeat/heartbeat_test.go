package heartbeat

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// fakeClock is a Clock whose wall and monotonic readings advance only via
// explicit calls to advance().
type fakeClock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{wall: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Wall() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *fakeClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

// advance moves both clocks forward by the same duration — the normal-flow case.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
	c.mono += d
}

// jump simulates a clock divergence — wall moves forward by wallDelta while
// monotonic advances only by monoDelta. wallDelta > monoDelta models a host
// sleep / suspend / NTP forward step.
func (c *fakeClock) jump(wallDelta, monoDelta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(wallDelta)
	c.mono += monoDelta
}

// recorder is an Emitter that captures events into a slice.
type recorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) snapshot() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.Event, len(r.events))
	copy(out, r.events)
	return out
}

func newTestDetector(clock Clock, rec *recorder) *Detector {
	return &Detector{
		Interval:  30 * time.Second,
		Threshold: 60 * time.Second,
		Clock:     clock,
		Emitter:   rec.emit,
		AgentID:   "pogod",
	}
}

func TestFirstTickSeedsBaselineWithoutEmitting(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	d := newTestDetector(clock, rec)

	if d.Tick() {
		t.Fatal("first tick must not emit — no baseline yet")
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("expected no events on first tick, got %d", len(got))
	}
}

func TestNormalTicksDoNotEmit(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	d := newTestDetector(clock, rec)

	d.Tick() // seed
	for i := 0; i < 5; i++ {
		clock.advance(30 * time.Second)
		if d.Tick() {
			t.Fatalf("normal tick %d emitted unexpectedly", i)
		}
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("expected zero events for steady ticks, got %d", len(got))
	}
}

func TestSmallSchedulingJitterDoesNotEmit(t *testing.T) {
	// A 5s wall jump while monotonic advances ~30s is below the 60s threshold;
	// nothing should fire. This guards against false positives from OS
	// scheduling slack pushing wall ahead of mono by a small margin.
	clock := newFakeClock()
	rec := &recorder{}
	d := newTestDetector(clock, rec)

	d.Tick() // seed
	clock.jump(35*time.Second, 30*time.Second)
	if d.Tick() {
		t.Fatal("5s gap should be below 60s threshold")
	}
}

func TestExactlyAtThresholdDoesNotEmit(t *testing.T) {
	// Threshold check is strict greater-than (gap > Threshold). A gap of
	// exactly the threshold value should not fire — only gaps that exceed it.
	clock := newFakeClock()
	rec := &recorder{}
	d := newTestDetector(clock, rec)

	d.Tick()                                   // seed
	clock.jump(90*time.Second, 30*time.Second) // gap = 60s == threshold
	if d.Tick() {
		t.Fatal("gap exactly at threshold should not emit (strict >)")
	}
}

func TestLargeJumpEmitsSystemWake(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	d := newTestDetector(clock, rec)

	d.Tick() // seed
	// Simulate a 1h host sleep: wall jumps 1h, mono only ticked 30s before
	// the goroutine got CPU again.
	clock.jump(time.Hour, 30*time.Second)
	if !d.Tick() {
		t.Fatal("expected system_wake emission for 1h gap")
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	ev := got[0]
	if ev.EventType != "system_wake" {
		t.Errorf("EventType: want system_wake, got %q", ev.EventType)
	}
	if ev.Agent != "pogod" {
		t.Errorf("Agent: want pogod, got %q", ev.Agent)
	}
	if _, ok := ev.Details["gap_duration"]; !ok {
		t.Error("missing details.gap_duration")
	}
	if secs, ok := ev.Details["gap_seconds"].(float64); !ok || secs <= 0 {
		t.Errorf("gap_seconds missing or non-positive: %v", ev.Details["gap_seconds"])
	} else {
		// 1h - 30s = 3570s
		want := 3570.0
		if secs < want-1 || secs > want+1 {
			t.Errorf("gap_seconds: want ~%g, got %g", want, secs)
		}
	}
}

func TestMultipleJumpsEachEmit(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	d := newTestDetector(clock, rec)

	d.Tick()
	clock.jump(10*time.Minute, 30*time.Second)
	if !d.Tick() {
		t.Fatal("first jump should emit")
	}
	clock.advance(30 * time.Second) // normal tick between jumps
	if d.Tick() {
		t.Fatal("normal tick between jumps must not emit")
	}
	clock.jump(time.Hour, 30*time.Second)
	if !d.Tick() {
		t.Fatal("second jump should emit")
	}

	if got := rec.snapshot(); len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
}

func TestBackwardWallJumpDoesNotEmit(t *testing.T) {
	// An NTP backward step makes elapsedWall < elapsedMono → gap is negative.
	// We only fire on forward jumps (host sleep, VM pause). Backward steps
	// are visible in the timestamps themselves and don't need a system_wake.
	clock := newFakeClock()
	rec := &recorder{}
	d := newTestDetector(clock, rec)

	d.Tick()
	clock.jump(-5*time.Minute, 30*time.Second)
	if d.Tick() {
		t.Fatal("backward wall jump must not fire system_wake")
	}
}

func TestCustomThresholdHonored(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	d := &Detector{
		Interval:  10 * time.Second,
		Threshold: 5 * time.Minute,
		Clock:     clock,
		Emitter:   rec.emit,
		AgentID:   "pogod",
	}

	d.Tick()
	// 4-minute gap is below the 5m threshold — must not fire.
	clock.jump(4*time.Minute+10*time.Second, 10*time.Second)
	if d.Tick() {
		t.Fatal("4m gap below 5m threshold should not fire")
	}
	// 6-minute gap exceeds threshold — must fire.
	clock.jump(6*time.Minute+10*time.Second, 10*time.Second)
	if !d.Tick() {
		t.Fatal("6m gap above 5m threshold should fire")
	}
}

func TestApplyDefaultsFillsZeroValues(t *testing.T) {
	rec := &recorder{}
	d := &Detector{
		Clock:   newFakeClock(),
		Emitter: rec.emit,
	}
	// First Tick should populate the defaults so subsequent reads make sense.
	d.Tick()

	if d.Interval != DefaultInterval {
		t.Errorf("Interval: want %s, got %s", DefaultInterval, d.Interval)
	}
	if d.Threshold != DefaultJumpThreshold {
		t.Errorf("Threshold: want %s, got %s", DefaultJumpThreshold, d.Threshold)
	}
	if d.AgentID != "pogod" {
		t.Errorf("AgentID: want pogod, got %q", d.AgentID)
	}
}

// TestRunCancelsCleanly verifies Run honors context cancellation. Uses the
// real clock — we just want the goroutine to start and stop without leaking.
func TestRunCancelsCleanly(t *testing.T) {
	rec := &recorder{}
	d := &Detector{
		Interval:  10 * time.Millisecond,
		Threshold: 10 * time.Second,
		Clock:     newFakeClock(),
		Emitter:   rec.emit,
		AgentID:   "pogod",
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of cancel")
	}
}

// TestNudgeShortCircuitsTick exercises the platform-shim integration point:
// when an external signal calls Nudge, the heartbeat goroutine performs an
// out-of-band Tick within milliseconds rather than waiting for the next
// scheduled tick. We use a long Interval (10s) and a short test deadline
// (1s) so a passing test proves the nudge path, not the ticker.
func TestNudgeShortCircuitsTick(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	d := &Detector{
		Interval:  10 * time.Second, // long enough that the regular ticker won't save us
		Threshold: 60 * time.Second,
		Clock:     clock,
		Emitter:   rec.emit,
		AgentID:   "pogod",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()

	// Wait for Run's seed tick. There is no public signal for "seed
	// complete," so we poll: the seed Tick sets started=true under the
	// detector's mutex.
	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		seeded := d.started
		d.mu.Unlock()
		if seeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not perform its seed Tick within 1s")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Simulate a host sleep: wall jumps 1h, mono barely advances.
	clock.jump(time.Hour, 100*time.Millisecond)

	d.Nudge()

	// The nudge-driven Tick should land within 1s.
	deadline = time.Now().Add(time.Second)
	for {
		if len(rec.snapshot()) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Nudge did not produce a system_wake event within 1s")
		}
		time.Sleep(5 * time.Millisecond)
	}

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].EventType != "system_wake" {
		t.Errorf("EventType: want system_wake, got %q", got[0].EventType)
	}
}

// TestNudgeBeforeRunStartsIsCoalesced verifies Nudge is safe to call before
// Run begins (the buffered channel queues exactly one signal) and that
// repeated nudges don't pile up — one Tick per wake regardless of how many
// platform notifications fire.
func TestNudgeBeforeRunStartsIsCoalesced(t *testing.T) {
	clock := newFakeClock()
	rec := &recorder{}
	d := &Detector{
		Interval:  10 * time.Second,
		Threshold: 60 * time.Second,
		Clock:     clock,
		Emitter:   rec.emit,
		AgentID:   "pogod",
	}

	// 100 nudges before Run starts — must not panic, must not block.
	for i := 0; i < 100; i++ {
		d.Nudge()
	}

	// The buffered channel holds exactly one entry.
	if got := len(d.nudgeChan()); got != 1 {
		t.Fatalf("nudge channel should hold 1 (coalesced), got %d", got)
	}
}

// TestEmissionLandsInEventLog drives the default events.Emit path so a
// system_wake event appears as JSONL in ~/.pogo/events.log (overridden to a
// temp file for the test). This is the integration-style check called for in
// the acceptance criteria.
func TestEmissionLandsInEventLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	events.SetLogPathForTesting(logPath)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	clock := newFakeClock()
	d := &Detector{
		Interval:  30 * time.Second,
		Threshold: 60 * time.Second,
		Clock:     clock,
		// Emitter unset → defaults to events.Emit, which honors the override.
		AgentID: "pogod",
	}

	d.Tick()
	clock.jump(2*time.Hour, 30*time.Second)
	if !d.Tick() {
		t.Fatal("expected emission for 2h gap")
	}

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 line in event log, got %d", len(lines))
	}
	var got events.Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EventType != "system_wake" {
		t.Errorf("EventType: want system_wake, got %q", got.EventType)
	}
	if got.Agent != "pogod" {
		t.Errorf("Agent: want pogod, got %q", got.Agent)
	}
	if got.SchemaVersion != events.SchemaVersion {
		t.Errorf("SchemaVersion: want %d, got %d", events.SchemaVersion, got.SchemaVersion)
	}
	if got.Timestamp == "" {
		t.Error("Timestamp must be set by events.Emit")
	}
}
