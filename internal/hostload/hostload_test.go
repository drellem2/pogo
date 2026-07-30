package hostload

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseCPUTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"0:01.25", 1250 * time.Millisecond, true},
		{"169:10.30", 169*time.Minute + 10300*time.Millisecond, true},
		{"12:34:56", 12*time.Hour + 34*time.Minute + 56*time.Second, true},
		{"2-03:04:05", 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second, true},
		{"", 0, false},
		{"garbage", 0, false},
		{"1:2:3:4", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCPUTime(c.in)
		if ok != c.ok {
			t.Errorf("parseCPUTime(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseCPUTime(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestParsePSRow(t *testing.T) {
	row, ok := parsePSRow("  1234   567 0:12.50")
	if !ok {
		t.Fatal("expected a parse")
	}
	if row.pid != 1234 || row.ppid != 567 || row.cpu != 12500*time.Millisecond {
		t.Errorf("got %+v", row)
	}
	if _, ok := parsePSRow("nonsense"); ok {
		t.Error("expected no parse for a short line")
	}
}

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

// TestReadRealHost exercises the real `ps` path. It asserts shape, not values:
// what the host is doing while the suite runs is not ours to predict.
func TestReadRealHost(t *testing.T) {
	r := &Reader{Roots: []int{1}, Window: 200 * time.Millisecond}
	s, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Cores <= 0 {
		t.Errorf("Cores = %d", s.Cores)
	}
	if s.FleetCores < 0 || s.ExternalCores < 0 {
		t.Errorf("negative cores: %+v", s)
	}
	// Rooted at pid 1, essentially everything is a descendant.
	if !s.Attributed {
		t.Error("Attributed = false rooted at pid 1")
	}
	if s.UsedCores() == 0 {
		t.Error("UsedCores = 0 on a host that is running this test")
	}
}
