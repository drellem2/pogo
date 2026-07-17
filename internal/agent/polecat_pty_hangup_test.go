package agent

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This file pins the premise that makes pogod's mail-check reap safe for
// polecats (mg-61a0).
//
// pogod's GC sweep reasons like this (cmd/pogod/main.go, registryLiveness.
// AgentState): an agent that is NOT in the registry and is NOT auto_start is
// GONE, so reap its mail-check. Crew survive that inference because they are
// auto_start (EXPECTED). Polecats are not — deliberately (mg-8677). So the
// sweep is betting that an unregistered polecat is a dead polecat.
//
// That bet is only safe because a polecat cannot outlive pogod, and NOTHING in
// AgentState checks it:
//
//   - The registry is in-memory with no adoption/reattach path, so a restarted
//     pogod's registry is empty PERMANENTLY for anything that outlived it.
//   - pogod has no signal handler at all (no signal.Notify), so SIGTERM — the
//     routine stop — kills it at the default disposition, and its only other
//     exit is log.Fatal(Serve(...)). Both skip deferred functions, as do
//     SIGKILL, panic and host crash. pogod runs NO cleanup on any path out and
//     never stops its agents. It used to carry a `defer StopAll` that read like
//     it did; mg-6b66 removed it as unreachable rather than leave the code
//     lying about how this fleet dies.
//
// The only thing that kills them is the PTY hangup: pogod owns the master, its
// death force-closes it, the terminal is revoked, and the agent — a session
// leader with that PTY as its controlling terminal, courtesy of
// pty.StartWithSize's Setsid (gh #22, see TestSpawnProcessGroupIsolation) —
// gets SIGHUP and dies at the default disposition.
//
// If that coupling breaks, the failure is silent and severe: the polecat is
// alive, unregistered, and swept — its mail-check is deleted from memory AND
// disk, and it goes dark mid-task with no error anywhere. That was reproduced
// end-to-end against a real pogod for mg-61a0 using a SIGHUP-ignoring polecat.
//
// These tests exist because the protection is ACCIDENTAL. It is a side effect
// of who owns a file descriptor, it is load-bearing, and it is a property of
// the HARNESS BINARY rather than of pogo: it holds only while the harness
// leaves SIGHUP at its default disposition. pogo is multi-provider (claude,
// codex, cursor, pi). A harness that traps SIGHUP to shut down gracefully — an
// entirely reasonable thing for a TUI to do — re-opens the dark-polecat path
// instantly and silently. TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP
// demonstrates exactly that.
//
// NOTE ON FIDELITY: these tests kill a REAL parent process rather than calling
// master.Close() in-process. That is not ceremony. An in-process close does NOT
// reproduce the hangup: while readOutput is blocked in read(2) on the master,
// the kernel still holds a reference to the file description, so the terminal
// is never revoked and no SIGHUP is sent — a `sleep` polecat survives it
// indefinitely. Only the parent's actual death force-closes the fd and hangs up
// the terminal. A test built on master.Close() would pass for the wrong reason
// and pin nothing.

// fakePogodEnv, when set, turns the helper test below into a stand-in pogod: it
// spawns one polecat through a real Registry, reports the pid, and then blocks
// forever waiting to be killed.
const (
	fakePogodEnv    = "POGO_TEST_FAKE_POGOD"
	fakePogodCmdEnv = "POGO_TEST_FAKE_POGOD_CMD"
	hupIgnorerEnv   = "POGO_TEST_HUP_IGNORING_POLECAT"
	readyFileEnv    = "POGO_TEST_POLECAT_READY_FILE"
)

// TestHupIgnoringPolecatHelper is not a test. It is a stand-in HARNESS that
// ignores SIGHUP — a claude/codex/cursor/pi that traps the signal to shut down
// gracefully — re-executed via the test binary so the suite carries no external
// dependency. It must be a SINGLE process that is itself the session leader:
// a `sh -c 'trap "" HUP; sleep 600'` wrapper looks like it ignores SIGHUP but
// does not model one, because the hangup kills its unprotected `sleep` child
// and the shell then exits when that child reaps — which measures the child's
// signal disposition, not the harness's.
func TestHupIgnoringPolecatHelper(t *testing.T) {
	if os.Getenv(hupIgnorerEnv) == "" {
		t.Skip("helper process for TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP; not a standalone test")
	}
	signal.Ignore(syscall.SIGHUP)
	// Announce readiness only AFTER the ignore is installed. The parent MUST
	// wait for this before killing pogod: a Go binary takes tens of
	// milliseconds to reach this line, and a hangup that lands first kills the
	// process at SIGHUP's default disposition — making the test pass for the
	// exact reason it is meant to disprove.
	if err := os.WriteFile(os.Getenv(readyFileEnv), []byte("ready"), 0o644); err != nil {
		panic(err)
	}
	select {} // outlive pogod
}

// TestFakePogodHelper is not a test. It is the child half of the tests below,
// re-executed via the test binary (the standard Go helper-process pattern). It
// plays pogod: own a Registry, own a polecat's PTY master, then die however the
// parent chooses to kill it.
func TestFakePogodHelper(t *testing.T) {
	if os.Getenv(fakePogodEnv) == "" {
		t.Skip("helper process for TestPolecatDoesNotOutlivePogod; not a standalone test")
	}
	dir, err := os.MkdirTemp("", "pg")
	if err != nil {
		fmt.Println("HELPER-ERR", err)
		os.Exit(1)
	}
	reg, err := NewRegistry(dir)
	if err != nil {
		fmt.Println("HELPER-ERR", err)
		os.Exit(1)
	}
	a, err := reg.Spawn(SpawnRequest{
		Name:    "outliver",
		Type:    TypePolecat,
		Command: strings.Split(os.Getenv(fakePogodCmdEnv), "\x1f"),
	})
	if err != nil {
		fmt.Println("HELPER-ERR", err)
		os.Exit(1)
	}
	fmt.Printf("POLECAT-PID=%d\n", a.PID)
	os.Stdout.Sync()
	select {} // block until the parent kills us — pogod dying with no teardown
}

// startFakePogod re-execs the test binary as a stand-in pogod, waits for it to
// report the polecat's pid, and returns both. The caller kills the fake pogod.
func startFakePogod(t *testing.T, polecatCmd ...string) (pogod *exec.Cmd, polecatPID int) {
	t.Helper()
	pogod = exec.Command(os.Args[0], "-test.run=TestFakePogodHelper", "-test.timeout=120s")
	pogod.Env = append(os.Environ(),
		fakePogodEnv+"=1",
		fakePogodCmdEnv+"="+strings.Join(polecatCmd, "\x1f"),
	)
	stdout, err := pogod.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := pogod.Start(); err != nil {
		t.Fatalf("starting fake pogod: %v", err)
	}
	t.Cleanup(func() {
		pogod.Process.Kill()
		pogod.Wait()
	})

	type res struct {
		pid int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if s, ok := strings.CutPrefix(line, "POLECAT-PID="); ok {
				pid, err := strconv.Atoi(s)
				ch <- res{pid, err}
				return
			}
			if strings.HasPrefix(line, "HELPER-ERR") {
				ch <- res{0, fmt.Errorf("fake pogod failed: %s", line)}
				return
			}
		}
		ch <- res{0, fmt.Errorf("fake pogod exited without reporting a polecat pid")}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("fake pogod: %v", r.err)
		}
		polecatPID = r.pid
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for the fake pogod to spawn its polecat")
	}

	t.Cleanup(func() { syscall.Kill(polecatPID, syscall.SIGKILL) })
	if !pidAlive(polecatPID) {
		t.Fatalf("precondition: polecat pid %d is not alive right after spawn", polecatPID)
	}
	return pogod, polecatPID
}

// TestPolecatDoesNotOutlivePogod is the control for the mail-check reap's
// ABSENT->GONE inference for polecats (mg-61a0). pogod is SIGKILLed — the
// harshest, most faithful "pogod died with no teardown" — and its polecat must
// not survive it.
func TestPolecatDoesNotOutlivePogod(t *testing.T) {
	pogod, pid := startFakePogod(t, "sleep", "600")

	// SIGKILL: no defers, no StopAll, no signal handler — exactly what a
	// crashed or SIGTERMed pogod gives its agents today.
	if err := pogod.Process.Kill(); err != nil {
		t.Fatalf("killing fake pogod: %v", err)
	}
	pogod.Wait()

	if !waitPidGone(pid, 10*time.Second) {
		t.Fatalf("REGRESSION (mg-61a0): polecat pid %d SURVIVED pogod's death.\n"+
			"A polecat that outlives pogod is unregistered forever after a pogod restart "+
			"(in-memory registry, no adoption path), so the GC sweep sees it as absent, "+
			"AgentState calls it GONE (not auto_start), and reaps its mail-check from memory "+
			"and disk — taking a LIVE polecat dark mid-task, silently. The reap's safety rests "+
			"entirely on this coupling; if it is broken, fix the coupling or give AgentState "+
			"positive liveness evidence for unregistered polecats. Do NOT loosen the reap "+
			"(mg-de08, mg-8677).", pid)
	}
}

// TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP is the negative control for
// the test above, and the reason that test is not merely decorative.
//
// It demonstrates that the ONLY thing standing between us and a dark polecat is
// the harness's SIGHUP disposition — nothing in pogo enforces it. Here the
// "harness" ignores SIGHUP and calmly outlives pogod: precisely the state from
// which a restarted pogod reaps a live agent's mail-check.
//
// It asserts survival on purpose: this is the documented shape of the hazard,
// not desired behavior. If this test ever starts FAILING (the polecat dies even
// while ignoring SIGHUP), something else began enforcing the coupling — find out
// what, and whether it is any more deliberate than the PTY hangup was, before
// trusting the reap's inference.
func TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	// Both vars are inherited by the fake pogod, and through it by the polecat.
	t.Setenv(hupIgnorerEnv, "1")
	t.Setenv(readyFileEnv, ready)
	pogod, pid := startFakePogod(t, os.Args[0], "-test.run=TestHupIgnoringPolecatHelper", "-test.timeout=120s")
	waitReadyFile(t, ready)

	if err := pogod.Process.Kill(); err != nil {
		t.Fatalf("killing fake pogod: %v", err)
	}
	pogod.Wait()

	if waitPidGone(pid, 5*time.Second) {
		t.Fatalf("polecat pid %d died despite ignoring SIGHUP — the PTY-hangup coupling that "+
			"TestPolecatDoesNotOutlivePogod pins is no longer the mechanism at work. "+
			"Re-derive what kills a polecat when pogod dies before trusting the mail-check "+
			"reap's ABSENT->GONE inference (mg-61a0).", pid)
	}
}

// waitReadyFile blocks until the polecat reports that its signal disposition is
// installed. Without this gate the hangup can beat the polecat's own startup and
// the test passes for the wrong reason (see TestHupIgnoringPolecatHelper).
func waitReadyFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the polecat to install its SIGHUP ignore (%s)", path)
}

// waitPidGone polls until pid is no longer alive, or timeout elapses. Polling
// rather than Wait: the process under test is not this process's child.
func waitPidGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !pidAlive(pid)
}
