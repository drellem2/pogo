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

	"github.com/drellem2/pogo/internal/testtmp"
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
// WHAT THESE TESTS ARE SENSITIVE TO, AND WHY THAT NEEDED SAYING OUT LOUD
// (mg-1a23). The coupling above is a statement about a signal DISPOSITION, so
// these tests inherit one too — from whoever ran `go test`. SIG_IGN survives
// fork AND execve (a caught handler does not; the kernel resets it at exec),
// and Go's runtime deliberately declines to install its own handler over an
// inherited SIG_IGN for SIGHUP and SIGINT. A shell that ignores SIGHUP
// therefore hands that disposition to this test binary, to the fake pogod it
// re-execs, and finally to `sleep 600` — silently. requirePolecatCanReceiveSIGHUP
// refuses to run either test in that state, because in it neither one is
// measuring pogo. Read its comment before touching anything here.
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

	// hupIgnoringLauncherEnv turns the launcher helper below into a stand-in
	// for a shell that ignores SIGHUP; hupIgnoringLauncherTargetEnv names the
	// test it should re-run underneath that disposition.
	hupIgnoringLauncherEnv       = "POGO_TEST_HUP_IGNORING_LAUNCHER"
	hupIgnoringLauncherTargetEnv = "POGO_TEST_HUP_IGNORING_LAUNCHER_TARGET"
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
	// testtmp, not os.MkdirTemp: this helper is DESIGNED to be killed — the
	// whole point of the test below is a pogod that dies with no teardown — so
	// t.Cleanup and defer are both unreachable here by construction. testtmp's
	// sweep removes it once this pid is gone (mg-de3c).
	dir, err := testtmp.Dir("fakepogod")
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
//
// The polecat comes back as a polecatTarget rather than an int: a pid alone
// cannot be waited on honestly (see that type's comment).
func startFakePogod(t *testing.T, polecatCmd ...string) (pogod *exec.Cmd, polecat polecatTarget) {
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

	var polecatPID int
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
	// Read the identity NOW, while the process we mean is provably the one
	// holding the number. Read later, it is the recycler's start time and the
	// comparison it feeds is worthless.
	polecat = polecatTarget{pid: polecatPID}
	polecat.start, polecat.startKnown = procStart(polecatPID)
	if !polecat.startKnown {
		// Not fatal: the identity check degrades to the bare-pid reading the
		// guard has always used, and says so if it ever fires. Failing here
		// instead would turn an unreadable `ps` into a product verdict, which
		// is the exact confusion mg-1a23 removed.
		t.Logf("could not read a start time for polecat pid %d; the wait below "+
			"falls back to kill(pid,0) alone and cannot detect pid recycling", polecatPID)
	}
	return pogod, polecat
}

// TestPolecatDoesNotOutlivePogod is the control for the mail-check reap's
// ABSENT->GONE inference for polecats (mg-61a0). pogod is SIGKILLed — the
// harshest, most faithful "pogod died with no teardown" — and its polecat must
// not survive it.
func TestPolecatDoesNotOutlivePogod(t *testing.T) {
	requirePolecatCanReceiveSIGHUP(t)
	pogod, polecat := startFakePogod(t, "sleep", "600")
	pid := polecat.pid

	// SIGKILL: no defers, no StopAll, no signal handler — exactly what a
	// crashed or SIGTERMed pogod gives its agents today.
	if err := pogod.Process.Kill(); err != nil {
		t.Fatalf("killing fake pogod: %v", err)
	}
	pogod.Wait()

	if !waitPolecatGone(t, polecat, 10*time.Second) {
		t.Fatalf("REGRESSION (mg-61a0): polecat pid %d SURVIVED pogod's death.\n"+
			"Observed state of that pid, so the next reader does not have to re-run this to\n"+
			"find out WHAT survived: %s\n"+
			"THIS VERDICT IS ALREADY NARROWED, so do not spend the evening mg-1a23 spent.\n"+
			"Three ways this message could have been a lie are ruled out BEFORE it prints:\n"+
			"  - an inherited SIG_IGN for SIGHUP — the test refuses to run at all in that\n"+
			"    state rather than reporting either verdict (mg-1a23);\n"+
			"  - a RECYCLED pid — the wait re-reads `ps -o lstart=` and a start time that is\n"+
			"    not the one captured at spawn means our polecat is gone (mg-13a3, mg-bec4);\n"+
			"  - a ZOMBIE — `Z` is a corpse awaiting reap, and pidAlive is kill(pid,0), which\n"+
			"    succeeds on one. The wait now reads the state and calls it gone (mg-bec4).\n"+
			"So a LIVE process, that is still ours, really did outlive its pogod.\n"+
			"A polecat that outlives pogod is unregistered forever after a pogod restart "+
			"(in-memory registry, no adoption path), so the GC sweep sees it as absent, "+
			"AgentState calls it GONE (not auto_start), and reaps its mail-check from memory "+
			"and disk — taking a LIVE polecat dark mid-task, silently. The reap's safety rests "+
			"entirely on this coupling; if it is broken, fix the coupling or give AgentState "+
			"positive liveness evidence for unregistered polecats. Do NOT loosen the reap "+
			"(mg-de08, mg-8677).", pid, describePid(pid))
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
	requirePolecatCanReceiveSIGHUP(t)
	ready := filepath.Join(t.TempDir(), "ready")
	// Both vars are inherited by the fake pogod, and through it by the polecat.
	t.Setenv(hupIgnorerEnv, "1")
	t.Setenv(readyFileEnv, ready)
	pogod, polecat := startFakePogod(t, os.Args[0], "-test.run=TestHupIgnoringPolecatHelper", "-test.timeout=120s")
	pid := polecat.pid
	waitReadyFile(t, ready)

	if err := pogod.Process.Kill(); err != nil {
		t.Fatalf("killing fake pogod: %v", err)
	}
	pogod.Wait()

	if waitPolecatGone(t, polecat, 5*time.Second) {
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

// polecatTarget is a polecat's pid TOGETHER WITH the kernel's start time for
// it, because a bare pid cannot answer the question these tests ask (mg-bec4).
//
// Both hangup tests below decide a product guarantee from `kill(pid, 0)`, and
// that call answers a narrower question than the one being asked. It succeeds
// for a process that is running, for a corpse awaiting reap, and for a wholly
// unrelated process that was handed the same number after ours exited. Only
// the first of those three is "the polecat outlived pogod"; the other two are
// "the polecat died" wearing the same reading. The guard used to report
// REGRESSION for all three.
//
// That is the SAME defect mg-1a23 named — a message asserting a cause it never
// checked — one step further in. mg-1a23 fixed the inherited-SIG_IGN case by
// refusing to judge, and made the other two legible by printing `ps` on the
// failure path. Legible is not ruled out: it still takes a human re-reading the
// output to notice, and the message they are re-reading says REGRESSION.
//
// pogo already owns the discrimination. internal/agent/witness.go pairs a pid
// with `ps -o lstart=` for exactly this reason and calls the mismatch case out
// by name ("the pid was recycled by an unrelated process; our polecat is GONE",
// mg-13a3). procStart is that helper, in this package, so this is one more
// caller rather than a second instrument to keep in step.
//
// startKnown=false means `ps` would not answer at spawn. The wait then degrades
// to the bare-pid reading and says so; it does not fabricate an identity.
type polecatTarget struct {
	pid        int
	start      time.Time
	startKnown bool
}

// goneDespiteLivePid reports whether the polecat is dead even though its pid
// still answers signal 0, and why. It is deliberately NOT called from the
// polling loop: each answer costs two `ps` forks, and a 10s wait polled at 20ms
// would spend 1,000 process spawns per call — churn that this very test is
// sensitive to, in a suite whose whole-tree run is the reported failure mode.
// Cheap poll, expensive adjudication, once, only where the verdict is decided.
func (tgt polecatTarget) goneDespiteLivePid() (bool, string) {
	if tgt.startKnown {
		start, ok := procStart(tgt.pid)
		if !ok {
			return true, fmt.Sprintf("pid %d no longer has a ps row, so it is gone as of now "+
				"(it died between the last poll and this check)", tgt.pid)
		}
		if !start.Equal(tgt.start) {
			return true, fmt.Sprintf("pid %d was RECYCLED — it now belongs to a process started %s, "+
				"not to our polecat, which started %s. Ours is gone (mg-13a3)",
				tgt.pid, start, tgt.start)
		}
	}
	if procIsZombie(tgt.pid) {
		return true, fmt.Sprintf("pid %d is a ZOMBIE (ps state Z): a corpse awaiting reap by the "+
			"process that inherited it. It is dead — kill(pid,0) succeeds on a zombie, which is "+
			"the whole reason this check exists. Whether and when launchd reaps it is not "+
			"something pogo controls or promises", tgt.pid)
	}
	return false, ""
}

// waitPolecatGone is waitPidGone with the verdict earned rather than assumed.
//
// The fast path is unchanged and costs what it always did. Only when the
// bounded wait expires with the pid still answering — the one moment a caller
// is about to announce a product regression — does it pay for the readings that
// separate "still ours and running" from "ours died and something else is
// wearing the number" and from "ours died and has not been reaped yet".
//
// Returning true from that late path is a PASS for TestPolecatDoesNotOutlivePogod
// and a FAIL for TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP, and both are
// correct: in each case the polecat is dead, which is what each test is asking
// about. The t.Logf is not decoration — it is the record that the verdict came
// from the late path, so a run that took the full timeout to reach a green is
// distinguishable afterwards from one that never waited at all.
func waitPolecatGone(t *testing.T, tgt polecatTarget, timeout time.Duration) bool {
	t.Helper()
	if waitPidGone(tgt.pid, timeout) {
		return true
	}
	if gone, why := tgt.goneDespiteLivePid(); gone {
		t.Logf("the %s wait on polecat pid %d expired with the pid still answering "+
			"kill(pid,0), but %s. Treating it as gone (mg-bec4).", timeout, tgt.pid, why)
		return true
	}
	return false
}

// procIsZombie reports whether pid is a corpse awaiting reap.
//
// Best-effort by construction: a `ps` that will not answer returns false, which
// leaves the caller's verdict exactly where it was rather than flipping it on a
// failed reading. The states are the BSD ones `ps -o stat=` prints, where the
// first character is the state and the rest are flags, so this matches on the
// prefix and not on equality.
func procIsZombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// sighupIgnoredDiagnosis is the phrase requirePolecatCanReceiveSIGHUP prints and
// TestGuardRefusesToJudgeAPolecatWhenSIGHUPIsIgnored matches on. It lives in one
// place deliberately: a message this file asserts against must not be able to
// drift away from the assertion.
const sighupIgnoredDiagnosis = "SIGHUP is IGNORED in this test process"

// requirePolecatCanReceiveSIGHUP refuses to run either hangup test when SIGHUP
// is ignored in THIS process, because in that state neither test is measuring
// pogo.
//
// SIG_IGN is inherited across fork AND execve — unlike a caught handler, which
// the kernel resets to SIG_DFL at exec — and Go's runtime deliberately declines
// to install its own handler over an inherited SIG_IGN for SIGHUP and SIGINT
// (runtime.sigInstallGoHandler special-cases exactly those two, so that a
// program launched under `nohup` stays under it). So if whoever ran `go test`
// had SIGHUP ignored, that disposition reaches this test binary, the fake pogod
// it re-execs, and finally `sleep 600` itself — unchanged and unannounced.
//
// At that point `sleep 600` IS the SIGHUP-ignoring harness that
// TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP models on purpose, and both
// tests below stop meaning what they say:
//
//   - TestPolecatDoesNotOutlivePogod runs its bounded wait to the full 10s and
//     reports REGRESSION (mg-61a0) — naming a product-guarantee failure for a
//     condition that is entirely a property of the invoking shell. Measured for
//     mg-1a23: 4 of 4 runs under `bash -c "trap ” HUP; exec ./agent.test
//     -test.run=TestPolecatDoesNotOutlivePogod"` failed at 10.03s with that
//     exact message, against 25 of 25 passes for the same binary at SIGHUP's
//     default disposition. Ignoring SIGINT instead leaves it passing, so the
//     sensitivity is to SIGHUP specifically and not to signals in general.
//
//   - TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP passes VACUOUSLY. It
//     asserts survival, and survival is now guaranteed by the environment
//     rather than by the helper's own signal.Ignore — it would pass even if
//     that call were deleted. A negative control that cannot fail is not a
//     control.
//
// Refusing loudly is the only honest option left. Reporting green would be a
// lie about a guarantee nobody measured, and reporting REGRESSION is the lie
// that cost mg-1a23 an evening across four agents: the failure was read as test
// weather for hours precisely because the message asserts a cause it never
// checked. A gate that goes red here is red for a reason its own output names,
// and the remedy is one line in whatever launched it.
func requirePolecatCanReceiveSIGHUP(t *testing.T) {
	t.Helper()
	if !signal.Ignored(syscall.SIGHUP) {
		return
	}
	t.Fatalf("%s, so this test cannot measure the PTY-hangup coupling it exists to pin "+
		"(mg-1a23).\n"+
		"SIG_IGN survives fork AND execve, and Go preserves an inherited SIG_IGN for SIGHUP, "+
		"so the disposition reaches the fake pogod and then the polecat itself: the spawned "+
		"process would ignore the hangup no matter what pogo does. That makes a REGRESSION "+
		"verdict here unearnable and a PASS unearned, which is why this refuses instead of "+
		"reporting either.\n"+
		"This is a property of the process that ran `go test`, NOT of the tree, the branch, "+
		"the network or host load. Something in the launch chain ignored SIGHUP — `nohup`, a "+
		"`trap '' HUP` in a wrapper script, or a parent that was itself launched that way. "+
		"Re-run without it and this test measures pogo again.", sighupIgnoredDiagnosis)
}

// describePid reports what the OS thinks pid is, for the failure path only.
//
// The guard's message used to say a pid "SURVIVED", but its only evidence is
// pidAlive — kill(pid, 0) — which cannot tell a running process from a zombie
// awaiting reap, nor from an unrelated process that recycled the number. Those
// three need different responses and the reader had no way to pick. Best-effort
// and diagnostic: a failed `ps` degrades the message, it does not change the
// verdict.
func describePid(pid int) string {
	out, err := exec.Command("ps", "-o", "pid=,ppid=,stat=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return fmt.Sprintf("(ps -p %d reported nothing: %v — the pid is gone as of now, "+
			"which means it died after the last poll)", pid, err)
	}
	if line := strings.TrimSpace(string(out)); line != "" {
		return line
	}
	return fmt.Sprintf("(ps -p %d printed no row — the pid is gone as of now)", pid)
}

// TestSigHupIgnoringLauncherHelper is not a test. It is a stand-in for a shell
// that ignores SIGHUP — `nohup`, or a wrapper carrying `trap ” HUP` — used to
// drive the precondition above from the outside rather than by faking its input.
// It ignores SIGHUP, re-execs the test binary on the target test, and prints
// that run's output between markers for the parent to read.
func TestSigHupIgnoringLauncherHelper(t *testing.T) {
	if os.Getenv(hupIgnoringLauncherEnv) == "" {
		t.Skip("helper process for TestGuardRefusesToJudgeAPolecatWhenSIGHUPIsIgnored; not a standalone test")
	}
	signal.Ignore(syscall.SIGHUP)
	target := os.Getenv(hupIgnoringLauncherTargetEnv)
	cmd := exec.Command(os.Args[0], "-test.run="+target, "-test.timeout=120s", "-test.v")
	// Clear the launcher's own trigger so the re-exec runs the target rather
	// than recursing into this helper if the target expression ever matches it.
	cmd.Env = append(os.Environ(), hupIgnoringLauncherEnv+"=")
	out, _ := cmd.CombinedOutput() // a failing target is the expected case
	fmt.Printf("LAUNCHED-BEGIN\n%s\nLAUNCHED-END\n", out)
}

// TestGuardRefusesToJudgeAPolecatWhenSIGHUPIsIgnored pins the precondition end
// to end: it runs TestPolecatDoesNotOutlivePogod underneath a launcher that has
// ignored SIGHUP, and asserts the run says so instead of announcing a product
// regression.
//
// It is written against the guard's OUTPUT rather than by calling
// requirePolecatCanReceiveSIGHUP directly, because the thing being pinned is
// inheritance across fork and exec — the part nobody checked for an evening.
// A unit test of the predicate would pass just as happily if the disposition
// stopped propagating.
func TestGuardRefusesToJudgeAPolecatWhenSIGHUPIsIgnored(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSigHupIgnoringLauncherHelper", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		hupIgnoringLauncherEnv+"=1",
		hupIgnoringLauncherTargetEnv+"=^TestPolecatDoesNotOutlivePogod$",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher helper failed: %v\n%s", err, out)
	}
	launched := string(out)
	if _, after, ok := strings.Cut(launched, "LAUNCHED-BEGIN\n"); ok {
		if before, ok := strings.CutSuffix(strings.TrimSpace(after), "LAUNCHED-END"); ok {
			launched = before
		}
	}

	if strings.Contains(launched, "REGRESSION (mg-61a0)") {
		t.Fatalf("the guard announced a product regression when the only thing wrong was the "+
			"launcher's SIGHUP disposition. That verdict is unearnable in this state (mg-1a23) "+
			"and reading it as one is what turned this test into weather.\nRun output:\n%s", launched)
	}
	if !strings.Contains(launched, sighupIgnoredDiagnosis) {
		t.Fatalf("expected the guard to refuse with %q under an ignored SIGHUP; it did not. "+
			"Either the precondition is gone, or SIG_IGN stopped reaching the test binary "+
			"through fork+exec — find out which before trusting this guard again.\nRun output:\n%s",
			sighupIgnoredDiagnosis, launched)
	}
}

// TestDescribePidSeparatesLiveFromGone pins the diagnostic the guard's failure
// message now leans on. It is a small helper, but it exists to be believed on a
// day when the guard has already fired and nobody can re-run the conditions —
// so a reading that is silently empty, or identical for a live and a dead pid,
// would put the reader back exactly where mg-1a23 found them.
func TestDescribePidSeparatesLiveFromGone(t *testing.T) {
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatalf("starting a live pid to describe: %v", err)
	}
	t.Cleanup(func() { live.Process.Kill(); live.Wait() })

	got := describePid(live.Process.Pid)
	if !strings.Contains(got, strconv.Itoa(live.Process.Pid)) || !strings.Contains(got, "sleep") {
		t.Fatalf("describePid on a live pid should report its row; got %q", got)
	}

	live.Process.Kill()
	live.Wait() // reaped, so the pid is gone rather than a zombie
	if gone := describePid(live.Process.Pid); gone == got {
		t.Fatalf("describePid gave the same answer for a live and a reaped pid (%q) — "+
			"the reading cannot distinguish the two cases the guard's message asks it to", gone)
	}
}

// TestPolecatWaitCallsAZombieGone pins the reading that the old guard could not
// make, using a REAL zombie rather than a stubbed one.
//
// Measured on this box while writing it (macOS 15.6, 2026-08-20): for a child
// that has been killed and NOT reaped, kill(pid,0) succeeds, `ps -o stat=`
// prints `Z`, `ps -o lstart=` still prints the ORIGINAL start time, and
// `command=` prints `<defunct>`. So the start-time comparison cannot catch this
// case — it matches — and the state read is the only thing that can. That is
// why goneDespiteLivePid does both and not either.
//
// It matters here because the polecat is orphaned by design: pogod is SIGKILLed
// and the polecat reparents, so whether its corpse is reaped inside the guard's
// 10s bound is launchd's business and not pogo's. A guarantee about pogo must
// not be decided by another process's scheduling.
func TestPolecatWaitCallsAZombieGone(t *testing.T) {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatalf("starting a process to zombify: %v", err)
	}
	pid := c.Process.Pid
	start, ok := procStart(pid)
	if !ok {
		t.Fatalf("could not read a start time for pid %d", pid)
	}
	// Kill without Wait: os/exec does not reap behind our back, so the corpse
	// stays a zombie for as long as this test wants one. Cleanup reaps it.
	if err := c.Process.Kill(); err != nil {
		t.Fatalf("killing pid %d: %v", pid, err)
	}
	t.Cleanup(func() { c.Wait() })

	// THE ORACLE IS READ INDEPENDENTLY OF procIsZombie, and that is the whole
	// design of this test rather than a detail. Staging the corpse with the
	// same call that is under test makes the test VACUOUS the moment that call
	// breaks — it t.Skips, and a skip is not a failure. Measured while writing
	// this: with procIsZombie stubbed to return false, an earlier version of
	// this test PASSED, as a skip, in 10.4s. describePid reads `command=`,
	// which prints `<defunct>` for a corpse, so the two readings can disagree.
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(describePid(pid), "<defunct>") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(describePid(pid), "<defunct>") {
		t.Skipf("pid %d never showed as `<defunct>` (%s); this platform reaps differently "+
			"and the case this test exists for cannot be staged here", pid, describePid(pid))
	}
	if !pidAlive(pid) {
		t.Fatalf("precondition: kill(%d,0) already fails, so the ambiguity this test is "+
			"about is not present and the assertion below would pass for the wrong reason", pid)
	}
	if !procIsZombie(pid) {
		t.Fatalf("procIsZombie says pid %d is not a zombie, but ps reports it as %q. The "+
			"state read is the ONLY thing that can catch this case — `ps -o lstart=` still "+
			"prints the original start time for a corpse, so the recycle check matches and "+
			"passes it through", pid, describePid(pid))
	}

	tgt := polecatTarget{pid: pid, start: start, startKnown: true}
	if gone, why := tgt.goneDespiteLivePid(); !gone {
		t.Fatalf("a zombie was not called gone (%q). kill(pid,0) succeeds on one, so the guard "+
			"would report REGRESSION (mg-61a0) for a polecat that is a corpse", why)
	} else if !strings.Contains(why, "ZOMBIE") {
		t.Fatalf("the zombie was called gone for the wrong stated reason: %q", why)
	}

	// End to end through the wait, with a bound short enough that the whole
	// answer must come from the late path.
	if !waitPolecatGone(t, tgt, 100*time.Millisecond) {
		t.Fatalf("waitPolecatGone kept a zombie pid %d alive past its own adjudication", pid)
	}
}

// TestPolecatWaitCallsARecycledPidGone pins the start-time half of the same
// adjudication.
//
// THE MISMATCH IS CONSTRUCTED, and that is worth stating rather than hiding: a
// real recycle needs the machine's pid space to wrap onto this exact number
// while we watch, which is not something a test can stage. What IS measured is
// the thing the guard depends on — that a live pid whose start time is not the
// one captured at spawn is reported GONE and named as a recycle, so the verdict
// tracks the PROCESS and not the number. internal/agent/witness.go decides the
// same question the same way (mg-13a3).
func TestPolecatWaitCallsARecycledPidGone(t *testing.T) {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatalf("starting a live process: %v", err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	pid := c.Process.Pid
	start, ok := procStart(pid)
	if !ok {
		t.Fatalf("could not read a start time for pid %d", pid)
	}

	// The number is live; the process behind it is not the one we spawned.
	stale := polecatTarget{pid: pid, start: start.Add(-time.Minute), startKnown: true}
	gone, why := stale.goneDespiteLivePid()
	if !gone {
		t.Fatalf("a pid whose start time does not match the one captured at spawn was NOT "+
			"called gone (%q) — the guard is deciding on the number rather than on the process", why)
	}
	if !strings.Contains(why, "RECYCLED") {
		t.Fatalf("the mismatch was called gone for the wrong stated reason: %q", why)
	}
}

// TestPolecatWaitDoesNotCallALivePolecatGone is the negative control, and it is
// the one that keeps the two tests above from being free.
//
// Every adjudication added here buys a way for the guard to report GONE, and a
// guard that reports GONE too eagerly is worse than the one it replaced: mg-61a0
// exists because a live, unregistered polecat gets swept, and a hangup test that
// cannot fail would hide exactly that. So a process that is running, is still
// ours, and is not a zombie must come back not-gone through BOTH entry points.
func TestPolecatWaitDoesNotCallALivePolecatGone(t *testing.T) {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatalf("starting a live process: %v", err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	pid := c.Process.Pid
	start, ok := procStart(pid)
	if !ok {
		t.Fatalf("could not read a start time for pid %d", pid)
	}

	tgt := polecatTarget{pid: pid, start: start, startKnown: true}
	if gone, why := tgt.goneDespiteLivePid(); gone {
		t.Fatalf("a live, unrecycled, non-zombie process was called gone: %q", why)
	}
	if waitPolecatGone(t, tgt, 100*time.Millisecond) {
		t.Fatalf("waitPolecatGone called live pid %d gone — a guard that cannot fail cannot "+
			"protect the mail-check reap's ABSENT->GONE inference (mg-61a0)", pid)
	}

	// The degraded reading must be conservative in the same direction: with no
	// start time to compare, a live process is still not gone.
	blind := polecatTarget{pid: pid}
	if gone, why := blind.goneDespiteLivePid(); gone {
		t.Fatalf("with startKnown=false a live process was called gone: %q — the fallback "+
			"must lose precision, not invent a verdict", why)
	}
}
