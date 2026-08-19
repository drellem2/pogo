package service

// The activation audit's own denominator (mg-7a20).
//
// WHAT WENT WRONG. The audit in launchagentaudit.go ranges over
// managedLaunchAgents(), which held three jobs, and the doctor row rendered
// "3 managed job(s) examined: 3 match this build". That sentence is true, it is
// clean, and on the box it was measured on there were THIRTEEN pogo launchd jobs
// loaded. A reader takes "3 of 3 match" for a pass over launchd activation; it
// is a pass over the fraction of it that happens to be enumerated. The audit's
// SCOPE had drifted and, exactly like the schedule drift it was built to catch,
// the drift left no artifact — the census was complete-looking either way.
//
// WHY THE FIX IS NOT "ADD THE OTHER TEN". The two counts are not one population
// measured twice. The REGISTRY grows only when someone adds a job to
// internal/service; the BOX's count grows on any install path — another repo's
// install.sh, pogo-reminders, a hand-written plist. A hand-maintained list of
// ten labels is correct the day it is written and silently wrong afterwards,
// producing the identical complete-looking output. Extending coverage is a
// separate, larger piece of work (it needs a Go-side render per job, and for
// anything the deploy installs it inherits mg-a03d's objection). This file does
// not attempt it.
//
// WHAT IT DOES INSTEAD: STATE THE DENOMINATOR AGAINST SOMETHING OBSERVED. The
// registry is a DECLARATION; `launchctl list` is an OBSERVATION. Comparing the
// two turns a silent exclusion into a stated one, and — the part that survives
// the population moving — a job that arrives by a path nobody here knows about
// shows up as UNEXPLAINED rather than as nothing at all. That is the asymmetry
// the whole file rests on: the reason table below is hand-maintained, like every
// declaration, but its DEFAULT IS LOUD. A label missing from it is reported, not
// omitted. A hand-maintained list whose absences are visible does not have the
// failure mode of one whose absences are silent.
//
// WHAT THIS OBSERVATION ITSELF CANNOT SEE, said out loud for the same reason the
// audit says NOT CHECKED out loud — this scope check is an artifact of the same
// kind as the defect it reports, so it has a scope of its own:
//
//   - It reads LOADED jobs. A plist sitting in ~/Library/LaunchAgents that was
//     never bootstrapped is outside even this denominator. "Loaded" is the word
//     used in the output for that reason; it is not a synonym for "installed".
//   - It reads the CURRENT user's domain, which is where `launchctl list` looks.
//     A job bootstrapped into system/ or another uid's gui/ is invisible here.
//   - It matches the `com.pogo.` label prefix. A pogo-related job labelled
//     anything else is not counted, and nothing here can know it exists.
//   - When launchctl cannot be run or its output cannot be parsed, the result is
//     NOT OBSERVED and never "0 outside the registry". An unavailable signal
//     that renders as a clean one is precisely the defect this file exists for.

import (
	"os/exec"
	"sort"
	"strings"
)

// pogoJobPrefix is the label prefix that makes a launchd job one of pogo's.
//
// This is the seam where this check's own scope is narrowest, so it is a named
// constant rather than an inline string: whatever it misses, it misses silently,
// and the output says so.
const pogoJobPrefix = "com.pogo."

// LaunchAgentExclusion is one loaded pogo job that the audit did not examine.
//
// Reason is empty when nobody has recorded why. That is not a defect in this
// type — it is the finding. Seven of the ten uncovered jobs on the box this was
// measured on had no recorded reason at all, and they are the actionable half:
// an exclusion with a reason is a decision, an exclusion without one is a job
// somebody added while nobody was looking at the auditor.
type LaunchAgentExclusion struct {
	Label  string
	Reason string
}

// Explained reports whether anyone recorded why this job is outside the audit.
func (e LaunchAgentExclusion) Explained() bool { return e.Reason != "" }

// LaunchAgentScope is the audit's declared population set against the observed
// one. It is deliberately separate from LaunchAgentAudit: an audit is a verdict
// about one job, and this is a verdict about the set of verdicts.
type LaunchAgentScope struct {
	// Observed is false when the loaded jobs could not be enumerated. Every
	// other field is meaningless then, and callers must render NOT OBSERVED
	// rather than any count.
	Observed bool
	// ObserveNote explains an Observed=false, in words an operator can act on.
	ObserveNote string
	// Loaded is every com.pogo.* label launchd has loaded in this user's domain.
	Loaded []string
	// Audited is the labels the registry actually examined, intersected with
	// Loaded — a registry job that is not loaded is reported by the audit's own
	// "not installed" state and must not inflate the covered count here.
	Audited []string
	// Excluded is the loaded jobs the registry did not examine, each with its
	// recorded reason or without one.
	Excluded []LaunchAgentExclusion
}

// Unexplained returns the excluded jobs nobody has recorded a reason for.
func (s LaunchAgentScope) Unexplained() []LaunchAgentExclusion {
	var out []LaunchAgentExclusion
	for _, e := range s.Excluded {
		if !e.Explained() {
			out = append(out, e)
		}
	}
	return out
}

// Reasons a job is outside the registry, and what they are worth.
//
// Every one of these says the same structural thing: ANOTHER REPO OWNS THE
// PLIST, so this build has nothing to render as the expected copy, and the byte
// comparison the audit is made of has no second operand. That is a real reason
// and not a restatement of "it is not in the registry" — it says why putting it
// in the registry is not a small edit.
//
// HOW THESE ROT, and why that is survivable. An entry is an attribution: it
// names who installs the job. If ownership moves to a third repo the attribution
// goes stale while the half that matters — this build does not render it, so it
// is not audited — stays true. And if ownership moves INTO internal/service, the
// job joins the registry, gets audited, and never reaches this table at all; a
// stale entry for an audited job is unreachable rather than wrong. The failure
// this table cannot have is the one it was built to prevent: a job with no entry
// is reported, loudly, as unexplained.
const (
	ownedByReminders = "installed by pogo-reminders, not by any installer in this repo (`internal/service` renders no plist for it), so this build has no expected copy to compare the installed one against"
	ownedByPogoPA    = "installed by pogo-pa, not by any installer in this repo, so this build has no expected copy to compare the installed one against"
	ownedBySleepwake = "installed by pogo-sleepwake, not by any installer in this repo, so this build has no expected copy to compare the installed one against"
	ownedByBridget   = "installed by the bridget repo (cloverross/bridget, see docs/design/bridget-integration-design.md), not by any installer in this repo, so this build has no expected copy to compare the installed one against"
)

func launchAgentExclusionReasons() map[string]string {
	return map[string]string{
		// Deliberately out, with the decision recorded at the registry itself —
		// see managedLaunchAgent's comment in launchagentaudit.go.
		"com.pogo.revisionprobe": "deliberately outside the registry (mg-a03d): it is rendered by scripts/install-revision-probe.sh from a tracked plist, because everything in `internal/service` ships in the pogo BINARY and a Go-rendered row would make the auditor for the deploy witness arrive BY the deploy",

		// Same argument as revisionprobe's, one level out: that job may not be
		// armed BY the deploy it watches, and this one may not be armed by
		// anything the FLEET provides. `internal/service` ships in the pogo
		// binary, which a working deploy installs and which an agent turn is
		// what anybody uses — so a Go-rendered row would make the auditor for
		// the fleet-stop witness arrive by the fleet.
		"com.pogo.fleetliveness": "deliberately outside the registry (mg-f867): it is rendered by scripts/install-fleet-liveness-probe.sh from a tracked plist, because everything in `internal/service` ships in the pogo BINARY and this witness must be armable on a box whose fleet has been dead for five days — the condition it exists to report",

		"com.pogo.notify":    ownedByReminders,
		"com.pogo.deadman":   ownedByReminders,
		"com.pogo.watchdog":  ownedByReminders,
		"com.pogo.wifi":      ownedByReminders,
		"com.pogo.gh-issues": ownedByReminders,

		"com.pogo.pa-calendar": ownedByPogoPA,
		"com.pogo.pa-heyfeed":  ownedByPogoPA,

		"com.pogo.sleepwake": ownedBySleepwake,

		"com.pogo.bridget": ownedByBridget,
	}
}

// ScopeLaunchAgents sets the audit's examined set against the pogo jobs launchd
// actually has loaded.
//
// Returns Observed=false on a platform with no LaunchAgents, for the same reason
// AuditLaunchAgents returns nil there: an empty loaded set and an unreadable one
// must never render as the same thing.
func ScopeLaunchAgents(audits []LaunchAgentAudit) LaunchAgentScope {
	if !LaunchAgentsSupported() {
		return LaunchAgentScope{ObserveNote: "no launchd on this platform"}
	}
	loaded, err := loadedPogoJobs()
	if err != nil {
		return LaunchAgentScope{ObserveNote: err.Error()}
	}
	return scopeLaunchAgents(audits, loaded)
}

// scopeLaunchAgents is the whole comparison as a pure function, so the interesting
// cases — a job nobody recorded a reason for, a registry job that is not loaded —
// are testable without a launchd to load anything into. The audit next door made
// the same choice for the same reason.
func scopeLaunchAgents(audits []LaunchAgentAudit, loaded []string) LaunchAgentScope {
	inRegistry := make(map[string]bool, len(audits))
	for _, a := range audits {
		inRegistry[a.Label] = true
	}
	reasons := launchAgentExclusionReasons()

	s := LaunchAgentScope{Observed: true, Loaded: append([]string(nil), loaded...)}
	sort.Strings(s.Loaded)
	for _, label := range s.Loaded {
		if inRegistry[label] {
			s.Audited = append(s.Audited, label)
			continue
		}
		s.Excluded = append(s.Excluded, LaunchAgentExclusion{Label: label, Reason: reasons[label]})
	}
	return s
}

// loadedPogoJobs asks launchd what it has loaded.
//
// Shelling out is the only way to ask — the loaded set is launchd's own state
// and exists in no file. That is why the parse below is a separate pure function
// and this wrapper is three lines: the part that can be got wrong is testable,
// and the part that cannot be tested off a Mac does nothing but hand over bytes.
func loadedPogoJobs() ([]string, error) {
	out, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return nil, &observeError{err}
	}
	return parseLaunchctlList(string(out)), nil
}

type observeError struct{ err error }

func (e *observeError) Error() string {
	return "`launchctl list` could not be run (" + e.err.Error() + "), so the set of pogo jobs actually loaded on this box is unknown"
}

func (e *observeError) Unwrap() error { return e.err }

// parseLaunchctlList pulls the pogo labels out of `launchctl list` output.
//
// The format is three tab-separated columns — PID, last exit status, label —
// under a header line, with "-" in either numeric column for a job that is
// loaded but not currently running. Only the label is read: a job's pid and exit
// status say nothing about whether the audit examined it, and reading them here
// would invite the row to grow a second opinion about job health that some other
// detector already owns.
func parseLaunchctlList(out string) []string {
	var labels []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		label := fields[len(fields)-1]
		if strings.HasPrefix(label, pogoJobPrefix) {
			labels = append(labels, label)
		}
	}
	return labels
}
