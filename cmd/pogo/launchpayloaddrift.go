package main

// The "launchd payload" row in `pogo doctor --check` (mg-30f8).
//
// WHY IT IS A SECOND ROW AND NOT MORE WORDS ON THE FIRST. The `launchd activation` row
// answers "does the installed plist match the plist this build renders". This one
// answers "does the file that plist POINTS AT match the file this build ships". They
// are different questions with different answers, and the state that produced this row
// is exactly the one a merged row would have averaged away: on 2026-09-08 every
// managed plist on the reference box was `ok` while the runner com.pogo.deploy
// executes was three weeks and 1191 lines stale. One row reading "3 of 4 clean" over
// both subjects is a sentence nobody can act on; two rows, one clean and one not, is.
//
// WHY IT WARNS AND NEVER FAILS. Same reason as the row next door: `fail` sets doctor's
// exit code, reconciling is a machine-local action with a blast radius (`install-deploy`
// boots out the nightly it would be running inside), and whoever scripts doctor's exit
// status did not ask to be blocked on somebody else's decision to run an installer. The
// exit code lives on `pogo check-activation`, which has a caller.
//
// AND IT INHERITS THE CIRCULARITY, at one more remove than the plist row does. This
// detector ships in the `pogo` binary; the binary is installed by the nightly; the
// nightly is the stale runner. So the detector for "the runner is stale" arrives BY the
// stale runner — and on the box this was written for, it had not arrived for three
// weeks. An absent `launchd payload` row means an old binary, never a clean box, and
// that cannot be disclaimed by the build that lacks the row.

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/drellem2/pogo/internal/service"
)

// launchPayloadCheckName is the doctor checklist row this renders on.
const launchPayloadCheckName = "launchd payload"

// launchPayloadLine renders one doctor check row from the payload audits.
//
// Takes the platform verdict separately so the "not applicable" branch is reachable in
// a test on the platform where it is in fact applicable — the same seam
// launchAgentActivationLine uses, for the same reason.
func launchPayloadLine(audits []service.PayloadScriptAudit, supported bool) (status, detail string) {
	if !supported {
		return "pass", fmt.Sprintf("not applicable on %s: the managed payload scripts are installed alongside launchd LaunchAgents, which exist only on macOS. This is not a report that any installed script is current", runtime.GOOS)
	}
	if len(audits) == 0 {
		return "warn", "NOT CHECKED: no managed payload script was examined, so nothing here says the file a launchd job EXECUTES matches the code that ships it"
	}

	var orphan, stale, unknown, absent, ok []service.PayloadScriptAudit
	for _, a := range audits {
		switch a.Status {
		case service.PayloadOrphan:
			orphan = append(orphan, a)
		case service.PayloadStale:
			stale = append(stale, a)
		case service.PayloadUnknown:
			unknown = append(unknown, a)
		case service.PayloadAbsent:
			absent = append(absent, a)
		default:
			ok = append(ok, a)
		}
	}

	// "could not be checked" rather than the literal NOT CHECKED phrase, for the
	// reason the plist row gives: the population renders on the clean line too, and a
	// clean line carrying the disclaimer verbatim is indistinguishable from a real
	// one to anything grepping for it, this file's own tests included.
	population := fmt.Sprintf("%d installed payload script(s) examined: %d match this build, %d drifted, %d orphaned, %d not installed, %d could not be checked",
		len(audits), len(ok), len(stale), len(orphan), len(absent), len(unknown))
	population += ". A payload script is COPIED into place by `pogo service install-*`; a merge does not refresh it, so the plist audit passing says nothing about this one"

	// Orphans lead. A drifted script runs old code; an orphaned one does not run at
	// all, and the exec failure is not a pogo log line.
	var lines []string
	for _, a := range orphan {
		lines = append(lines, fmt.Sprintf("%s/%s — %s", a.Label, a.Name, a.Detail))
	}
	for _, a := range stale {
		lines = append(lines, fmt.Sprintf("%s/%s — %s", a.Label, a.Name, a.Detail))
	}
	for _, a := range unknown {
		lines = append(lines, fmt.Sprintf("%s/%s — %s", a.Label, a.Name, a.Detail))
	}

	if len(lines) > 0 {
		lead := fmt.Sprintf("%d installed payload script(s) disagree with the code that ships them", len(stale)+len(unknown))
		if len(orphan) > 0 {
			lead = fmt.Sprintf("%d launchd job(s) name a program THAT IS NOT THERE", len(orphan))
		}
		return "warn", fmt.Sprintf("%s: %s. %s", lead, strings.Join(lines, "; "), population)
	}

	if len(absent) > 0 {
		names := make([]string, 0, len(absent))
		for _, a := range absent {
			names = append(names, fmt.Sprintf("%s (`%s`)", a.Name, a.Remedy))
		}
		return "pass", fmt.Sprintf("every installed payload script is byte-identical to the copy this build ships; %s not installed at all, alongside a job that is also not installed. %s",
			strings.Join(names, ", "), population)
	}

	return "pass", fmt.Sprintf("every installed payload script is byte-identical to the copy this build ships. %s", population)
}

// payloadStateLabel is the fixed-width tag each payload row leads with in
// `pogo check-activation`. Written out rather than derived from the service constants so
// a column change here cannot silently rename a state a caller greps for — the same
// reason activationStateLabel is written out.
func payloadStateLabel(state string) string {
	switch state {
	case service.PayloadOK:
		return "OK      "
	case service.PayloadStale:
		return "STALE   "
	case service.PayloadOrphan:
		return "ORPHAN  " // the loud one: launchd fires a program that does not exist
	case service.PayloadAbsent:
		return "ABSENT  "
	default:
		return "UNKNOWN "
	}
}
