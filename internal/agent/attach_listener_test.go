package agent

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// listenerID returns the identity of the agent's current attach listener. A
// rebind installs a new listener and a new dead channel, so a change here is
// exactly what "the socket was rebound" means. Inode numbers are not usable for
// this: Linux happily reuses one across an unlink/rebind of the same path.
func listenerID(a *Agent) (net.Listener, chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listener, a.listenerDead
}

// attachWaitTimeout bounds every "wait until the supervisor has repaired this"
// poll below (mg-5aac). It is a BACKSTOP against a hung test, not a budget the
// repair is expected to use: on a quiet host each of these waits returns in
// single-digit milliseconds, so raising the bound costs nothing there and costs
// only patience on a host that is merely slow.
//
// It was 2-3s, and that was a load-sensitivity of the test rather than of the
// code. A wait sized to a quiet box turns "this machine is busy" into "this
// branch is broken", and the branch in the queue when the box is busy is
// whichever one happened to be next — so the cost lands on an author who
// touched none of this. Slow must be slow, not wrong.
const attachWaitTimeout = 30 * time.Second

// waitUntil polls cond until it holds, and reports whether it ever did. The
// deadline is attachWaitTimeout, shortened when the test binary's own deadline
// would arrive first — a bounded false is a legible failure, whereas running
// into `go test -timeout` is an unlabelled goroutine dump.
func waitUntil(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(attachWaitTimeout)
	if d, ok := t.Deadline(); ok {
		// Leave a margin so the assertion, not the timeout, reports the failure.
		if margin := d.Add(-5 * time.Second); margin.Before(deadline) {
			deadline = margin
		}
	}
	for {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForRebind blocks until the agent's listener identity changes from the one
// given, reporting whether it did.
func waitForRebind(t *testing.T, a *Agent, oldL net.Listener) bool {
	t.Helper()
	return waitUntil(t, func() bool {
		l, _ := listenerID(a)
		return l != oldL && l != nil
	})
}

// ownsSocketPath reports whether the file at socketPath is the one the agent's
// listener is currently bound to.
func ownsSocketPath(a *Agent) bool {
	cur, err := os.Stat(a.SocketPath())
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.socketInfo != nil && os.SameFile(cur, a.socketInfo)
}

// Regression coverage for mg-d216: `pogo agent attach mayor` failed with
// "connect: connection refused" against a socket file that existed, while the
// mayor process was alive and healthy. On a unix socket that pairing means the
// file is there but nothing is accepting: the accept loop had died under a live
// process and never came back.
//
// The old acceptLoop returned on *any* Accept error, including transient ones
// (EMFILE/ENFILE under fd exhaustion). The listener stayed bound, the backlog
// filled after ~128 queued connects, and every later attach was refused. Nothing
// ever rebound it.
//
// These tests pin both halves of the fix: the accept loop survives transient
// errors, and a supervisor rebinds a socket that dies or vanishes under a live
// agent — without ever resurrecting one for an agent that has been cleaned up.

// withSupervisorInterval retunes the supervisor tick for the duration of a test.
// Agents snapshot the value on the spawning goroutine (Agent.supervisorInterval),
// so this write never races a running supervisor.
func withSupervisorInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := attachSupervisorInterval
	attachSupervisorInterval = d
	t.Cleanup(func() { attachSupervisorInterval = prev })
}

// waitForAttach polls the agent's socket until a dial succeeds, and reports
// whether it ever did.
func waitForAttach(t *testing.T, a *Agent) bool {
	t.Helper()
	return waitUntil(t, func() bool {
		conn, err := net.Dial("unix", a.SocketPath())
		if err != nil {
			return false
		}
		conn.Close()
		return true
	})
}

// killAcceptLoop reproduces the observed fault: the accept loop stops, but the
// socket file is left behind. Disarming unlink-on-close is what makes the file
// linger exactly as it did on the wedged mayor.
func killAcceptLoop(t *testing.T, a *Agent) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listener == nil {
		t.Fatal("agent has no attach listener")
	}
	disarmUnlinkOnClose(a.listener)
	a.listener.Close()
}

// TestAttachRebindsAfterAcceptLoopDies is the direct regression for the reported
// bug: file present, nothing accepting, process alive → attach must recover.
func TestAttachRebindsAfterAcceptLoopDies(t *testing.T) {
	withSupervisorInterval(t, 10*time.Millisecond)
	a := spawnAgent(t, "rebind-dead-loop", "sleep", "30")

	if !waitForAttach(t, a) {
		t.Fatal("attach socket not usable before the fault was injected")
	}
	oldListener, oldDead := listenerID(a)

	killAcceptLoop(t, a)

	if !waitForRebind(t, a, oldListener) {
		t.Fatal("supervisor never rebound the listener after the accept loop died")
	}
	if _, dead := listenerID(a); dead == oldDead {
		t.Error("listener rebound but the accept loop was not restarted")
	}
	if !waitForAttach(t, a) {
		t.Fatal("attach socket never recovered after the accept loop died")
	}
	if !ownsSocketPath(a) {
		t.Error("agent does not own the socket file at its own path after rebind")
	}
}

// TestAttachRebindsAfterSocketFileRemoved covers the other live-process failure:
// the socket file is unlinked underneath a working listener (macOS reaps stale
// entries under $TMPDIR), which leaves the listener bound to an orphaned inode.
func TestAttachRebindsAfterSocketFileRemoved(t *testing.T) {
	withSupervisorInterval(t, 10*time.Millisecond)
	a := spawnAgent(t, "rebind-unlinked", "sleep", "30")

	if !waitForAttach(t, a) {
		t.Fatal("attach socket not usable before the fault was injected")
	}
	oldListener, _ := listenerID(a)
	if err := os.Remove(a.SocketPath()); err != nil {
		t.Fatalf("remove socket: %v", err)
	}

	if !waitForRebind(t, a, oldListener) {
		t.Fatal("supervisor never rebound the listener after the socket file was removed")
	}
	if !waitForAttach(t, a) {
		t.Fatal("attach socket never recovered after the socket file was removed")
	}
}

// TestAttachRebindsAfterSocketFileReplaced covers a foreign bind taking over the
// path: the agent's listener is still alive but is no longer reachable by name.
func TestAttachRebindsAfterSocketFileReplaced(t *testing.T) {
	withSupervisorInterval(t, 10*time.Millisecond)
	a := spawnAgent(t, "rebind-replaced", "sleep", "30")

	if !waitForAttach(t, a) {
		t.Fatal("attach socket not usable before the fault was injected")
	}
	oldListener, _ := listenerID(a)

	// Replace the socket file with a foreign listener nobody accepts on —
	// ATOMICALLY, via a bind at a sibling path and a rename over the target.
	//
	// This is the load sensitivity this test was filed for (mg-5aac), and it was
	// in the injection rather than in the code under test. The old form was
	// `os.Remove(path)` and then `net.Listen(path)`, which leaves a window in
	// which the path names NOTHING. The supervisor polls that same path every
	// 10ms here; landing in the window it reads `socket_file_missing`, rebinds,
	// and recreates the file — so the test's own foreign bind then loses with
	// EADDRINUSE and the test fails having injected no fault at all.
	//
	// The window is normally microseconds, so the race is invisible on a quiet
	// box; under load the two statements drift apart and the failure rate climbs
	// with it. Measured: widening the gap to 30ms fails 10 runs out of 10 with
	// `foreign listen: ... bind: address already in use`.
	//
	// Rename closes the window rather than narrowing it. The path names the
	// agent's own socket right up to the instant it names the foreign one, so
	// `socket_file_missing` is unreachable by construction and the only reason
	// the supervisor can see is the one under test, `socket_file_replaced`.
	// A SIBLING of the socket, not a suffix on it: rename is only atomic within
	// one filesystem, and a unix socket path is capped at ~104 bytes — which is
	// why these tests bind under shortSocketDir in the first place. `f.sock` is
	// shorter than any agent name, so this cannot be the thing that overflows it.
	foreignPath := filepath.Join(filepath.Dir(a.SocketPath()), "f.sock")
	foreign, err := net.Listen("unix", foreignPath)
	if err != nil {
		t.Fatalf("foreign listen: %v", err)
	}
	disarmUnlinkOnClose(foreign)
	defer foreign.Close()
	defer os.Remove(foreignPath)
	if err := os.Rename(foreignPath, a.SocketPath()); err != nil {
		t.Fatalf("replace socket file with the foreign one: %v", err)
	}

	// The supervisor must notice the path no longer names its own socket and
	// reclaim it. Listener identity is the assertion — a dial would succeed
	// against the foreign listener's backlog and prove nothing.
	if !waitForRebind(t, a, oldListener) {
		t.Fatal("supervisor never reclaimed the replaced socket path")
	}
	if !ownsSocketPath(a) {
		t.Error("agent does not own the socket file at its own path after reclaim")
	}
	if !waitForAttach(t, a) {
		t.Fatal("reclaimed socket is not attachable")
	}
}

// TestAttachNotRecreatedAfterCleanup: a retired agent's supervisor must not
// resurrect the socket. Cleanup is the authority on teardown.
func TestAttachNotRecreatedAfterCleanup(t *testing.T) {
	withSupervisorInterval(t, 10*time.Millisecond)
	a := spawnAgent(t, "no-zombie-socket", "sleep", "30")

	if !waitForAttach(t, a) {
		t.Fatal("attach socket not usable before cleanup")
	}
	a.Cleanup()

	// Give the supervisor several ticks to misbehave.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(a.SocketPath()); !os.IsNotExist(err) {
		t.Errorf("socket file exists after Cleanup: stat err = %v", err)
	}
}

// TestRebindListenerNoOpAfterCleanup pins the race directly: a supervisor that
// decided to rebind, but lost to Cleanup, must not bind — and must report that
// it didn't, so no bogus agent_attach_rebound event is emitted.
func TestRebindListenerNoOpAfterCleanup(t *testing.T) {
	withSupervisorInterval(t, time.Hour) // supervisor must not act on its own here
	a := spawnAgent(t, "rebind-after-cleanup", "sleep", "30")

	a.Cleanup()

	rebound, err := a.rebindListener()
	if err != nil {
		t.Fatalf("rebindListener after Cleanup: %v", err)
	}
	if rebound {
		t.Error("rebindListener rebound a socket for a cleaned-up agent")
	}
	if _, err := os.Stat(a.SocketPath()); !os.IsNotExist(err) {
		t.Errorf("socket file recreated after Cleanup: stat err = %v", err)
	}
}

// TestSupervisorThrottlesRebindFlap is the regression for review round 1's
// blocking finding: a listener that dies the instant it is rebound must not
// drive the supervisor as a hot loop. Unthrottled this measured ~10,900
// rebinds/sec, each writing an agent_attach_rebound event (~7.8 GB/hour into
// events.log) while pegging a core, for the life of the process.
//
// The flap is simulated by killing every new accept loop the moment it appears,
// which is what a recurring permanent Accept error (ENOMEM, EPERM) does.
func TestSupervisorThrottlesRebindFlap(t *testing.T) {
	withSupervisorInterval(t, time.Hour) // isolate: only the dead-channel path drives this
	a := spawnAgent(t, "rebind-flap", "sleep", "30")

	if !waitForAttach(t, a) {
		t.Fatal("attach socket not usable before the fault was injected")
	}

	const window = 600 * time.Millisecond
	seen := map[net.Listener]bool{}
	prev, _ := listenerID(a)

	// Kill each listener as soon as the supervisor installs it.
	killAcceptLoopLocked(a)
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if cur, _ := listenerID(a); cur != nil && cur != prev {
			seen[cur] = true
			prev = cur
			killAcceptLoopLocked(a)
		}
		time.Sleep(time.Millisecond)
	}

	// With backoff (50ms doubling to 30s) a 600ms window admits ~4 rebinds.
	// Without it, thousands. The bound is deliberately loose — this test guards
	// an order of magnitude, not an exact schedule.
	const maxRebinds = 15
	if len(seen) > maxRebinds {
		t.Errorf("supervisor rebound %d times in %s — rebind path is not throttled", len(seen), window)
	}
	if len(seen) == 0 {
		t.Error("supervisor never rebound the flapping listener at all")
	}
}

// killAcceptLoopLocked closes the current listener while leaving its socket file,
// without the *testing.T fatal path — safe to call from a polling loop.
func killAcceptLoopLocked(a *Agent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listener != nil {
		disarmUnlinkOnClose(a.listener)
		a.listener.Close()
	}
}

// TestIsRetryableAcceptErr pins the classification the accept loop rests on.
// Errno.Temporary() covers only EINTR/EMFILE/ENFILE/timeouts, so the errnos
// accept(2) raises under memory pressure must be named explicitly — otherwise
// they fall through to the (throttled, but pointless) rebind path.
func TestIsRetryableAcceptErr(t *testing.T) {
	opErr := func(e syscall.Errno) error {
		return &net.OpError{Op: "accept", Net: "unix", Err: os.NewSyscallError("accept", e)}
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"EMFILE (fd exhaustion — the mg-d216 trigger)", opErr(syscall.EMFILE), true},
		{"ENFILE (system-wide fd exhaustion)", opErr(syscall.ENFILE), true},
		{"EINTR", opErr(syscall.EINTR), true},
		{"ENOMEM (memory pressure)", opErr(syscall.ENOMEM), true},
		{"ENOBUFS (memory pressure)", opErr(syscall.ENOBUFS), true},
		{"ECONNABORTED (peer vanished)", opErr(syscall.ECONNABORTED), true},
		{"ECONNRESET (peer vanished)", opErr(syscall.ECONNRESET), true},
		{"EPERM (sandbox policy — permanent)", opErr(syscall.EPERM), false},
		{"EBADF (permanent)", opErr(syscall.EBADF), false},
		{"EINVAL (permanent)", opErr(syscall.EINVAL), false},
		{"ErrClosed (deliberate)", net.ErrClosed, false},
	}
	for _, tc := range cases {
		if got := isRetryableAcceptErr(tc.err); got != tc.want {
			t.Errorf("isRetryableAcceptErr(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRebindBackoffIsBounded keeps the repair loop's throttle honest.
func TestRebindBackoffIsBounded(t *testing.T) {
	if got := nextRebindBackoff(0); got != attachRebindMinBackoff {
		t.Errorf("first rebind backoff = %s, want %s", got, attachRebindMinBackoff)
	}
	if got := nextRebindBackoff(attachRebindMinBackoff); got != 2*attachRebindMinBackoff {
		t.Errorf("second rebind backoff = %s, want %s", got, 2*attachRebindMinBackoff)
	}
	if got := nextRebindBackoff(attachRebindMaxBackoff); got != attachRebindMaxBackoff {
		t.Errorf("saturated rebind backoff = %s, want %s", got, attachRebindMaxBackoff)
	}
	if got := nextRebindBackoff(attachRebindMaxBackoff / 2); got != attachRebindMaxBackoff {
		t.Errorf("rebind backoff overshoot = %s, want clamp to %s", got, attachRebindMaxBackoff)
	}
}

// stubListener feeds acceptLoop a scripted sequence of Accept errors.
type stubListener struct {
	mu     sync.Mutex
	errs   []error
	calls  int
	closed chan struct{}
}

func (s *stubListener) Accept() (net.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return nil, err
	}
	return nil, net.ErrClosed
}

func (s *stubListener) Close() error   { return nil }
func (s *stubListener) Addr() net.Addr { return &net.UnixAddr{Name: "stub", Net: "unix"} }

func (s *stubListener) acceptCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestAcceptLoopSurvivesTemporaryError pins the root cause: an EMFILE from
// Accept — the shape fd exhaustion produces — must be retried, not fatal.
// Before the fix the loop returned on the first error and the agent became
// permanently unattachable.
func TestAcceptLoopSurvivesTemporaryError(t *testing.T) {
	emfile := &net.OpError{
		Op:  "accept",
		Net: "unix",
		Err: os.NewSyscallError("accept", syscall.EMFILE),
	}
	// Sanity-check the assumption the retry predicate rests on.
	var tmp interface{ Temporary() bool }
	if !errors.As(error(emfile), &tmp) || !tmp.Temporary() {
		t.Fatal("EMFILE OpError is not reported as temporary; the retry predicate is wrong")
	}

	stub := &stubListener{errs: []error{emfile, emfile, emfile}}
	a := &Agent{Name: "temp-err"}
	dead := make(chan struct{})

	go a.acceptLoop(stub, dead)

	select {
	case <-dead:
		// The loop retried all three EMFILEs, then hit ErrClosed and exited.
		if got := stub.acceptCalls(); got != 4 {
			t.Errorf("Accept calls = %d, want 4 (3 retries + terminal ErrClosed)", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("acceptLoop never terminated")
	}
}

// TestAcceptLoopBackoffIsBounded keeps the retry from becoming a hot spin or an
// unbounded sleep.
func TestAcceptLoopBackoffIsBounded(t *testing.T) {
	if got := nextAcceptBackoff(0); got != attachAcceptMinBackoff {
		t.Errorf("first backoff = %s, want %s", got, attachAcceptMinBackoff)
	}
	if got := nextAcceptBackoff(attachAcceptMinBackoff); got != 2*attachAcceptMinBackoff {
		t.Errorf("second backoff = %s, want %s", got, 2*attachAcceptMinBackoff)
	}
	if got := nextAcceptBackoff(attachAcceptMaxBackoff); got != attachAcceptMaxBackoff {
		t.Errorf("saturated backoff = %s, want %s", got, attachAcceptMaxBackoff)
	}
	if got := nextAcceptBackoff(attachAcceptMaxBackoff / 2); got != attachAcceptMaxBackoff {
		t.Errorf("backoff overshoot = %s, want clamp to %s", got, attachAcceptMaxBackoff)
	}
}
