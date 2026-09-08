package main

import (
	"os"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/service"
)

func okScript(label, name string) service.PayloadScriptAudit {
	return service.PayloadScriptAudit{
		Label: label, Name: name, Path: "/tmp/bin/" + name, Source: "/src/" + name,
		Status: service.PayloadOK, InstalledLines: 100, SourceLines: 100,
		Detail: "installed copy at /tmp/bin/" + name + " is byte-identical to /src/" + name,
	}
}

func staleScript(label, name string) service.PayloadScriptAudit {
	return service.PayloadScriptAudit{
		Label: label, Name: name, Path: "/tmp/bin/" + name, Source: "/src/" + name,
		Status: service.PayloadStale, InstalledLines: 3953, SourceLines: 5024,
		MissingIDs: []string{"mg-19e4", "mg-a854"}, Remedy: "pogo service install-deploy",
		Detail: "THE FILE " + label + " EXECUTES IS NOT THE FILE THIS BUILD SHIPS: /tmp/bin/" + name + " differs",
	}
}

// The row's reason for existing: a box whose plists are all clean and whose runner is
// three weeks stale must WARN. Before this row that box passed every launchd check pogo
// had.
func TestPayloadRowWarnsOnAStaleInstalledScript(t *testing.T) {
	status, detail := launchPayloadLine([]service.PayloadScriptAudit{
		okScript("com.pogo.recovery", "pogo-recovery.sh"),
		staleScript("com.pogo.deploy", "pogo-deploy.sh"),
	}, true)

	if status != "warn" {
		t.Fatalf("status = %q, want warn — detail: %s", status, detail)
	}
	if !strings.Contains(detail, "pogo-deploy.sh") {
		t.Errorf("the finding does not name the drifted file:\n%s", detail)
	}
	// The population renders on every row, clean ones included: a count that appears
	// only on findings cannot be told from a check that stopped running.
	if !strings.Contains(detail, "2 installed payload script(s) examined") {
		t.Errorf("the population is missing from the warn line:\n%s", detail)
	}
	// And the row has to say WHY a passing plist row is not evidence about it, or a
	// reader who has just read "every managed plist matches this build" concludes the
	// box is current.
	if !strings.Contains(detail, "a merge does not refresh it") {
		t.Errorf("the row does not say why the plist audit passing means nothing here:\n%s", detail)
	}
}

// Orphans lead. A stale script runs old code; an orphaned one does not run at all, and
// launchd's exec failure is not a pogo log line.
func TestPayloadRowLeadsWithOrphansNotDrift(t *testing.T) {
	orphan := service.PayloadScriptAudit{
		Label: "com.pogo.reclaim", Name: "pogo-reclaim.sh", Path: "/tmp/bin/pogo-reclaim.sh",
		Status: service.PayloadOrphan, Remedy: "pogo service install-reclaim",
		Detail: "ORPHANED JOB: com.pogo.reclaim is installed and names /tmp/bin/pogo-reclaim.sh as its program, and there is NO FILE THERE",
	}
	_, detail := launchPayloadLine([]service.PayloadScriptAudit{
		staleScript("com.pogo.deploy", "pogo-deploy.sh"), orphan,
	}, true)

	if !strings.HasPrefix(detail, "1 launchd job(s) name a program THAT IS NOT THERE") {
		t.Errorf("the orphan does not lead:\n%s", detail)
	}
	orphanAt, staleAt := strings.Index(detail, "pogo-reclaim.sh"), strings.Index(detail, "pogo-deploy.sh")
	if orphanAt < 0 || staleAt < 0 || orphanAt > staleAt {
		t.Errorf("the orphan is not reported ahead of the drift:\n%s", detail)
	}
}

// An empty audit is NOT CHECKED, never a pass. Same argument as the plist row: an empty
// slice is indistinguishable from "checked, all clean", and on this subject that
// confusion IS the defect.
func TestPayloadRowEmptyAuditIsNotCheckedNotAPass(t *testing.T) {
	status, detail := launchPayloadLine(nil, true)
	if status != "warn" {
		t.Errorf("status = %q, want warn", status)
	}
	if !strings.Contains(detail, "NOT CHECKED") {
		t.Errorf("an empty audit does not say NOT CHECKED:\n%s", detail)
	}
}

// A source this build could not find has not been compared against anything, so it warns
// rather than passing quietly.
func TestPayloadRowUnfindableSourceWarns(t *testing.T) {
	status, detail := launchPayloadLine([]service.PayloadScriptAudit{{
		Label: "com.pogo.deploy", Name: "pogo-deploy.sh", Path: "/tmp/bin/pogo-deploy.sh",
		Status: service.PayloadUnknown,
		Detail: "NOT CHECKED: this build could not locate its copy of scripts/launchd/pogo-deploy.sh",
	}}, true)
	if status != "warn" {
		t.Errorf("status = %q, want warn — an uncomparable payload must not read as clean", status)
	}
	if !strings.Contains(detail, "could not be checked") {
		t.Errorf("the population does not carry the uncomparable count:\n%s", detail)
	}
}

func TestPayloadRowCleanBoxPasses(t *testing.T) {
	status, detail := launchPayloadLine([]service.PayloadScriptAudit{
		okScript("com.pogo.deploy", "pogo-deploy.sh"),
		okScript("com.pogo.deploy", "net-control.sh"),
	}, true)
	if status != "pass" {
		t.Fatalf("status = %q, want pass — detail: %s", status, detail)
	}
	if !strings.Contains(detail, "2 installed payload script(s) examined: 2 match this build") {
		t.Errorf("the clean line does not carry its population:\n%s", detail)
	}
}

// A script absent alongside a job that is also absent is consistent, and it passes —
// but the sentence must say so rather than reading as a full clean bill.
func TestPayloadRowAbsentAlongsideAnAbsentJobPasses(t *testing.T) {
	status, detail := launchPayloadLine([]service.PayloadScriptAudit{
		okScript("com.pogo.deploy", "pogo-deploy.sh"),
		{
			Label: "com.pogo.reclaim", Name: "pogo-reclaim.sh", Path: "/tmp/bin/pogo-reclaim.sh",
			Status: service.PayloadAbsent, Remedy: "pogo service install-reclaim",
			Detail: "not installed: no file at /tmp/bin/pogo-reclaim.sh",
		},
	}, true)
	if status != "pass" {
		t.Fatalf("status = %q, want pass — detail: %s", status, detail)
	}
	if !strings.Contains(detail, "pogo-reclaim.sh") || !strings.Contains(detail, "not installed at all") {
		t.Errorf("the absent script is not named on the pass line:\n%s", detail)
	}
}

func TestPayloadRowNotApplicableOffDarwin(t *testing.T) {
	status, detail := launchPayloadLine(nil, false)
	if status != "pass" {
		t.Errorf("status = %q, want pass on an unsupported platform", status)
	}
	if !strings.Contains(detail, "not a report that any installed script is current") {
		t.Errorf("the not-applicable line reads as a clean bill:\n%s", detail)
	}
}

// The wiring. launchPayloadLine can be perfect while nothing ever renders it — and a
// detector nobody runs is the shape of the defect this row reports.
func TestPayloadRowIsWiredIntoDoctorAndCheckActivation(t *testing.T) {
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(main), "launchPayloadLine(service.AuditPayloadScripts()") {
		t.Error("`pogo doctor --check` does not render the launchd payload row — the audit exists and nothing calls it")
	}

	ca, err := os.ReadFile("checkactivation.go")
	if err != nil {
		t.Fatalf("read checkactivation.go: %v", err)
	}
	if !strings.Contains(string(ca), "service.AuditPayloadScripts()") {
		t.Error("`pogo check-activation` does not audit payload scripts — the doctor row has no exit code and no caller, and the nightly reads check-activation")
	}
}

// TestDoctorCheck_LaunchPayloadLineIsPresent runs the real compiled binary, mirroring
// the plist row's own test. The row must appear on every run whatever this machine's
// installed scripts say — a detector that renders nothing when it has nothing to report
// is invisible in exactly the way its subject fails, and "the plist row is there so the
// payload row must be too" is precisely the inference this whole item exists to break.
func TestDoctorCheck_LaunchPayloadLineIsPresent(t *testing.T) {
	line, ok := doctorChecks(t, nil)[launchPayloadCheckName]
	if !ok {
		t.Fatalf("no %q row in doctor --check; the detector must be visible even when it finds nothing", launchPayloadCheckName)
	}
	// Status is deliberately not asserted: it is a fact about whichever box runs the
	// suite, and on the box this was written for it is `warn` for three real reasons.
	if parts := strings.SplitN(line, "\t", 2); len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		t.Errorf("row = %q, want a non-empty detail", line)
	}
	if strings.HasPrefix(line, "fail\t") {
		t.Errorf("row = %q; this detector must never set doctor's exit code — reconciling is a machine-local action with a blast radius and doctor's callers did not ask to be blocked on it", line)
	}
}
