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

// TestActivationLineWarnsOnScheduleDrift is the ticket, at the surface an
// operator reads. The row has to lead with the FIRES: a plist whose log path
// moved and a plist missing two of its three fires are both "stale", and only
// the second means the job is doing a fraction of what the code believes.
func TestActivationLineWarnsOnScheduleDrift(t *testing.T) {
	status, detail := launchAgentActivationLine([]service.LaunchAgentAudit{
		cleanAudit("com.pogo.daemon"),
		staleDeployAudit(),
	}, true)

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
		status, detail := launchAgentActivationLine(audits, true)
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
	_, notApplicable := launchAgentActivationLine(nil, false)
	if !strings.Contains(notApplicable, "not applicable") || !strings.Contains(notApplicable, "This is not a report") {
		t.Errorf("unsupported-platform detail = %q, want it to disclaim rather than read as a pass", notApplicable)
	}

	status, empty := launchAgentActivationLine(nil, true)
	if status != "warn" || !strings.Contains(empty, "NOT CHECKED") {
		t.Errorf("empty audit on a supported platform = %q/%q, want warn + NOT CHECKED", status, empty)
	}

	_, absent := launchAgentActivationLine([]service.LaunchAgentAudit{
		cleanAudit("com.pogo.daemon"),
		{Label: "com.pogo.deploy", Status: service.LaunchAgentAbsent, Remedy: "pogo service install-deploy", Detail: "not installed"},
	}, true)
	if !strings.Contains(absent, "com.pogo.deploy") || !strings.Contains(absent, "never installed") {
		t.Errorf("absent-job detail = %q, want it to name the uninstalled job and say the audit has no opinion on it", absent)
	}

	_, clean := launchAgentActivationLine([]service.LaunchAgentAudit{cleanAudit("com.pogo.daemon")}, true)
	if strings.Contains(clean, "NOT CHECKED") || !strings.Contains(clean, "matches the plist this build renders") {
		t.Errorf("clean detail = %q, want a positive statement about what was compared", clean)
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
