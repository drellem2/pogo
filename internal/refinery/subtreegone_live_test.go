package refinery

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSubtreeGoneIsProvenAgainstARealProcess is the positive control the
// SubtreeGone answer did not have.
//
// # Why a "not found" needs a control at all
//
// mg-48d8 records an operator proposing `ps aux | grep -i refinery` as a gate
// liveness check and shipping conclusions from it. It reported eleven matches
// at 0.0% CPU — every one of them the operator's own shell wrappers, including
// the grep's — and was read as "gate idle, hung" while the gate was healthily
// producing output at that same moment. A second attempt, `pgrep -P <pogod>`,
// returned NOTHING for a pogod with 24 direct children confirmed by `ps -axo
// pid,ppid`.
//
// Both failures have one shape: a check whose NEGATIVE answer was trusted
// without anyone establishing that it could produce a positive one. "Not found"
// from an instrument that has never found anything is not a measurement.
//
// So this test runs both arms against the SAME pid:
//
//	found → the process exists and the subtree walk reaches it
//	gone  → the same pid, after the process is killed AND reaped
//
// Same pid in both arms is the load-bearing detail. A negative arm that used a
// made-up pid would pass against a walk that could never find anything.
func TestSubtreeGoneIsProvenAgainstARealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("starts and kills a real process")
	}

	// A command line containing none of "gate", "refinery", or "pogo". If the
	// walk ever regresses to name-matching, this process is invisible to it and
	// the FOUND arm below fails — which is the regression that put a healthy
	// gate on the record as hung.
	cmd := exec.Command("sh", "-c", `sleep 120`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	killed := false
	defer func() {
		if !killed {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	}()

	// ARM ONE — FOUND. Establish the instrument works before its silence is
	// given any meaning.
	found, err := sampleSubtree(pid, time.Now())
	if err != nil {
		t.Fatalf("sampling a live subtree failed: %v", err)
	}
	if len(found.CPU) == 0 {
		t.Fatal("the subtree walk found NOTHING for a process that demonstrably exists — " +
			"until this passes, a 'gone' answer from it means nothing")
	}
	if _, ok := found.CPU[pid]; !ok {
		t.Fatalf("the subtree of pid %d does not contain pid %d itself", pid, pid)
	}

	live := &StepProgress{
		Step: "quality-gates", StartTime: time.Now().Add(-time.Minute),
		Heartbeat: time.Now(), HeartbeatInterval: (30 * time.Second).String(),
		GatePID: pid, CPUSampledAt: time.Now(), CPUProcs: len(found.CPU), CPUWindow: "30s",
	}
	if got := live.Subtree(); got == SubtreeGone {
		t.Fatalf("a running gate classified as GONE (procs=%d)", live.CPUProcs)
	}

	// ARM TWO — GONE. Kill the group and reap, so the pid is genuinely absent
	// from the table rather than lingering as a zombie.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = cmd.Wait()
	killed = true

	gone, err := sampleSubtree(pid, time.Now())
	if err != nil {
		t.Fatalf("sampling a dead subtree must not error: %v", err)
	}
	if len(gone.CPU) != 0 {
		// Not a flake to retry past: either the reap has not landed (in which
		// case Wait lied) or the pid was recycled, and both make the arm
		// meaningless.
		t.Fatalf("pid %d still has a subtree of %d processes after being killed and reaped", pid, len(gone.CPU))
	}

	dead := *live
	dead.CPUProcs = len(gone.CPU)
	dead.CPUSampledAt = time.Now()
	if got := dead.Subtree(); got != SubtreeGone {
		t.Fatalf("Subtree() = %v for a process that has been killed and reaped, want SubtreeGone", got)
	}
	if v := dead.Diagnosis(time.Now()); !strings.Contains(v, "GONE") {
		t.Errorf("the verdict for a vanished gate process must say so, got: %s", v)
	}

	// The two arms must not render the same, or the classification is a
	// constant wearing the costume of a measurement.
	if live.Subtree() == dead.Subtree() {
		t.Fatal("a live gate and a killed one classified identically")
	}
}

// TestSubtreeCPUComesFromTheDescendantsNotTheRoot encodes the third failure
// from mg-48d8's retraction: even having found the right process, sampling IT
// is the wrong question. The gate's own top-level process is a shell that
// blocks in wait(2) — measured at 0.0% while its child ran at 543.4%.
//
// So the assertion is comparative, not a threshold: the subtree total must
// exceed what the root alone accounts for. A threshold would pass on a busy
// root and prove nothing about the walk.
func TestSubtreeCPUComesFromTheDescendantsNotTheRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("spins a CPU")
	}

	// The root shell forks a spinner and waits: exactly the shape of a gate
	// whose `./test.sh` sits idle while `go test` underneath it works.
	cmd := exec.Command("sh", "-c", `(while :; do :; done) & wait`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	prev := settleSubtree(t, pid)
	time.Sleep(1500 * time.Millisecond)
	cur, err := sampleSubtree(pid, time.Now())
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	whole, ok := compareSubtree(prev, cur)
	if !ok {
		t.Fatal("compareSubtree refused a live pair")
	}
	// The same comparison restricted to the root process alone — what a check
	// that sampled the gate's own pid would have seen.
	rootOnly, ok := compareSubtree(
		&subtreeSample{At: prev.At, CPU: map[int]time.Duration{pid: prev.CPU[pid]}},
		&subtreeSample{At: cur.At, CPU: map[int]time.Duration{pid: cur.CPU[pid]}},
	)
	if !ok {
		t.Fatal("compareSubtree refused the root-only pair")
	}
	t.Logf("subtree=%.2f cores (%d procs)  root-only=%.2f cores", whole.Cores, whole.Procs, rootOnly.Cores)

	if !whole.Busy() {
		t.Errorf("a subtree with a spinning descendant measured as idle (%.2f cores)", whole.Cores)
	}
	if whole.Cores <= rootOnly.Cores {
		t.Errorf("the subtree total (%.2f cores) did not exceed the root alone (%.2f) — the work is "+
			"in the descendants, and a check that samples only the gate's own process cannot see it",
			whole.Cores, rootOnly.Cores)
	}
	if rootOnly.Busy() {
		t.Errorf("the root shell, which only waits, measured as busy at %.2f cores — the fixture is "+
			"not reproducing the observed shape (gate root 0.0%%, child 543.4%%)", rootOnly.Cores)
	}
}
