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

// spinCore burns one core inside this process until the test ends. It is what
// turns the real-host assertion below into an assertion about the INSTRUMENT
// rather than about the machine: a sample can only report CPU that was
// actually spent, and the only CPU a test can guarantee was spent is its own.
//
// One goroutine — one core at most — deliberately. The suite shares this host
// with the merge gate and with other agents, and a test that measures
// contention must not manufacture it.
func spinCore(t *testing.T) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}

// TestReadRealHost exercises the real process-table path. It asserts shape,
// not values: what the host is doing while the suite runs is not ours to
// predict.
//
// The one value it does assert — that CPU which was demonstrably spent is
// reported as spent — is the assertion that went red across CI from 11:17 on
// 2026-07-30 (mg-79e3), and it is kept. Two things have changed since:
//
//   - mg-79e3: the window is chosen from the host's measured CPU-time
//     resolution instead of being hardcoded at 200ms, so the test measures
//     over a window this host can actually answer. Where no such window exists
//     the test says so and skips, rather than asserting a number the
//     environment cannot produce.
//   - mg-d54a: the CPU is now SPENT BY THIS TEST. The old form asserted
//     "UsedCores != 0 on a host that is running this test" and took the host's
//     busyness for granted, which is not a property of any host — 7 of 15 CI
//     runs on main failed exactly there, on an idle 4-core Linux runner. The
//     premise was false at the root, because the process running this test
//     SLEEPS through the window it is measuring: its own CPU across a 200ms
//     window, read from getrusage on 2026-08-20, was 34-162µs — ZERO ticks of
//     a 10ms column, ten times out of ten. Every nonzero the old assertion
//     ever saw came from OTHER processes on a busy developer box, and an idle
//     runner has none. See TestSubTickWorkIsAnHonestZero for the arithmetic.
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

	// Burn a core for the rest of the test, so that there is CPU on this host
	// for the sample to find whatever else the host is or is not doing.
	spinCore(t)

	r := &Reader{Roots: []int{1}, Window: window}

	// Up to three samples. The claim is "work that was done is reported", not
	// "the scheduler handed us a core on the first try", so any one nonzero
	// reading settles it. No MAGNITUDE is asserted: cores are shared, and a
	// floor on a shared resource is an assertion about the box, which is how
	// four innocent branches went red in one evening (mg-6c90).
	var s Sample
	for attempt := 1; attempt <= 3; attempt++ {
		var err error
		s, err = r.Read(context.Background())
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		t.Logf("sample %d over %s: %s", attempt, window, s)
		if s.UsedCores() > 0 {
			break
		}
	}

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
		t.Errorf("UsedCores = 0 while this test was burning a core (source %s, window %s) — "+
			"a sample that cannot see CPU this process demonstrably spent is not measuring",
			src, s.Window)
	}
}

// TestSubTickWorkIsAnHonestZero pins the state the old TestReadRealHost denied
// could exist, and it runs on every platform — which is the point, because the
// merge gate runs on darwin and this failure only ever showed up on Linux.
//
// Every Row.CPU is truncated to the source's Resolution, so a process that
// spent less than one tick inside the window reports the SAME cumulative
// figure at both ends of it. Real work, zero delta.
//
// Over a window the source CAN resolve, that zero is a measurement — "nothing
// crossed a tick here" — and Read must report it as one. It is emphatically
// not Unresolvable, which means the window was too short for the instrument;
// conflating the two throws away the distinction that field exists for.
func TestSubTickWorkIsAnHonestZero(t *testing.T) {
	// A 10ms source over a 200ms window: resolvable five times over. The
	// processes are doing work, but each spent under one tick of it, so the
	// truncated column they report does not move.
	procs := []proc{
		{pid: 100, ppid: 1, before: 12, at: 12},
		{pid: 200, ppid: 100, before: 3, at: 3},
		{pid: 300, ppid: 1, before: 900, at: 900},
	}
	snap, _ := snapshots(procs, 200*time.Millisecond)
	r := &Reader{
		Roots:    []int{100},
		Window:   time.Millisecond, // the real wait; the snapshot stamps drive the math
		Source:   proctable.Source{Name: "linux-procfs", Resolution: 10 * time.Millisecond},
		snapshot: snap,
		loadavg:  func() float64 { return 1.98 },
		cores:    4,
	}
	s, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !s.Resolved() {
		t.Fatalf("a 200ms window is resolvable at 10ms resolution, but Read called it %q", s.Unresolvable)
	}
	if s.UsedCores() != 0 {
		t.Errorf("UsedCores = %.3f, want 0 — no process crossed a tick inside the window", s.UsedCores())
	}
	// The roots WERE found: this is an idle fleet, not a missing one, and the
	// two must stay distinguishable at a zero.
	if !s.Attributed {
		t.Error("Attributed = false with pid 100 in the table — a zero here means idle, not unattributed")
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
