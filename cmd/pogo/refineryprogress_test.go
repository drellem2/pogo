package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

// TestFormatMRProgressDistinguishesSlowFromDead is the operator-facing positive
// control mg-8595 asks for: `pogo refinery show` must let a human tell a gate
// running for 10 minutes from a gate that died 10 minutes ago, without reading
// the process table — reading the process table is what misled the original
// diagnosis, so an answer that requires it does not count.
//
// Both fixtures have identical elapsed times, so the output cannot be
// separating them on duration.
func TestFormatMRProgressDistinguishesSlowFromDead(t *testing.T) {
	now := time.Now()
	started := now.Add(-10 * time.Minute)

	running := formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		GateIndex:         1,
		GateCount:         2,
		StartTime:         started,
		Heartbeat:         now.Add(-4 * time.Second),
		Beats:             20,
		HeartbeatInterval: "30s",
		OutputLines:       412,
		LastOutput:        now.Add(-5 * time.Second),
		TimeoutAt:         now.Add(50 * time.Minute),
	}, now)

	died := formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		GateIndex:         1,
		GateCount:         2,
		StartTime:         started,
		Heartbeat:         started, // beat once on entry, then the runner died
		Beats:             1,
		HeartbeatInterval: "30s",
	}, now)

	if running == died {
		t.Fatal("a running gate and a dead one must not render identically — that is the defect")
	}
	for _, want := range []string{"Running:   10m0s", "Heartbeat: 4s ago", "beat 20, every 30s",
		"Gate says: 412 lines", "ALIVE and working", "waiting is correct", "Timeout:"} {
		if !strings.Contains(running, want) {
			t.Errorf("a running gate's output should contain %q, got:\n%s", want, running)
		}
	}
	for _, want := range []string{"Running:   10m0s", "Heartbeat: 10m0s ago", "nothing yet",
		"DEAD", "Waiting will not help"} {
		if !strings.Contains(died, want) {
			t.Errorf("a dead runner's output should contain %q, got:\n%s", want, died)
		}
	}
	// A dead runner has no timeout to report, and must not imply one.
	if strings.Contains(died, "Timeout:") {
		t.Errorf("an unbounded gate must not print a timeout line, got:\n%s", died)
	}
	// Both must name which gate of how many, so a multi-gate run is locatable.
	for _, out := range []string{running, died} {
		if !strings.Contains(out, "./build.sh (1 of 2)") {
			t.Errorf("output should name the gate and its position, got:\n%s", out)
		}
	}
}

// TestFormatMRProgressFinishedRecord checks a completed run reports duration
// rather than a stale-heartbeat scare. The heartbeat necessarily goes stale once
// the gates finish; rendering that as DEAD would be a false alarm on every
// merged MR.
func TestFormatMRProgressFinishedRecord(t *testing.T) {
	now := time.Now()
	out := formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		StartTime:         now.Add(-32 * time.Minute),
		EndTime:           now.Add(-2 * time.Minute),
		Heartbeat:         now.Add(-2 * time.Minute),
		Beats:             60,
		HeartbeatInterval: "30s",
		OutputLines:       900,
		LastOutput:        now.Add(-2 * time.Minute),
		TimeoutAt:         now.Add(28 * time.Minute),
	}, now)

	if !strings.Contains(out, "Running:   30m0s") {
		t.Errorf("a finished record should report how long the gates took, got:\n%s", out)
	}
	if !strings.Contains(out, "Finished:") {
		t.Errorf("a finished record should print its end time, got:\n%s", out)
	}
	if strings.Contains(out, "Heartbeat:") {
		t.Errorf("a finished record should not present a heartbeat age as live, got:\n%s", out)
	}
	if strings.Contains(out, "DEAD") {
		t.Errorf("a finished record must not read as a dead runner, got:\n%s", out)
	}
	if strings.Contains(out, "Timeout:") {
		t.Errorf("a finished record should not print a pending timeout, got:\n%s", out)
	}
}

// TestFormatMRProgressSilentGateIsReportedAsUnresolved covers the case the
// instrument genuinely cannot settle. Reporting it as "working" would rebuild
// the ambiguity; reporting it as dead would be a false alarm.
func TestFormatMRProgressSilentGateIsReportedAsUnresolved(t *testing.T) {
	now := time.Now()
	out := formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		Gate:              "./test.sh",
		StartTime:         now.Add(-25 * time.Minute),
		Heartbeat:         now.Add(-2 * time.Second),
		Beats:             50,
		HeartbeatInterval: "30s",
		TimeoutAt:         now.Add(35 * time.Minute),
	}, now)

	if !strings.Contains(out, "ALIVE, gate silent") {
		t.Errorf("a silent gate under a live runner should be named as such, got:\n%s", out)
	}
	if !strings.Contains(out, "cannot be told from here") {
		t.Errorf("the verdict must admit what it cannot resolve, got:\n%s", out)
	}
	if strings.Contains(out, "DEAD") {
		t.Errorf("a live runner must not be reported as dead, got:\n%s", out)
	}
	if !strings.Contains(out, "Timeout:   ") {
		t.Errorf("the unresolvable case must be bounded by a printed timeout, got:\n%s", out)
	}
}

// TestFormatMRProgressNoRecord checks an MR that never reached the gates prints
// nothing rather than an empty block.
func TestFormatMRProgressNoRecord(t *testing.T) {
	if out := formatMRProgress(nil, time.Now()); out != "" {
		t.Errorf("no progress record should render nothing, got:\n%s", out)
	}
}
