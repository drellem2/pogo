package main

// The "consumer source liveness" line in `pogo doctor --check` (mg-c2f5).
//
// WHY IT REPORTS HERE. The fault is a job that is loaded, healthy, polling on
// schedule and reading a directory nothing writes to. Nothing about it produces
// output — there is no error, no missed fire, no log line for mail that did not
// arrive — so it has to surface somewhere a person looks WITHOUT being prompted
// by the failure. `pogo doctor --check` is the checklist the doctor agent runs
// first and the PM tier runs during sweeps, and it already carries the package's
// other absence-detectors (see launchagentdrift.go, which chose it for the same
// reason and against the same alternative: mail would have landed in the pile).
//
// WHY IT WARNS AND NEVER FAILS. `fail` sets doctor's exit code. What to do about
// a starved consumer — finish a staged cutover, re-point it, retire it — is a
// decision with a blast radius that belongs to whoever owns the routing, and on
// 2026-08-09 three separate agents proposed the WRONG one of those three
// remedies for this very consumer. A detector that grows into a gate through the
// exit code is still a gate, and this one would be gating on somebody else's
// pending decision.
//
// WHY THE ROW RENDERS EVEN WHEN CLEAN, and why it renders the population. A row
// that appears only on a finding is invisible in exactly the way its subject is:
// you cannot tell "no starved consumer" from "the check stopped running" from
// "the check found nothing to examine". The distinction is the entire content of
// the ticket, so all four states — live, starved, missing, undetermined — are
// said out loud and counted, and the last is never phrased as the first.
//
// THE CIRCULARITY, said out loud because this row is subject to its own defect.
// It ships inside the `pogo` binary, so it is absent from every build predating
// it — the detector for "a consumer watching a dead source" is itself a consumer
// of plists, and can be absent, or pointed at a LaunchAgents directory that has
// none. An ABSENT `consumer source liveness` row means an old binary, not a
// clean machine; a present row that says NOT CHECKED means the sweep could not
// reach a verdict. Neither is a pass and neither is spelled like one.

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/drellem2/pogo/internal/sourcewatch"
)

// sourceLivenessCheckName is the doctor checklist row this renders on.
const sourceLivenessCheckName = "consumer source liveness"

// sourceLivenessLine renders one doctor check row from a sweep.
//
// Takes the platform verdict separately from the report so the "not applicable"
// branch is reachable in a test on the platform where it is, in fact,
// applicable — the same seam launchAgentActivationLine uses, for the same
// reason.
func sourceLivenessLine(rep sourcewatch.Report, supported bool) (status, detail string) {
	if !supported {
		return "pass", fmt.Sprintf("not applicable on %s: the consumers this compares are launchd LaunchAgents, which exist only on macOS. This is not a report that any consumer is reading live data", runtime.GOOS)
	}
	if rep.Err != nil {
		return "warn", fmt.Sprintf("NOT CHECKED: no consumer was examined (%v), so nothing here says any job is reading a source anything writes to", rep.Err)
	}
	if len(rep.Verdicts) == 0 {
		return "warn", "NOT CHECKED: no job was found declaring a comparable data source, so no consumer's configured source was compared against where data is actually arriving"
	}

	// The population renders on every row including the clean one. It says
	// "could not be judged" rather than the literal NOT CHECKED phrase, because
	// a clean line carrying that phrase would be indistinguishable from a real
	// disclaimer to anything grepping for it — including this file's own tests.
	population := fmt.Sprintf("%d consumer(s) examined over a %s window: %d reading live sources, %d starved, %d pointed at a missing directory, %d could not be judged",
		len(rep.Verdicts), rep.Window,
		rep.Count(sourcewatch.StatusLive),
		rep.Count(sourcewatch.StatusStarved),
		rep.Count(sourcewatch.StatusMissing),
		rep.Count(sourcewatch.StatusUndetermined))

	// Starvation leads, always. A consumer pointed at a directory that does not
	// exist is a visible mistake somebody will hit; a consumer pointed at a
	// directory that exists and is empty is the one that reads healthy from
	// every angle, and burying it under the others is how it ran 40 hours.
	var starved, missing, undetermined []string
	for _, v := range rep.Verdicts {
		switch v.Status {
		case sourcewatch.StatusStarved:
			starved = append(starved, v.Detail)
		case sourcewatch.StatusMissing:
			missing = append(missing, v.Detail)
		case sourcewatch.StatusUndetermined:
			undetermined = append(undetermined, v.Detail)
		}
	}

	if len(starved) > 0 {
		lead := fmt.Sprintf("%d consumer(s) are reading a source NOTHING IS WRITING TO while comparable sources receive", len(starved))
		lines := append(append([]string{}, starved...), missing...)
		return "warn", fmt.Sprintf("%s: %s. %s", lead, strings.Join(lines, "; "), population)
	}
	if len(missing) > 0 {
		return "warn", fmt.Sprintf("consumer(s) configured to read a directory that is not there: %s. %s", strings.Join(missing, "; "), population)
	}
	if len(undetermined) > 0 {
		return "warn", fmt.Sprintf("NOT CHECKED for %d of %d consumer(s): %s. %s",
			len(undetermined), len(rep.Verdicts), strings.Join(undetermined, "; "), population)
	}

	return "pass", fmt.Sprintf("every consumer's configured source has received data in the window — each one was compared against where data is actually arriving, not against its own poll loop. %s", population)
}
