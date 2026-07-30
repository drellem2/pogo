package hostload

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/proctable"
)

// table builds a snapshot pair from (pid, ppid, cpuSecondsBefore, cpuSecondsAfter).
type proc struct {
	pid, ppid  int
	before, at float64
}

func snapshots(procs []proc, window time.Duration) (psSnapshot, *int) {
	calls := 0
	t0 := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	fn := func() (map[int]procRow, time.Time, error) {
		calls++
		rows := map[int]procRow{}
		for _, p := range procs {
			secs := p.before
			if calls > 1 {
				secs = p.at
			}
			// A negative "before" marks a process that did not exist yet.
			if calls == 1 && p.before < 0 {
				continue
			}
			rows[p.pid] = procRow{pid: p.pid, ppid: p.ppid,
				cpu: time.Duration(secs * float64(time.Second))}
		}
		at := t0
		if calls > 1 {
			at = t0.Add(window)
		}
		return rows, at, nil
	}
	return fn, &calls
}

func read(t *testing.T, procs []proc, roots []int) Sample {
	t.Helper()
	const window = time.Second
	snap, _ := snapshots(procs, window)
	r := &Reader{
		Roots:    roots,
		Window:   time.Millisecond, // the real wait; the snapshot stamps drive the math
		snapshot: snap,
		loadavg:  func() float64 { return 42.0 },
		cores:    10,
	}
	s, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return s
}

func near(got, want float64) bool { return math.Abs(got-want) < 0.01 }

// TestOneAgentManyProcessesCountsTheResource is the regression this package
// exists for. The live instance of mg-1b8c was ONE polecat that had
// self-parallelised into three Python processes at ~5.7 cores of a 10-core
// box. A guard that counts agents sees one agent and calls the host idle.
//
// The assertion is therefore on cores, not on agents: a single agent subtree
// must report ~5.7 cores, and the process count must be visible next to it so
// nobody re-derives "one agent" from the sample.
func TestOneAgentManyProcessesCountsTheResource(t *testing.T) {
	procs := []proc{
		{pid: 100, ppid: 1, before: 0, at: 0},        // pogod, idle
		{pid: 200, ppid: 100, before: 0, at: 0.05},   // the polecat's claude process
		{pid: 201, ppid: 200, before: 10, at: 12.72}, // its three compute children
		{pid: 202, ppid: 200, before: 10, at: 12.69},
		{pid: 203, ppid: 200, before: 10, at: 10.26},
	}
	s := read(t, procs, []int{100})

	if !near(s.FleetCores, 5.72) {
		t.Errorf("FleetCores = %.2f, want ~5.72 — the subtree's CPU, not a count of agents", s.FleetCores)
	}
	if s.FleetProcs != 4 {
		t.Errorf("FleetProcs = %d, want 4 (one agent, four busy processes)", s.FleetProcs)
	}
	if !near(s.ExternalCores, 0) {
		t.Errorf("ExternalCores = %.2f, want 0", s.ExternalCores)
	}
	if !s.Attributed {
		t.Error("Attributed = false, want true — pid 100 is in the table")
	}
	if got := s.FleetSaturation(); !near(got, 0.572) {
		t.Errorf("FleetSaturation = %.3f, want ~0.572", got)
	}
}

// TestExternalCPUIsNotAttributedToTheFleet holds the second half of the
// measurement that justified this package: on the measured host a VPN
// extension held ~0.9 cores and the indexer ~0.3, none of it ours. A guard
// keyed on total host CPU hands an unrelated process a veto over dispatch, so
// the split has to survive.
func TestExternalCPUIsNotAttributedToTheFleet(t *testing.T) {
	procs := []proc{
		{pid: 100, ppid: 1, before: 0, at: 0},
		{pid: 200, ppid: 100, before: 0, at: 1.0}, // fleet: 1 core
		{pid: 679, ppid: 1, before: 0, at: 0.95},  // NordLynx
		{pid: 18240, ppid: 1, before: 0, at: 0.3}, // mds_stores
	}
	s := read(t, procs, []int{100})

	if !near(s.FleetCores, 1.0) {
		t.Errorf("FleetCores = %.2f, want 1.0", s.FleetCores)
	}
	if !near(s.ExternalCores, 1.25) {
		t.Errorf("ExternalCores = %.2f, want 1.25", s.ExternalCores)
	}
	if !near(s.UsedCores(), 2.25) {
		t.Errorf("UsedCores = %.2f, want 2.25", s.UsedCores())
	}
	if !near(s.FreeCores(), 7.75) {
		t.Errorf("FreeCores = %.2f, want 7.75", s.FreeCores())
	}
}

// TestLoadAverageIsCarriedButNeverDecidedOn pins the distinction the package
// comment makes. The measured host reported a load average of 214 against ~7.5
// cores in use; a sample must be able to report a wild load average without
// that number moving anything a caller decides on.
func TestLoadAverageIsCarriedButNeverDecidedOn(t *testing.T) {
	procs := []proc{
		{pid: 100, ppid: 1, before: 0, at: 0},
		{pid: 200, ppid: 100, before: 0, at: 0.5},
	}
	const window = time.Second
	snap, _ := snapshots(procs, window)
	r := &Reader{Roots: []int{100}, Window: time.Millisecond, snapshot: snap,
		loadavg: func() float64 { return 214.0 }, cores: 10}
	s, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.LoadAvg1 != 214.0 {
		t.Errorf("LoadAvg1 = %v, want 214 carried through", s.LoadAvg1)
	}
	if !near(s.FleetCores, 0.5) {
		t.Errorf("FleetCores = %.2f, want 0.5 — a load average of 214 must not move it", s.FleetCores)
	}
	if !strings.Contains(s.String(), "context only") {
		t.Errorf("String() must label the load average as context; got %q", s.String())
	}
}

// TestUnattributedIsNotIdle: when no fleet root is present, FleetCores is zero
// because nothing could be attributed. That is a different fact from "the
// fleet is idle" and a caller must be able to tell them apart.
func TestUnattributedIsNotIdle(t *testing.T) {
	procs := []proc{
		{pid: 679, ppid: 1, before: 0, at: 3.0},
	}
	s := read(t, procs, []int{100}) // pid 100 is not in the table
	if s.Attributed {
		t.Error("Attributed = true, want false — no fleet root in the process table")
	}
	if !near(s.FleetCores, 0) {
		t.Errorf("FleetCores = %.2f, want 0", s.FleetCores)
	}
	if !near(s.ExternalCores, 3.0) {
		t.Errorf("ExternalCores = %.2f, want 3.0", s.ExternalCores)
	}
	if got := s.String(); !strings.Contains(got, "unattributed") {
		t.Errorf("String() must say the fleet share is unattributed; got %q", got)
	}

	// And with no roots configured at all.
	s = read(t, procs, nil)
	if s.Attributed {
		t.Error("Attributed = true with no roots configured")
	}
}

// TestProcessBornDuringWindowCounts: a gate that forks a compiler mid-sample
// must contribute, or a spawn storm reads as an idle host.
func TestProcessBornDuringWindowCounts(t *testing.T) {
	procs := []proc{
		{pid: 100, ppid: 1, before: 0, at: 0},
		{pid: 300, ppid: 100, before: -1, at: 0.8}, // did not exist at t0
	}
	s := read(t, procs, []int{100})
	if !near(s.FleetCores, 0.8) {
		t.Errorf("FleetCores = %.2f, want 0.8 for a process born inside the window", s.FleetCores)
	}
}

// TestNewProcessCPUIsCappedAtTheWindow: a `ps` row claiming more CPU than the
// host could physically have delivered inside the window must not skew a
// sample without bound.
func TestNewProcessCPUIsCappedAtTheWindow(t *testing.T) {
	procs := []proc{
		{pid: 100, ppid: 1, before: 0, at: 0},
		{pid: 300, ppid: 100, before: -1, at: 9000}, // absurd
	}
	s := read(t, procs, []int{100})
	if !near(s.FleetCores, 10) {
		t.Errorf("FleetCores = %.2f, want it capped at the 10-core host", s.FleetCores)
	}
}

// TestCyclicPSTableTerminates: a malformed parent chain must not spin.
func TestCyclicPSTableTerminates(t *testing.T) {
	rows := map[int]procRow{
		1: {pid: 1, ppid: 2, cpu: time.Second},
		2: {pid: 2, ppid: 1, cpu: time.Second},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fleetSubtree(rows, []int{99})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fleetSubtree did not terminate on a cyclic table")
	}
}

func TestReadRespectsContextCancellation(t *testing.T) {
	procs := []proc{{pid: 100, ppid: 1, before: 0, at: 0}}
	snap, calls := snapshots(procs, time.Second)
	r := &Reader{Roots: []int{100}, Window: time.Minute, snapshot: snap,
		loadavg: func() float64 { return 0 }, cores: 10}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Read(ctx); err == nil {
		t.Fatal("expected a context error")
	}
	if *calls != 1 {
		t.Errorf("took %d snapshots after cancellation, want 1", *calls)
	}
}

// TestReadRealHost exercises the real process-table path. It asserts shape,
// not values: what the host is doing while the suite runs is not ours to
// predict.
//
// The one value it does assert — that SOMETHING is using CPU on a host that is
// currently running this test — is the assertion that went red across CI from
// 11:17 on 2026-07-30 (mg-79e3), and it is kept. What changed is that the
// window is now chosen from the host's measured CPU-time resolution instead of
// being hardcoded at 200ms, so the test measures over a window this host can
// actually answer. Where no such window exists the test says so and skips,
// rather than asserting a number the environment cannot produce.
func TestReadRealHost(t *testing.T) {
	src := proctable.Current()
	t.Logf("process-table source: %s", src)

	window := 200 * time.Millisecond
	if !src.CanResolve(window) {
		window = src.MinWindow()
	}
	// A window this wide is no longer a unit test, and a source that needs one
	// cannot support the sub-second measurements the refinery's gate watch
	// takes either. Naming the environment is the report; a zero would not be.
	if window > 2*time.Second {
		t.Skipf("no usable measurement here: %s, so the shortest trustworthy window is %s",
			src, src.MinWindow())
	}

	r := &Reader{Roots: []int{1}, Window: window}
	s, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	t.Logf("sample over %s: %s", window, s)
	if s.Cores <= 0 {
		t.Errorf("Cores = %d", s.Cores)
	}
	if s.FleetCores < 0 || s.ExternalCores < 0 {
		t.Errorf("negative cores: %+v", s)
	}
	if s.Source != src.Name {
		t.Errorf("Source = %q, want %q — a sample must name the instrument it was taken with", s.Source, src.Name)
	}
	if !s.Resolved() {
		t.Fatalf("Read reported the window as unresolvable despite choosing it from the source: %s", s.Unresolvable)
	}
	// Rooted at pid 1, essentially everything is a descendant.
	if !s.Attributed {
		t.Error("Attributed = false rooted at pid 1")
	}
	if s.UsedCores() == 0 {
		t.Errorf("UsedCores = 0 on a host that is running this test (source %s, window %s)", src, s.Window)
	}
}

// TestReadOnAHostTooCoarseToMeasure is the other half, and it runs everywhere.
// It is what keeps the skip in TestReadRealHost honest: a source whose CPU
// column cannot resolve the window must produce a REASON, not the zeros that
// read as an idle host. Those zeros are what a dispatch guard would have acted
// on, and "the fleet is using nothing" and "I cannot tell" call for opposite
// decisions.
func TestReadOnAHostTooCoarseToMeasure(t *testing.T) {
	// A host whose processes really are burning CPU...
	procs := []proc{{pid: 100, ppid: 1, before: 0, at: 4}, {pid: 200, ppid: 1, before: 0, at: 2}}
	snap, _ := snapshots(procs, 400*time.Millisecond)
	r := &Reader{
		Roots: []int{100},
		// The snapshot stamps are 400ms apart; the real wait is short because
		// the arithmetic under test is driven by the stamps, not the sleep.
		Window:   time.Millisecond,
		Source:   proctable.Source{Name: "coarse-ps", Resolution: time.Second},
		snapshot: snap,
		loadavg:  func() float64 { return 1.5 },
		cores:    10,
	}
	s, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Resolved() {
		t.Fatal("a 400ms window on a whole-second column was reported as a measurement")
	}
	if !strings.Contains(s.Unresolvable, "coarse-ps") || !strings.Contains(s.Unresolvable, "400ms") {
		t.Errorf("the reason must name the source and the window, got %q", s.Unresolvable)
	}
	if s.Source != "coarse-ps" {
		t.Errorf("Source = %q, want coarse-ps", s.Source)
	}
	// The context that is still true is still reported; the numbers that would
	// be fabrications are not.
	if s.Cores != 10 || s.LoadAvg1 != 1.5 {
		t.Errorf("host context must survive an unresolvable sample: %+v", s)
	}
	if s.FleetCores != 0 || s.ExternalCores != 0 || s.Attributed {
		t.Errorf("an unresolvable sample must not carry attribution numbers: %+v", s)
	}
	if !strings.Contains(s.String(), "unmeasurable") {
		t.Errorf("an unresolvable sample must not render as a reading: %q", s.String())
	}
}

// TestReadStampsTheSourceOnEverySample keeps the environment on the record.
// This failure was undiagnosable from the CI log precisely because no sample
// said where it came from.
func TestReadStampsTheSourceOnEverySample(t *testing.T) {
	snap, _ := snapshots([]proc{{pid: 100, ppid: 1, before: 0, at: 1}}, time.Second)
	r := &Reader{Roots: []int{100}, Window: time.Millisecond, snapshot: snap,
		loadavg: func() float64 { return 0 }, cores: 4}
	s, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Source == "" {
		t.Error("a sample with no named source cannot be told from one taken with a broken instrument")
	}
	if !s.Resolved() {
		t.Errorf("a 1s window must resolve on this host: %s", s.Unresolvable)
	}
}
