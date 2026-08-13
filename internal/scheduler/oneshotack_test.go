package scheduler

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// One-shot completion (mg-64e6).
//
// A one-shot used to be deleted in the same Tick pass that delivered it, tagged
// `one_shot_complete`. The fire had already handed the agent a
// `pogo schedule ack <id> --agent <a> --token <t>` command, so that command
// could never work: by the time any agent could run it there was no entry to
// look up. Not a race — the delete was in the same pass as the delivery, so no
// ack could ever win.
//
// The tests below pin the three properties that fixes:
//
//	1. the ack instruction the agent is HANDED is the one that works;
//	2. a retained one-shot does not refire, and does not accumulate;
//	3. the removal record says which end happened — acked, unacked, undelivered
//	   or skipped — rather than asserting "complete" at the one moment nothing
//	   could know.

// ackCmdRE extracts the id/agent/token triple out of the body an agent actually
// receives. Parsing the delivered text rather than reading entry.PendingToken is
// deliberate: the defect was that the instruction and the redeemable state
// disagreed, so a test that reads the state directly cannot see it.
var ackCmdRE = regexp.MustCompile(`pogo schedule ack (\S+) --agent (\S+) --token (\S+)`)

// deliveredBody renders the body a PogodDeliverer would send for this fire.
func deliveredBody(e Entry, fireTime time.Time) string { return buildBody(e, fireTime) }

// TestOneShotAckInstructionIsRedeemable is the direct regression test: run the
// exact command the fire printed and it must be accepted.
func TestOneShotAckInstructionIsRedeemable(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "crew-mayor", OneShot: true, ID: "verify-absentwatch-live-mayor",
		NextFire: now.Add(time.Minute),
		Message:  "verify absentwatch is live",
	}, now); err != nil {
		t.Fatal(err)
	}

	fireAt := now.Add(2 * time.Minute)
	res := s.Tick(context.Background(), fireAt)
	if len(res) != 1 || !res[0].Delivered {
		t.Fatalf("want one delivered fire, got %+v", res)
	}

	fires := rec.snapshot()
	if len(fires) != 1 {
		t.Fatalf("want 1 delivery, got %d", len(fires))
	}
	body := deliveredBody(fires[0].Entry, fires[0].FireTime)
	m := ackCmdRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("delivered body carries no ack instruction:\n%s", body)
	}
	id, agentName, token := m[1], m[2], m[3]

	// The agent's work takes 90 minutes, as mayor's did.
	ackAt := fireAt.Add(90 * time.Minute)
	got, err := s.Ack(agentName, id, token, ackAt)
	if err != nil {
		t.Fatalf("Ack with the token the fire handed out: %v", err)
	}
	if got.Entry.FiresCompleted != 1 || got.Entry.FiresDelivered != 1 {
		t.Errorf("counters: want 1/1 delivered/completed, got %d/%d",
			got.Entry.FiresDelivered, got.Entry.FiresCompleted)
	}
	if got.Latency != 90*time.Minute {
		t.Errorf("latency: want 90m, got %s", got.Latency)
	}

	// Redeemed, so it leaves the live set — and says so.
	if n := len(s.List("crew-mayor")); n != 0 {
		t.Errorf("after ack: want 0 live entries, got %d", n)
	}
	if ev := findScheduleRemoved(t, s.logPath, id, "one_shot_acked"); ev == nil {
		t.Errorf("no schedule_removed reason=one_shot_acked for %q", id)
	}
	assertEventPresent(t, s.logPath, "scheduler_fire_completed", id)
}

// TestOneShotIsRetainedButNeverRefires guards the mechanism that makes the
// retention safe: NextFire still holds the time it came due, so a missing
// spent-marker would refire it on every tick forever.
func TestOneShotIsRetainedButNeverRefires(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-p1", OneShot: true, ID: "once", NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		s.Tick(context.Background(), now.Add(time.Duration(2+i)*time.Minute))
	}
	if n := len(rec.snapshot()); n != 1 {
		t.Fatalf("one-shot delivered %d times, want exactly 1", n)
	}
	live := s.List("cat-p1")
	if len(live) != 1 {
		t.Fatalf("want the fired one-shot retained for its ack, got %d entries", len(live))
	}
	if live[0].LastFire.IsZero() {
		t.Error("retained one-shot has no LastFire — the spent marker dueLocked reads")
	}
	if live[0].PendingToken == "" {
		t.Error("retained one-shot has no outstanding token, so there is nothing to retain it for")
	}
	if live[0].FiresDelivered != 1 {
		t.Errorf("FiresDelivered: want 1, got %d", live[0].FiresDelivered)
	}
}

// TestOneShotUnackedIsReapedAndSaysSo pins the other half of the label fix: a
// one-shot nobody answered leaves a record that is DIFFERENT from one whose
// work was reported done.
func TestOneShotUnackedIsReapedAndSaysSo(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-dead", OneShot: true, ID: "never-answered", NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), now.Add(2*time.Minute))

	// Inside the window it is still redeemable, so it stays.
	s.Tick(context.Background(), now.Add(2*time.Minute).Add(AckStaleWindow-time.Hour))
	if n := len(s.List("cat-dead")); n != 1 {
		t.Fatalf("reaped inside AckStaleWindow — a long turn can still ack; live=%d", n)
	}

	s.Tick(context.Background(), now.Add(2*time.Minute).Add(AckStaleWindow+time.Minute))
	if n := len(s.List("cat-dead")); n != 0 {
		t.Fatalf("want the expired one-shot reaped, got %d live", n)
	}
	if ev := findScheduleRemoved(t, s.logPath, "never-answered", "one_shot_unacked"); ev == nil {
		t.Fatal("no schedule_removed reason=one_shot_unacked")
	}
	if ev := findScheduleRemoved(t, s.logPath, "never-answered", "one_shot_acked"); ev != nil {
		t.Error("an unanswered one-shot was recorded as acked")
	}
}

// TestOneShotDeliveryFailureIsNotRecordedAsComplete covers the latent second
// arm the diagnostic called out: the fire-time delete was not gated on delivery
// success, so a one-shot whose delivery FAILED was also removed as
// `one_shot_complete`.
func TestOneShotDeliveryFailureIsNotRecordedAsComplete(t *testing.T) {
	rec := &recorder{failNth: 1}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-broken", OneShot: true, ID: "undeliverable", NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	res := s.Tick(context.Background(), now.Add(2*time.Minute))
	if len(res) != 1 || res[0].Delivered {
		t.Fatalf("want one FAILED fire, got %+v", res)
	}
	// Nothing to complete, so it does not linger.
	if n := len(s.List("cat-broken")); n != 0 {
		t.Errorf("undelivered one-shot retained (%d live) — it holds no redeemable token", n)
	}
	if ev := findScheduleRemoved(t, s.logPath, "undeliverable", "one_shot_undelivered"); ev == nil {
		t.Fatal("no schedule_removed reason=one_shot_undelivered")
	}
}

// TestOneShotSkippedFireIsLabelledSkipped: a stale one-shot dropped by the skip
// replay policy triggered no turn at all, and must not read as a completion.
func TestOneShotSkippedFireIsLabelledSkipped(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	s.SkipWindow = time.Minute
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-slept", OneShot: true, ID: "stale-once",
		NextFire: now.Add(time.Minute), ReplayPolicy: ReplaySkip,
	}, now); err != nil {
		t.Fatal(err)
	}
	res := s.Tick(context.Background(), now.Add(time.Hour))
	if len(res) != 1 || !res[0].Skipped {
		t.Fatalf("want one skipped fire, got %+v", res)
	}
	if n := len(rec.snapshot()); n != 0 {
		t.Fatalf("skipped fire was delivered %d times", n)
	}
	if ev := findScheduleRemoved(t, s.logPath, "stale-once", "one_shot_skipped"); ev == nil {
		t.Fatal("no schedule_removed reason=one_shot_skipped")
	}
}

// TestOneShotCompleteLabelIsGone is a label guard rather than a behaviour test.
// `one_shot_complete` was emitted at FIRE time and asserted something the
// scheduler cannot know; a reader — human or detector — reads it as "the
// one-shot completed". Nothing may reintroduce it, on any path.
func TestOneShotCompleteLabelIsGone(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	for i, e := range []Entry{
		{Agent: "cat-a", OneShot: true, ID: "acked-one", NextFire: now.Add(time.Minute), Message: "go"},
		{Agent: "cat-b", OneShot: true, ID: "unacked-one", NextFire: now.Add(time.Minute)},
	} {
		if _, err := s.Add(e, now); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	fireAt := now.Add(2 * time.Minute)
	s.Tick(context.Background(), fireAt)

	for _, f := range rec.snapshot() {
		if f.Entry.ID != "acked-one" {
			continue
		}
		m := ackCmdRE.FindStringSubmatch(deliveredBody(f.Entry, f.FireTime))
		if m == nil {
			t.Fatal("acked-one carried no ack instruction")
		}
		if _, err := s.Ack(m[2], m[1], m[3], fireAt.Add(time.Minute)); err != nil {
			t.Fatalf("Ack: %v", err)
		}
	}
	// Let the other one expire.
	s.Tick(context.Background(), fireAt.Add(AckStaleWindow+time.Minute))

	for _, line := range readLines(t, s.logPath) {
		if strings.Contains(line, "one_shot_complete") {
			t.Fatalf("the one_shot_complete label is back: %s", line)
		}
	}
}

// TestOneShotAwaitingAckSurvivesPauseResume mirrors pogod's park/unpark path,
// which round-trips entries through JSON and re-Adds them. A retained one-shot
// must come back still redeemable and still spent — Validate rejects a one-shot
// with a zero NextFire, which is why the spent marker is LastFire and not a
// cleared NextFire.
func TestOneShotAwaitingAckSurvivesPauseResume(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-parked", OneShot: true, ID: "parked-once",
		NextFire: now.Add(time.Minute), Message: "do the thing",
	}, now); err != nil {
		t.Fatal(err)
	}
	fireAt := now.Add(2 * time.Minute)
	s.Tick(context.Background(), fireAt)

	live := s.List("cat-parked")
	if len(live) != 1 {
		t.Fatalf("want 1 retained entry, got %d", len(live))
	}
	raw, err := json.Marshal(live[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove("cat-parked", "parked-once"); err != nil {
		t.Fatal(err)
	}

	var restored Entry
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(restored, fireAt.Add(time.Minute)); err != nil {
		t.Fatalf("re-Add of a fired one-shot: %v", err)
	}

	// Still spent...
	s.Tick(context.Background(), fireAt.Add(2*time.Minute))
	if n := len(rec.snapshot()); n != 1 {
		t.Errorf("restored one-shot refired: %d deliveries", n)
	}
	// ...and still redeemable.
	if _, err := s.Ack("cat-parked", "parked-once", live[0].PendingToken, fireAt.Add(3*time.Minute)); err != nil {
		t.Errorf("Ack after park/unpark: %v", err)
	}
}

// TestSpentOneShotWithoutPendingSinceIsStillReaped covers the GC's fallback
// clock. A spent one-shot persisted by a binary that predates PendingSince, or
// hand-written into schedules.json, would otherwise be retained forever — a
// leak introduced by the retention, in the entry the retention is for.
func TestSpentOneShotWithoutPendingSinceIsStillReaped(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	now := fixedTime()
	key := entryKey{Agent: "cat-legacy", ID: "legacy-once"}
	s.mu.Lock()
	s.entries[key] = &Entry{
		Agent: "cat-legacy", ID: "legacy-once", OneShot: true,
		NextFire: now, LastFire: now, Delivery: DeliveryNudge, ReplayPolicy: ReplayOnce,
		PendingToken: "deadbeef", // outstanding, but with no issue time recorded
	}
	s.mu.Unlock()

	if n := s.GCExpiredOneShots(now.Add(time.Hour)); n != 0 {
		t.Fatalf("reaped %d inside the window", n)
	}
	if n := s.GCExpiredOneShots(now.Add(AckStaleWindow + time.Minute)); n != 1 {
		t.Fatalf("want 1 reaped past the window, got %d", n)
	}
	if _, ok := s.Get("cat-legacy", "legacy-once"); ok {
		t.Error("entry survived its reap")
	}
}

// ackingDeliverer redeems the fire from inside Deliver, i.e. in the narrowest
// possible window between the token going out and the fire-time removal block
// running.
type ackingDeliverer struct {
	s      *Scheduler
	ackErr error
}

func (d *ackingDeliverer) Deliver(_ context.Context, e Entry, t time.Time) error {
	_, d.ackErr = d.s.Ack(e.Agent, e.ID, e.PendingToken, t)
	return nil
}

// TestOneShotAckedDuringDeliveryIsNotLabelledUndelivered is the check on the
// fix itself. The fire-time path decides "undelivered" by looking at whether a
// token is outstanding, and an ack CLEARS that token — so if the one-shot were
// marked spent after delivery rather than before it, an ack landing in that
// window would leave a completed one-shot recorded as never delivered: the
// same class of defect as the one this change removes, one lock section over.
//
// No real agent acks this fast (Deliver returns in microseconds; the agent runs
// a shell command). The point is that the label cannot be wrong, not that the
// window is reachable.
func TestOneShotAckedDuringDeliveryIsNotLabelledUndelivered(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	d := &ackingDeliverer{s: s}
	s.deliverer = d
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "cat-fast", OneShot: true, ID: "fast-ack", NextFire: now.Add(time.Minute),
	}, now); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), now.Add(2*time.Minute))

	if d.ackErr != nil {
		t.Fatalf("in-flight ack rejected: %v", d.ackErr)
	}
	if n := len(s.List("cat-fast")); n != 0 {
		t.Errorf("acked one-shot still live: %d entries", n)
	}
	if ev := findScheduleRemoved(t, s.logPath, "fast-ack", "one_shot_acked"); ev == nil {
		t.Error("no schedule_removed reason=one_shot_acked")
	}
	for _, wrong := range []string{"one_shot_undelivered", "one_shot_unacked", "one_shot_skipped"} {
		if ev := findScheduleRemoved(t, s.logPath, "fast-ack", wrong); ev != nil {
			t.Errorf("an ACKED one-shot was also recorded as %s", wrong)
		}
	}
}

// assertEventPresent fails unless logPath holds an event of the given type
// whose details.schedule_id matches.
func assertEventPresent(t *testing.T, logPath, eventType, scheduleID string) {
	t.Helper()
	for _, line := range readLines(t, logPath) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["event_type"] != eventType {
			continue
		}
		d, _ := m["details"].(map[string]any)
		if d != nil && d["schedule_id"] == scheduleID {
			return
		}
	}
	t.Errorf("no %s event for schedule_id=%q in %s", eventType, scheduleID, logPath)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}
