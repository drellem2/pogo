// Command witnessfixture writes a polecat witness record for a live pid, using
// the SAME recorder pogod uses. Control scaffolding for
// scripts/pogo-self-deploy_live_test.sh (mg-65b2) — not a shipped binary.
//
// WHY THIS EXISTS RATHER THAN A HAND-WRITTEN JSON FIXTURE. A witness record is
// (pid, start_time), and the drain believes it only when start_time matches what
// `ps -o lstart=` reports for that pid RIGHT NOW — a bare pid is a false witness,
// because pids get recycled. So a fixture has to carry a real kernel start time,
// and the control has three ways to get one:
//
//	printf a JSON literal        — cannot: the start time is not knowable up front
//	convert `ps lstart` in shell — `date -j` is BSD-only and CI is ubuntu; and it
//	                               would be a SECOND implementation of the parse,
//	                               free to drift from the one under test
//	call the real recorder       — this
//
// The third is not merely the portable option, it is the honest one: the fixture
// is written by the code path pogod actually runs at spawn, so the control proves
// the drain reads what pogod WRITES rather than what this test's author believed
// pogod writes. If RecordPolecatWitness ever changes shape, this moves with it
// and the control keeps measuring the real seam instead of silently testing a
// format nothing produces any more.
//
// It lives under scripts/ and not cmd/ on purpose: build.sh compiles ./cmd/...
// and the deployer installs DEPLOYED_CMDS from the same place, so anything here
// stays out of both the artifact set and the drift check.
//
// Usage: witnessfixture <name> <pid> [work-item-id]
// Writes to the witness at $POGO_HOME (see config.PogoHome), like pogod does.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/drellem2/pogo/internal/agent"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: witnessfixture <name> <pid> [work-item-id]")
		os.Exit(2)
	}
	pid, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "witnessfixture: bad pid %q: %v\n", os.Args[2], err)
		os.Exit(2)
	}
	workItem := ""
	if len(os.Args) > 3 {
		workItem = os.Args[3]
	}
	// Fails loudly if the pid is not alive: RecordPolecatWitness probes its start
	// time, and a fixture that silently recorded an unprobeable process would
	// hand the control a witness the drain reads as DEAD — turning the
	// "polecats are alive, refuse" assertion into a vacuous pass.
	if err := agent.RecordPolecatWitness(os.Args[1], pid, workItem); err != nil {
		fmt.Fprintf(os.Stderr, "witnessfixture: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("recorded %s pid=%d at %s\n", os.Args[1], pid, agent.WitnessPath())
}
