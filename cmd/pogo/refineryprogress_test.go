package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
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
	// Each reading is asserted WITH the layer that owns it. mg-48d8: the
	// heartbeat is the runner's and the output is the gate's, and a reader who
	// cannot see which is which reads the first as a claim about the second.
	for _, want := range []string{"Running:   10m0s", "RUNNER heartbeat", "4s old, beat 20, every 30s",
		"GATE   stdout", "412 lines, last 5s ago", "ALIVE and working", "waiting is correct", "Timeout:"} {
		if !strings.Contains(running, want) {
			t.Errorf("a running gate's output should contain %q, got:\n%s", want, running)
		}
	}
	for _, want := range []string{"Running:   10m0s", "RUNNER heartbeat", "DEAD      10m0s old",
		"no output at all", "Waiting will not help"} {
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
	// The heartbeat row survives on a finished record but must claim nothing:
	// it necessarily goes stale once the gates finish, so an age presented as
	// live would read as a dead runner on every merged MR.
	if !strings.Contains(out, "RUNNER heartbeat       n/a") {
		t.Errorf("a finished record's heartbeat must be marked n/a, got:\n%s", out)
	}
	if !strings.Contains(out, "makes no liveness claim") {
		t.Errorf("a finished record must say why its heartbeat means nothing, got:\n%s", out)
	}
	if strings.Contains(out, "GATE   process subtree") {
		t.Errorf("a finished record has no processes to measure and must not print a subtree row, got:\n%s", out)
	}
	if strings.Contains(out, "DEAD") {
		t.Errorf("a finished record must not read as a dead runner, got:\n%s", out)
	}
	if strings.Contains(out, "Timeout:") {
		t.Errorf("a finished record should not print a pending timeout, got:\n%s", out)
	}
}

// TestFormatMRProgressSilentGateIsReportedAsUnresolved covers the case the
// instrument genuinely cannot settle: the runner is beating, the gate is
// silent, and the process subtree could not be measured. Reporting it as
// "working" would rebuild the ambiguity; reporting it as dead would be a false
// alarm; reporting it as idle would invent a measurement that was never taken.
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
		CPUUnavailable:    "reading the process table failed: ps: exit status 1",
	}, now)

	// The verdict names the layer it is undetermined ABOUT. "ALIVE but
	// UNDETERMINED", which this replaces, left the reader to supply the
	// subject — and the reader supplied the runner, whose heartbeat was fresh
	// and irrelevant (mg-48d8).
	if !strings.Contains(out, "GATE UNDETERMINED") {
		t.Errorf("a silent, unmeasurable gate under a live runner should be named as such, got:\n%s", out)
	}
	if !strings.Contains(out, "RUNNER heartbeat       alive") {
		t.Errorf("the live runner must still be reported, on its own row, got:\n%s", out)
	}
	if !strings.Contains(out, "cannot be told from here") {
		t.Errorf("the verdict must admit what it cannot resolve, got:\n%s", out)
	}
	if !strings.Contains(out, "reading the process table failed") {
		t.Errorf("the verdict must say WHY there is no measurement, got:\n%s", out)
	}
	if strings.Contains(out, "idle") {
		t.Errorf("an unmeasured subtree must never be reported as an idle one, got:\n%s", out)
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

// TestFormatMRProgressSeparatesASlowHostFromASlowChange is the operator-facing
// half of mg-1b8c. Two gates, identical in every respect a reader could see
// before this change — same command, same elapsed time, same output, same
// heartbeat — and one of them was competing with the rest of the fleet for a
// full host the whole time.
//
// The distinction has to be visible without reading the process table, for the
// same reason mg-8595's did: by the time anyone reads `refinery show`, the run
// is often over and the process table no longer holds the answer.
func TestFormatMRProgressSeparatesASlowHostFromASlowChange(t *testing.T) {
	now := time.Now()
	started := now.Add(-40 * time.Minute)

	base := func(c *hostload.Summary) string {
		return formatMRProgress(&refinery.StepProgress{
			Step:              "quality-gates",
			Gate:              "./build.sh",
			StartTime:         started,
			Heartbeat:         now.Add(-3 * time.Second),
			Beats:             80,
			HeartbeatInterval: "30s",
			OutputLines:       900,
			LastOutput:        now.Add(-2 * time.Second),
			Contention:        c,
		}, now)
	}

	var loaded, quiet hostload.Tracker
	for i := 0; i < 40; i++ {
		loaded.Add(hostload.Sample{Cores: 10, FleetCores: 8.1, ExternalCores: 1.5,
			FleetProcs: 11, LoadAvg1: 140, Attributed: true})
		quiet.Add(hostload.Sample{Cores: 10, FleetCores: 0.9, ExternalCores: 0.7,
			FleetProcs: 8, LoadAvg1: 3, Attributed: true})
	}
	ls, qs := loaded.Summary(), quiet.Summary()

	onFullHost, onQuietHost := base(&ls), base(&qs)

	if !strings.Contains(onFullHost, "HOST SATURATED") {
		t.Errorf("a gate that ran on a full host must say so:\n%s", onFullHost)
	}
	if !strings.Contains(onFullHost, "fleet held 8.1 of 10 cores") {
		t.Errorf("the numbers must be printed, not just the verdict:\n%s", onFullHost)
	}
	if !strings.Contains(onQuietHost, "HOST   load            has capacity") {
		t.Errorf("a gate that ran on a quiet host must say so positively — absence would mean "+
			"\"not measured\", which is a third thing:\n%s", onQuietHost)
	}
	if strings.Contains(onQuietHost, "HOST SATURATED") {
		t.Errorf("a quiet host was reported as saturated:\n%s", onQuietHost)
	}
	// Both ran for the same 40 minutes: the elapsed time is not what separates
	// them, and must not be.
	if !strings.Contains(onFullHost, "40m0s") || !strings.Contains(onQuietHost, "40m0s") {
		t.Errorf("fixtures must share an elapsed time:\n%s\n---\n%s", onFullHost, onQuietHost)
	}
}

// TestFormatMRProgressSaysNothingWhenNothingWasMeasured. An unsampled run must
// not gain a host line — "the host was fine" and "we did not look" are
// different claims and only one of them is true here.
func TestFormatMRProgressSaysNothingWhenNothingWasMeasured(t *testing.T) {
	now := time.Now()
	out := formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		StartTime:         now.Add(-5 * time.Minute),
		Heartbeat:         now,
		HeartbeatInterval: "30s",
	}, now)
	if strings.Contains(out, "HOST") {
		t.Errorf("an unsampled run must print no host row:\n%s", out)
	}

	empty := &hostload.Summary{}
	out = formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		StartTime:         now.Add(-5 * time.Minute),
		Heartbeat:         now,
		HeartbeatInterval: "30s",
		Contention:        empty,
	}, now)
	if strings.Contains(out, "HOST") {
		t.Errorf("a zero-sample contention record must print no host row:\n%s", out)
	}
}
