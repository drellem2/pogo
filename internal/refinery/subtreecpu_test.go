package refinery

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// fakePS installs a canned process table for the duration of a test.
func fakePS(t *testing.T, rows []procRow) {
	t.Helper()
	prev := psSnapshot
	psSnapshot = func() ([]procRow, error) { return rows, nil }
	t.Cleanup(func() { psSnapshot = prev })
}

func TestSampleSubtreeIncludesOrphanedDescendantsViaProcessGroup(t *testing.T) {
	// 500 is the gate shell (its own group leader, as Setpgid arranges). 501
	// is its child. 502 is a grandchild whose parent already exited, so the
	// kernel reparented it to pid 1 — a ppid walk alone loses it, and losing
	// it is how a busy subtree gets reported as idle.
	fakePS(t, []procRow{
		{PID: 1, PPID: 0, PGID: 1, CPU: time.Hour},
		{PID: 500, PPID: 99, PGID: 500, CPU: time.Second},
		{PID: 501, PPID: 500, PGID: 500, CPU: 2 * time.Second},
		{PID: 502, PPID: 1, PGID: 500, CPU: 30 * time.Second},
		{PID: 600, PPID: 1, PGID: 600, CPU: 9 * time.Hour}, // unrelated
	})
	s, err := sampleSubtree(500, time.Now())
	if err != nil {
		t.Fatalf("sampleSubtree: %v", err)
	}
	for _, pid := range []int{500, 501, 502} {
		if _, ok := s.CPU[pid]; !ok {
			t.Errorf("pid %d missing from subtree", pid)
		}
	}
	if _, ok := s.CPU[600]; ok {
		t.Error("unrelated pid 600 was swept into the subtree")
	}
	if len(s.CPU) != 3 {
		t.Errorf("subtree has %d procs, want 3: %v", len(s.CPU), s.CPU)
	}
}

func TestSampleSubtreeRefusesToInheritForeignProcessGroup(t *testing.T) {
	// The gate is NOT its group leader — Setpgid did not take, so it sits in
	// pogod's group. Sweeping that group in would attribute the daemon and
	// every other agent's CPU to this one gate, which would make every gate
	// look busy forever. Only ppid descendants may count here.
	fakePS(t, []procRow{
		{PID: 99, PPID: 1, PGID: 99, CPU: 10 * time.Hour}, // pogod, group leader
		{PID: 500, PPID: 99, PGID: 99, CPU: time.Second},  // the gate, same group
		{PID: 501, PPID: 500, PGID: 99, CPU: 2 * time.Second},
		{PID: 700, PPID: 99, PGID: 99, CPU: 5 * time.Hour}, // sibling agent
	})
	s, err := sampleSubtree(500, time.Now())
	if err != nil {
		t.Fatalf("sampleSubtree: %v", err)
	}
	if _, ok := s.CPU[99]; ok {
		t.Error("pogod (pid 99) was swept into the gate subtree")
	}
	if _, ok := s.CPU[700]; ok {
		t.Error("sibling agent (pid 700) was swept into the gate subtree")
	}
	if len(s.CPU) != 2 {
		t.Errorf("subtree has %d procs, want 2 (500, 501): %v", len(s.CPU), s.CPU)
	}
}

func TestSampleSubtreeMissingRootIsEmptyNotError(t *testing.T) {
	fakePS(t, []procRow{{PID: 1, PPID: 0, PGID: 1, CPU: time.Hour}})
	s, err := sampleSubtree(500, time.Now())
	if err != nil {
		t.Fatalf("sampleSubtree: %v", err)
	}
	if len(s.CPU) != 0 {
		t.Errorf("expected an empty subtree for a vanished root, got %v", s.CPU)
	}
}

func TestCompareSubtreeDistinguishesBusyFromIdle(t *testing.T) {
	t0 := time.Now()
	t1 := t0.Add(30 * time.Second)

	busyPrev := &subtreeSample{At: t0, CPU: map[int]time.Duration{500: time.Second, 501: 60 * time.Second}}
	busyCur := &subtreeSample{At: t1, CPU: map[int]time.Duration{500: time.Second, 501: 177 * time.Second}}
	a, ok := compareSubtree(busyPrev, busyCur)
	if !ok {
		t.Fatal("compareSubtree refused a valid pair")
	}
	// 117 CPU-seconds in a 30s window ≈ 3.9 cores — the real observation from
	// the ticket, where a gate silent for 8m31s was in fact burning ~3.9 cores.
	if a.Cores < 3.8 || a.Cores > 4.0 {
		t.Errorf("Cores = %.2f, want ~3.9", a.Cores)
	}
	if !a.Busy() {
		t.Error("a subtree burning 3.9 cores must not read as idle")
	}

	idlePrev := &subtreeSample{At: t0, CPU: map[int]time.Duration{500: time.Second, 501: 60 * time.Second}}
	idleCur := &subtreeSample{At: t1, CPU: map[int]time.Duration{500: time.Second, 501: 60 * time.Second}}
	a, ok = compareSubtree(idlePrev, idleCur)
	if !ok {
		t.Fatal("compareSubtree refused a valid pair")
	}
	if a.Busy() {
		t.Errorf("a subtree that consumed nothing must read as idle, got Cores=%.4f", a.Cores)
	}
	if a.Procs != 2 {
		t.Errorf("Procs = %d, want 2", a.Procs)
	}
}

func TestCompareSubtreeCountsChurnAsWork(t *testing.T) {
	// A gate that forks short-lived workers can do heavy work while every
	// individual process is born and reaped inside one window, leaving the
	// CPU delta near zero. Churn is what keeps that from reading as idle.
	t0 := time.Now()
	t1 := t0.Add(30 * time.Second)
	prev := &subtreeSample{At: t0, CPU: map[int]time.Duration{500: time.Second, 501: 0}}
	cur := &subtreeSample{At: t1, CPU: map[int]time.Duration{500: time.Second, 502: 0}}
	a, ok := compareSubtree(prev, cur)
	if !ok {
		t.Fatal("compareSubtree refused a valid pair")
	}
	if a.Churn != 2 {
		t.Errorf("Churn = %d, want 2 (501 gone, 502 new)", a.Churn)
	}
	if !a.Busy() {
		t.Error("process churn must count as work")
	}
}

func TestCompareSubtreeRefusesASingleSample(t *testing.T) {
	// One sample cannot make a rate. Reporting one as "idle" would fabricate
	// the exact claim this file exists to avoid.
	cur := &subtreeSample{At: time.Now(), CPU: map[int]time.Duration{500: time.Second}}
	if _, ok := compareSubtree(nil, cur); ok {
		t.Error("compareSubtree accepted a single sample")
	}
	if _, ok := compareSubtree(cur, cur); ok {
		t.Error("compareSubtree accepted a zero-width window")
	}
}

func TestPSSnapshotReadsTheRealProcessTable(t *testing.T) {
	// A positive control on the real `ps` invocation: if the flags or the
	// TIME format are wrong on this platform, every CPU reading silently
	// degrades to "unavailable" and the whole signal is gone. This test is
	// what notices.
	rows, err := psSnapshot()
	if err != nil {
		t.Fatalf("psSnapshot: %v", err)
	}
	if len(rows) < 10 {
		t.Fatalf("ps returned %d rows; the process table is not that small", len(rows))
	}
	self := os.Getpid()
	found := false
	for _, r := range rows {
		if r.PID == self {
			found = true
		}
	}
	if !found {
		t.Errorf("ps output did not contain this test process (pid %d)", self)
	}
}

// TestSubtreeCPUOnALiveBusyProcess is the end-to-end positive control: a real
// process spinning a real CPU must be measured as busy, and a real process
// sleeping must be measured as idle. Both arms run, because a measurement that
// only ever reports one answer is the defect this replaces.
func TestSubtreeCPUOnALiveBusyProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("spins a CPU for a second")
	}
	run := func(script string) (busy bool, procs int) {
		t.Helper()
		cmd := exec.Command("sh", "-c", script)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}()

		t0 := time.Now()
		prev, err := sampleSubtree(cmd.Process.Pid, t0)
		if err != nil {
			t.Fatalf("sample: %v", err)
		}
		time.Sleep(1500 * time.Millisecond)
		t1 := time.Now()
		cur, err := sampleSubtree(cmd.Process.Pid, t1)
		if err != nil {
			t.Fatalf("sample: %v", err)
		}
		a, ok := compareSubtree(prev, cur)
		if !ok {
			t.Fatal("compareSubtree refused a live pair")
		}
		t.Logf("script=%q cores=%.2f procs=%d churn=%d", script, a.Cores, a.Procs, a.Churn)
		return a.Busy(), a.Procs
	}

	// A child that spins: `sh -c` forks for a compound command, so the work
	// happens in a descendant and only a subtree walk finds it.
	busy, procs := run(`(while :; do :; done) & wait`)
	if !busy {
		t.Error("a subtree spinning a CPU was measured as idle")
	}
	if procs < 2 {
		t.Errorf("expected at least the shell and its spinner, got %d procs", procs)
	}

	// Sleeping is the honest idle case. It must NOT read as busy — otherwise
	// the signal always says healthy and cannot report a stall.
	if busy, _ = run(`sleep 30`); busy {
		t.Error("a sleeping subtree was measured as busy")
	}
}

func TestSampleSubtreeSurfacesPSFailure(t *testing.T) {
	prev := psSnapshot
	psSnapshot = func() ([]procRow, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { psSnapshot = prev })
	if _, err := sampleSubtree(500, time.Now()); err == nil {
		t.Fatal("expected an error when the process table cannot be read")
	}
}
