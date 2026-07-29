package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the standing demonstration of the defect confirmed-delivery
// exists to fix (mg-ebee), plus the acceptance tests for the fix itself.
//
// The two Test...WaitIdle... cases below assert TODAY'S behaviour of the
// wait-idle path: they pass on the unfixed tree and are expected to keep
// passing after the fix, because the wait-idle path is left intact (see
// NudgeConfirm's doc comment for what replaces its guarantee). They are the
// red control — a failing-mode witness, not a regression guard. mg-4ad1
// landed CI "demonstrated red" for the same reason: a control that has only
// ever been observed passing has not been observed working.
//
// The fakes stand in for a harness's input loop. Each one appends a line to
// its WITNESS file for every prompt it actually SUBMITS — which is precisely
// what a Claude Code UserPromptSubmit hook does, so the same file serves as
// the fake's witness in the red tests and as the real receipt file in the
// confirm tests.

// fakeHarness writes script to a temp file and returns its path.
func fakeHarness(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake harness: %v", err)
	}
	return path
}

// witnessFile returns a path in a temp dir for a fake harness to append its
// submissions to. The file is deliberately NOT created: absence means "no
// prompt has ever been submitted", which is what the receipt reader sees too.
func witnessFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "submits")
}

func readWitness(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read witness %s: %v", path, err)
	}
	return splitNonEmptyLines(string(data))
}

func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	return out
}

// deafHarness prints one line so the agent registers as idle, then never reads
// its stdin. Bytes written to the PTY master sit unread in the tty input queue
// — the exact shape of "the text is in the box and nothing submitted it".
const deafHarness = `#!/bin/sh
echo ready
sleep 30
`

// busyHarness never stops producing output (so it is never idle) while its
// input loop reads and submits every line. This is a working agent: the state
// the wait-idle precondition is the negation of.
const busyHarness = `#!/bin/sh
( while :; do echo working; sleep 0.05; done ) &
bg=$!
trap 'kill $bg 2>/dev/null' EXIT
while IFS= read -r line; do
	[ -n "$line" ] && printf '%s\n' "$line" >> "$WITNESS"
done
`

// unsentBoxHarness models the failure Orc measured against the real Claude
// binary: the typed text lands in the input box but the return that would
// submit it is swallowed. A later bare return submits whatever the box holds.
const unsentBoxHarness = `#!/bin/sh
echo ready
buf=""
while IFS= read -r line; do
	if [ -n "$line" ]; then
		buf="$line"
	elif [ -n "$buf" ]; then
		printf '%s\n' "$buf" >> "$WITNESS"
		buf=""
	fi
done
`

// TestWaitIdleNudgeReportsSuccessForUndeliveredMessage is the defect, stated as
// a test: the agent is idle, the write to the PTY master succeeds, and
// NudgeWithMode returns nil — "success" — for a message nothing ever received.
func TestWaitIdleNudgeReportsSuccessForUndeliveredMessage(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	witness := witnessFile(t)
	a, err := reg.Spawn(SpawnRequest{
		Name:    "deaf-waitidle",
		Type:    TypePolecat,
		Command: []string{"sh", fakeHarness(t, "deaf.sh", deafHarness)},
		Env:     []string{"WITNESS=" + witness},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Let the harness emit its one line and go quiet, so wait-idle's
	// precondition is satisfied.
	waitUntilIdle(t, a, 5*time.Second)

	if err := a.NudgeWithMode("do the thing", NudgeWaitIdle, 5*time.Second); err != nil {
		t.Fatalf("wait-idle nudge to an idle agent should report success today, got: %v", err)
	}

	// Give any submission every chance to show up.
	time.Sleep(500 * time.Millisecond)

	if got := readWitness(t, witness); len(got) != 0 {
		t.Fatalf("harness submitted %v — the fake is not deaf, so this control proves nothing", got)
	}
	t.Log("DEFECT CONFIRMED: NudgeWithMode(wait-idle) returned nil for a message " +
		"the agent never received (witness file empty)")
}

// TestWaitIdleNudgeRefusesToReachABusyAgent is the sharper form of the same
// defect, and the one that hit pm-pogo's 09:00 sweep: the precondition is
// `idle`, a working agent is by definition producing output, so the message is
// never even written. The failure is honest but the delivery is impossible.
func TestWaitIdleNudgeRefusesToReachABusyAgent(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	witness := witnessFile(t)
	a, err := reg.Spawn(SpawnRequest{
		Name:    "busy-waitidle",
		Type:    TypePolecat,
		Command: []string{"sh", fakeHarness(t, "busy.sh", busyHarness)},
		Env:     []string{"WITNESS=" + witness},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitUntilBusy(t, a, 5*time.Second)

	err = a.NudgeWithMode("run the sweep", NudgeWaitIdle, 2*time.Second)
	if err == nil {
		t.Fatal("wait-idle nudge to a busy agent unexpectedly succeeded")
	}
	if !containsAll(err.Error(), "wait for idle", "still producing output") {
		t.Fatalf("unexpected error shape: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	if got := readWitness(t, witness); len(got) != 0 {
		t.Fatalf("expected nothing delivered to a busy agent under wait-idle, got %v", got)
	}

	// The control has to rule out "the fake was deaf too" — otherwise an empty
	// witness proves nothing about reachability. Write the same message with no
	// precondition at all: it lands, so the input loop was listening the entire
	// time wait-idle spent refusing to use it.
	if err := a.NudgeWithMode("run the sweep", NudgeImmediate, 2*time.Second); err != nil {
		t.Fatalf("immediate nudge: %v", err)
	}
	if !waitForWitnessLines(t, witness, 1, 5*time.Second) {
		t.Fatal("busy harness never submitted the immediate nudge — its input loop " +
			"is not alive, so this control proves nothing")
	}
	t.Logf("DEFECT CONFIRMED: a busy agent whose input loop is provably listening "+
		"is unreachable under wait-idle; nothing was written to its PTY. err = %v", err)
}

// waitForWitnessLines polls until the witness holds at least n lines.
func waitForWitnessLines(t *testing.T, path string, n int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(readWitness(t, path)) >= n {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitUntilIdle blocks until the agent has produced output and then gone quiet
// for its idle threshold.
func waitUntilIdle(t *testing.T, a *Agent, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a.IsIdle(a.nudge.IdleThreshold) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("agent %q never went idle within %s", a.Name, timeout)
}

// waitUntilBusy blocks until the agent has been continuously producing output:
// it has written at least once, and the gap since its last write has stayed
// below the idle threshold across two consecutive observations.
func waitUntilBusy(t *testing.T, a *Agent, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		last := a.outputBuf.LastWriteTime()
		if !last.IsZero() && time.Since(last) < a.nudge.IdleThreshold {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("agent %q never produced continuous output within %s", a.Name, timeout)
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
