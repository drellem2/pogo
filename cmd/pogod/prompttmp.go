package main

import (
	"log"

	"github.com/drellem2/pogo/internal/agent"
)

// sweepExpandedPromptsAtStartup reclaims the per-spawn prompt files of polecats
// that are gone, once, as pogod comes up.
//
// The mechanism that actually bounds that directory is Registry.Remove, which
// deletes a spawn's prompt when the agent leaves the fleet. This covers the one
// path no in-process callback can: a pogod that DIED while polecats were
// running never ran Remove for any of them. It is the same gap, at the same
// moment, for the same reason as startGitGC's startup sweep (mg-30d5 D3) — and
// it is deliberately not on that ticker and not behind [gitgc] enabled, because
// residue only appears when a pogod dies, so a pogod starting is exactly when
// there is something to reclaim and the only time there is.
//
// It is here, in the daemon, rather than on the library's spawn path, and that
// is a safety property: an empty live set is indistinguishable from a witness
// store this process cannot see, and a test binary has an empty witness (pinned
// POGO_HOME) alongside the real machine-wide $TMPDIR. See internal/agent/
// prompttmp.go — a sweep reachable from library code deletes the running
// fleet's prompts every time the suite runs.
//
// It runs the same instrument-first refusal as runGitGCSweep: a witness store
// that cannot be read cancels the sweep, because an unreadable store is not an
// empty fleet, and sweeping against a live set known to be missing survivors is
// how a polecat that outlived its pogod loses the prompt it is running on
// (mg-0130). Best-effort otherwise: nothing here may keep the daemon from
// starting (mg-5197).
func sweepExpandedPromptsAtStartup(reg *agent.Registry) {
	live, err := livePolecatSet(reg)
	if err != nil {
		log.Printf("pogod: expanded-prompt sweep skipped — cannot read polecat witness: %v", err)
		return
	}
	if n := agent.SweepExpandedPrompts(live); n > 0 {
		log.Printf("pogod: expanded-prompt sweep — removed %d file(s) in %s with no live owner",
			n, agent.PromptTempDir())
	}
}
