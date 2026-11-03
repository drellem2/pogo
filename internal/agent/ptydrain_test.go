package agent

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// ttyRevocationStall is comfortably longer than the ~0.6s a macOS session
// leader spends stuck in `?Es` before its controlling tty is revoked. Tests
// below leave output undrained for this long specifically to get PAST that
// revocation, because before it the output is still there on any shape and the
// test would pass against the very bug it is guarding.
const ttyRevocationStall = 1500 * time.Millisecond

func drainAll(m *os.File) int {
	var n int
	buf := make([]byte, 4096)
	for {
		k, err := m.Read(buf)
		n += k
		if err != nil {
			return n
		}
	}
}

// TestStartPTYSurvivesOutputLeftUndrainedPastRevocation pins the mechanism
// behind mg-9aa1 at the level it lives: the PTY, not the agent.
//
// A child that writes and exits into a tty nobody is reading does not exit
// straight away — it sits in `?Es` and cmd.Wait() blocks. With no slave fd open
// anywhere else, that ends after roughly 0.6s in a tty revocation that DISCARDS
// the buffered output, so the reader that finally arrives gets a clean EOF and
// zero bytes. Measured on the old shape (pty.StartWithSize, which closes the
// parent's slave at spawn): output left undrained for 2s was lost in 5 of 5
// trials, and 0 of 5 with the parent holding a slave.
//
// That is the reported failure exactly — "process exited with <nil>, complete
// output: \"\"", in a test that took 0.61s. The 0.61s is the revocation stall.
//
// This test reproduces the losing condition on purpose: nothing drains the tty
// until well past the stall. Against the old shape it fails; against startPTY
// it passes.
func TestStartPTYSurvivesOutputLeftUndrainedPastRevocation(t *testing.T) {
	const msg = "written into a tty that nobody is draining"
	for i := 0; i < 3; i++ {
		cmd := exec.Command("echo", msg)
		m, s, err := startPTY(cmd, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			t.Fatalf("startPTY: %v", err)
		}

		time.Sleep(ttyRevocationStall) // nothing is reading the master

		got := make(chan int, 1)
		go func() { got <- drainAll(m) }()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("trial %d: child exited with %v, want a clean exit", i, err)
		}
		s.Close() // teardown, at a moment we control and after the drain
		n := <-got
		m.Close()
		if n == 0 {
			t.Fatalf("trial %d: output was discarded with the tty; the parent's slave fd "+
				"is not holding the revocation off", i)
		}
	}
}

// TestFastExitingChildOutputSurvivesLateReader is the same guarantee one level
// up, through Spawn: when Done() closes, a fast-exiting child's output is
// COMPLETE even though the reader goroutine was very slow to reach its first
// Read.
//
// The hook is the point of the test. Under fleet load the reader lost this race
// on its own, and no amount of manufactured load reproduces it on demand —
// which is what made the flake expensive to chase. Forcing the reader past the
// revocation stall reproduces it by construction instead. The delay has to
// exceed ttyRevocationStall: a merely late reader still finds the output on
// either shape, so a shorter delay would pass against the bug.
func TestFastExitingChildOutputSurvivesLateReader(t *testing.T) {
	const msg = "output written just before a very fast exit"

	prev := readOutputStartHook
	readOutputStartHook = func() { time.Sleep(ttyRevocationStall) }
	t.Cleanup(func() { readOutputStartHook = prev })

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	a, err := reg.Spawn(SpawnRequest{
		Name:    "late-reader",
		Type:    TypePolecat,
		Command: []string{"echo", msg},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	select {
	case <-a.Done():
	case <-time.After(argvDeliveryDeadlockBackstop):
		t.Fatalf("agent never exited within %v — deadlock backstop, not a timing measurement",
			argvDeliveryDeadlockBackstop)
	}

	// Done() closing is itself the guarantee that the reader has drained (see
	// waitAndHandle), so this is a completeness assertion, not a poll.
	if got := string(a.RecentOutput(4096)); !strings.Contains(got, msg) {
		t.Errorf("output of a fast-exiting child was lost to a late reader; "+
			"exit=%v, complete output: %q", a.ExitErr(), got)
	}
}

// TestStopReturnsWhenTheOutputReaderNeverFinishes is mg-d7e17's reproduction.
// cmd/pogod's TestRequestedFleetStopRaisesNoRestartFailedCondition hung for
// the full 20m package budget on CI (drellem2/pogo run 41695518754, linux). The
// dump had exactly one agent left. Its child was reaped and the parent's slave
// released, waitAndHandle was parked on `<-a.outputDone`, readOutput sat in a
// raw read(2) on the master that never returned, and StopAll was parked on
// `<-agent.done` behind both.
//
// Why that read never got its EIO is NOT established. Holding a second slave
// descriptor does not reproduce it on darwin, because the session leader's exit
// revokes the tty there anyway. So this test wedges the reader directly, with
// readOutputAttachedHook: it has attached and then never returns. That is the
// CI dump's state by construction, on any kernel.
//
// Before the fix, Stop never returned. After it, Stop is bounded by
// outputDrainTimeout: the agent is reported done with whatever output was
// drained, instead of holding a fleet shutdown forever.
func TestStopReturnsWhenTheOutputReaderNeverFinishes(t *testing.T) {
	prevDrain := outputDrainTimeout
	outputDrainTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	prevHook := readOutputAttachedHook
	readOutputAttachedHook = func() { <-release }
	// Release the wedged reader, so its goroutine does not outlive the test.
	// Cleanups run LIFO, so the restore is registered first and runs after the release.
	t.Cleanup(func() { readOutputAttachedHook = prevHook; outputDrainTimeout = prevDrain })
	t.Cleanup(func() { close(release) })

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	a, err := reg.Spawn(SpawnRequest{Name: "wedged-reader", Type: TypePolecat, Command: []string{"cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- reg.Stop("wedged-reader", 2*time.Second) }()

	// Bound: the SIGINT window (2s) plus the drain bound, with generous slack.
	// The wedge this guards lasts FOREVER, so the slack costs nothing in
	// discrimination.
	const bound = 30 * time.Second
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(bound):
		t.Fatalf("Stop did not return within %v with the output reader wedged: "+
			"waitAndHandle waits on a reader that can never finish (mg-d7e17)", bound)
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("Stop returned but Done() is still open")
	}
}
