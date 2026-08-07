package main

import (
	"log"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/server"
)

// autoStartDisabledReason is what a start-orchestration reports when the
// daemon is configured never to spawn a crew. It is phrased as the config key
// an operator would change, because it is printed straight through to them.
const autoStartDisabledReason = "[agents] autostart = false"

// unconfiguredReason is what the sweep reports on a daemon with no config file
// at all. It names the missing artifact rather than the flag, because there is
// no flag to change — the daemon is unconfigured, not configured-off.
const unconfiguredReason = "no config file; this daemon is not configured for orchestration"

// agentStarterFor builds the server's AgentStarter: the agent-side analogue of
// the RefineryStarter, invoked whenever a `start` must leave the declared crew
// running — a return to full mode, and (since mg-060c) a start against a daemon
// that was already in full mode.
//
// It re-runs the auto-start sweep — the same sweep the boot path runs, behind
// the same TWO gates. Honouring `[agents] autostart = false` here is not
// defensive tidiness: without it, a stop-orchestration followed by a
// start-orchestration would spawn a fleet on a daemon whose boot path
// deliberately refuses to, so a mode round-trip would become a side door around
// the config (gh #108).
//
// The `configured` gate is the same argument one step further out, and it
// became load-bearing when this sweep moved onto the common path: pogod's boot
// skips prompt refresh and auto-start entirely when no config file exists, so
// that an isolated or unconfigured daemon (tests, CI, POGO_HOME sandboxes)
// cannot put an unrequested fleet on the machine (mg-3dc3). A `pogo server
// start` that swept unconditionally would hand exactly that daemon the side
// door the boot path closed.
//
// Both gates are read at call time rather than captured as bools, so they
// reflect the config the daemon is running under when the start happens, not
// the one it happened to boot with.
func agentStarterFor(configured func() bool, enabled func() bool, sweep func() []agent.AutoStartResult) server.AgentStarter {
	return func() server.AgentStartOutcome {
		if !configured() {
			log.Printf("pogod: start — %s; not starting any agents", unconfiguredReason)
			return server.AgentStartOutcome{Skipped: unconfiguredReason}
		}
		if !enabled() {
			log.Printf("pogod: start — crew auto-start disabled (%s); not starting any agents",
				autoStartDisabledReason)
			return server.AgentStartOutcome{Skipped: autoStartDisabledReason}
		}
		return server.AgentStartOutcome{Results: sweep()}
	}
}
