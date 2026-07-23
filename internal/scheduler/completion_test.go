package scheduler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// eventsOfType returns every event of the given type from a scheduler's own
// events.log, in file order.
func eventsOfType(t *testing.T, path, eventType string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open events.log: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["event_type"] == eventType {
			out = append(out, m)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan events.log: %v", err)
	}
	return out
}

func details(t *testing.T, ev map[string]any) map[string]any {
	t.Helper()
	d, _ := ev["details"].(map[string]any)
	if d == nil {
		t.Fatalf("event has no details: %+v", ev)
	}
	return d
}

// addFiring registers a minute-cron schedule already due at now.
func addFiring(t *testing.T, s *Scheduler, agent, id string, now time.Time) Entry {
	t.Helper()
	e, err := s.Add(Entry{
		ID:       id,
		Agent:    agent,
		Cron:     "* * * * *",
		Message:  "check your mail",
		NextFire: now,
	}, now)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return e
}

// TestFireCarriesRedeemableToken pins the round trip: a delivered fire carries
// a token in its body, that token acks, and the ack lands as a
// scheduler_fire_completed event with the running counters.
func TestFireCarriesRedeemableToken(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	addFiring(t, s, "pm-pogo", "sweep", now)

	s.Tick(context.Background(), now)

	fires := rec.snapshot()
	if len(fires) != 1 {
		t.Fatalf("got %d fires, want 1", len(fires))
	}
	tok := fires[0].Entry.PendingToken
	if tok == "" {
		t.Fatal("delivered fire carried no completion token")
	}

	// The token and the redeeming command must be in the BYTES the agent sees —
	// that is what makes the signal harness-independent.
	body := buildBody(fires[0].Entry, now)
	if !strings.Contains(body, "ack="+tok) {
		t.Errorf("body missing ack= footer field:\n%s", body)
	}
	want := "pogo schedule ack sweep --agent pm-pogo --token " + tok
	if !strings.Contains(body, want) {
		t.Errorf("body missing redeem command %q:\n%s", want, body)
	}

	res, err := s.Ack("pm-pogo", "sweep", tok, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if res.Entry.FiresCompleted != 1 || res.Entry.FiresDelivered != 1 {
		t.Errorf("counters = %d/%d, want 1/1", res.Entry.FiresCompleted, res.Entry.FiresDelivered)
	}
	if res.Entry.UnackedStreak != 0 {
		t.Errorf("UnackedStreak = %d after ack, want 0", res.Entry.UnackedStreak)
	}
	if res.LatencyMS != 3000 {
		t.Errorf("LatencyMS = %d, want 3000", res.LatencyMS)
	}

	evs := eventsOfType(t, s.logPath, "scheduler_fire_completed")
	if len(evs) != 1 {
		t.Fatalf("got %d scheduler_fire_completed events, want 1", len(evs))
	}
	d := details(t, evs[0])
	if d["fire_token"] != tok {
		t.Errorf("event fire_token = %v, want %v", d["fire_token"], tok)
	}
	if d["to"] != "pm-pogo" {
		t.Errorf("event to = %v, want pm-pogo", d["to"])
	}
	if d["fires_completed"] != float64(1) {
		t.Errorf("event fires_completed = %v, want 1", d["fires_completed"])
	}
}

// TestDeadFleetIsDistinguishableFromHealthy is the load-bearing test: it
// reproduces the 2026-07-22 shape — every fire delivered on time, every
// consuming turn accomplishing nothing — and asserts that the resulting record
// is NOT the same as a healthy fleet's.
//
// The bar the dispatch note set: the completion signal must read obviously
// differently from the 647 identical successes `scheduler_fire_delivered`
// logged during the outage.
func TestDeadFleetIsDistinguishableFromHealthy(t *testing.T) {
	const fires = 20

	// A healthy agent: every fire acked.
	healthy := newSchedulerForTest(t, &recorder{})
	// A dead agent: every fire delivered, consumed, and unanswered — exactly
	// the auth-expiry shape, where the turn ran but did no work.
	dead := newSchedulerForTest(t, &recorder{})

	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	addFiring(t, healthy, "pm-pogo", "sweep", base)
	addFiring(t, dead, "pm-pogo", "sweep", base)

	// Seed BOTH with one completed fire, so the dead one is CompletionTracked
	// (a schedule that has never acked is unknown, not failing — that guard is
	// the whole reason the streak is trustworthy) and the delivery counts stay
	// symmetric. Before the outage, the 2026-07-22 agents were likewise healthy.
	for _, s := range []*Scheduler{healthy, dead} {
		s.Tick(context.Background(), base)
		seed, _ := s.Get("pm-pogo", "sweep")
		if _, err := s.Ack("pm-pogo", "sweep", seed.PendingToken, base); err != nil {
			t.Fatalf("seed Ack: %v", err)
		}
	}

	for i := 1; i <= fires; i++ {
		at := base.Add(time.Duration(i) * 10 * time.Minute)

		healthy.Tick(context.Background(), at)
		h, _ := healthy.Get("pm-pogo", "sweep")
		if _, err := healthy.Ack("pm-pogo", "sweep", h.PendingToken, at.Add(2*time.Second)); err != nil {
			t.Fatalf("healthy Ack %d: %v", i, err)
		}

		// The dead agent consumes the fire and does nothing. No ack.
		dead.Tick(context.Background(), at)
	}

	hs := healthy.Completion("", 0)
	ds := dead.Completion("", 0)

	// Delivery counts are IDENTICAL — that is the bug being fixed. Both agents
	// look perfect through the pre-mg-a754 lens.
	if hs.FiresDelivered != ds.FiresDelivered {
		t.Fatalf("premise broken: delivered %d (healthy) vs %d (dead) — the two "+
			"fleets must be indistinguishable by DELIVERY for this test to mean anything",
			hs.FiresDelivered, ds.FiresDelivered)
	}

	// Completion tells them apart, and not subtly.
	if hs.Ratio != 1.0 {
		t.Errorf("healthy ratio = %v, want 1.0", hs.Ratio)
	}
	if hs.Stalled != 0 {
		t.Errorf("healthy stalled = %d, want 0", hs.Stalled)
	}
	if ds.Stalled != 1 || ds.Tracked != 1 {
		t.Errorf("dead stalled/tracked = %d/%d, want 1/1", ds.Stalled, ds.Tracked)
	}
	if ds.Ratio >= 0.1 {
		t.Errorf("dead ratio = %v, want near zero", ds.Ratio)
	}

	deadEntry, _ := dead.Get("pm-pogo", "sweep")
	if deadEntry.UnackedStreak != fires {
		t.Errorf("dead UnackedStreak = %d, want %d", deadEntry.UnackedStreak, fires)
	}

	// And the events log itself stops lying: the LAST delivery event carries
	// the streak, so the record is self-describing without a join.
	delivered := eventsOfType(t, dead.logPath, "scheduler_fire_delivered")
	if len(delivered) != fires+1 { // +1 for the seed fire
		t.Fatalf("got %d delivered events, want %d", len(delivered), fires+1)
	}
	last := details(t, delivered[len(delivered)-1])
	if last["unacked_streak"] != float64(fires) {
		t.Errorf("last delivered event unacked_streak = %v, want %d", last["unacked_streak"], fires)
	}
	if last["completion_tracked"] != true {
		t.Errorf("last delivered event completion_tracked = %v, want true", last["completion_tracked"])
	}
}

// TestUntrackedScheduleIsUnknownNotFailing pins the guard against the obvious
// false positive: an agent that simply never acks must not be reported as
// stalled. Absent evidence is not evidence of failure.
func TestUntrackedScheduleIsUnknownNotFailing(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	addFiring(t, s, "legacy-agent", "sweep", base)

	for i := 0; i < 10; i++ {
		s.Tick(context.Background(), base.Add(time.Duration(i)*time.Minute))
	}

	stats := s.Completion("", 0)
	if stats.Schedules != 1 {
		t.Fatalf("Schedules = %d, want 1", stats.Schedules)
	}
	if stats.Tracked != 0 {
		t.Errorf("Tracked = %d, want 0 — a never-acking schedule is unknown", stats.Tracked)
	}
	if stats.Stalled != 0 {
		t.Errorf("Stalled = %d, want 0 — never-acked must not read as failing", stats.Stalled)
	}

	// The delivery event must say so explicitly rather than omitting the field
	// ambiguously.
	evs := eventsOfType(t, s.logPath, "scheduler_fire_delivered")
	d := details(t, evs[len(evs)-1])
	if d["completion_tracked"] != false {
		t.Errorf("completion_tracked = %v, want false", d["completion_tracked"])
	}
	if _, ok := d["unacked_streak"]; ok {
		t.Errorf("unacked_streak must be absent for an untracked schedule, got %v", d["unacked_streak"])
	}
}

// TestStaleTokenRejected pins the replay guard: only the outstanding fire's
// token counts. Without this, an agent (or a copy-pasted transcript line) could
// redeem an old token and manufacture a healthy-looking ratio.
func TestStaleTokenRejected(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	addFiring(t, s, "pm-pogo", "sweep", base)

	s.Tick(context.Background(), base)
	first, _ := s.Get("pm-pogo", "sweep")
	oldToken := first.PendingToken

	// Next fire supersedes it.
	s.Tick(context.Background(), base.Add(time.Minute))
	second, _ := s.Get("pm-pogo", "sweep")
	if second.PendingToken == oldToken {
		t.Fatal("second fire reused the first fire's token")
	}

	if _, err := s.Ack("pm-pogo", "sweep", oldToken, base.Add(2*time.Minute)); !errors.Is(err, ErrStaleToken) {
		t.Errorf("acking a superseded token: err = %v, want ErrStaleToken", err)
	}
	got, _ := s.Get("pm-pogo", "sweep")
	if got.FiresCompleted != 0 {
		t.Errorf("FiresCompleted = %d after a stale ack, want 0", got.FiresCompleted)
	}

	// Double-redeeming the live token is likewise rejected.
	if _, err := s.Ack("pm-pogo", "sweep", second.PendingToken, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("first ack of live token: %v", err)
	}
	if _, err := s.Ack("pm-pogo", "sweep", second.PendingToken, base.Add(3*time.Minute)); !errors.Is(err, ErrNoPendingFire) {
		t.Errorf("re-acking: err = %v, want ErrNoPendingFire", err)
	}
	got, _ = s.Get("pm-pogo", "sweep")
	if got.FiresCompleted != 1 {
		t.Errorf("FiresCompleted = %d after a double ack, want 1", got.FiresCompleted)
	}
}

// TestAckStaleWindow pins the age bound: a token older than AckStaleWindow is
// rejected even when no newer fire has superseded it (a one-shot, or a schedule
// with a long cron). Crediting it would attribute completion to work that
// finished a day late.
func TestAckStaleWindow(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	if _, err := s.Add(Entry{
		ID: "wakeup", Agent: "cat-foo", OneShot: true, NextFire: base,
	}, base); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.Tick(context.Background(), base)

	// One-shot entries are deleted after firing, so re-register to keep an
	// entry around whose token is simply old.
	e, err := s.Add(Entry{ID: "slow", Agent: "cat-foo", Cron: "0 0 * * *", NextFire: base}, base)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	_ = e
	s.Tick(context.Background(), base)
	fired, _ := s.Get("cat-foo", "slow")

	if _, err := s.Ack("cat-foo", "slow", fired.PendingToken, base.Add(AckStaleWindow+time.Hour)); !errors.Is(err, ErrStaleToken) {
		t.Errorf("acking past the stale window: err = %v, want ErrStaleToken", err)
	}
	// Just inside the window still works.
	if _, err := s.Ack("cat-foo", "slow", fired.PendingToken, base.Add(AckStaleWindow-time.Hour)); err != nil {
		t.Errorf("acking inside the stale window: %v", err)
	}
}

// TestCountersSurviveRestart pins the denominator's durability. The ratio is
// useless if it resets on the pogod restarts an outage tends to produce, which
// is why the counters live on the persisted Entry rather than in memory.
func TestCountersSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)

	s, err := New(path, &recorder{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addFiring(t, s, "pm-pogo", "sweep", base)
	s.Tick(context.Background(), base)
	e, _ := s.Get("pm-pogo", "sweep")
	if _, err := s.Ack("pm-pogo", "sweep", e.PendingToken, base); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	s.Tick(context.Background(), base.Add(time.Minute))
	s.Tick(context.Background(), base.Add(2*time.Minute))

	// Restart: a fresh Scheduler over the same file.
	s2, err := New(path, &recorder{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := s2.Get("pm-pogo", "sweep")
	if !ok {
		t.Fatal("schedule missing after restart")
	}
	if got.FiresDelivered != 3 || got.FiresCompleted != 1 {
		t.Errorf("after restart counters = %d/%d, want 3/1", got.FiresCompleted, got.FiresDelivered)
	}
	if got.UnackedStreak != 2 {
		t.Errorf("after restart UnackedStreak = %d, want 2", got.UnackedStreak)
	}
	if !got.CompletionTracked() {
		t.Error("CompletionTracked lost across restart")
	}

	// The outstanding token must survive too, or an agent mid-turn when pogod
	// restarted could never ack the fire it is still working on.
	if got.PendingToken == "" {
		t.Error("pending token lost across restart")
	}
	if _, err := s2.Ack("pm-pogo", "sweep", got.PendingToken, base.Add(3*time.Minute)); err != nil {
		t.Errorf("ack after restart: %v", err)
	}
}

// TestFailedDeliveryIssuesNoOutstandingFire pins the separation of the two
// faults. A fire that never reached the agent triggered no turn, so leaving it
// outstanding would blur "the bytes did not arrive" (already reported as
// scheduler_fire_failed) into "the turn accomplished nothing".
func TestFailedDeliveryIssuesNoOutstandingFire(t *testing.T) {
	rec := &recorder{failNth: 1}
	s := newSchedulerForTest(t, rec)
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	addFiring(t, s, "pm-pogo", "sweep", base)

	s.Tick(context.Background(), base)

	e, _ := s.Get("pm-pogo", "sweep")
	if e.PendingToken != "" {
		t.Errorf("PendingToken = %q after a failed delivery, want empty", e.PendingToken)
	}
	if e.FiresDelivered != 0 {
		t.Errorf("FiresDelivered = %d after a failed delivery, want 0", e.FiresDelivered)
	}
	if e.UnackedStreak != 0 {
		t.Errorf("UnackedStreak = %d after a failed delivery, want 0", e.UnackedStreak)
	}
}

// TestSkippedFireIssuesNoToken pins the same for the replay-skip path: an
// elided fire triggers no turn, so crediting it with an outstanding token would
// manufacture a stall out of a policy decision.
func TestSkippedFireIssuesNoToken(t *testing.T) {
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	s.SkipWindow = time.Minute
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	if _, err := s.Add(Entry{
		ID: "poll", Agent: "pm-pogo", Cron: "* * * * *",
		ReplayPolicy: ReplaySkip, NextFire: base,
	}, base); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Fire far past the skip window.
	s.Tick(context.Background(), base.Add(time.Hour))

	if len(rec.snapshot()) != 0 {
		t.Fatal("premise broken: fire was delivered, expected skip")
	}
	e, _ := s.Get("pm-pogo", "poll")
	if e.PendingToken != "" {
		t.Errorf("PendingToken = %q after a skipped fire, want empty", e.PendingToken)
	}
	if e.FiresDelivered != 0 {
		t.Errorf("FiresDelivered = %d after a skipped fire, want 0", e.FiresDelivered)
	}
}

// TestAckResolvesByIDWhenUnambiguous mirrors the GET/DELETE disambiguation
// contract so `pogo schedule ack <id>` behaves like its siblings.
func TestAckResolvesByIDWhenUnambiguous(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	addFiring(t, s, "pm-pogo", "sweep", base)
	s.Tick(context.Background(), base)
	e, _ := s.Get("pm-pogo", "sweep")

	if _, err := s.Ack("", "sweep", e.PendingToken, base); err != nil {
		t.Fatalf("id-only Ack: %v", err)
	}

	// Two owners: the id alone is ambiguous.
	addFiring(t, s, "pm-other", "sweep", base)
	s.Tick(context.Background(), base.Add(time.Minute))
	e2, _ := s.Get("pm-pogo", "sweep")
	if _, err := s.Ack("", "sweep", e2.PendingToken, base.Add(time.Minute)); err == nil {
		t.Error("id-only Ack with two owners: want ambiguity error, got nil")
	}

	if _, err := s.Ack("pm-pogo", "nope", "abcd", base); !errors.Is(err, ErrScheduleNotFound) {
		t.Errorf("unknown id: err = %v, want ErrScheduleNotFound", err)
	}
}

// TestBodyUnchangedWithoutToken pins backward compatibility: an Entry with no
// issued token renders the exact pre-mg-a754 footer, so every doc and prompt
// describing `[scheduler id=... due=... fired=...]` stays accurate.
func TestBodyUnchangedWithoutToken(t *testing.T) {
	at := time.Date(2026, 5, 3, 9, 0, 14, 0, time.UTC)
	e := Entry{ID: "sweep-morning", Agent: "pm-pogo", Message: "sweep",
		NextFire: time.Date(2026, 5, 3, 9, 0, 0, 0, time.UTC)}

	body := buildBody(e, at)
	want := "sweep\n\n[scheduler id=sweep-morning due=2026-05-03T09:00:00Z fired=2026-05-03T09:00:14Z]"
	if body != want {
		t.Errorf("body without a token changed shape:\n got: %q\nwant: %q", body, want)
	}
}

// TestAckHTTP pins the wire contract the `pogo schedule ack` CLI depends on,
// including the status codes: a stale ack is 409 (well-formed, lost a race with
// the next fire), not 400 (a bug in the caller). The caller needs to tell those
// apart to know whether to log quietly or shout.
func TestAckHTTP(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)
	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	// Pin the handler's clock to the fixture instead of the wall clock. Without
	// this the ack path (api.go handleAck) would compare the fire's age against
	// time.Now(), so the fixed `base` above ages past AckStaleWindow a day after
	// this test was written and the 200 assertion below rots into a 409. `base`
	// is now an arbitrary instant, not a live calendar assumption — the test
	// passes on any date it runs (mg-a35b).
	s.SetClock(func() time.Time { return base })
	addFiring(t, s, "pm-pogo", "sweep", base)
	s.Tick(context.Background(), base)
	e, _ := s.Get("pm-pogo", "sweep")

	post := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/scheduler/schedules/"+id+"/ack",
			strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := post("sweep", `{"agent":"pm-pogo","token":"`+e.PendingToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res AckResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode AckResult: %v", err)
	}
	if res.Entry.FiresCompleted != 1 {
		t.Errorf("FiresCompleted = %d, want 1", res.Entry.FiresCompleted)
	}

	if rec := post("sweep", `{"agent":"pm-pogo","token":"deadbeef"}`); rec.Code != http.StatusConflict {
		t.Errorf("stale-token ack status = %d, want 409", rec.Code)
	}
	if rec := post("nope", `{"agent":"pm-pogo","token":"deadbeef"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown-schedule ack status = %d, want 404", rec.Code)
	}
	if rec := post("sweep", `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed ack status = %d, want 400", rec.Code)
	}

	// Completion roll-up over the wire.
	req := httptest.NewRequest("GET", "/scheduler/completion", nil)
	crec := httptest.NewRecorder()
	mux.ServeHTTP(crec, req)
	if crec.Code != http.StatusOK {
		t.Fatalf("completion status = %d", crec.Code)
	}
	var stats CompletionStats
	if err := json.Unmarshal(crec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode CompletionStats: %v", err)
	}
	if stats.Tracked != 1 || stats.FiresCompleted != 1 {
		t.Errorf("stats = %+v, want tracked=1 completed=1", stats)
	}
	if stats.StallThreshold != DefaultStallThreshold {
		t.Errorf("StallThreshold = %d, want %d", stats.StallThreshold, DefaultStallThreshold)
	}
}

// TestAckHTTPFreshnessWindow proves the ack freshness window (AckStaleWindow) is
// enforced against the scheduler's injected clock, not the real calendar date.
// The same fire fixture must ack 200 when "now" sits inside the window and 409
// when "now" is deliberately advanced past it — and both assertions hold on ANY
// date the suite runs, because the handler reads s.now() (which this test pins)
// rather than time.Now(). This is the regression guard for the mg-a35b class:
// the age that trips the 409 is constructed here by moving the clock, never by
// the wall clock drifting past a hardcoded fixture.
func TestAckHTTPFreshnessWindow(t *testing.T) {
	s := newSchedulerForTest(t, &recorder{})
	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	now := base
	s.SetClock(func() time.Time { return now })

	post := func(id, agent, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/scheduler/schedules/"+id+"/ack",
			strings.NewReader(`{"agent":"`+agent+`","token":"`+token+`"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Fire "fresh" at base, ack it one minute later — comfortably inside the
	// 24h window regardless of what today's date is.
	addFiring(t, s, "pm-pogo", "fresh", base)
	s.Tick(context.Background(), base)
	fresh, _ := s.Get("pm-pogo", "fresh")
	now = base.Add(time.Minute)
	if rec := post("fresh", "pm-pogo", fresh.PendingToken); rec.Code != http.StatusOK {
		t.Fatalf("in-window ack = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Fire "stale" at base, then advance the injected clock one minute past
	// AckStaleWindow before acking. The token is valid; only its age is out of
	// bounds, so the handler must return 409 — and it does so because we moved
	// the clock, not because the machine's date changed.
	addFiring(t, s, "pm-pogo", "stale", base)
	s.Tick(context.Background(), base)
	stale, _ := s.Get("pm-pogo", "stale")
	now = base.Add(AckStaleWindow + time.Minute)
	if rec := post("stale", "pm-pogo", stale.PendingToken); rec.Code != http.StatusConflict {
		t.Errorf("aged-past-window ack = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}
