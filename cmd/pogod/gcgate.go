package main

import (
	"log"
	"sync"
	"time"
)

// startupGCSettle is how long after pogod's first auto-start sweep the
// mail-check reap stays held. It covers the gap between "the crew have been
// spawned" and "the crew are registered and answering", so the first sweep the
// reap does see is evaluated against a settled registry rather than one still
// filling in.
//
// The value is deliberately generous relative to what it protects and cheap
// relative to what it costs: mail-check loops run on a */10 cron, so holding
// the reap for 30s delays collecting a genuinely dead polecat's schedule by
// well under one fire interval — it cannot accumulate scheduler_fire_failed
// events. The failure modes are not symmetric. A late reap is invisible; an
// early one silently kills the fleet's mail delivery (mg-de08).
const startupGCSettle = 30 * time.Second

// startupGCGate holds the mail-check reap until pogod knows its own desired
// state (mg-de08, PART B of architect's ruling).
//
// registryLiveness is the invariant — never reap without positive evidence of
// death — and this gate is what keeps that invariant from being evaluated
// against data that isn't loaded yet. A pogod that has just started has an
// empty registry AND has not yet run AutoStartAgents(), so for a window at
// boot every answer it could give about an agent is uninformed. The heartbeat
// starts ticking inside that window.
//
// The gate opens once, permanently: after the first auto-start sweep has
// completed and the settle window has elapsed. It is never closed again — a
// pogod that is up and sweeping has the evidence it needs, and re-closing it
// would only re-open the window this exists to shut.
//
// Safe for concurrent use: markAutoStartComplete runs on the startup goroutine
// while open runs on the heartbeat's Tick.
type startupGCGate struct {
	settle time.Duration

	mu      sync.Mutex
	swept   bool
	readyAt time.Time
}

func newStartupGCGate(settle time.Duration) *startupGCGate {
	return &startupGCGate{settle: settle}
}

// markAutoStartComplete records that pogod's first auto-start sweep has
// finished and arms the settle window. Call it on EVERY startup path that gets
// past the sweep — including the paths that deliberately start nothing
// ([agents] autostart = false, an unconfigured daemon). Those daemons still
// need the reap to run eventually: a sandbox that never opens the gate would
// never collect a real polecat's dead mail-check.
//
// Repeated calls are a no-op; the first sweep is the one that counts.
func (g *startupGCGate) markAutoStartComplete(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.swept {
		return
	}
	g.swept = true
	g.readyAt = now.Add(g.settle)
	log.Printf("pogod: auto-start sweep complete; mail-check reap opens at %s (%s settle, mg-de08)",
		g.readyAt.UTC().Format(time.RFC3339), g.settle)
}

// open reports whether the mail-check reap may run as of now. It is false
// until the first auto-start sweep has completed and the settle window has
// elapsed.
func (g *startupGCGate) open(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.swept && !now.Before(g.readyAt)
}
