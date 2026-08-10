package main

// Tests for the "launchd activation" doctor row (mg-fc99).

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/service"
)

func staleDeployAudit() service.LaunchAgentAudit {
	return service.LaunchAgentAudit{
		Label:         "com.pogo.deploy",
		Path:          "/Users/x/Library/LaunchAgents/com.pogo.deploy.plist",
		Status:        service.LaunchAgentStale,
		ScheduleDrift: true,
		Remedy:        "pogo service install-deploy",
		Detail:        "installed 03:00, expected 03:00, 04:00, 05:00 — INERT — run `pogo service install-deploy`",
	}
}

func cleanAudit(label string) service.LaunchAgentAudit {
	return service.LaunchAgentAudit{Label: label, Status: service.LaunchAgentOK, Detail: "matches this build (03:00)"}
}

// coveredScope is a box where every loaded pogo job is in the registry. It is the
// scope the pre-mg-7a20 row implicitly assumed on every box, which is the whole
// reason it read as a pass over launchd activation; tests that are about drift
// rather than about scope use it so the two subjects do not confound.
func coveredScope(labels ...string) service.LaunchAgentScope {
	return service.LaunchAgentScope{Observed: true, Loaded: labels, Audited: labels}
}

// TestActivationLineWarnsOnScheduleDrift is the ticket, at the surface an
// operator reads. The row has to lead with the FIRES: a plist whose log path
// moved and a plist missing two of its three fires are both "stale", and only
// the second means the job is doing a fraction of what the code believes.
func TestActivationLineWarnsOnScheduleDrift(t *testing.T) {
	status, detail := launchAgentActivationLine([]service.LaunchAgentAudit{
		cleanAudit("com.pogo.daemon"),
		staleDeployAudit(),
	}, true, coveredScope("com.pogo.daemon", "com.pogo.deploy"))

	if status != "warn" {
		t.Fatalf("status = %q, want warn; detail = %q", status, detail)
	}
	if !strings.Contains(detail, "FIRE AT DIFFERENT TIMES") {
		t.Errorf("detail = %q, want the schedule drift to lead", detail)
	}
	for _, want := range []string{"com.pogo.deploy", "04:00", "pogo service install-deploy"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail does not mention %q; detail = %q", want, detail)
		}
	}
	if !strings.Contains(detail, "2 managed job(s) examined") {
		t.Errorf("detail = %q, want the population it examined — a warning about one job says nothing about the others", detail)
	}
}

// TestActivationLineNeverFails pins that this is a detector, not a gate. `fail`
// sets doctor's exit code, and reconciling a plist is a machine-local ops action
// with a blast radius; nobody scripting doctor's exit status asked to be blocked
// on somebody else's decision to run an installer.
func TestActivationLineNeverFails(t *testing.T) {
	for _, audits := range [][]service.LaunchAgentAudit{
		{staleDeployAudit()},
		{{Label: "com.pogo.daemon", Status: service.LaunchAgentUnknown, Detail: "NOT CHECKED: ..."}},
		{{Label: "com.pogo.deploy", Status: service.LaunchAgentAbsent, Remedy: "pogo service install-deploy", Detail: "not installed"}},
		{cleanAudit("com.pogo.daemon")},
		nil,
	} {
		status, detail := launchAgentActivationLine(audits, true, coveredScope("com.pogo.daemon", "com.pogo.deploy"))
		if status != "warn" && status != "pass" {
			t.Errorf("status = %q for %+v (detail = %q); this row must only ever pass or warn", status, audits, detail)
		}
	}
}

// TestActivationLineSaysWhenItCheckedNothing. Three of this row's states are
// "no drift to report" and only one of them means the plists were compared and
// matched. Rendering them identically would reproduce, one level up, the exact
// defect the row exists to catch.
func TestActivationLineSaysWhenItCheckedNothing(t *testing.T) {
	_, notApplicable := launchAgentActivationLine(nil, false, service.LaunchAgentScope{})
	if !strings.Contains(notApplicable, "not applicable") || !strings.Contains(notApplicable, "This is not a report") {
		t.Errorf("unsupported-platform detail = %q, want it to disclaim rather than read as a pass", notApplicable)
	}

	status, empty := launchAgentActivationLine(nil, true, coveredScope())
	if status != "warn" || !strings.Contains(empty, "NOT CHECKED") {
		t.Errorf("empty audit on a supported platform = %q/%q, want warn + NOT CHECKED", status, empty)
	}

	_, absent := launchAgentActivationLine([]service.LaunchAgentAudit{
		cleanAudit("com.pogo.daemon"),
		{Label: "com.pogo.deploy", Status: service.LaunchAgentAbsent, Remedy: "pogo service install-deploy", Detail: "not installed"},
	}, true, coveredScope("com.pogo.daemon", "com.pogo.deploy"))
	if !strings.Contains(absent, "com.pogo.deploy") || !strings.Contains(absent, "never installed") {
		t.Errorf("absent-job detail = %q, want it to name the uninstalled job and say the audit has no opinion on it", absent)
	}

	_, clean := launchAgentActivationLine([]service.LaunchAgentAudit{cleanAudit("com.pogo.daemon")}, true, coveredScope("com.pogo.daemon"))
	if strings.Contains(clean, "NOT CHECKED") || !strings.Contains(clean, "matches the plist this build renders") {
		t.Errorf("clean detail = %q, want a positive statement about what was compared", clean)
	}
}

// TestActivationLineStatesBothNumbers is mg-7a20, at the surface an operator
// reads. The pre-fix row said "3 managed job(s) examined: 3 match this build" on
// a box with thirteen pogo jobs loaded — a complete-looking census of a third of
// the subject. The gap has to be IN the output, not derivable by a reader who
// thinks to run `launchctl list` and subtract.
func TestActivationLineStatesBothNumbers(t *testing.T) {
	audits := []service.LaunchAgentAudit{
		cleanAudit("com.pogo.daemon"), cleanAudit("com.pogo.recovery"), cleanAudit("com.pogo.deploy"),
	}
	scope := service.LaunchAgentScope{
		Observed: true,
		Loaded:   []string{"com.pogo.daemon", "com.pogo.recovery", "com.pogo.deploy", "com.pogo.notify", "com.pogo.zzz-new"},
		Audited:  []string{"com.pogo.daemon", "com.pogo.recovery", "com.pogo.deploy"},
		Excluded: []service.LaunchAgentExclusion{
			{Label: "com.pogo.notify", Reason: "installed by pogo-reminders"},
			{Label: "com.pogo.zzz-new"},
		},
	}

	status, detail := launchAgentActivationLine(audits, true, scope)

	if !strings.Contains(detail, "3 of 5 pogo launchd job(s) LOADED") {
		t.Errorf("detail = %q, want examined-of-loaded stated as one number pair", detail)
	}
	if !strings.Contains(detail, "2 outside it — 1 with a recorded reason, 1 with NONE") {
		t.Errorf("detail = %q, want the explained/unexplained split", detail)
	}
	if !strings.Contains(detail, "com.pogo.zzz-new") {
		t.Errorf("detail = %q, want the unexplained job NAMED; a count nobody can act on is not the actionable half", detail)
	}
	if strings.Contains(detail, "com.pogo.notify") {
		t.Errorf("detail = %q, want explained exclusions counted but not listed — naming ten settled decisions on every clean run is how the unexplained one gets skimmed past", detail)
	}
	if status != "warn" {
		t.Errorf("status = %q, want warn: every plist compared matched, but a loaded pogo job nobody has ruled on is not a pass over launchd activation", status)
	}
}

// TestActivationLinePassesWhenEveryExclusionIsExplained. The warn above must be
// silenceable by recording the reason, or the row degrades into a permanent
// warning that operators learn to ignore — and a detector nobody reads is the
// state this whole file exists to prevent.
func TestActivationLinePassesWhenEveryExclusionIsExplained(t *testing.T) {
	status, detail := launchAgentActivationLine([]service.LaunchAgentAudit{cleanAudit("com.pogo.daemon")}, true,
		service.LaunchAgentScope{
			Observed: true,
			Loaded:   []string{"com.pogo.daemon", "com.pogo.notify"},
			Audited:  []string{"com.pogo.daemon"},
			Excluded: []service.LaunchAgentExclusion{{Label: "com.pogo.notify", Reason: "installed by pogo-reminders"}},
		})

	if status != "pass" {
		t.Errorf("status = %q, want pass once every exclusion carries a reason; detail = %q", status, detail)
	}
	if !strings.Contains(detail, "1 of 2 pogo launchd job(s) LOADED") {
		t.Errorf("detail = %q, want the denominator stated on the CLEAN row too — a scope sentence that appears only when the scope is bad is invisible in exactly the way a drifted scope is", detail)
	}
	if !strings.Contains(detail, "1 with a recorded reason, 0 with NONE") {
		t.Errorf("detail = %q, want the split stated even when nothing is unexplained", detail)
	}
}

// TestActivationLineDisclaimsAnUnobservedScope. The remedy is an artifact of the
// same kind as the defect: if the observation fails and the row silently prints
// the registry size as though it were a share of the box, mg-7a20 is back with an
// extra step in front of it.
func TestActivationLineDisclaimsAnUnobservedScope(t *testing.T) {
	status, detail := launchAgentActivationLine([]service.LaunchAgentAudit{cleanAudit("com.pogo.daemon")}, true,
		service.LaunchAgentScope{ObserveNote: "`launchctl list` could not be run (exec: not found)"})

	if !strings.Contains(detail, "SCOPE NOT OBSERVED") {
		t.Errorf("detail = %q, want the failed observation said out loud", detail)
	}
	if !strings.Contains(detail, "REGISTRY size") {
		t.Errorf("detail = %q, want the surviving count named as a registry size rather than a share of the box", detail)
	}
	if status != "warn" {
		t.Errorf("status = %q, want warn: an unavailable signal must not render as a clean one", status)
	}
}

// TestActivationLineNamesAnExaminedButUnloadedJob. Examined and loaded are two
// different sets, not one measured twice: a registry job that is not loaded makes
// the two counts disagree, and a reader who subtracts them without being told why
// concludes the row is broken and stops reading it.
func TestActivationLineNamesAnExaminedButUnloadedJob(t *testing.T) {
	_, detail := launchAgentActivationLine([]service.LaunchAgentAudit{
		cleanAudit("com.pogo.daemon"),
		{Label: "com.pogo.deploy", Status: service.LaunchAgentAbsent, Remedy: "pogo service install-deploy", Detail: "not installed"},
	}, true, service.LaunchAgentScope{
		Observed: true,
		Loaded:   []string{"com.pogo.daemon"},
		Audited:  []string{"com.pogo.daemon"},
	})

	if !strings.Contains(detail, "1 more examined but not loaded") {
		t.Errorf("detail = %q, want the registry job that is not loaded accounted for", detail)
	}
}

// TestActivationLineStatesItsOwnBlindSpots. The observed half has a scope too —
// it reads LOADED jobs in ONE domain under ONE label prefix. Leaving that unsaid
// would be the same trade this ticket was filed to undo, one level further down.
func TestActivationLineStatesItsOwnBlindSpots(t *testing.T) {
	_, detail := launchAgentActivationLine([]service.LaunchAgentAudit{cleanAudit("com.pogo.daemon")}, true,
		coveredScope("com.pogo.daemon"))

	for _, want := range []string{"never bootstrapped", "another domain", "different label"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to disclaim %q — what this comparison cannot see", detail, want)
		}
	}
}

// TestDoctorCheck_LaunchAgentActivationLineIsPresent runs the real compiled
// binary. The row must appear on every run whatever this machine's plists say —
// a detector that renders nothing when it has nothing to report is invisible in
// exactly the way its subject fails.
func TestDoctorCheck_LaunchAgentActivationLineIsPresent(t *testing.T) {
	line, ok := doctorChecks(t, nil)[launchAgentCheckName]
	if !ok {
		t.Fatalf("no %q row in doctor --check; the detector must be visible even when it finds nothing", launchAgentCheckName)
	}
	// Status is deliberately not asserted: it is a fact about whichever box runs
	// the suite. What must hold everywhere is that the row says something.
	if parts := strings.SplitN(line, "\t", 2); len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		t.Errorf("row = %q, want a non-empty detail", line)
	}
	if strings.HasPrefix(line, "fail\t") {
		t.Errorf("row = %q; this detector must never set doctor's exit code", line)
	}
}
