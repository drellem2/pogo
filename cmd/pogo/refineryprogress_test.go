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

// TestProgressClockTimesAreUTCAndSaidSo is the behavioural half of the mg-0235
// recurrence check: the static scan proves the layout carries a Z, this proves
// the value was actually converted to match it. A layout with the letter Z in
// it and no .UTC() is worse than a bare one — it labels the host's local clock
// as UTC and gives the reader nothing to be suspicious about.
//
// The failure this reproduces: a reader on a UTC clock read "started 18:51:05"
// at 18:28 and concluded the gate had started IN THE FUTURE. The fixture below
// is that reading — a value carrying a +01:00 offset, rendered while `now` is
// an hour behind its local digits.
func TestProgressClockTimesAreUTCAndSaidSo(t *testing.T) {
	plusOne := time.FixedZone("BST", 3600)
	started := time.Date(2026, 8, 6, 17, 51, 5, 0, time.UTC)
	now := started.Add(3 * time.Minute)

	out := formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		Gate:              "./build.sh",
		StartTime:         started.In(plusOne), // as unmarshalled from a stored +01:00 offset
		Heartbeat:         now,
		HeartbeatInterval: "30s",
		TimeoutAt:         started.Add(2 * time.Hour).In(plusOne),
	}, now)

	if !strings.Contains(out, "started 17:51:05Z") {
		t.Errorf("start time is not rendered as labelled UTC — a reader holding a UTC clock reads it as an hour in the future:\n%s", out)
	}
	if strings.Contains(out, "18:51:05") {
		t.Errorf("start time rendered in the value's stored +01:00 offset:\n%s", out)
	}
	if !strings.Contains(out, "Timeout:   19:51:05Z") {
		t.Errorf("timeout is not rendered as labelled UTC:\n%s", out)
	}

	// Finished is on the other branch, so it needs its own reading.
	done := formatMRProgress(&refinery.StepProgress{
		Step:              "quality-gates",
		StartTime:         started.In(plusOne),
		EndTime:           started.Add(90 * time.Second).In(plusOne),
		Heartbeat:         now,
		HeartbeatInterval: "30s",
	}, now)
	if !strings.Contains(done, "Finished:  17:52:35Z") {
		t.Errorf("finish time is not rendered as labelled UTC:\n%s", done)
	}
}

// gateSaid builds an excerpt fixture the way a running gate would have produced
// it: an opening header, a long silent middle the bound cannot keep, and a
// recent line.
func gateSaid(total int) *refinery.GateExcerpt {
	head := []string{
		"watchlist consistent: 17 paths; import closure 10 modules; datasets read 1",
		"=== watched paths changed:",
		"    .github/workflows/script-controls.yml",
	}
	tail := []string{"ok  internal/refinery  12.400s", "ok  internal/hostload  0.310s"}
	return &refinery.GateExcerpt{
		Head: head, Tail: tail,
		HeadLimit: 25, TailLimit: 40, LineBytes: 500,
		Lines:  total,
		Elided: total - len(head) - len(tail),
	}
}

// TestRefineryShowPrintsWhatARunningGateSaid is the operator-facing half of
// mg-9adc's positive control. Every row `refinery show` printed before this was
// metadata ABOUT the gate's output — how many lines, how long ago, at what CPU
// — and none of them can tell a compute phase from a hang, because none of them
// says what the gate was doing when it went quiet.
//
// The opening line is asserted explicitly, not just "some text": the incident
// this closes turned on a gate's FIRST line, which a tail of any reasonable
// size would have dropped 77 minutes in.
func TestRefineryShowPrintsWhatARunningGateSaid(t *testing.T) {
	now := time.Now()
	out := formatMRProgress(&refinery.StepProgress{
		Step: "quality-gates", Gate: "./scripts/refinery_gate.sh",
		StartTime: now.Add(-77 * time.Minute), Heartbeat: now.Add(-3 * time.Second),
		Beats: 154, HeartbeatInterval: "30s",
		OutputLines: 312, LastOutput: now.Add(-26 * time.Minute),
		OutputExcerpt: gateSaid(312),
	}, now)
	t.Logf("RUNNING GATE:\n%s", out)

	if !strings.Contains(out, "watchlist consistent: 17 paths") {
		t.Errorf("the gate's OPENING line must be readable while it runs — that line is what "+
			"refuted the wrong hypothesis, and it is far outside any tail; got:\n%s", out)
	}
	if !strings.Contains(out, "ok  internal/refinery  12.400s") {
		t.Errorf("the gate's most recent line must be readable too, got:\n%s", out)
	}
	// The bound, stated. A bounded read that manufactures an absence is its own
	// defect, and this arc has already been bitten by exactly that.
	for _, want := range []string{"312 lines so far", "NOT shown", "head 25", "tail 40", "not shown here"} {
		if !strings.Contains(out, want) {
			t.Errorf("the excerpt must state its bound; %q missing from:\n%s", want, out)
		}
	}
	// And the line numbers, so head-then-tail cannot read as contiguous.
	if !strings.Contains(out, "  1 | watchlist consistent") || !strings.Contains(out, "312 | ok  internal/hostload") {
		t.Errorf("kept lines must carry the gate's own numbering, got:\n%s", out)
	}
}

// TestRefineryShowSeparatesASilentGateFromAnUnrecordedOne. "The gate has said
// nothing" and "nothing here captured what it said" lead to opposite actions,
// and rendering them the same is how a reader concludes a steadily-printing
// gate is mute.
func TestRefineryShowSeparatesASilentGateFromAnUnrecordedOne(t *testing.T) {
	now := time.Now()
	base := refinery.StepProgress{
		Step: "quality-gates", Gate: "./build.sh", StartTime: now.Add(-8 * time.Minute),
		Heartbeat: now.Add(-2 * time.Second), Beats: 16, HeartbeatInterval: "30s",
	}
	silent := base
	silent.OutputExcerpt = &refinery.GateExcerpt{HeadLimit: 25, TailLimit: 40, LineBytes: 500}
	unrecorded := base // OutputExcerpt stays nil: an older pogod's record

	gotSilent := formatMRProgress(&silent, now)
	gotUnrecorded := formatMRProgress(&unrecorded, now)
	if gotSilent == gotUnrecorded {
		t.Fatal("a gate that said nothing and a record that captured nothing must not render identically")
	}
	if !strings.Contains(gotSilent, "NOTHING YET") {
		t.Errorf("a silent gate's excerpt must report the silence as a measurement, got:\n%s", gotSilent)
	}
	if !strings.Contains(gotUnrecorded, "NOT RECORDED") {
		t.Errorf("a record with no excerpt must say so rather than look silent, got:\n%s", gotUnrecorded)
	}
}

// TestFinishedGateGetsNoBoundedExcerpt: once the merge resolves, `refinery
// show` prints the gate's FULL output. A bounded excerpt above an unbounded
// transcript adds a second, smaller copy that a skimming reader can mistake for
// the whole of it.
func TestFinishedGateGetsNoBoundedExcerpt(t *testing.T) {
	now := time.Now()
	out := formatMRProgress(&refinery.StepProgress{
		Step: "quality-gates", Gate: "./build.sh",
		StartTime: now.Add(-20 * time.Minute), EndTime: now.Add(-1 * time.Minute),
		HeartbeatInterval: "30s", OutputLines: 312, OutputExcerpt: gateSaid(312),
	}, now)
	if strings.Contains(out, "Gate output so far") {
		t.Errorf("a finished record must not print a bounded excerpt — the full output follows it; got:\n%s", out)
	}
}

// TestQueueViewSaysWhatTheGateSaidNotOnlyHowMuch. `refinery queue` is the view
// people poll while they wait, and its output row was a pure volume reading:
// "140 lines, last 26m ago" is byte-identical for a gate mid-suite and a gate
// wedged at a build step. One line of the gate's own words each end fixes that
// without turning the row into a transcript.
func TestQueueViewSaysWhatTheGateSaidNotOnlyHowMuch(t *testing.T) {
	now := time.Now()
	out := formatQueue([]refinery.MergeRequest{{
		ID: "mr-active", Branch: "polecat-9adc", Author: "mg-9adc",
		Status: refinery.StatusProcessing, SubmitTime: now.Add(-80 * time.Minute),
		StartTime: now.Add(-78 * time.Minute),
		Progress: &refinery.StepProgress{
			Step: "quality-gates", Gate: "./scripts/refinery_gate.sh",
			StartTime: now.Add(-77 * time.Minute), Heartbeat: now.Add(-3 * time.Second),
			Beats: 154, HeartbeatInterval: "30s",
			OutputLines: 312, LastOutput: now.Add(-26 * time.Minute),
			OutputExcerpt: gateSaid(312),
		},
	}}, now)
	t.Logf("QUEUE:\n%s", out)

	if !strings.Contains(out, "said first:  watchlist consistent: 17 paths") {
		t.Errorf("the queue row must carry the gate's opening line, got:\n%s", out)
	}
	if !strings.Contains(out, "said latest: ok  internal/hostload") {
		t.Errorf("the queue row must carry the gate's most recent line, got:\n%s", out)
	}
	// Two lines are a summary, and a summary that does not say where the rest is
	// reads as the whole of it.
	if !strings.Contains(out, "pogo refinery show mr-active") {
		t.Errorf("the summary must name the command that prints the bounded excerpt, got:\n%s", out)
	}
}
