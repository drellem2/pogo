package main

// The "launchd activation" line in `pogo doctor --check` (mg-fc99).
//
// WHY IT REPORTS HERE. The thing being detected is an install that never ran, so
// it has to surface somewhere a person looks WITHOUT being prompted by the
// failure — the failure emits nothing. `pogo doctor --check` is the checklist
// the doctor agent runs first and the PM tier runs during sweeps, and it already
// carries the package's other detectors. Mail would have landed in the pile.
//
// WHY IT WARNS AND NEVER FAILS. `fail` sets doctor's exit code. A stale plist is
// a real defect, but reconciling it is a machine-local ops action with a blast
// radius (`pogo service install` bounces the daemon; `install-deploy` rewrites
// the nightly's schedule), and whoever is scripting doctor's exit status did not
// ask to be blocked on somebody else's decision to run it. Same reasoning as the
// audit-successor line: a detector that grows into a gate through the exit code
// is still a gate.
//
// WHAT IT DOES NOT DO, AND THE CIRCULARITY IN THAT (mg-b201). It reports; it
// never reconciles, and nothing else does either — no boot hook, no login hook,
// no step of the nightly redeploy re-asserts an installed plist against the
// shipped one. Reconciliation is a human running `pogo service install-deploy`,
// and that is recorded rather than fixed (see scripts/launchd/README.md, "What
// re-asserts an installed plist against the shipped one").
//
// The circularity is the part worth carrying: this row ships inside the `pogo`
// binary, so it is absent from every build predating it — which is to say the
// detector for "merged but not installed" is itself subject to being merged but
// not installed. On 2026-08-07 it was: the box's `pogo` was built 07-30, this
// check landed after, and `pogo doctor --check` printed no launchd row at all
// while the deploy plist had drifted to a single fire. Hence the NOT CHECKED
// state below being said out loud — but note that it cannot cover this case,
// because a binary without the row emits no row to disclaim. An absent `launchd
// activation` row means an old binary, not a clean one.
//
// WHY THE ROW RENDERS EVEN WHEN CLEAN. A row that appears only on drift is
// invisible in exactly the way its subject is: you cannot tell "no drift" from
// "the check stopped running" from "the check was never wired in". The four
// states — checked/clean, drifted, not-installed, NOT CHECKED — are each said out
// loud, and the last two are never phrased as the first.

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/drellem2/pogo/internal/service"
)

// launchAgentCheckName is the doctor checklist row this renders on.
const launchAgentCheckName = "launchd activation"

// launchAgentActivationLine renders one doctor check row from an audit.
//
// Takes the audits and the platform verdict separately so the "not applicable"
// branch is reachable in a test on the platform where it is, in fact, applicable
// — the alternative is a branch that only the CI box can exercise and only by
// being the wrong OS.
func launchAgentActivationLine(audits []service.LaunchAgentAudit, supported bool) (status, detail string) {
	if !supported {
		return "pass", fmt.Sprintf("not applicable on %s: the managed jobs are launchd LaunchAgents, which exist only on macOS. This is not a report that any scheduled job is active", runtime.GOOS)
	}
	if len(audits) == 0 {
		return "warn", "NOT CHECKED: no managed launchd job was examined, so nothing here says an installed plist matches the code that ships it"
	}

	var drifted, stale, unknown, absent, ok []service.LaunchAgentAudit
	for _, a := range audits {
		switch a.Status {
		case service.LaunchAgentStale:
			if a.ScheduleDrift {
				drifted = append(drifted, a)
			} else {
				stale = append(stale, a)
			}
		case service.LaunchAgentUnknown:
			unknown = append(unknown, a)
		case service.LaunchAgentAbsent:
			absent = append(absent, a)
		default:
			ok = append(ok, a)
		}
	}

	// "could not be checked" rather than "NOT CHECKED": the population renders on
	// every row including the clean one, and a clean line carrying the literal
	// disclaimer phrase would be indistinguishable from a real one to anything
	// grepping for it — including this file's own tests.
	population := fmt.Sprintf("%d managed job(s) examined: %d match this build, %d drifted, %d not installed, %d could not be checked",
		len(audits), len(ok), len(drifted)+len(stale), len(absent), len(unknown))

	// Schedule drift leads, always. A plist that differs in a log path is stale;
	// a plist that differs in its FIRES is a job doing a fraction of what the
	// code believes it does, with no log line for the part it skipped. Burying
	// the second among the first is how mg-8f7e stayed invisible for five days.
	var lines []string
	for _, a := range drifted {
		lines = append(lines, fmt.Sprintf("%s — %s", a.Label, a.Detail))
	}
	for _, a := range stale {
		lines = append(lines, fmt.Sprintf("%s — %s", a.Label, a.Detail))
	}
	for _, a := range unknown {
		lines = append(lines, fmt.Sprintf("%s — %s", a.Label, a.Detail))
	}

	if len(lines) > 0 {
		lead := "installed plist(s) disagree with the code that ships them"
		if len(drifted) > 0 {
			lead = fmt.Sprintf("%d job(s) FIRE AT DIFFERENT TIMES than the shipped code expects", len(drifted))
		}
		return "warn", fmt.Sprintf("%s: %s. %s", lead, strings.Join(lines, "; "), population)
	}

	if len(absent) > 0 {
		names := make([]string, 0, len(absent))
		for _, a := range absent {
			names = append(names, fmt.Sprintf("%s (`%s`)", a.Label, a.Remedy))
		}
		return "pass", fmt.Sprintf("every installed plist matches this build; %s not installed at all — this audit compares installed plists and says nothing about a job that was never installed. %s",
			strings.Join(names, ", "), population)
	}

	return "pass", fmt.Sprintf("every managed plist on disk matches the plist this build renders. %s", population)
}
