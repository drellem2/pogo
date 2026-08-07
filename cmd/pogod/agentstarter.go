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

// agentStarterFor builds the server's AgentStarter: the agent-side analogue of
// the RefineryStarter, invoked when the daemon returns to full mode.
//
// It re-runs the auto-start sweep — the same sweep the boot path runs, with the
// same gate. Honouring `[agents] autostart = false` here is not defensive
// tidiness: without it, a stop-orchestration followed by a start-orchestration
// would spawn a fleet on a daemon whose boot path deliberately refuses to, so
// a mode round-trip would become a side door around the config (gh #108).
//
// enabled is read at call time rather than captured as a bool so the gate
// reflects the config the daemon is running under when the transition happens,
// not the one it happened to boot with.
func agentStarterFor(enabled func() bool, sweep func() []agent.AutoStartResult) server.AgentStarter {
	return func() server.AgentStartOutcome {
		if !enabled() {
			log.Printf("pogod: orchestration restart — crew auto-start disabled (%s); not starting any agents",
				autoStartDisabledReason)
			return server.AgentStartOutcome{Skipped: autoStartDisabledReason}
		}
		return server.AgentStartOutcome{Results: sweep()}
	}
}
