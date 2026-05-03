// Package heartbeat provides a portable detector for host-sleep, suspend,
// VM-pause, and NTP-step events by comparing elapsed wall-clock time against
// elapsed monotonic time on each tick.
//
// On each tick the Detector reads a wall-clock timestamp and a monotonic
// duration, computes their deltas since the previous tick, and emits a
// system_wake event to the pogo event log when the wall delta exceeds the
// monotonic delta by more than a configurable threshold. This is the
// OS-agnostic core of the sleep-resilience design (see
// docs/sleep-resilience-design.md §1) — no cgo, no platform shims.
package heartbeat

import (
	"context"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

const (
	// DefaultInterval is how often the heartbeat goroutine ticks.
	DefaultInterval = 30 * time.Second
	// DefaultJumpThreshold is the gap above which a tick is treated as a
	// system_wake. Two interval lengths gives ample slack for OS scheduling
	// jitter without missing real sleep events.
	DefaultJumpThreshold = 2 * DefaultInterval
)

// Clock supplies wall-clock and monotonic time. The real implementation reads
// the host clock; tests substitute a controllable fake.
type Clock interface {
	Wall() time.Time
	Mono() time.Duration
}

type realClock struct {
	start time.Time
}

func newRealClock() *realClock { return &realClock{start: time.Now()} }

func (c *realClock) Wall() time.Time { return time.Now().Round(0) }

func (c *realClock) Mono() time.Duration { return time.Since(c.start) }

// Emitter writes a system_wake event somewhere. The default writes to the
// shared event log via internal/events.Emit; tests substitute an in-memory
// recorder.
type Emitter func(events.Event)

func defaultEmitter(e events.Event) { events.Emit(context.Background(), e) }

// Detector watches for clock jumps. The zero value is not usable — call New
// or populate fields before invoking Run.
type Detector struct {
	Interval  time.Duration
	Threshold time.Duration
	Clock     Clock
	Emitter   Emitter
	// AgentID is the value stamped into the events.Agent field. Defaults to
	// "pogod" since the daemon owns the heartbeat loop.
	AgentID string

	mu       sync.Mutex
	started  bool
	prevWall time.Time
	prevMono time.Duration
}

// New returns a Detector wired to the real wall/monotonic clocks and the
// default event emitter.
func New() *Detector {
	return &Detector{
		Interval:  DefaultInterval,
		Threshold: DefaultJumpThreshold,
		Clock:     newRealClock(),
		Emitter:   defaultEmitter,
		AgentID:   "pogod",
	}
}

// Run blocks until ctx is canceled. It seeds the baseline on entry, then
// ticks every Interval. Returns once the context is canceled.
func (d *Detector) Run(ctx context.Context) {
	d.applyDefaults()

	// Seed the baseline so the first scheduled tick has a comparison point.
	// This intentionally never emits — there's no prior state to compare
	// against.
	d.Tick()

	ticker := time.NewTicker(d.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Tick()
		}
	}
}

// Tick performs one heartbeat sample. It returns true iff a system_wake event
// was emitted. Exposed so tests can drive the Detector deterministically.
func (d *Detector) Tick() bool {
	d.applyDefaults()

	d.mu.Lock()
	defer d.mu.Unlock()

	wall := d.Clock.Wall()
	mono := d.Clock.Mono()

	if !d.started {
		d.prevWall = wall
		d.prevMono = mono
		d.started = true
		return false
	}

	elapsedWall := wall.Sub(d.prevWall)
	elapsedMono := mono - d.prevMono
	gap := elapsedWall - elapsedMono

	d.prevWall = wall
	d.prevMono = mono

	if gap > d.Threshold {
		d.emit(gap, elapsedWall, elapsedMono)
		return true
	}
	return false
}

func (d *Detector) emit(gap, wallElapsed, monoElapsed time.Duration) {
	d.Emitter(events.Event{
		EventType: "system_wake",
		Agent:     d.AgentID,
		Details: map[string]any{
			"gap_duration":         gap.String(),
			"gap_seconds":          gap.Seconds(),
			"wall_elapsed_seconds": wallElapsed.Seconds(),
			"mono_elapsed_seconds": monoElapsed.Seconds(),
			"tick_interval":        d.Interval.String(),
			"jump_threshold":       d.Threshold.String(),
		},
	})
}

func (d *Detector) applyDefaults() {
	if d.Interval <= 0 {
		d.Interval = DefaultInterval
	}
	if d.Threshold <= 0 {
		d.Threshold = 2 * d.Interval
	}
	if d.Clock == nil {
		d.Clock = newRealClock()
	}
	if d.Emitter == nil {
		d.Emitter = defaultEmitter
	}
	if d.AgentID == "" {
		d.AgentID = "pogod"
	}
}
