package agent

import (
	"errors"
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

// busyDeafHarness produces output forever and reads its stdin, recording every
// line it is given — including empty ones, so a bare return is visible — but
// never submits anything. It is a mid-turn agent whose harness has queued the
// prompt and not yet processed it.
const busyDeafHarness = `#!/bin/sh
( while :; do echo working; sleep 0.05; done ) &
bg=$!
trap 'kill $bg 2>/dev/null' EXIT
while IFS= read -r line; do
	printf '[%s]\n' "$line" >> "$RAW"
done
`

// dropFirstHarness swallows the first message outright — not the submit, the
// whole thing — and ignores bare returns. Only a resend gets through, so it is
// the only fake in this file that requires escalation step 3.
const dropFirstHarness = `#!/bin/sh
echo ready
n=0
while IFS= read -r line; do
	[ -z "$line" ] && continue
	n=$((n+1))
	[ "$n" -ge 2 ] && printf '%s\n' "$line" >> "$WITNESS"
done
`

// spawnWithReceipt spawns an agent whose fake harness writes its submissions to
// the file the confirm path reads as this agent's receipt. In production the
// two halves are a harness and its UserPromptSubmit hook; here one script plays
// both, which is the same contract: only the harness can make the count move.
func spawnWithReceipt(t *testing.T, reg *Registry, name, script string, env ...string) (*Agent, string) {
	t.Helper()
	receipt := witnessFile(t)
	a, err := reg.Spawn(SpawnRequest{
		Name:    name,
		Type:    TypePolecat,
		Command: []string{"sh", fakeHarness(t, name+".sh", script)},
		Env:     append([]string{"WITNESS=" + receipt}, env...),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a.receiptFile = receipt
	return a, receipt
}

// TestNudgeConfirmDeliversToABusyAgent is the acceptance bar: the agent is
// mid-output — the state wait-idle refuses to deliver into — and the message
// lands, proved by the agent's own receipt rather than by pogod's optimism.
func TestNudgeConfirmDeliversToABusyAgent(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, receipt := spawnWithReceipt(t, reg, "busy-confirm", busyHarness)
	waitUntilBusy(t, a, 5*time.Second)

	if err := a.NudgeWithMode("run the sweep", NudgeConfirm, 6*time.Second); err != nil {
		t.Fatalf("confirm nudge to a busy agent: %v", err)
	}

	got := readWitness(t, receipt)
	if len(got) != 1 || got[0] != "run the sweep" {
		t.Fatalf("expected exactly one submission of the message, got %v", got)
	}
	t.Log("a busy agent — unreachable under wait-idle — received the message, confirmed")
}

// TestNudgeConfirmRefusesWhenNothingReceivedIt is the other half of the defect:
// where wait-idle returned nil, confirm escalates and then says no.
func TestNudgeConfirmRefusesWhenNothingReceivedIt(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, receipt := spawnWithReceipt(t, reg, "deaf-confirm", deafHarness)
	waitUntilIdle(t, a, 5*time.Second)

	err = a.NudgeWithMode("do the thing", NudgeConfirm, 5*time.Second)
	if !errors.Is(err, ErrNudgeUnconfirmed) {
		t.Fatalf("want ErrNudgeUnconfirmed, got %v", err)
	}
	if !containsAll(err.Error(), "bare return", "resend", "did not receive it") {
		t.Fatalf("error should name what was tried: %v", err)
	}
	if got := readWitness(t, receipt); len(got) != 0 {
		t.Fatalf("deaf harness recorded submissions %v", got)
	}
}

// TestNudgeConfirmBareReturnSubmitsWithoutDuplicating covers the measured
// failure — text loaded in the composer, submit lost — and the reason the bare
// return goes FIRST: it lands the message exactly once.
func TestNudgeConfirmBareReturnSubmitsWithoutDuplicating(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, receipt := spawnWithReceipt(t, reg, "unsent-box", unsentBoxHarness)
	waitUntilIdle(t, a, 5*time.Second)

	if err := a.NudgeWithMode("claim your work item", NudgeConfirm, 9*time.Second); err != nil {
		t.Fatalf("confirm nudge: %v", err)
	}

	// Settle, so a duplicate arriving late still fails this test.
	time.Sleep(time.Second)
	got := readWitness(t, receipt)
	if len(got) != 1 {
		t.Fatalf("bare return must submit the loaded message exactly once, got %d: %v", len(got), got)
	}
	if got[0] != "claim your work item" {
		t.Fatalf("submitted %q, want the original message", got[0])
	}
}

// TestNudgeConfirmEscalatesToResend proves step 3 exists and is reached: this
// harness drops the first message entirely, so no bare return can rescue it.
func TestNudgeConfirmEscalatesToResend(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, receipt := spawnWithReceipt(t, reg, "drop-first", dropFirstHarness)
	waitUntilIdle(t, a, 5*time.Second)

	if err := a.NudgeWithMode("second time lucky", NudgeConfirm, 9*time.Second); err != nil {
		t.Fatalf("confirm nudge: %v", err)
	}
	got := readWitness(t, receipt)
	if len(got) != 1 || got[0] != "second time lucky" {
		t.Fatalf("expected the resend to land exactly once, got %v", got)
	}
}

// TestNudgeConfirmDoesNotRetryAMidTurnAgent: a prompt typed mid-turn is queued
// legitimately, so absence of a receipt means "not yet", not "lost". The
// message must be typed once and never followed by a bare return or a resend.
func TestNudgeConfirmDoesNotRetryAMidTurnAgent(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	raw := witnessFile(t)
	a, receipt := spawnWithReceipt(t, reg, "busy-queued", busyDeafHarness, "RAW="+raw)
	waitUntilBusy(t, a, 5*time.Second)

	err = a.NudgeWithMode("sweep now", NudgeConfirm, 3*time.Second)
	if !errors.Is(err, ErrNudgeQueued) {
		t.Fatalf("want ErrNudgeQueued for a mid-turn agent, got %v", err)
	}
	if errors.Is(err, ErrNudgeUnconfirmed) {
		t.Fatal("a queued nudge must not be reported as unconfirmed-and-refused")
	}
	if got := readWitness(t, receipt); len(got) != 0 {
		t.Fatalf("harness recorded a submission it never made: %v", got)
	}

	// RAW holds every line the harness read, empty ones included. Exactly one
	// entry means the message was typed once with no bare return and no resend.
	time.Sleep(500 * time.Millisecond)
	typed := readWitness(t, raw)
	if len(typed) != 1 || typed[0] != "[sweep now]" {
		t.Fatalf("mid-turn agent must be typed to exactly once and never retried, got %v", typed)
	}
}

// TestNudgeConfirmFallsBackToWaitIdleWithoutAReceiptSignal: an agent whose
// harness cannot report submissions gets exactly the behaviour it got before
// receipts existed — including, unchanged, the busy-agent refusal.
func TestNudgeConfirmFallsBackToWaitIdleWithoutAReceiptSignal(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	witness := witnessFile(t)
	a, err := reg.Spawn(SpawnRequest{
		Name:    "no-receipt",
		Type:    TypePolecat,
		Command: []string{"sh", fakeHarness(t, "busy.sh", busyHarness)},
		Env:     []string{"WITNESS=" + witness},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if a.hasReceiptSignal() {
		t.Fatal("a bare-registry spawn has no provider and so can have no receipt signal")
	}
	waitUntilBusy(t, a, 5*time.Second)

	err = a.NudgeWithMode("run the sweep", NudgeConfirm, 2*time.Second)
	if err == nil || !containsAll(err.Error(), "wait for idle", "still producing output") {
		t.Fatalf("want the pre-existing wait-idle failure, got %v", err)
	}
}

// TestMidTurnIsNotTheNegationOfIdle guards the distinction the escalation turns
// on: an agent that has never written anything is silent, not mid-turn. IsIdle
// answers false for it (it has no quiet period to measure), and reading that as
// "busy" would switch the escalation off for every freshly-spawned harness —
// exactly the case the startup drop lives in.
func TestMidTurnIsNotTheNegationOfIdle(t *testing.T) {
	a := &Agent{
		Name:      "silent",
		outputBuf: NewRingBuffer(1024),
		nudge:     DefaultNudgeProfile,
		done:      make(chan struct{}),
	}
	if a.IsIdle(a.nudge.IdleThreshold) {
		t.Fatal("an agent with no output has no measurable quiet period")
	}
	if a.midTurn() {
		t.Fatal("an agent that has never written anything is not mid-turn")
	}

	a.outputBuf.Write([]byte("working"))
	if !a.midTurn() {
		t.Fatal("an agent that just wrote is mid-turn")
	}
}
