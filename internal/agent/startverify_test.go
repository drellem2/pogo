package agent

import (
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// newRenudgeTestAgent builds a minimal Agent whose PTY master is the write end
// of an os.Pipe, so a test can read back exactly the bytes verifyStartAndRenudge
// delivers (a bare CR per renudge) with no terminal-echo ambiguity. The returned
// read closure drains and returns everything written so far; call it after
// verifyStartAndRenudge returns.
func newRenudgeTestAgent(t *testing.T, workItemID string) (*Agent, func() string, chan struct{}) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done := make(chan struct{})
	a := &Agent{
		Name:       "renudge-test",
		Type:       TypePolecat,
		WorkItemID: workItemID,
		master:     pw,
		outputBuf:  NewRingBuffer(1024),
		done:       done,
		nudge:      DefaultNudgeProfile,
	}
	readAll := func() string {
		_ = pw.Close()
		b, _ := io.ReadAll(pr)
		_ = pr.Close()
		return string(b)
	}
	return a, readAll, done
}

// fastRenudgeRegistry returns a Registry wired with the given verifier and a
// sub-second start-verify window so tests don't wait the real 25s.
func fastRenudgeRegistry(v StartVerifier, maxAttempts int) *Registry {
	return &Registry{
		startVerifier:          v,
		startVerifyDelay:       10 * time.Millisecond,
		startVerifyMaxAttempts: maxAttempts,
	}
}

// countingVerifier returns a StartVerifier that reports `started` per the
// returned schedule and records how many times it was called. results[i] is the
// (started, err) for the i-th call; the last entry repeats for any further calls.
type verifyCall struct {
	started bool
	err     error
}

func countingVerifier(results []verifyCall) (StartVerifier, func() int) {
	var mu sync.Mutex
	calls := 0
	fn := func(workItemID string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		i := calls
		calls++
		if i >= len(results) {
			i = len(results) - 1
		}
		return results[i].started, results[i].err
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	return fn, count
}

// TestVerifyStartAndRenudge_UnstartedRenudgesUpToMax is the core failure case:
// a polecat whose work item stays unclaimed (the concurrent-spawn init-stall)
// draws one bare CR per attempt, bounded by max attempts — mg-feb3.
func TestVerifyStartAndRenudge_UnstartedRenudgesUpToMax(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	verifier, count := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "\r\r\r" {
		t.Errorf("expected 3 bare CRs delivered, got %q", got)
	}
	if c := count(); c != 3 {
		t.Errorf("expected verifier consulted once per attempt (3), got %d", c)
	}
}

// TestVerifyStartAndRenudge_StartedNoRenudge: a healthy start (item already
// claimed by the first check) must never touch the PTY.
func TestVerifyStartAndRenudge_StartedNoRenudge(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	verifier, count := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("expected no renudge for a started agent, got %q", got)
	}
	if c := count(); c != 1 {
		t.Errorf("expected verifier consulted exactly once, got %d", c)
	}
}

// TestVerifyStartAndRenudge_RecoversAfterOneRenudge is the intended recovery:
// unstarted on the first check, then the single bare CR flushes the buffered
// kickoff and the item is claimed, so the watcher stops after one renudge.
func TestVerifyStartAndRenudge_RecoversAfterOneRenudge(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	verifier, count := countingVerifier([]verifyCall{{started: false}, {started: true}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "\r" {
		t.Errorf("expected exactly one bare CR before recovery, got %q", got)
	}
	if c := count(); c != 2 {
		t.Errorf("expected verifier consulted twice (unstarted, then started), got %d", c)
	}
}

// TestVerifyStartAndRenudge_QueryErrorSkips: an unreadable mg state is
// inconclusive — the watcher must NOT renudge blind (a stray CR into a working
// agent is worse) and must stop rather than retry.
func TestVerifyStartAndRenudge_QueryErrorSkips(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	verifier, count := countingVerifier([]verifyCall{{err: errors.New("mg unreadable")}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("expected no renudge on query error, got %q", got)
	}
	if c := count(); c != 1 {
		t.Errorf("expected verifier consulted once then bail, got %d", c)
	}
}

// TestVerifyStartAndRenudge_NoWorkItemIsNoop: an agent with no work item id has
// no hard signal to gate on, so the watcher is a no-op and never consults the
// verifier.
func TestVerifyStartAndRenudge_NoWorkItemIsNoop(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "")
	verifier, count := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("expected no renudge for an agent with no work item, got %q", got)
	}
	if c := count(); c != 0 {
		t.Errorf("expected verifier never consulted, got %d", c)
	}
}

// TestVerifyStartAndRenudge_NilVerifierIsNoop: a bare registry (no verifier
// wired) leaves the initial nudge to stand on its own.
func TestVerifyStartAndRenudge_NilVerifierIsNoop(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	reg := fastRenudgeRegistry(nil, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("expected no renudge with nil verifier, got %q", got)
	}
}

// TestVerifyStartAndRenudge_AgentExitAbortsWait: an agent that exits during the
// wait must abandon the watch promptly (nothing to renudge), not sleep out the
// full window.
func TestVerifyStartAndRenudge_AgentExitAbortsWait(t *testing.T) {
	a, readAll, done := newRenudgeTestAgent(t, "mg-test")
	verifier, count := countingVerifier([]verifyCall{{started: false}})
	reg := &Registry{
		startVerifier:          verifier,
		startVerifyDelay:       10 * time.Second, // long, so only the exit can unblock it
		startVerifyMaxAttempts: 1,
	}

	close(done)
	start := time.Now()
	reg.verifyStartAndRenudge(a)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("expected prompt return on agent exit, took %v", elapsed)
	}
	if got := readAll(); got != "" {
		t.Errorf("expected no renudge after agent exit, got %q", got)
	}
	if c := count(); c != 0 {
		t.Errorf("expected verifier never consulted after exit, got %d", c)
	}
}

// TestSpawnDrivesStartVerify is the end-to-end wiring check: a real Spawn must,
// after delivering the initial nudge, run the start-verify/auto-renudge watcher.
// The agent prints the prompt-ready sentinel then idles (via cat), so the
// wait-ready initial nudge fires quickly; a verifier that reports the work item
// perpetually unclaimed must then be consulted once per bounded attempt. Proves
// the Spawn goroutine calls verifyStartAndRenudge, which the unit tests stub out.
func TestSpawnDrivesStartVerify(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	verifier, count := countingVerifier([]verifyCall{{started: false}})
	reg.SetStartVerifier(verifier)
	// White-box: shrink the real 25s window so the bounded loop finishes fast.
	reg.startVerifyDelay = 100 * time.Millisecond
	reg.startVerifyMaxAttempts = 2

	// Emit the default prompt-ready sentinel, then hand off to cat so the PTY
	// goes quiet — the wait-ready gate opens and the initial nudge is delivered.
	if _, err := reg.Spawn(SpawnRequest{
		Name:         "spawn-verify",
		Type:         TypePolecat,
		Command:      []string{"sh", "-c", "printf '? for shortcuts'; exec cat"},
		InitialNudge: "kickoff",
		WorkItemID:   "mg-spawn",
	}); err != nil {
		t.Fatal(err)
	}

	// Initial nudge waits IdleThreshold (2s) after the sentinel, then the
	// verify loop runs 2 attempts × 100ms. Poll generously.
	deadline := time.Now().Add(15 * time.Second)
	for count() < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if c := count(); c != 2 {
		t.Errorf("expected Spawn to drive %d start-verify checks, got %d", 2, c)
	}
}

// TestStartVerifyDefaults confirms the exported defaults are the ~25s / bounded
// values the ticket specifies, and that the zero-value overrides fall back to
// them.
func TestStartVerifyDefaults(t *testing.T) {
	reg := &Registry{}
	if d := reg.startVerifyDelayOrDefault(); d != DefaultStartVerifyDelay {
		t.Errorf("zero override should yield DefaultStartVerifyDelay, got %v", d)
	}
	if m := reg.startVerifyMaxAttemptsOrDefault(); m != DefaultStartVerifyMaxAttempts {
		t.Errorf("zero override should yield DefaultStartVerifyMaxAttempts, got %v", m)
	}
	if DefaultStartVerifyDelay < 20*time.Second || DefaultStartVerifyDelay > 30*time.Second {
		t.Errorf("DefaultStartVerifyDelay %v outside the ticket's ~20-30s window", DefaultStartVerifyDelay)
	}
	if DefaultStartVerifyMaxAttempts <= 0 {
		t.Errorf("DefaultStartVerifyMaxAttempts must be bounded and positive, got %d", DefaultStartVerifyMaxAttempts)
	}
}
