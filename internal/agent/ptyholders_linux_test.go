package agent

import (
	"bytes"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// syncBuffer is a log sink safe for the concurrent writers a Registry has.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestPTYSlaveHoldersNamesAnOpenSlave is the instrument's own positive control:
// a slave this process holds open must be found, and flagged as ours. Without
// it, the empty holder list the drain-timeout log prints would be
// indistinguishable from a scan that sees nothing at all.
func TestPTYSlaveHoldersNamesAnOpenSlave(t *testing.T) {
	m, s, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer m.Close()
	path := s.Name()

	holders, _ := ptySlaveHolders(path)
	if len(holders) != 1 || !strings.Contains(holders[0], "pid="+strconv.Itoa(os.Getpid())+" ") ||
		!strings.Contains(holders[0], "THIS pogod") {
		s.Close()
		t.Fatalf("ptySlaveHolders(%s) with the slave open here = %v, want exactly this process, flagged as a leak",
			path, holders)
	}

	s.Close()
	if holders, _ := ptySlaveHolders(path); len(holders) != 0 {
		t.Fatalf("ptySlaveHolders(%s) after closing the slave = %v, want none", path, holders)
	}
}

// TestStopIsBoundedAndNamesTheSlaveHolder is mg-c7a0d's linux reproduction of
// the real wedge behind mg-d7e17, with no hook: the agent backgrounds a
// descendant that ignores SIGHUP and inherits stdout/stderr, then exits. On
// linux the master Read gets EIO only when the slave's open file description is
// released, and the straggler still references it, so readOutput blocks after
// the reap. (Without `trap "" HUP` the session leader's exit kills the sleep
// and nothing wedges; on darwin the tty revoke unblocks the reader anyway.)
//
// Asserts both halves of what pogod promises in that state: Stop returns,
// bounded by outputDrainTimeout, and the drain-timeout log names the holder,
// so a CI recurrence diagnoses itself. This is also why the 10s bound cannot be
// removed: `nohup x &` from an agent's shell tool is exactly this shape.
func TestStopIsBoundedAndNamesTheSlaveHolder(t *testing.T) {
	prevDrain := outputDrainTimeout
	outputDrainTimeout = 500 * time.Millisecond
	t.Cleanup(func() { outputDrainTimeout = prevDrain })

	logs := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	a, err := reg.Spawn(SpawnRequest{
		Name:    "slave-holder",
		Type:    TypePolecat,
		Command: []string{"sh", "-c", `trap "" HUP; sleep 60 & exec cat`},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// The straggler is in the agent's session and process group (sh is the
	// session leader; a non-interactive sh puts `&` jobs in its own group).
	// Kill the group so the sleep does not outlive the test holding a pty.
	pgid := a.PID
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	// Let sh fork the sleep before cat is told to stop.
	time.Sleep(300 * time.Millisecond)

	stopped := make(chan error, 1)
	go func() { stopped <- reg.Stop("slave-holder", 2*time.Second) }()

	const bound = 30 * time.Second // the wedge without the bound lasts 60s, the sleep's life
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(bound):
		t.Fatalf("Stop did not return within %v while a descendant held the PTY slave (mg-d7e17)", bound)
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("Stop returned but Done() is still open")
	}

	out := logs.String()
	if !strings.Contains(out, "agent slave-holder: output reader still not finished") {
		t.Fatalf("no drain timeout was logged; the positive control did not wedge the reader. log:\n%s", out)
	}
	re := regexp.MustCompile(`processes still holding PTY slave (\S+): (\d+) \[(.*)\]`)
	mm := re.FindStringSubmatch(out)
	if mm == nil {
		t.Fatalf("drain timeout logged without the holder line (mg-c7a0d). log:\n%s", out)
	}
	if !strings.HasPrefix(mm[1], "/dev/pts/") {
		t.Errorf("holder line names slave %q, want a /dev/pts path", mm[1])
	}
	if !strings.Contains(mm[3], "comm=sleep") {
		t.Errorf("holder line does not name the sleep that holds the slave: %q", mm[0])
	}
	if strings.Contains(mm[3], "THIS pogod") {
		t.Errorf("holder line blames this process for a slave closeSlave already released: %q", mm[0])
	}
}
