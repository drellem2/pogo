package scheduler

import (
	"context"
	"math"
	"testing"
	"time"
)

// The defect these tests pin (mg-57e9): the delivery record used to be written
// AFTER s.deliverer.Deliver returned, while the completion token was issued
// before it. A nudge delivery blocks on the harness's PTY idle gate for as long
// as the target agent's current turn runs, so any ack landing inside that
// window was counted against a delivery that had not been recorded yet.
//
// Reproduced against the live daemon while this fix was being written, on this
// polecat's own mail-check schedule:
//
//	$ pogo schedule ack mail-check-mg-57e9 --agent p57e9 --token ee3c211f
//	Acked mail-check-mg-57e9 for p57e9 — 1/0 fires completed (latency 37369ms).
//	$ pogo schedule list --agent p57e9 --json
//	"fires_delivered": 1, "fires_completed": 1, "unacked_streak": 1
//
// Two distinct wrong readings, both asserted against below: a ratio above 1 at
// ack time, and a phantom UnackedStreak of 1 afterwards — one increment below
// DefaultStallThreshold, on a fire the agent had already answered.

// slowAckingDeliverer stands in for the PTY idle gate. It acks the fire from
// inside Deliver, i.e. in the window between the bytes reaching the agent and
// Deliver returning, and records what the entry looked like at that instant —
// which is exactly what `pogo schedule ack` prints back to the agent.
type slowAckingDeliverer struct {
	s       *Scheduler
	ackErr  error
	atAck   Entry
	failErr error
}

func (d *slowAckingDeliverer) Deliver(_ context.Context, e Entry, t time.Time) error {
	res, err := d.s.Ack(e.Agent, e.ID, e.PendingToken, t.Add(37*time.Second))
	d.ackErr = err
	d.atAck = res.Entry
	return d.failErr
}

func TestAckDuringDeliveryNeverOutrunsItsOwnDeliveryRecord(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	d := &slowAckingDeliverer{s: s}
	s.deliverer = d
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-busy", ID: "mail-check", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatal(err)
	}
	res := s.Tick(context.Background(), now.Add(10*time.Minute))
	if len(res) != 1 {
		t.Fatalf("Tick fired %d entries, want 1", len(res))
	}
	if d.ackErr != nil {
		t.Fatalf("in-flight ack rejected: %v", d.ackErr)
	}

	// What the agent was told at ack time. Before the fix this line read
	// "1/0 fires completed".
	if d.atAck.FiresCompleted > d.atAck.FiresDelivered {
		t.Errorf("at ack time the entry read %d/%d — a completion ratio above 1, "+
			"which every reader of that column treats as impossible",
			d.atAck.FiresCompleted, d.atAck.FiresDelivered)
	}

	// And the identity mg-a14c pinned (rate == 1/gap - outstanding/delivered)
	// has to survive the ack instant, not just settle down afterwards. With
	// delivered still 0 the rate is a division by zero and the residual is not
	// a number, which is a stronger statement of the same defect.
	assertAttentionGapIdentity(t, d.atAck, "at ack time")

	live, ok := s.Get("cat-busy", "mail-check")
	if !ok {
		t.Fatal("schedule vanished")
	}
	if live.FiresDelivered != 1 || live.FiresCompleted != 1 {
		t.Errorf("settled counters = %d/%d, want 1/1", live.FiresCompleted, live.FiresDelivered)
	}
	if live.UnackedStreak != 0 {
		t.Errorf("UnackedStreak = %d after the fire was acked, want 0 — a phantom "+
			"streak carries an already-answered fire toward the stall threshold (%d)",
			live.UnackedStreak, DefaultStallThreshold)
	}
	if res[0].UnackedStreak != 0 {
		t.Errorf("FireResult.UnackedStreak = %d, want 0: the streak reported for a "+
			"delivery is the one true when the record landed, not when the bytes left",
			res[0].UnackedStreak)
	}
	assertAttentionGapIdentity(t, live, "after the tick settled")
}

// TestFailedDeliveryRetractsItsDeliveryRecord is the other half of recording
// early. Undelivered bytes triggered no turn, so they must not survive in
// FiresDelivered — the same reason the token is dropped on this path.
func TestFailedDeliveryRetractsItsDeliveryRecord(t *testing.T) {
	rec := &recorder{failNth: 1}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-unreachable", ID: "mail-check", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), now.Add(10*time.Minute))

	live, ok := s.Get("cat-unreachable", "mail-check")
	if !ok {
		t.Fatal("schedule vanished")
	}
	if live.FiresDelivered != 0 {
		t.Errorf("FiresDelivered = %d after a FAILED delivery, want 0 — "+
			"scheduler_fire_failed already reports it, and counting it here blurs "+
			"the two faults the pair of signals exists to separate", live.FiresDelivered)
	}
	if live.UnackedStreak != 0 {
		t.Errorf("UnackedStreak = %d after a failed delivery, want 0", live.UnackedStreak)
	}
	if live.PendingToken != "" {
		t.Error("a failed delivery left a redeemable token outstanding")
	}

	// The next fire succeeds and reads 1, not 2: the retraction released the
	// slot rather than merely hiding it.
	s.Tick(context.Background(), now.Add(20*time.Minute))
	live, _ = s.Get("cat-unreachable", "mail-check")
	if live.FiresDelivered != 1 || live.UnackedStreak != 1 {
		t.Errorf("after one failed and one successful fire: delivered=%d streak=%d, want 1 and 1",
			live.FiresDelivered, live.UnackedStreak)
	}
}

// TestFailedDeliveryThatWasNonethelessAckedKeepsBothCounts is the enumeration
// of this fix against its own defect. Retracting unconditionally would produce
// completed=1 against delivered=0 on this path — the identical wrong reading,
// arrived at from the opposite direction. The token guard is what rules it out:
// an ack clears the token, so the retraction declines to run.
func TestFailedDeliveryThatWasNonethelessAckedKeepsBothCounts(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	d := &slowAckingDeliverer{s: s, failErr: errFakeDeliveryFailure}
	s.deliverer = d
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-partial", ID: "mail-check", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), now.Add(10*time.Minute))
	if d.ackErr != nil {
		t.Fatalf("in-flight ack rejected: %v", d.ackErr)
	}

	live, ok := s.Get("cat-partial", "mail-check")
	if !ok {
		t.Fatal("schedule vanished")
	}
	if live.FiresCompleted > live.FiresDelivered {
		t.Errorf("counters = %d/%d — the retraction ran on a fire the agent had "+
			"demonstrably received, re-creating the ratio above 1 it exists to remove",
			live.FiresCompleted, live.FiresDelivered)
	}
	if live.FiresDelivered != 1 || live.FiresCompleted != 1 {
		t.Errorf("counters = %d/%d, want 1/1: an agent that acked it saw the bytes",
			live.FiresCompleted, live.FiresDelivered)
	}
	assertAttentionGapIdentity(t, live, "after an acked-but-failed delivery")
}

// assertAttentionGapIdentity re-checks mg-a14c's pinned identity
// (rate == 1/gap - outstanding/delivered) on a live entry rather than on a
// hand-written fixture. A residual that is NaN or Inf rather than merely large
// is the signature of delivered==0 with completions on the books.
func assertAttentionGapIdentity(t *testing.T, e Entry, when string) {
	t.Helper()
	gap := e.AttentionGap()
	if gap == 0 {
		if e.FiresCompleted > 0 {
			t.Errorf("%s: %d completions but an unmeasured gap — delivered is %d",
				when, e.FiresCompleted, e.FiresDelivered)
		}
		return
	}
	boundary := 0.0
	if e.Outstanding() {
		boundary = 1
	}
	rate := float64(e.FiresCompleted) / float64(e.FiresDelivered)
	resid := rate - (1/gap - boundary/float64(e.FiresDelivered))
	if math.IsNaN(resid) || math.IsInf(resid, 0) || math.Abs(resid) > 1e-9 {
		t.Errorf("%s: identity residual = %v, want 0 (delivered=%d completed=%d outstanding=%v)",
			when, resid, e.FiresDelivered, e.FiresCompleted, e.Outstanding())
	}
}
