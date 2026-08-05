package refinery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/proctable"
)

// TestQueueWithProcessingIncludesTheInFlightRequest is the omission mg-0c51 was
// filed about. Queue() lists only pending requests, so a refinery grinding
// through a merge served the same array as one that had stopped — and served it
// unchanged across polls, because the row that was moving was the row that was
// not in the list.
func TestQueueWithProcessingIncludesTheInFlightRequest(t *testing.T) {
	r := newProgressTestRefinery(t, time.Hour)
	active := &MergeRequest{ID: "mr-active", Branch: "b-active", Status: StatusProcessing, StartTime: time.Now()}
	markInFlight(r, active)
	r.queue = []*MergeRequest{
		{ID: "mr-1", Branch: "b-1", Status: StatusQueued},
		{ID: "mr-2", Branch: "b-2", Status: StatusQueued},
	}

	if got := len(r.Queue()); got != 2 {
		t.Errorf("Queue() must stay pending-only, got %d rows", got)
	}

	full := r.QueueWithProcessing()
	if len(full) != 3 {
		t.Fatalf("QueueWithProcessing() returned %d rows, want 3", len(full))
	}
	if full[0].ID != "mr-active" || full[0].Status != StatusProcessing {
		t.Errorf("the in-flight request must lead the list, got %s/%s", full[0].ID, full[0].Status)
	}
	if full[1].ID != "mr-1" || full[2].ID != "mr-2" {
		t.Errorf("pending order must be preserved, got %s then %s", full[1].ID, full[2].ID)
	}
}

// TestQueueWithProcessingIdleRefinery is the control: with nothing in flight,
// the list is exactly the pending ones. If it were not, "nothing is running"
// could never be distinguished from "something is".
func TestQueueWithProcessingIdleRefinery(t *testing.T) {
	r := newProgressTestRefinery(t, time.Hour)
	r.queue = []*MergeRequest{{ID: "mr-1", Status: StatusQueued}}
	full := r.QueueWithProcessing()
	if len(full) != 1 || full[0].Status != StatusQueued {
		t.Fatalf("idle refinery should list only pending rows, got %+v", full)
	}
	for _, mr := range full {
		if mr.Status == StatusProcessing {
			t.Error("an idle refinery must not report anything as processing")
		}
	}
}

// TestQueueEndpointServesTheInFlightRequest checks the wire, not just the
// method: the CLI reads /refinery/queue and it is the endpoint that was
// omitting the row.
func TestQueueEndpointServesTheInFlightRequest(t *testing.T) {
	r := newProgressTestRefinery(t, time.Hour)
	markInFlight(r, &MergeRequest{ID: "mr-active", Status: StatusProcessing})
	r.queue = []*MergeRequest{{ID: "mr-1", Status: StatusQueued}}

	mux := http.NewServeMux()
	r.RegisterHandlers(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/refinery/queue")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The shape must stay an array of merge requests: existing --json
	// consumers key off .status, which already carries the distinction.
	var got []MergeRequest
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("queue response is no longer a merge-request array: %v", err)
	}
	if len(got) != 2 || got[0].ID != "mr-active" || got[0].Status != StatusProcessing {
		t.Fatalf("endpoint did not serve the in-flight request first, got %+v", got)
	}
}

// TestStatusReportsWhatIsInFlight covers the summary view. QueueLen counts
// pending requests only, so on its own it cannot separate a busy refinery from
// a stopped one — both can report "Queue: 2".
func TestStatusReportsWhatIsInFlight(t *testing.T) {
	r := newProgressTestRefinery(t, time.Hour)
	r.queue = []*MergeRequest{{ID: "mr-1"}, {ID: "mr-2"}}

	idle := r.GetStatus()
	if idle.Processing != "" {
		t.Errorf("an idle refinery must report nothing in flight, got %q", idle.Processing)
	}

	started := time.Now().Add(-3 * time.Minute)
	markInFlight(r, &MergeRequest{ID: "mr-active", Status: StatusProcessing, StartTime: started})
	busy := r.GetStatus()
	if busy.Processing != "mr-active" {
		t.Errorf("a busy refinery must name what is in flight, got %q", busy.Processing)
	}
	if !busy.ProcessingSince.Equal(started) {
		t.Errorf("ProcessingSince = %v, want %v", busy.ProcessingSince, started)
	}
	if busy.QueueLen != idle.QueueLen {
		t.Error("QueueLen must keep counting pending requests only")
	}
	// The whole point: the two statuses must not look the same TO A CLIENT.
	// Compared as JSON rather than as structs — Status carries pointer fields,
	// so struct inequality would hold on pointer identity alone and the
	// assertion would pass without proving anything.
	idleJSON, _ := json.Marshal(idle)
	busyJSON, _ := json.Marshal(busy)
	if string(idleJSON) == string(busyJSON) {
		t.Fatalf("busy and idle statuses serialise identically — the summary cannot tell them apart: %s", busyJSON)
	}
}

// TestHeldMergeRequestDropsItsInFlightStamp checks the one path that returns a
// dequeued request to the queue. A queued row still carrying StartTime would
// report "in flight for 40m" while sitting in line — the same class of untruth
// as omitting the in-flight row in the first place.
func TestHeldMergeRequestDropsItsInFlightStamp(t *testing.T) {
	r := newProgressTestRefinery(t, time.Hour)
	mr := &MergeRequest{ID: "mr-held", Status: StatusProcessing, StartTime: time.Now().Add(-40 * time.Minute)}
	markInFlight(r, mr)
	r.holdMergeRequest(mr, "mg-qa")

	if !mr.StartTime.IsZero() {
		t.Errorf("a re-queued request must not keep its in-flight stamp, got %v", mr.StartTime)
	}
	if r.GetStatus().Processing != "" {
		t.Error("a held request must not still be reported as in flight")
	}
	for _, q := range r.QueueWithProcessing() {
		if q.Status == StatusProcessing {
			t.Errorf("held request %s is still listed as processing", q.ID)
		}
	}
}

// TestDequeueStampsStartTime checks the field the "in flight for N" line reads.
func TestDequeueStampsStartTime(t *testing.T) {
	r := newProgressTestRefinery(t, time.Hour)
	r.queue = []*MergeRequest{{ID: "mr-1", Status: StatusQueued}}
	before := time.Now()
	mr := r.dequeue()
	if mr == nil {
		t.Fatal("dequeue returned nothing")
	}
	if mr.StartTime.Before(before) {
		t.Errorf("dequeue must stamp StartTime, got %v", mr.StartTime)
	}
}

// TestGateWatchMeasuresARealSubtreesCPU is the end-to-end positive control for
// the signal the CLI now prints. It runs two real gates through the real
// runner — one that computes, one that sleeps — and asserts the records
// separate them.
//
// Both arms are required. A measurement that only ever reports "busy" is the
// defect described in mg-1b8c's lineage: a view that always reads healthy
// cannot report the state it exists to report.
//
// # It reads the record WHILE the gate runs, and that is not incidental
//
// The signal exists for a live gate: a coordinator polling `pogo refinery
// queue` mid-merge, asking whether the thing it is waiting on is computing or
// stopped. The SEALED record of a finished gate honestly reports
// `SubtreeGone`, because by then the process is gone — so whether an assertion
// on the sealed record saw a live subtree came down to whether the last
// heartbeat landed before or after the gate exited. On darwin it did; on Linux
// it did not, and the CI log showed `cores=0.00 procs=0 churn=3` — an empty
// subtree, classified gone, read as a failure of the measurement (mg-79e3).
//
// # Which environments this runs in
//
// The measurement differences the host's CPU-time column, so it is only as
// good as that column's precision — 10ms on darwin and on any Linux with a
// readable /proc, whole seconds where only procps `ps` is available. At a
// 400ms heartbeat the last of those cannot separate a spinning gate from a
// sleeping one, and every assertion below would be made against a quantised
// zero. So the environment is checked FIRST and named in the output. Where the
// measurement is supported the full discrimination is asserted, unweakened.
// Where it is not, the records must carry the reason — which is itself
// asserted — and the numeric arms are skipped rather than lowered, because a
// number this host cannot produce is not a number to assert on.
func TestGateWatchMeasuresARealSubtreesCPU(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real gates for a couple of seconds")
	}

	const heartbeat = 400 * time.Millisecond
	t.Logf("process-table source: %s; gate heartbeat %s", procSource, heartbeat)
	supported := procSource.CanResolve(heartbeat)

	// run executes a gate and returns every progress record the refinery
	// published while it was still running.
	run := func(gate string) []StepProgress {
		t.Helper()
		r := newProgressTestRefinery(t, heartbeat)
		wtDir := t.TempDir()
		writeGateConfig(t, wtDir, "quality_gate = "+quoteTOML(gate))
		mr := &MergeRequest{ID: "mr-cpu", Status: StatusProcessing}
		r.byID[mr.ID] = mr

		done := make(chan error, 1)
		go func() {
			_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
			done <- err
		}()

		var seen []StepProgress
		poll := time.NewTicker(heartbeat / 4)
		defer poll.Stop()
		for {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("gate %q should have passed: %v", gate, err)
				}
				if len(seen) == 0 {
					t.Fatalf("gate %q published no record while it ran", gate)
				}
				return seen
			case <-poll.C:
				r.mu.Lock()
				if mr.Progress != nil && mr.Progress.EndTime.IsZero() {
					seen = append(seen, *mr.Progress)
				}
				r.mu.Unlock()
			}
		}
	}

	// settled keeps the readings taken over a window in which the subtree
	// neither gained nor lost a process. Churn counts as work by design — a
	// gate forking short-lived workers IS working — so the startup and
	// teardown windows say "busy" for both arms and can discriminate nothing.
	// Excluding them is what forces each arm to prove itself on the CPU path,
	// which is the path that was blind.
	settled := func(rs []StepProgress) []StepProgress {
		var out []StepProgress
		for _, p := range rs {
			if !p.CPUSampledAt.IsZero() && p.CPUProcs > 0 && p.CPUChurn == 0 {
				out = append(out, p)
			}
		}
		return out
	}
	peak := func(rs []StepProgress) float64 {
		hi := 0.0
		for _, p := range rs {
			if p.CPUCores > hi {
				hi = p.CPUCores
			}
		}
		return hi
	}

	// A gate whose real work happens in a DESCENDANT: `sh -c` forks for a
	// compound command, so only a subtree walk finds the CPU. The spin loop
	// uses shell builtins only, so the work shows up as CPU time rather than
	// as process churn — this arm has to prove the CPU path, not the churn
	// fallback.
	busyRecords := run(`(while :; do :; done) & spinner=$!; sleep 2; kill $spinner; exit 0`)
	last := busyRecords[len(busyRecords)-1]
	t.Logf("busy gate: pid=%d source=%s readings=%d", last.GatePID, last.CPUSource, len(busyRecords))
	for _, p := range busyRecords {
		t.Logf("  busy: cores=%.2f procs=%d churn=%d window=%s unavailable=%q",
			p.CPUCores, p.CPUProcs, p.CPUChurn, p.CPUWindow, p.CPUUnavailable)
	}
	if last.GatePID == 0 {
		t.Error("the gate's pid must be recorded — without it there is no subtree to measure")
	}
	if last.CPUSource != procSource.Name {
		t.Errorf("the record must name its instrument, got %q want %q", last.CPUSource, procSource.Name)
	}

	if !supported {
		// The unsupported path is asserted, not waved through. A host that
		// cannot measure must produce UNKNOWN with a stated reason; if it
		// produced a number here, the number would be the quantised zero this
		// whole change exists to stop being reported as idle.
		if last.Subtree() != SubtreeUnknown {
			t.Errorf("%s cannot resolve a %s window, so the record must be UNKNOWN, got %v (cores=%.2f)",
				procSource, heartbeat, last.Subtree(), last.CPUCores)
		}
		if !strings.Contains(last.CPUUnavailable, procSource.Name) {
			t.Errorf("an unmeasurable environment must name itself in the reason, got %q", last.CPUUnavailable)
		}
		t.Skipf("subtree CPU is not measurable here: %s", last.CPUUnavailable)
	}

	busy := settled(busyRecords)
	if len(busy) == 0 {
		t.Fatalf("a 2s gate at a %s heartbeat produced no settled measurement on %s (last unavailable=%q)",
			heartbeat, procSource, last.CPUUnavailable)
	}
	busyPeak := peak(busy)
	if busyPeak < 0.5 {
		t.Errorf("a spinning gate peaked at %.2f cores across %d settled readings; "+
			"the subtree walk is not finding the work", busyPeak, len(busy))
	}
	// And an upper bound, because the rate has to be RIGHT and not merely
	// non-zero. This gate holds exactly one spinner, so ~1.0 cores is the
	// physical answer and anything near twice that is the instrument, not the
	// work — a CPU column too coarse for the window reports nothing for
	// several windows and then a whole tick at once, which reads as a
	// multi-core burst. Without this bound the test is blind to precision loss
	// in the direction that inflates, and precision loss is what put it in CI
	// (mg-79e3).
	const oneSpinnerCeiling = 2.0
	if busyPeak > oneSpinnerCeiling {
		t.Errorf("a gate holding ONE spinner peaked at %.2f cores over %s windows; "+
			"%s is over-reporting, most likely quantising work into bursts",
			busyPeak, heartbeat, procSource)
	}
	sawBusy := false
	for _, p := range busy {
		if p.Subtree() == SubtreeBusy {
			sawBusy = true
		}
	}
	if !sawBusy {
		t.Errorf("a spinning gate must classify as busy in at least one of its %d settled readings", len(busy))
	}

	// The negative arm. A sleeping gate consumes nothing, and the record must
	// be willing to say so — on EVERY settled reading, not merely one, because
	// a measurement that reports busy unconditionally would still satisfy an
	// "at least one" test.
	idleRecords := run(`sleep 2`)
	for _, p := range idleRecords {
		t.Logf("  idle: cores=%.2f procs=%d churn=%d window=%s unavailable=%q",
			p.CPUCores, p.CPUProcs, p.CPUChurn, p.CPUWindow, p.CPUUnavailable)
	}
	idle := settled(idleRecords)
	if len(idle) == 0 {
		t.Fatalf("a 2s sleeping gate at a %s heartbeat produced no settled measurement on %s", heartbeat, procSource)
	}
	for _, p := range idle {
		if p.Subtree() != SubtreeIdle {
			t.Errorf("a sleeping gate must classify as idle (cores=%.2f churn=%d procs=%d) — the "+
				"measurement is reporting busy unconditionally", p.CPUCores, p.CPUChurn, p.CPUProcs)
		}
	}
	if idlePeak := peak(idle); busyPeak <= idlePeak {
		t.Errorf("a computing gate (%.2f cores) must measure higher than a sleeping one (%.2f cores)",
			busyPeak, idlePeak)
	}
}

// TestGateWatchRefusesToMeasureBelowTheHostsResolution runs the unsupported
// path EVERYWHERE, by standing in a coarse process-table source. It is the
// half that the skip above cannot cover on a machine where the measurement
// works, and it pins the distinction the whole signal rests on: a window the
// host cannot resolve must yield "no measurement, and here is why", never the
// 0.00 cores that renders as a quiet, healthy-looking idle gate.
func TestGateWatchRefusesToMeasureBelowTheHostsResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real gate")
	}
	prev := procSource
	procSource = proctable.Source{Name: "coarse-ps", Resolution: time.Second}
	t.Cleanup(func() { procSource = prev })

	r := newProgressTestRefinery(t, 200*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, `quality_gate = `+quoteTOML(`(while :; do :; done) & spinner=$!; sleep 1; kill $spinner; exit 0`))
	mr := &MergeRequest{ID: "mr-coarse", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("gate should have passed: %v", err)
	}
	p := mr.Progress
	t.Logf("coarse-source gate: cores=%.2f unavailable=%q summary=%q", p.CPUCores, p.CPUUnavailable, p.CPUSummary())

	if p.Subtree() != SubtreeUnknown {
		t.Errorf("a 200ms window on a whole-second CPU column must classify as unknown, got %v (cores=%.2f)",
			p.Subtree(), p.CPUCores)
	}
	if p.CPUCores != 0 || !p.CPUSampledAt.IsZero() {
		t.Errorf("an unmeasurable window must carry no numbers, got cores=%.2f sampled=%v", p.CPUCores, p.CPUSampledAt)
	}
	for _, want := range []string{"coarse-ps", "1s", "5s"} {
		if !strings.Contains(p.CPUUnavailable, want) {
			t.Errorf("the reason must mention %q so a reader can act on it, got %q", want, p.CPUUnavailable)
		}
	}
	if strings.Contains(p.CPUSummary(), "idle") {
		t.Errorf("an unmeasurable subtree must never render as idle: %q", p.CPUSummary())
	}
	if p.CPUSource != "coarse-ps" {
		t.Errorf("CPUSource = %q, want coarse-ps", p.CPUSource)
	}
}

// TestCPUSummaryNeverCallsAnUnmeasuredSubtreeIdle guards the distinction that
// makes the signal safe to act on: "could not measure" and "measured, idle"
// lead to opposite decisions and must never render as one another.
func TestCPUSummaryNeverCallsAnUnmeasuredSubtreeIdle(t *testing.T) {
	unmeasured := &StepProgress{CPUUnavailable: "gate process not started yet"}
	if unmeasured.Subtree() != SubtreeUnknown {
		t.Errorf("an unsampled record must classify as unknown, got %v", unmeasured.Subtree())
	}
	s := unmeasured.CPUSummary()
	if !strings.Contains(s, "UNKNOWN") || !strings.Contains(s, "not started yet") {
		t.Errorf("an unmeasured subtree must say so, and why: %q", s)
	}
	if strings.Contains(s, "idle") {
		t.Errorf("an unmeasured subtree must never render as idle: %q", s)
	}

	measured := &StepProgress{CPUSampledAt: time.Now(), CPUProcs: 1, CPUWindow: "30s"}
	if measured.Subtree() != SubtreeIdle {
		t.Errorf("a zero-CPU sample must classify as idle, got %v", measured.Subtree())
	}
	if !strings.Contains(measured.CPUSummary(), "idle") {
		t.Errorf("a measured idle subtree must say idle, got %q", measured.CPUSummary())
	}

	gone := &StepProgress{CPUSampledAt: time.Now(), CPUProcs: 0}
	if gone.Subtree() != SubtreeGone {
		t.Errorf("an empty subtree means the process is gone, got %v", gone.Subtree())
	}
}

// TestApplyCPUClearsAStaleRate checks that a measurement which becomes
// unavailable takes its numbers with it. A rate left behind after the sampler
// starts failing would be read as current, which is the same class of error as
// reporting a heartbeat from a dead runner.
func TestApplyCPUClearsAStaleRate(t *testing.T) {
	w := &gateWatch{interval: 30 * time.Second}
	w.pid.Store(4856)
	p := &StepProgress{}

	w.cpu = cpuReading{At: time.Now(), Activity: subtreeActivity{Cores: 3.9, Procs: 2, Window: 30 * time.Second}}
	w.applyCPU(p)
	if p.CPUCores != 3.9 || p.CPUSampledAt.IsZero() || p.GatePID != 4856 {
		t.Fatalf("measurement was not recorded: %+v", p)
	}

	w.cpu = cpuReading{Unavailable: "reading the process table failed: ps: boom"}
	w.applyCPU(p)
	if !p.CPUSampledAt.IsZero() || p.CPUCores != 0 || p.CPUProcs != 0 || p.CPUWindow != "" {
		t.Errorf("a lost measurement must clear the numbers, got %+v", p)
	}
	if p.CPUUnavailable == "" {
		t.Error("a lost measurement must carry the reason")
	}
	if p.Subtree() != SubtreeUnknown {
		t.Errorf("a cleared measurement must classify as unknown, got %v", p.Subtree())
	}
}

// TestASlowProcessTableDoesNotDelayTheHeartbeat guards the ordering hazard the
// CPU signal introduces. Reading the process table shells out to `ps`, and a
// loaded host — exactly the condition under which someone inspects a refinery —
// can make that slow. If the sampler ran on the heartbeat goroutine, a slow ps
// would delay beats, a delayed beat reads as a DEAD runner, and the fix for a
// blind view would have manufactured a false alarm in the one case it matters.
func TestASlowProcessTableDoesNotDelayTheHeartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("real timing")
	}
	prev := psSnapshot
	psSnapshot = func() ([]procRow, error) {
		time.Sleep(2 * time.Second)
		return nil, errors.New("ps was slow")
	}
	t.Cleanup(func() { psSnapshot = prev })

	r := newProgressTestRefinery(t, 100*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, `quality_gate = "sleep 1"`)
	mr := &MergeRequest{ID: "mr-slow-ps", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("gate should have passed: %v", err)
	}

	// A 1s gate at a 100ms cadence beats ~10 times. Anything near zero means
	// the sampler is on the beat path.
	if mr.Progress.Beats < 5 {
		t.Errorf("heartbeat stalled behind the process-table read: %d beats in ~1s at a 100ms interval",
			mr.Progress.Beats)
	}
	// And the failure is reported, not silently rendered as an idle subtree.
	if mr.Progress.Subtree() != SubtreeUnknown {
		t.Errorf("a failed process-table read must classify as unknown, got %v", mr.Progress.Subtree())
	}
	if !strings.Contains(mr.Progress.CPUUnavailable, "ps was slow") {
		t.Errorf("the reason must be carried, got %q", mr.Progress.CPUUnavailable)
	}
}

// quoteTOML renders a shell command as a TOML basic string.
func quoteTOML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
