package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/heartbeat"
)

// fakeHBClock is a Clock for the heartbeat detector that mirrors the test
// pattern in internal/heartbeat/heartbeat_test.go but lives here so the
// scheduler integration test isn't coupled to heartbeat's internal helpers.
type fakeHBClock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func (c *fakeHBClock) Wall() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}
func (c *fakeHBClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}
func (c *fakeHBClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
	c.mono += d
}
func (c *fakeHBClock) jump(wallDelta, monoDelta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(wallDelta)
	c.mono += monoDelta
}

// TestHeartbeatDrivesScheduler exercises the wiring contract called for in the
// acceptance criteria: a heartbeat tick passes its wall reading to the
// scheduler via OnTick, and clock jumps are observed through the same loop.
func TestHeartbeatDrivesScheduler(t *testing.T) {
	rec := &recorder{}
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	now := fixedTime()
	s, err := New(path, rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Entry{
		Agent: "crew", Cron: "*/5 * * * *", ID: "p",
	}, now); err != nil {
		t.Fatal(err)
	}

	clock := &fakeHBClock{wall: now}
	d := &heartbeat.Detector{
		Interval:  time.Minute,
		Threshold: 2 * time.Minute,
		Clock:     clock,
		Emitter:   func(_ events.Event) {},
		AgentID:   "pogod-test",
		OnTick:    func(t time.Time) { s.Tick(context.Background(), t) },
	}

	// Seed: first tick records baseline only.
	d.Tick()

	// Five normal ticks at +1m each → reaches T+5m → exactly one fire.
	for i := 0; i < 5; i++ {
		clock.advance(time.Minute)
		d.Tick()
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("want 1 fire after 5 normal ticks, got %d", got)
	}

	// Now simulate a 1-hour host sleep — wall jumps 1h, mono only ticks 1m
	// before goroutine resumes. With ReplayOnce default, exactly ONE more
	// fire is delivered, regardless of how many 5-minute boundaries the
	// jump straddled.
	rec.mu.Lock()
	rec.fires = nil
	rec.mu.Unlock()
	clock.jump(time.Hour, time.Minute)
	d.Tick()
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("after 1h jump: want exactly 1 fire (at-most-once), got %d", got)
	}
}
