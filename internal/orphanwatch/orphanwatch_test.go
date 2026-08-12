package orphanwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/proctable"
)

// fakeTable returns a Table func that reports the same processes twice, the
// second time with the given CPU deltas applied. cores is CPU-seconds added per
// process over a one-second window.
func fakeTable(rows []proctable.Row, delta map[int]time.Duration) func() ([]proctable.Row, error) {
	call := 0
	return func() ([]proctable.Row, error) {
		call++
		out := make([]proctable.Row, len(rows))
		copy(out, rows)
		if call > 1 {
			for i := range out {
				out[i].CPU += delta[out[i].PID]
			}
		}
		return out, nil
	}
}

// fineSource is a source precise enough that a one-second window resolves,
// so these tests do not depend on the host's ps dialect.
var fineSource = proctable.Source{Name: "test", Resolution: 10 * time.Millisecond}

const testRoot = "/tmp/polecats"

// baseOpts runs the scan on a stubbed clock: Sleep advances it by exactly the
// window it was asked to wait, so the rate the scan computes is arithmetic a
// reader can check rather than a function of how fast the test host ran.
func baseOpts(t *testing.T) Options {
	t.Helper()
	clock := time.Unix(0, 0)
	return Options{
		PolecatsRoot: testRoot,
		Window:       time.Second,
		Source:       fineSource,
		Sleep:        func(d time.Duration) { clock = clock.Add(d) },
		Now:          func() time.Time { return clock },
	}
}

// TestScanReportsDeadOwnerAndSparesLiveOwner is the core discrimination, on a
// population where the two processes are IDENTICAL in every respect the old
// heuristic could see: both ppid=1, both burning a core, both attributable.
// Only the registry answer differs.
func TestScanReportsDeadOwnerAndSparesLiveOwner(t *testing.T) {
	rows := []proctable.Row{
		{PID: 100, PPID: 1, PGID: 100},
		{PID: 200, PPID: 1, PGID: 200},
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{100: time.Second, 200: time.Second})
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{
			100: testRoot + "/pdead/code/audit",
			200: testRoot + "/plive/code/audit",
		}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{"plive": true}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 1 {
		t.Fatalf("orphans = %d (%+v), want exactly 1", len(rep.Orphans), rep.Orphans)
	}
	if rep.Orphans[0].PID != 100 {
		t.Errorf("reported pid = %d, want 100 (the dead owner's)", rep.Orphans[0].PID)
	}
	if rep.Orphans[0].Owner != "pdead" {
		t.Errorf("owner = %q, want %q", rep.Orphans[0].Owner, "pdead")
	}
	if rep.LiveOwner != 1 {
		t.Errorf("live_owner = %d, want 1; the live polecat's worker must be counted as spared, not merely omitted", rep.LiveOwner)
	}
	if rep.Busy != 2 {
		t.Errorf("busy = %d, want 2", rep.Busy)
	}
}

// TestScanIgnoresPPID is the explicit refutation of the heuristic this replaces.
// The orphan is given a LIVE, ordinary parent and the spared process is given
// ppid=1 — the exact inverse of what a ppid rule would conclude. The verdict
// must not move.
func TestScanIgnoresPPID(t *testing.T) {
	rows := []proctable.Row{
		{PID: 100, PPID: 4242, PGID: 4242}, // dead owner, but properly parented
		{PID: 200, PPID: 1, PGID: 200},     // live owner, reparented to launchd
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{100: time.Second, 200: time.Second})
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{100: testRoot + "/pdead", 200: testRoot + "/plive"}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{"plive": true}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0].PID != 100 {
		t.Fatalf("orphans = %+v, want exactly pid 100; the verdict moved with ppid", rep.Orphans)
	}
}

// TestScanIgnoresIdleProcesses separates this defect from the one it must not
// collect: a process blocked forever consumes no CPU. The 31h39m pogo-deploy.sh
// stuck in an unbounded `git fetch` is correctly parented, reported by nothing,
// and burning ~0% — a real problem, and not this one.
func TestScanIgnoresIdleProcesses(t *testing.T) {
	rows := []proctable.Row{{PID: 100, PPID: 1, PGID: 100, CPU: 90 * time.Minute}}
	opts := baseOpts(t)
	// A large ACCUMULATED CPU with no growth in the window: exactly the shape a
	// lifetime-average %cpu column would misread as busy.
	opts.Table = fakeTable(rows, map[int]time.Duration{100: 0})
	opts.Cwds = func([]int) map[int]string { return map[int]string{100: testRoot + "/pdead"} }
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 0 {
		t.Errorf("orphans = %+v, want none; a stalled process is a different defect", rep.Orphans)
	}
	if rep.Busy != 0 {
		t.Errorf("busy = %d, want 0", rep.Busy)
	}
}

// TestScanFailsClosedOnUnattributableCwd pins the blind spot as a COUNTER, never
// a verdict. A worker that chdir'd out of its polecat tree lands here, and so
// does every unrelated program on the machine.
func TestScanFailsClosedOnUnattributableCwd(t *testing.T) {
	rows := []proctable.Row{
		{PID: 100, PPID: 1, PGID: 100},
		{PID: 200, PPID: 1, PGID: 200},
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{100: time.Second, 200: time.Second})
	opts.Cwds = func([]int) map[int]string {
		// 100's cwd is elsewhere; 200's could not be read at all.
		return map[int]string{100: "/Users/daniel/dev/pogo"}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 0 {
		t.Fatalf("orphans = %+v, want none; unattributable is not orphan", rep.Orphans)
	}
	if rep.Unattributable != 1 {
		t.Errorf("unattributable = %d, want 1", rep.Unattributable)
	}
	if rep.CwdUnreadable != 1 {
		t.Errorf("cwd_unreadable = %d, want 1; an unreadable cwd is an instrument limit and must be counted apart from a readable one that carries no marker", rep.CwdUnreadable)
	}
}

// TestScanRefusesWithoutLiveness is the safety margin, asserted directly. With
// the registry unreachable every attributable process has a dead-looking owner,
// so a scan that carried on would name all of them.
func TestScanRefusesWithoutLiveness(t *testing.T) {
	rows := []proctable.Row{{PID: 100, PPID: 1, PGID: 100}}

	t.Run("nil source", func(t *testing.T) {
		opts := baseOpts(t)
		opts.Table = fakeTable(rows, map[int]time.Duration{100: time.Second})
		opts.Cwds = func([]int) map[int]string { return map[int]string{100: testRoot + "/plive"} }
		_, err := Scan(opts)
		if !errors.Is(err, ErrNoLiveness) {
			t.Fatalf("Scan with no LiveOwners: err = %v, want ErrNoLiveness", err)
		}
	})

	t.Run("registry error", func(t *testing.T) {
		opts := baseOpts(t)
		opts.Table = fakeTable(rows, map[int]time.Duration{100: time.Second})
		opts.Cwds = func([]int) map[int]string { return map[int]string{100: testRoot + "/plive"} }
		opts.LiveOwners = func() (map[string]bool, error) { return nil, errors.New("connection refused") }
		rep, err := Scan(opts)
		if !errors.Is(err, ErrNoLiveness) {
			t.Fatalf("Scan with a failing registry: err = %v, want ErrNoLiveness", err)
		}
		if len(rep.Orphans) != 0 {
			t.Errorf("orphans = %+v on a failed liveness lookup; must be empty", rep.Orphans)
		}
	})
}

// TestScanRefusesAnUnresolvableWindow pins the other refusal. A window this host
// cannot resolve yields zero rates, and a fabricated zero reported as a
// measurement is how a CPU signal goes silently blind.
func TestScanRefusesAnUnresolvableWindow(t *testing.T) {
	opts := baseOpts(t)
	opts.Source = proctable.Source{Name: "coarse", Resolution: time.Second}
	opts.Window = 50 * time.Millisecond
	opts.Table = fakeTable(nil, nil)
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	if _, err := Scan(opts); err == nil {
		t.Fatal("Scan over a window the source cannot resolve returned nil error; it must refuse rather than report zeroes")
	}
}

// TestScanSkipsProcessesBornInsideTheWindow guards against the newborn case
// being charged its whole lifetime CPU as if it were earned in the window,
// which would put short-lived helpers over the floor.
func TestScanSkipsProcessesBornInsideTheWindow(t *testing.T) {
	call := 0
	opts := baseOpts(t)
	opts.Table = func() ([]proctable.Row, error) {
		call++
		if call == 1 {
			return nil, nil
		}
		return []proctable.Row{{PID: 100, PPID: 1, PGID: 100, CPU: 30 * time.Second}}, nil
	}
	opts.Cwds = func([]int) map[int]string { return map[int]string{100: testRoot + "/pdead"} }
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.Busy != 0 || len(rep.Orphans) != 0 {
		t.Errorf("busy = %d orphans = %+v, want 0 and none for a process with no earlier reading", rep.Busy, rep.Orphans)
	}
}

// TestScanAttributesThroughASymlinkedRoot is a regression guard for the failure
// the live probe caught on its first run.
//
// A working directory read out of the kernel is fully symlink-resolved; a
// configured root is not. On darwin /tmp, /var and every t.TempDir() sit behind
// a symlink into /private, so a scan rooted at the configured spelling compares
// two different strings for the same directory, attributes nothing, and reports
// a tree full of orphans as clean. That is the worst possible failure for this
// detector — a silent green — and it is invisible without this test.
func TestScanAttributesThroughASymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "polecats")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", link, err)
	}

	rows := []proctable.Row{{PID: 100, PPID: 1, PGID: 100}}
	opts := baseOpts(t)
	opts.PolecatsRoot = link // the configured spelling
	opts.Table = fakeTable(rows, map[int]time.Duration{100: time.Second})
	// The kernel's spelling, which is what a real cwd read returns.
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{100: filepath.Join(resolved, "pdead", "code")}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 1 {
		t.Fatalf("orphans = %d (unattributable=%d), want 1; the scan did not see through the symlinked root",
			len(rep.Orphans), rep.Unattributable)
	}
	if rep.Orphans[0].Owner != "pdead" {
		t.Errorf("owner = %q, want %q", rep.Orphans[0].Owner, "pdead")
	}
}

// TestScanOrdersByCost puts the costliest first, so a reader acting on a long
// report acts on the expensive one.
func TestScanOrdersByCost(t *testing.T) {
	rows := []proctable.Row{
		{PID: 100, PPID: 1, PGID: 100},
		{PID: 200, PPID: 1, PGID: 200},
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{
		100: 400 * time.Millisecond,
		200: 900 * time.Millisecond,
	})
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{100: testRoot + "/pa", 200: testRoot + "/pb"}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 2 {
		t.Fatalf("orphans = %d, want 2", len(rep.Orphans))
	}
	if rep.Orphans[0].PID != 200 {
		t.Errorf("first orphan = pid %d (%.2f cores), want pid 200 — costliest first", rep.Orphans[0].PID, rep.Orphans[0].Cores)
	}
	if got := rep.TotalCores(); got < 1.2 || got > 1.4 {
		t.Errorf("TotalCores() = %.2f, want ~1.30", got)
	}
}

// TestDispositionsAreTheCountersPerProcess pins the invariant that makes the map
// safe to reason from: it is the same information the four counters carry, keyed
// by pid instead of summed. If the two ever disagree, a caller asking about a
// process it constructed gets a different answer from a reader of the report,
// and the whole point of mg-db12's separation is gone.
//
// The population deliberately fills all four buckets at once.
func TestDispositionsAreTheCountersPerProcess(t *testing.T) {
	rows := []proctable.Row{
		{PID: 100, PPID: 1, PGID: 100}, // dead owner   -> orphan
		{PID: 200, PPID: 1, PGID: 200}, // live owner   -> live_owner
		{PID: 300, PPID: 1, PGID: 300}, // cwd off-root -> unattributable
		{PID: 400, PPID: 1, PGID: 400}, // no cwd       -> cwd_unreadable
		{PID: 500, PPID: 1, PGID: 500}, // idle         -> not a candidate at all
		{PID: 600, PPID: 1, PGID: 600}, // dead owner, trivial -> below_owner_floor
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{
		100: time.Second, 200: time.Second, 300: time.Second, 400: time.Second,
		600: 50 * time.Millisecond,
	})
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{
			100: testRoot + "/pdead/code",
			200: testRoot + "/plive/code",
			300: "/Users/daniel/dev/pogo",
			600: testRoot + "/ptrivial/code",
		}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{"plive": true}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := map[int]Disposition{
		100: DispositionOrphan,
		200: DispositionLiveOwner,
		300: DispositionUnattributable,
		400: DispositionCwdUnreadable,
		600: DispositionBelowOwnerFloor,
	}
	for pid, w := range want {
		got, ok := rep.DispositionOf(pid)
		if !ok {
			t.Errorf("pid %d has no disposition; want %q", pid, w)
			continue
		}
		if got != w {
			t.Errorf("pid %d disposition = %q, want %q", pid, got, w)
		}
	}
	if d, ok := rep.DispositionOf(500); ok {
		t.Errorf("idle pid 500 got disposition %q; a process below the rate floor was never "+
			"examined and must be ABSENT, not binned — that is the fifth state the probe reads", d)
	}
	if len(rep.Dispositions) != 5 {
		t.Errorf("dispositions = %v, want exactly the 5 busy pids", rep.Dispositions)
	}

	// The histogram identity: every counter equals the number of pids in its
	// bucket, and the buckets exhaust the busy population.
	counts := map[Disposition]int{}
	for _, d := range rep.Dispositions {
		counts[d]++
	}
	for d, n := range map[Disposition]int{
		DispositionOrphan:          len(rep.Orphans),
		DispositionLiveOwner:       rep.LiveOwner,
		DispositionUnattributable:  rep.Unattributable,
		DispositionCwdUnreadable:   rep.CwdUnreadable,
		DispositionBelowOwnerFloor: rep.BelowOwnerFloor,
	} {
		if counts[d] != n {
			t.Errorf("%q: %d pids in the map but the counter says %d", d, counts[d], n)
		}
	}
	if len(rep.Dispositions) != rep.Busy {
		t.Errorf("dispositions = %d, busy = %d; every busy process must land in exactly one bucket",
			len(rep.Dispositions), rep.Busy)
	}
}

// TestASwarmOfSMALLProcessesIsSTILLOneDeadOwnersCompute is mg-c675, and it is
// the case a per-process floor cannot see.
//
// 52 busy-loops orphaned by one polecat held 8.7 of this host's 10 cores for 41
// minutes. Per process that is 8.7/52 = 0.167 cores — BELOW the 0.20 floor,
// which was calibrated on mg-4518's population of one-to-three orphans at 0.38
// to 0.94 cores each. The floor is not merely too high for this population: the
// per-process rate of a swarm contending for a fixed number of cores is
// capacity/N, so the detector goes blinder the worse the leak gets, and a leak
// large enough to saturate the host is the one it cannot report at all.
func TestASwarmOfSMALLProcessesIsSTILLOneDeadOwnersCompute(t *testing.T) {
	const swarm = 52
	// 8.7 cores shared by 52 contending processes, as measured on 2026-08-12.
	totalCores := 8.7
	each := time.Duration(totalCores / swarm * float64(time.Second))

	rows := make([]proctable.Row, 0, swarm)
	delta := make(map[int]time.Duration, swarm)
	dirs := make(map[int]string, swarm)
	for i := 0; i < swarm; i++ {
		pid := 1000 + i
		rows = append(rows, proctable.Row{PID: pid, PPID: 1, PGID: pid})
		delta[pid] = time.Duration(each)
		dirs[pid] = testRoot + "/pdead/scratch"
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, delta)
	opts.Cwds = func([]int) map[int]string { return dirs }
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != swarm {
		t.Fatalf("orphans = %d, want %d; one dead owner holding %.1f cores is a finding "+
			"whether it holds them in one process or in fifty (busy=%d, below_owner_floor=%d)",
			len(rep.Orphans), swarm, totalCores, rep.Busy, rep.BelowOwnerFloor)
	}
	if got := rep.TotalCores(); got < totalCores-0.1 || got > totalCores+0.1 {
		t.Errorf("total = %.2f cores, want ~%.1f", got, totalCores)
	}
}

// TestSubdividingComputeCannotGetUNDERTheOwnerFloor is the property the
// per-process floor did not have, asserted as a property rather than at one
// point: the SAME one core of orphaned compute is reported whether the dead
// owner is holding it in 1 process or in 100.
//
// Under the old rule this table is a step function — reported at 1 and 2
// processes, invisible from 5 on — and the direction of that step is what makes
// it dangerous, because the leak gets less visible as it gets bigger.
func TestSubdividingComputeCannotGetUNDERTheOwnerFloor(t *testing.T) {
	// The range stops at 50 because that is where the property stops being
	// true: 1.00/50 = 0.020 cores is exactly DefaultCandidateFloor, and below it
	// nothing is attributed at all. That boundary is not hidden here — it is
	// asserted directly by TestTheResidualBlindSpotIsWHERETheDocSaysItIs.
	for _, n := range []int{1, 2, 5, 20, 50} {
		t.Run(fmt.Sprintf("%dprocs", n), func(t *testing.T) {
			total := 1.0
			each := time.Duration(total / float64(n) * float64(time.Second))
			rows := make([]proctable.Row, 0, n)
			delta := make(map[int]time.Duration, n)
			dirs := make(map[int]string, n)
			for i := 0; i < n; i++ {
				pid := 1000 + i
				rows = append(rows, proctable.Row{PID: pid, PPID: 1, PGID: pid})
				delta[pid] = each
				dirs[pid] = testRoot + "/pdead"
			}
			opts := baseOpts(t)
			opts.Table = fakeTable(rows, delta)
			opts.Cwds = func([]int) map[int]string { return dirs }
			opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

			rep, err := Scan(opts)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(rep.Orphans) != n {
				t.Fatalf("orphans = %d, want %d; %.2f cores per process is under the %.2f "+
					"reporting floor, and that must not matter — the owner still holds one core",
					len(rep.Orphans), n, total/float64(n), rep.Floor)
			}
			if len(rep.Owners) != 1 || rep.Owners[0].Procs != n {
				t.Fatalf("owners = %+v, want one owner with %d processes", rep.Owners, n)
			}
			if got := rep.Owners[0].Cores; got < 0.99 || got > 1.01 {
				t.Errorf("owner total = %.3f cores, want ~1.00", got)
			}
		})
	}
}

// TestATrivialSurvivorIsSparedButSTILLDecidedAbout pins the other side of the
// per-owner rule. Lowering the candidate floor to 0.02 admits small processes
// for ATTRIBUTION, and this asserts that admitting them did not also start
// reporting them: a dead owner holding 0.05 cores is not a finding.
//
// It must land in below_owner_floor and not simply vanish. The two readings —
// "spared as trivial" and "never examined" — are what tells a reader whether
// the floor is set too high, and they are indistinguishable once flattened.
func TestATrivialSurvivorIsSparedButSTILLDecidedAbout(t *testing.T) {
	rows := []proctable.Row{{PID: 100, PPID: 1, PGID: 100}}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{100: 50 * time.Millisecond})
	opts.Cwds = func([]int) map[int]string { return map[int]string{100: testRoot + "/pdead"} }
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 0 {
		t.Fatalf("orphans = %+v, want none; 0.05 cores is under the reporting floor", rep.Orphans)
	}
	if rep.BelowOwnerFloor != 1 {
		t.Errorf("below_owner_floor = %d, want 1", rep.BelowOwnerFloor)
	}
	if d, ok := rep.DispositionOf(100); !ok || d != DispositionBelowOwnerFloor {
		t.Errorf("disposition = %q (present=%v), want %q; a spared process was DECIDED about "+
			"and must not read the same as one that was never examined",
			d, ok, DispositionBelowOwnerFloor)
	}
	if len(rep.Owners) != 0 {
		t.Errorf("owners = %+v, want none", rep.Owners)
	}
}

// TestTheOwnerFloorSumsWITHINAnOwnerNotAcrossThem pins the unit. Two dead
// polecats holding 0.15 cores each is not one finding of 0.30: nobody would be
// told to kill anything, because neither owner is costing enough to act on and
// the sum belongs to no one.
func TestTheOwnerFloorSumsWITHINAnOwnerNotAcrossThem(t *testing.T) {
	rows := []proctable.Row{
		{PID: 100, PPID: 1, PGID: 100},
		{PID: 200, PPID: 1, PGID: 200},
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{100: 150 * time.Millisecond, 200: 150 * time.Millisecond})
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{100: testRoot + "/pdeadA", 200: testRoot + "/pdeadB"}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Orphans) != 0 {
		t.Fatalf("orphans = %+v, want none; 0.15 + 0.15 across two owners is not one 0.30 finding",
			rep.Orphans)
	}
	if rep.BelowOwnerFloor != 2 {
		t.Errorf("below_owner_floor = %d, want 2", rep.BelowOwnerFloor)
	}
}

// TestTheStuckProcessCLASSIsStillNotCollected guards the discriminator the
// lower candidate floor could have dissolved. A pogo-deploy.sh blocked 31h39m in
// an unbounded `git fetch` sits at ~0.00 cores; it is a different defect, it
// routes elsewhere, and a detector that started naming it would be reporting
// two things under one word.
func TestTheStuckProcessCLASSIsStillNotCollected(t *testing.T) {
	rows := []proctable.Row{{PID: 100, PPID: 1, PGID: 100, CPU: 31*time.Hour + 39*time.Minute}}
	opts := baseOpts(t)
	// A millisecond of CPU in a one-second window: 0.001 cores, the shape of a
	// process blocked in a syscall that occasionally wakes.
	opts.Table = fakeTable(rows, map[int]time.Duration{100: time.Millisecond})
	opts.Cwds = func([]int) map[int]string { return map[int]string{100: testRoot + "/pdead"} }
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.Busy != 0 {
		t.Fatalf("busy = %d, want 0; a blocked process is a different defect and must not "+
			"become a candidate", rep.Busy)
	}
	if d, ok := rep.DispositionOf(100); ok {
		t.Errorf("pid 100 got disposition %q; below the candidate floor is the fifth state "+
			"and must stay ABSENT from the map", d)
	}
}

// TestACandidateFloorAtOrAboveTheReportingFloorIsREFUSED stops an operator
// re-creating the mg-c675 defect through the flag, and pins that the refusal is
// a refusal rather than a repair.
//
// The first attempt at this CLAMPED the candidate floor down to the reporting
// floor, and this test is what caught it: clamping produces the per-process rule
// exactly — nothing is attributed unless it alone clears the floor — so the
// guard against the defect was the defect. There is no safe substitute value, so
// the run refuses instead of conducting a different one.
func TestACandidateFloorAtOrAboveTheReportingFloorIsREFUSED(t *testing.T) {
	const swarm = 10
	rows := make([]proctable.Row, 0, swarm)
	delta := make(map[int]time.Duration, swarm)
	dirs := make(map[int]string, swarm)
	for i := 0; i < swarm; i++ {
		pid := 1000 + i
		rows = append(rows, proctable.Row{PID: pid, PPID: 1, PGID: pid})
		delta[pid] = 100 * time.Millisecond // 0.10 cores each, 1.00 together
		dirs[pid] = testRoot + "/pdead"
	}
	opts := baseOpts(t)
	opts.Floor = 0.20
	opts.CandidateFloor = 5.0 // absurd, and the point
	opts.Table = fakeTable(rows, delta)
	opts.Cwds = func([]int) map[int]string { return dirs }
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	if _, err := Scan(opts); !errors.Is(err, ErrCandidateFloorAboveFloor) {
		t.Fatalf("Scan error = %v, want %v; a candidate floor over the reporting floor must not "+
			"be silently repaired into a scan that cannot see a swarm", err, ErrCandidateFloorAboveFloor)
	}

	// And the boundary case, which a `>` comparison would have let through:
	// equal floors are the per-process rule too.
	opts.CandidateFloor = opts.Floor
	if _, err := Scan(opts); !errors.Is(err, ErrCandidateFloorAboveFloor) {
		t.Errorf("candidate floor EQUAL to the reporting floor: err = %v, want %v — equal is the "+
			"per-process rule, not one notch away from it", err, ErrCandidateFloorAboveFloor)
	}

	// The same population under the defaults is a finding, so the refusal above
	// is about the configuration and not about the population.
	opts.CandidateFloor = 0
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan with default candidate floor: %v", err)
	}
	if len(rep.Orphans) != swarm {
		t.Errorf("orphans = %d, want %d", len(rep.Orphans), swarm)
	}
}

// TestTheResidualBlindSpotIsWHERETheDocSaysItIs asserts the limit of this fix,
// in executable form, because a limit that lives only in a doc comment is a
// limit nobody re-measures.
//
// Reporting per owner removes the pathology where the detector got blinder as
// the leak got worse, but it does not make the detector unbounded: a population
// still escapes when its TOTAL clears the reporting floor while every member
// sits under the candidate floor. That needs more than Floor/CandidateFloor = 10
// members all under 0.02 cores.
//
// It is a far smaller hole than the one it replaces — spinning processes only
// get that small when there are more than ~500 of them on a 10-core box, where
// the old rule lost a 52-process swarm holding 87% of the host — and it is on
// the same side of the line as the stuck-process class the candidate floor
// exists to exclude. If this test ever starts failing because the candidate
// floor moved, that is the trade being re-made deliberately, which is the point.
func TestTheResidualBlindSpotIsWHERETheDocSaysItIs(t *testing.T) {
	const swarm = 100
	total := 1.0
	each := time.Duration(total / float64(swarm) * float64(time.Second)) // 0.01 cores
	if got := total / float64(swarm); got >= DefaultCandidateFloor {
		t.Fatalf("fixture is not below the candidate floor: %.3f >= %.3f", got, DefaultCandidateFloor)
	}
	rows := make([]proctable.Row, 0, swarm)
	delta := make(map[int]time.Duration, swarm)
	dirs := make(map[int]string, swarm)
	for i := 0; i < swarm; i++ {
		pid := 1000 + i
		rows = append(rows, proctable.Row{PID: pid, PPID: 1, PGID: pid})
		delta[pid] = each
		dirs[pid] = testRoot + "/pdead"
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, delta)
	opts.Cwds = func([]int) map[int]string { return dirs }
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.Busy != 0 || len(rep.Orphans) != 0 {
		t.Fatalf("busy = %d, orphans = %d; this fixture is the DOCUMENTED blind spot and the "+
			"package doc must be corrected if it is now visible", rep.Busy, len(rep.Orphans))
	}
	// Lowering the candidate floor recovers it, which is what makes this a
	// threshold rather than a structural limit. fakeTable is stateful — it
	// applies the deltas from its second call on — so the second scan needs its
	// own, or it differences two already-advanced samples and measures nothing.
	opts.Table = fakeTable(rows, delta)
	opts.CandidateFloor = 0.005
	rep, err = Scan(opts)
	if err != nil {
		t.Fatalf("Scan at a lower candidate floor: %v", err)
	}
	if len(rep.Orphans) != swarm {
		t.Errorf("orphans at candidate floor 0.005 = %d, want %d; the blind spot is the "+
			"CANDIDATE FLOOR and nothing else", len(rep.Orphans), swarm)
	}
}

// TestOwnersAggregatesExactlyTheReportedOrphans pins the two views of one
// finding against each other, so the summary a reader acts on cannot drift from
// the pid list they kill by.
func TestOwnersAggregatesExactlyTheReportedOrphans(t *testing.T) {
	rows := []proctable.Row{
		{PID: 100, PPID: 1, PGID: 100, CPU: time.Minute},
		{PID: 200, PPID: 1, PGID: 200, CPU: 2 * time.Minute},
		{PID: 300, PPID: 1, PGID: 300, CPU: 3 * time.Minute},
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{
		100: 300 * time.Millisecond, 200: 300 * time.Millisecond, 300: time.Second,
	})
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{
			100: testRoot + "/pdeadA", 200: testRoot + "/pdeadA", 300: testRoot + "/pdeadB",
		}
	}
	opts.LiveOwners = func() (map[string]bool, error) { return map[string]bool{}, nil }

	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byOwner := map[string]*OwnerLoad{}
	for _, o := range rep.Orphans {
		load := byOwner[o.Owner]
		if load == nil {
			load = &OwnerLoad{Owner: o.Owner}
			byOwner[o.Owner] = load
		}
		load.Procs++
		load.Cores += o.Cores
		load.CPU += o.CPU
	}
	if len(rep.Owners) != len(byOwner) {
		t.Fatalf("owners = %+v, want one row per owner in %+v", rep.Owners, rep.Orphans)
	}
	for _, got := range rep.Owners {
		want := byOwner[got.Owner]
		if want == nil {
			t.Errorf("owner %q reported but holds no reported orphan", got.Owner)
			continue
		}
		if got.Procs != want.Procs || got.CPU != want.CPU {
			t.Errorf("owner %q = %+v, want procs=%d cpu=%s", got.Owner, got, want.Procs, want.CPU)
		}
		if diff := got.Cores - want.Cores; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("owner %q cores = %.4f, want %.4f", got.Owner, got.Cores, want.Cores)
		}
	}
	// Costliest first, so the reader's eye lands on the owner worth acting on.
	if rep.Owners[0].Owner != "pdeadB" {
		t.Errorf("owners[0] = %q, want pdeadB (1.00 cores) ahead of pdeadA (0.60)", rep.Owners[0].Owner)
	}
}
