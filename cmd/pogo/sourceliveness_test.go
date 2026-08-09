package main

// Tests for the "consumer source liveness" doctor row (mg-c2f5).

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/sourcewatch"
)

func slVerdict(label, status, detail string) sourcewatch.Verdict {
	return sourcewatch.Verdict{
		Consumer: sourcewatch.Consumer{Label: label, SourceKey: "MAIL_DIR", Source: "/m/" + label + "/new"},
		Status:   status,
		Detail:   detail,
	}
}

func slReport(verdicts ...sourcewatch.Verdict) sourcewatch.Report {
	return sourcewatch.Report{
		Now:      time.Date(2026, 8, 9, 18, 0, 0, 0, time.UTC),
		Window:   sourcewatch.DefaultWindow,
		Scanned:  len(verdicts),
		Verdicts: verdicts,
	}
}

// TestSourceLivenessWarnsOnAStarvedConsumer is the ticket at the surface an
// operator reads. Starvation has to LEAD: a consumer pointed at a directory
// that does not exist is a visible mistake somebody will trip over, while a
// consumer pointed at a real directory nothing writes to reads healthy from
// every angle — which is the one that ran 40 hours.
func TestSourceLivenessWarnsOnAStarvedConsumer(t *testing.T) {
	status, detail := sourceLivenessLine(slReport(
		slVerdict("com.pogo.deadman", sourcewatch.StatusLive, "deadman reads /m/human/new, which had 9 arrival(s)"),
		slVerdict("com.pogo.ghost", sourcewatch.StatusMissing, "com.pogo.ghost reads a path that is not a directory"),
		slVerdict("com.pogo.notify", sourcewatch.StatusStarved, "com.pogo.notify reads MAIL_DIR=/m/daniel/new and NOTHING HAS ARRIVED THERE"),
	), true)

	if status != "warn" {
		t.Fatalf("status = %q, want warn; detail = %q", status, detail)
	}
	if !strings.Contains(detail, "NOTHING IS WRITING TO") {
		t.Errorf("detail = %q, want the starvation to lead", detail)
	}
	if i, j := strings.Index(detail, "com.pogo.notify"), strings.Index(detail, "ghost"); i < 0 || j < 0 || i > j {
		t.Errorf("detail = %q, want the starved consumer named ahead of the merely misconfigured one", detail)
	}
	if !strings.Contains(detail, "3 consumer(s) examined") {
		t.Errorf("detail = %q, want the population — a warning about one consumer says nothing about the others", detail)
	}
}

// TestSourceLivenessNeverFails pins that this is a detector, not a gate. `fail`
// sets doctor's exit code, and what to do about a starved consumer is a routing
// decision with a blast radius that belongs to whoever owns it.
func TestSourceLivenessNeverFails(t *testing.T) {
	for _, rep := range []sourcewatch.Report{
		slReport(slVerdict("a", sourcewatch.StatusStarved, "starved")),
		slReport(slVerdict("a", sourcewatch.StatusMissing, "missing")),
		slReport(slVerdict("a", sourcewatch.StatusUndetermined, "NOT CHECKED: quiet everywhere, not a pass")),
		slReport(slVerdict("a", sourcewatch.StatusLive, "live")),
		slReport(),
		{Err: errors.New("cannot read ~/Library/LaunchAgents")},
	} {
		status, detail := sourceLivenessLine(rep, true)
		if status != "warn" && status != "pass" {
			t.Errorf("status = %q for %+v (detail = %q); this row must only ever pass or warn", status, rep, detail)
		}
	}
}

// TestSourceLivenessSaysWhenItCheckedNothing is this row's half of the test the
// ticket set for the fix:
//
//	What would this instrument report if the thing it NAMES stopped entirely?
//	If the answer is green, it is measuring its own execution.
//
// Three of this row's states are "no starvation to report" and only one of them
// means consumers were compared against live data and came back clean.
// Rendering them identically would reproduce, one level up, the exact defect the
// row exists to catch.
func TestSourceLivenessSaysWhenItCheckedNothing(t *testing.T) {
	_, notApplicable := sourceLivenessLine(sourcewatch.Report{}, false)
	if !strings.Contains(notApplicable, "not applicable") || !strings.Contains(notApplicable, "This is not a report") {
		t.Errorf("unsupported-platform detail = %q, want it to disclaim rather than read as a pass", notApplicable)
	}

	status, empty := sourceLivenessLine(slReport(), true)
	if status != "warn" || !strings.Contains(empty, "NOT CHECKED") {
		t.Errorf("empty sweep = %q/%q, want warn + NOT CHECKED", status, empty)
	}

	status, broken := sourceLivenessLine(sourcewatch.Report{Err: errors.New("boom")}, true)
	if status != "warn" || !strings.Contains(broken, "NOT CHECKED") {
		t.Errorf("failed sweep = %q/%q, want warn + NOT CHECKED", status, broken)
	}

	// The one that matters most: every source quiet. If the fleet dies and
	// nothing is written anywhere, no consumer can be convicted by comparison —
	// and this row must not therefore report a healthy machine.
	status, quiet := sourceLivenessLine(slReport(
		slVerdict("com.pogo.notify", sourcewatch.StatusUndetermined, "NOT CHECKED: nothing arrived anywhere, this is not a pass"),
		slVerdict("com.pogo.deadman", sourcewatch.StatusUndetermined, "NOT CHECKED: nothing arrived anywhere, this is not a pass"),
	), true)
	if status != "warn" {
		t.Fatalf("fleet-wide silence = %q/%q; a machine where nothing is being written must not read as a clean bill", status, quiet)
	}
	if !strings.Contains(quiet, "NOT CHECKED for 2 of 2") {
		t.Errorf("detail = %q, want it to say how much of the population it could not judge", quiet)
	}

	_, clean := sourceLivenessLine(slReport(slVerdict("a", sourcewatch.StatusLive, "live")), true)
	if strings.Contains(clean, "NOT CHECKED") {
		t.Errorf("clean detail = %q must not carry the disclaimer phrase", clean)
	}
	if !strings.Contains(clean, "not against its own poll loop") {
		t.Errorf("clean detail = %q, want a positive statement about WHAT was compared — the whole defect is an instrument that measures its own execution", clean)
	}
}

// TestDoctorCheck_SourceLivenessLineIsPresent runs the real compiled binary. The
// row must appear on every run whatever this machine's consumers are doing — a
// detector that renders nothing when it has nothing to report is invisible in
// exactly the way its subject fails.
func TestDoctorCheck_SourceLivenessLineIsPresent(t *testing.T) {
	line, ok := doctorChecks(t, nil)[sourceLivenessCheckName]
	if !ok {
		t.Fatalf("no %q row in doctor --check; the detector must be visible even when it finds nothing", sourceLivenessCheckName)
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
