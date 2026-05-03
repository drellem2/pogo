package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// recorder is a Deliverer that captures fires for assertion. The lock matters
// because Tick may be invoked from a heartbeat-driven goroutine in production,
// even though tests below drive Tick directly on the test goroutine.
type recorder struct {
	mu      sync.Mutex
	fires   []recordedFire
	failNth int // if >0, return error on the Nth call
	calls   int
}

type recordedFire struct {
	Entry    Entry
	FireTime time.Time
}

func (r *recorder) Deliver(_ context.Context, e Entry, t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.fires = append(r.fires, recordedFire{Entry: e, FireTime: t})
	if r.failNth > 0 && r.calls == r.failNth {
		return errFakeDeliveryFailure
	}
	return nil
}

func (r *recorder) snapshot() []recordedFire {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedFire, len(r.fires))
	copy(out, r.fires)
	return out
}

var errFakeDeliveryFailure = &fakeErr{"fake delivery failure"}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }

func newSchedulerForTest(t *testing.T, deliverer Deliverer) *Scheduler {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	s, err := New(path, deliverer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// fixedTime is a stable timestamp used as "T0" across tests. Picking a wall
// time at the top of an hour (12:00 UTC) means cron expressions like
// "*/5 * * * *" line up cleanly with elapsed minutes.
func fixedTime() time.Time {
	return time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
}

func TestAddAndPersistRoundTrip(t *testing.T) {
	rec := &recorder{}
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")

	now := fixedTime()
	s, err := New(path, rec)
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.Add(Entry{
		Agent: "crew-research",
		Cron:  "*/5 * * * *",
		ID:    "research-poll",
	}, now)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.ID != "research-poll" {
		t.Errorf("ID: want research-poll, got %q", added.ID)
	}
	if added.Delivery != DeliveryNudge {
		t.Errorf("Delivery default: want nudge, got %q", added.Delivery)
	}
	if added.ReplayPolicy != ReplayOnce {
		t.Errorf("ReplayPolicy default: want once, got %q", added.ReplayPolicy)
	}
	wantNext := time.Date(2026, 5, 3, 12, 5, 0, 0, time.UTC)
	if !added.NextFire.Equal(wantNext) {
		t.Errorf("NextFire: want %s, got %s", wantNext, added.NextFire)
	}

	// Reload from disk in a fresh Scheduler — the entry must come back.
	s2, err := New(path, rec)
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	got, ok := s2.Get("research-poll")
	if !ok {
		t.Fatal("entry missing after reload")
	}
	if !got.NextFire.Equal(wantNext) {
		t.Errorf("reloaded NextFire: want %s, got %s", wantNext, got.NextFire)
	}
}

func TestTickFiresWhenDueAndReschedules(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "crew-research", Cron: "*/5 * * * *", ID: "poll",
	}, now); err != nil {
		t.Fatal(err)
	}

	// At T+0, nothing is due (NextFire is T+5min).
	if got := s.Tick(context.Background(), now); len(got) != 0 {
		t.Fatalf("unexpected fires at T+0: %d", len(got))
	}

	// At T+5min, exactly one fire.
	t1 := now.Add(5 * time.Minute)
	res := s.Tick(context.Background(), t1)
	if len(res) != 1 {
		t.Fatalf("at T+5: want 1 fire, got %d", len(res))
	}
	if !res[0].Delivered {
		t.Errorf("fire not delivered: err=%v", res[0].DeliverErr)
	}
	if res[0].Missed != 0 {
		t.Errorf("missed: want 0, got %d", res[0].Missed)
	}
	got, _ := s.Get("poll")
	wantNext := now.Add(10 * time.Minute)
	if !got.NextFire.Equal(wantNext) {
		t.Errorf("rescheduled NextFire: want %s, got %s", wantNext, got.NextFire)
	}

	// At T+10min, second fire.
	t2 := now.Add(10 * time.Minute)
	res = s.Tick(context.Background(), t2)
	if len(res) != 1 {
		t.Fatalf("at T+10: want 1 fire, got %d", len(res))
	}
	if len(rec.snapshot()) != 2 {
		t.Errorf("recorder calls: want 2, got %d", len(rec.snapshot()))
	}
}

// TestNTicksDeliversCorrectNudges drives the scheduler through N ticks against
// a fake clock and asserts the recorder saw the expected fires per the
// acceptance criteria ("fake heartbeat clock fires N ticks").
func TestNTicksDeliversCorrectNudges(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "crew-research", Cron: "*/15 * * * *", ID: "poll-15m",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Entry{
		Agent: "crew-doctor", Cron: "*/10 * * * *", ID: "doctor",
	}, now); err != nil {
		t.Fatal(err)
	}

	// Tick every minute for 60 minutes. Expect:
	//   poll-15m: at T+15, T+30, T+45, T+60 → 4 fires
	//   doctor : at T+10, T+20, T+30, T+40, T+50, T+60 → 6 fires
	for m := 1; m <= 60; m++ {
		s.Tick(context.Background(), now.Add(time.Duration(m)*time.Minute))
	}

	fires := rec.snapshot()
	count := map[string]int{}
	for _, f := range fires {
		count[f.Entry.ID]++
	}
	if count["poll-15m"] != 4 {
		t.Errorf("poll-15m: want 4 fires, got %d", count["poll-15m"])
	}
	if count["doctor"] != 6 {
		t.Errorf("doctor: want 6 fires, got %d", count["doctor"])
	}
}

// TestClockJumpAtMostOnceReplayFiresExactly Once is the explicit acceptance
// case: a wall-clock jump straddles many fire points, but the at-most-once
// (default) replay policy delivers a single fire and reschedules to the
// next future occurrence.
func TestClockJumpAtMostOnceReplayFiresExactlyOnce(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "crew-research", Cron: "*/5 * * * *", ID: "poll",
		ReplayPolicy: ReplayOnce,
	}, now); err != nil {
		t.Fatal(err)
	}

	// Simulate a 1-hour sleep: jump from T+0 to T+1h. The scheduler should
	// fire exactly once even though 12 fire points (T+5, +10, … +60) passed.
	jumped := now.Add(time.Hour)
	res := s.Tick(context.Background(), jumped)
	if len(res) != 1 {
		t.Fatalf("want 1 fire after 1h jump, got %d", len(res))
	}
	if res[0].Missed != 11 {
		// 11 missed fires between T+5 and T+60 inclusive of T+60 itself
		// is the original due, plus 11 additional intermediate periods.
		t.Errorf("missed count: want 11, got %d", res[0].Missed)
	}

	// NextFire after a 1h jump should land on the next 5-minute boundary
	// strictly after T+60 → T+1h05m.
	got, _ := s.Get("poll")
	wantNext := now.Add(65 * time.Minute)
	if !got.NextFire.Equal(wantNext) {
		t.Errorf("post-jump NextFire: want %s, got %s", wantNext, got.NextFire)
	}

	// Verify only one delivery happened — the at-most-once guarantee.
	if len(rec.snapshot()) != 1 {
		t.Errorf("delivery count: want 1, got %d", len(rec.snapshot()))
	}
}

func TestReplaySkipDropsFireOlderThanWindow(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	s.SkipWindow = 90 * time.Second // tight window for the test
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "crew-poll", Cron: "*/5 * * * *", ID: "skipper",
		ReplayPolicy: ReplaySkip,
	}, now); err != nil {
		t.Fatal(err)
	}

	// Big jump — fire was due at T+5 but we tick at T+1h. Skip policy:
	// don't deliver, just advance.
	jumped := now.Add(time.Hour)
	res := s.Tick(context.Background(), jumped)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if !res[0].Skipped {
		t.Error("skip policy should mark the fire skipped")
	}
	if len(rec.snapshot()) != 0 {
		t.Errorf("skip should not deliver, got %d deliveries", len(rec.snapshot()))
	}
	got, _ := s.Get("skipper")
	if !got.NextFire.After(jumped) {
		t.Errorf("NextFire should be after jump, got %s", got.NextFire)
	}
}

func TestReplayCountAccumulatesMissed(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "crew-poll", Cron: "*/5 * * * *", ID: "counter",
		ReplayPolicy: ReplayCount,
	}, now); err != nil {
		t.Fatal(err)
	}

	// 30-minute jump — 6 periods would have fired (T+5..T+30). Count policy
	// fires once but records 5 missed (T+10..T+30).
	res := s.Tick(context.Background(), now.Add(30*time.Minute))
	if len(res) != 1 {
		t.Fatalf("want 1 fire, got %d", len(res))
	}
	if res[0].Missed != 5 {
		t.Errorf("missed: want 5, got %d", res[0].Missed)
	}
	got, _ := s.Get("counter")
	if got.MissedFires != 5 {
		t.Errorf("MissedFires accumulator: want 5, got %d", got.MissedFires)
	}
}

func TestOneShotFiresOnceAndIsRemoved(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	added, err := s.Add(Entry{
		Agent:    "cat-foo",
		OneShot:  true,
		NextFire: now.Add(10 * time.Minute),
		ID:       "wakeup",
	}, now)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added.OneShot {
		t.Error("one_shot flag lost")
	}

	// Before due: nothing.
	if got := s.Tick(context.Background(), now.Add(5*time.Minute)); len(got) != 0 {
		t.Fatalf("early tick fired: %v", got)
	}

	// At due: fires once.
	res := s.Tick(context.Background(), now.Add(10*time.Minute))
	if len(res) != 1 {
		t.Fatalf("due tick: want 1 fire, got %d", len(res))
	}

	// After firing: gone.
	if _, ok := s.Get("wakeup"); ok {
		t.Error("one-shot entry should be removed after firing")
	}

	// A subsequent tick must not refire.
	if got := s.Tick(context.Background(), now.Add(20*time.Minute)); len(got) != 0 {
		t.Fatalf("refire after one-shot: %v", got)
	}
}

func TestRemoveByID(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	now := fixedTime()
	if _, err := s.Add(Entry{Agent: "a", Cron: "* * * * *", ID: "x"}, now); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Remove("x")
	if err != nil || !removed {
		t.Fatalf("Remove: removed=%v err=%v", removed, err)
	}
	if _, ok := s.Get("x"); ok {
		t.Error("entry still present after Remove")
	}
	// Removing again returns false, no error.
	removed, err = s.Remove("x")
	if err != nil || removed {
		t.Errorf("Remove second time: removed=%v err=%v", removed, err)
	}
}

func TestListFiltersByAgent(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	now := fixedTime()
	if _, err := s.Add(Entry{Agent: "alpha", Cron: "* * * * *", ID: "a1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Entry{Agent: "beta", Cron: "* * * * *", ID: "b1"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Entry{Agent: "alpha", Cron: "0 * * * *", ID: "a2"}, now); err != nil {
		t.Fatal(err)
	}

	if got := s.List("alpha"); len(got) != 2 {
		t.Errorf("alpha list: want 2, got %d", len(got))
	}
	if got := s.List("beta"); len(got) != 1 {
		t.Errorf("beta list: want 1, got %d", len(got))
	}
	if got := s.List(""); len(got) != 3 {
		t.Errorf("unfiltered list: want 3, got %d", len(got))
	}
}

func TestDeliveryFailureStillReschedules(t *testing.T) {
	// If the deliverer errors, the entry must still advance — otherwise a
	// permanently-broken nudge channel pins the same fire forever and we
	// re-deliver every tick.
	rec := &recorder{failNth: 1}
	s := newSchedulerForTest(t, rec)
	now := fixedTime()

	if _, err := s.Add(Entry{
		Agent: "x", Cron: "*/5 * * * *", ID: "p",
	}, now); err != nil {
		t.Fatal(err)
	}

	res := s.Tick(context.Background(), now.Add(5*time.Minute))
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if res[0].Delivered {
		t.Error("delivery should have been marked failed")
	}
	if res[0].DeliverErr == nil {
		t.Error("DeliverErr should be set on failure")
	}
	got, _ := s.Get("p")
	wantNext := now.Add(10 * time.Minute)
	if !got.NextFire.Equal(wantNext) {
		t.Errorf("NextFire after failure: want %s, got %s", wantNext, got.NextFire)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
	}{
		{"missing agent", Entry{Cron: "* * * * *"}},
		{"bad cron", Entry{Agent: "a", Cron: "not a cron"}},
		{"oneshot with cron", Entry{Agent: "a", OneShot: true, Cron: "* * * * *", NextFire: time.Now()}},
		{"oneshot without next_fire", Entry{Agent: "a", OneShot: true}},
		{"recurring without cron", Entry{Agent: "a"}},
		{"unknown delivery", Entry{Agent: "a", Cron: "* * * * *", Delivery: "carrier-pigeon"}},
		{"unknown replay", Entry{Agent: "a", Cron: "* * * * *", ReplayPolicy: "lol"}},
	}
	s := newSchedulerForTest(t, nil)
	now := fixedTime()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Add(c.entry, now); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestAddRequestInDuration(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	now := fixedTime()
	entry, err := s.addFromRequest(AddRequest{
		Agent: "cat-foo",
		In:    "30m",
	}, now)
	if err != nil {
		t.Fatalf("addFromRequest: %v", err)
	}
	if !entry.OneShot {
		t.Error("In duration should produce one-shot entry")
	}
	want := now.Add(30 * time.Minute)
	if !entry.NextFire.Equal(want) {
		t.Errorf("NextFire: want %s, got %s", want, entry.NextFire)
	}
}

func TestAddRequestRejectsBadDuration(t *testing.T) {
	s := newSchedulerForTest(t, nil)
	if _, err := s.addFromRequest(AddRequest{Agent: "a", In: "no"}, fixedTime()); err == nil {
		t.Error("expected error for invalid duration")
	}
	if _, err := s.addFromRequest(AddRequest{Agent: "a", In: "-1m"}, fixedTime()); err == nil {
		t.Error("expected error for negative duration")
	}
}

// TestPersistedJSONIsHumanReadable guards the on-disk format against
// accidental obfuscation. The file is part of the operator-visible substrate
// (per ARCHITECTURE.md "filesystem is the coordination layer") and should be
// readable + diffable.
func TestPersistedJSONIsHumanReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	now := fixedTime()
	s, err := New(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Entry{Agent: "alpha", Cron: "*/5 * * * *", ID: "a"}, now); err != nil {
		t.Fatal(err)
	}
	data, err := readFile(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("file empty")
	}
	if string(data[:1]) != "{" {
		t.Errorf("file should be JSON object, starts with %q", string(data[:1]))
	}
}
