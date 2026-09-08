package agent

import (
	"errors"
	"testing"
	"time"
)

// idleSubmitHarness prints one line so the agent registers as idle, then reads
// and submits every line it is given. A plain healthy agent.
const idleSubmitHarness = `#!/bin/sh
echo ready
while IFS= read -r line; do
	[ -n "$line" ] && printf '%s\n' "$line" >> "$WITNESS"
done
`

// TestQueuedNudgeIsRecordedForAMidTurnDelivery.
//
// deliverConfirmed correctly declines to escalate at a mid-turn agent — the
// harness emits no receipt for such a prompt, so a bare return would resubmit
// something that landed. What that leaves behind is an obligation: a prompt
// sitting in a working agent's composer, with nothing watching whether the end
// of the turn ever drains it. This record is what makes mg-5246's mid-session
// wedge detectable at all, so its absence must fail the build rather than
// quietly disarm internal/midsessionwedge.
func TestQueuedNudgeIsRecordedForAMidTurnDelivery(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	raw := witnessFile(t)
	a, _ := spawnWithReceipt(t, reg, "queued-record", busyDeafHarness, "RAW="+raw)
	waitUntilBusy(t, a, 5*time.Second)

	if q := a.QueuedNudge(); q != nil {
		t.Fatalf("an agent that has been nudged zero times already owes a submit: %+v", q)
	}

	before, err := CountSubmits(a.ReceiptFile())
	if err != nil {
		t.Fatalf("CountSubmits: %v", err)
	}
	if err := a.NudgeWithMode("sweep now", NudgeConfirm, 3*time.Second); !errors.Is(err, ErrNudgeQueued) {
		t.Fatalf("want ErrNudgeQueued, got %v", err)
	}

	q := a.QueuedNudge()
	if q == nil {
		t.Fatal("a mid-turn delivery left no queued-nudge record; the mid-session wedge " +
			"detector reads this to tell an agent that OWES a submit from a finished " +
			"polecat parked at an empty composer, and without it the two are identical")
	}
	if q.Submits != before {
		t.Errorf("queued record Submits = %d, want the count at delivery (%d): the "+
			"obligation is discharged by the count EXCEEDING this, so a wrong baseline "+
			"either never clears or clears immediately", q.Submits, before)
	}
	if q.At.IsZero() {
		t.Error("queued record has no timestamp")
	}
}

// TestQueuedNudgeIsCopiedOut: a caller must not be able to reach into the
// agent's record. The watcher holds these across ticks.
func TestQueuedNudgeIsCopiedOut(t *testing.T) {
	a := &Agent{}
	a.noteQueuedNudge(4)
	q := a.QueuedNudge()
	if q == nil {
		t.Fatal("no record")
	}
	q.Submits = 999
	if again := a.QueuedNudge(); again.Submits != 4 {
		t.Errorf("mutating the returned record changed the agent's: %d", again.Submits)
	}
}

// TestConfirmedDeliveryDischargesTheRecord. A confirmed submit drains whatever
// was loaded, including anything an earlier mid-turn delivery left behind. If
// the record survived, the watcher would judge a healthy agent's next quiet
// spell against an obligation that was already met.
func TestConfirmedDeliveryDischargesTheRecord(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, _ := spawnWithReceipt(t, reg, "queued-clear", idleSubmitHarness)
	waitUntilIdle(t, a, 5*time.Second)

	a.noteQueuedNudge(0)
	if a.QueuedNudge() == nil {
		t.Fatal("record did not take")
	}
	if err := a.NudgeWithMode("hello", NudgeConfirm, 9*time.Second); err != nil {
		t.Fatalf("confirm nudge: %v", err)
	}
	if q := a.QueuedNudge(); q != nil {
		t.Errorf("a CONFIRMED submit left the queued record standing (%+v); the composer "+
			"has demonstrably drained, and a stale obligation makes the next quiet spell "+
			"read as a wedge", q)
	}
}

// TestHasReceiptSignalMatchesTheConfirmPath. A detector built on the receipt
// count must ask the same question the confirm path asks, or it will judge an
// agent whose submits are not recorded anywhere.
func TestHasReceiptSignalMatchesTheConfirmPath(t *testing.T) {
	a := &Agent{}
	if a.HasReceiptSignal() {
		t.Error("an agent with no receipt file claims a receipt signal")
	}
	a.receiptFile = "/some/path.submits"
	if !a.HasReceiptSignal() {
		t.Error("an agent with a receipt file denies having a receipt signal")
	}
	if a.HasReceiptSignal() != a.hasReceiptSignal() {
		t.Error("the exported reader disagrees with the one deliverConfirmed uses")
	}
}
