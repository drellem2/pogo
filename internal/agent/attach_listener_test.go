package agent

import (
	"errors"
	"net"
	"os"
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

// waitForRebind blocks until the agent's listener identity changes from the one
// given, reporting whether it did within the timeout.
func waitForRebind(t *testing.T, a *Agent, oldL net.Listener, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l, _ := listenerID(a); l != oldL && l != nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
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
// whether it ever did within the timeout.
func waitForAttach(t *testing.T, a *Agent, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", a.SocketPath())
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
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

	if !waitForAttach(t, a, 2*time.Second) {
		t.Fatal("attach socket not usable before the fault was injected")
	}
	oldListener, oldDead := listenerID(a)

	killAcceptLoop(t, a)

	if !waitForRebind(t, a, oldListener, 3*time.Second) {
		t.Fatal("supervisor never rebound the listener after the accept loop died")
	}
	if _, dead := listenerID(a); dead == oldDead {
		t.Error("listener rebound but the accept loop was not restarted")
	}
	if !waitForAttach(t, a, 2*time.Second) {
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

	if !waitForAttach(t, a, 2*time.Second) {
		t.Fatal("attach socket not usable before the fault was injected")
	}
	oldListener, _ := listenerID(a)
	if err := os.Remove(a.SocketPath()); err != nil {
		t.Fatalf("remove socket: %v", err)
	}

	if !waitForRebind(t, a, oldListener, 3*time.Second) {
		t.Fatal("supervisor never rebound the listener after the socket file was removed")
	}
	if !waitForAttach(t, a, 2*time.Second) {
		t.Fatal("attach socket never recovered after the socket file was removed")
	}
}

// TestAttachRebindsAfterSocketFileReplaced covers a foreign bind taking over the
// path: the agent's listener is still alive but is no longer reachable by name.
func TestAttachRebindsAfterSocketFileReplaced(t *testing.T) {
	withSupervisorInterval(t, 10*time.Millisecond)
	a := spawnAgent(t, "rebind-replaced", "sleep", "30")

	if !waitForAttach(t, a, 2*time.Second) {
		t.Fatal("attach socket not usable before the fault was injected")
	}
	oldListener, _ := listenerID(a)

	// Replace the socket file with a foreign listener nobody accepts on.
	os.Remove(a.SocketPath())
	foreign, err := net.Listen("unix", a.SocketPath())
	if err != nil {
		t.Fatalf("foreign listen: %v", err)
	}
	disarmUnlinkOnClose(foreign)
	defer foreign.Close()

	// The supervisor must notice the path no longer names its own socket and
	// reclaim it. Listener identity is the assertion — a dial would succeed
	// against the foreign listener's backlog and prove nothing.
	if !waitForRebind(t, a, oldListener, 3*time.Second) {
		t.Fatal("supervisor never reclaimed the replaced socket path")
	}
	if !ownsSocketPath(a) {
		t.Error("agent does not own the socket file at its own path after reclaim")
	}
	if !waitForAttach(t, a, 2*time.Second) {
		t.Fatal("reclaimed socket is not attachable")
	}
}

// TestAttachNotRecreatedAfterCleanup: a retired agent's supervisor must not
// resurrect the socket. Cleanup is the authority on teardown.
func TestAttachNotRecreatedAfterCleanup(t *testing.T) {
	withSupervisorInterval(t, 10*time.Millisecond)
	a := spawnAgent(t, "no-zombie-socket", "sleep", "30")

	if !waitForAttach(t, a, 2*time.Second) {
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
