package orphanwatch

import (
	"errors"
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
	}
	opts := baseOpts(t)
	opts.Table = fakeTable(rows, map[int]time.Duration{
		100: time.Second, 200: time.Second, 300: time.Second, 400: time.Second,
	})
	opts.Cwds = func([]int) map[int]string {
		return map[int]string{
			100: testRoot + "/pdead/code",
			200: testRoot + "/plive/code",
			300: "/Users/daniel/dev/pogo",
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
	if len(rep.Dispositions) != 4 {
		t.Errorf("dispositions = %v, want exactly the 4 busy pids", rep.Dispositions)
	}

	// The histogram identity: every counter equals the number of pids in its
	// bucket, and the buckets exhaust the busy population.
	counts := map[Disposition]int{}
	for _, d := range rep.Dispositions {
		counts[d]++
	}
	for d, n := range map[Disposition]int{
		DispositionOrphan:         len(rep.Orphans),
		DispositionLiveOwner:      rep.LiveOwner,
		DispositionUnattributable: rep.Unattributable,
		DispositionCwdUnreadable:  rep.CwdUnreadable,
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
