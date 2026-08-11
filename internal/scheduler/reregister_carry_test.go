package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// The ack destroyed by the boot path (mg-3cbb).
//
// Every crew agent re-registers its schedules on startup by procedure. Before
// this, that re-registration retracted any fire the agent was still holding, so
// an agent that came back, did the work of a catch-up fire, and ran the exact
// ack command the fire handed it was refused — and the record it left was
// byte-identical to an agent that received the fire and died.

func carryScheduler(t *testing.T) *Scheduler {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "schedules.json"), DelivererFunc(
		func(ctx context.Context, entry Entry, fireTime time.Time) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func mailCheck(agent string) Entry {
	return Entry{
		Agent: agent, ID: "mail-check-" + agent, Cron: "*/10 * * * *",
		Delivery: DeliveryNudge, Message: "check your mail",
	}
}

// TestReregistration_KeepsAnOutstandingFireRedeemable is the live case pm-pogo
// demonstrated on 2026-08-11: boot, re-register, then ack the fire that was
// waiting through the outage.
func TestReregistration_KeepsAnOutstandingFireRedeemable(t *testing.T) {
	s := carryScheduler(t)
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)

	if _, err := s.Add(mailCheck("pm-pogo"), now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// A fire is delivered and goes unacked while the agent is dark.
	fired := now.Add(10 * time.Minute)
	s.Tick(context.Background(), fired)
	e, ok := s.Get("pm-pogo", "mail-check-pm-pogo")
	if !ok || e.PendingToken == "" {
		t.Fatalf("no fire outstanding after Tick; entry=%+v ok=%v", e, ok)
	}
	token := e.PendingToken

	// The agent comes back and re-registers with the same --id, exactly as the
	// startup procedure instructs.
	if _, err := s.Add(mailCheck("pm-pogo"), fired.Add(time.Hour)); err != nil {
		t.Fatalf("re-Add: %v", err)
	}

	// It then works the fire and acks it with the token the fire handed it.
	res, err := s.Ack("pm-pogo", "mail-check-pm-pogo", token, fired.Add(time.Hour+time.Minute))
	if err != nil {
		t.Fatalf("Ack after re-registration: %v — the boot path retracted a fire the agent was holding", err)
	}
	if res.Entry.FiresCompleted != 1 {
		t.Errorf("FiresCompleted = %d, want 1", res.Entry.FiresCompleted)
	}
	if res.Entry.FiresDelivered != 1 {
		t.Errorf("FiresDelivered = %d, want 1 — the carried fire was delivered under this same (agent, id), and crediting a completion against a zeroed denominator reads 1/0",
			res.Entry.FiresDelivered)
	}
	if res.Entry.UnackedStreak != 0 {
		t.Errorf("UnackedStreak = %d, want 0 after an accepted ack", res.Entry.UnackedStreak)
	}
}

// TestReregistration_StillZeroesTheLifetimeCounters. internal/ackwatch depends
// on the reset: it treats a re-registered schedule as unrepresentative for a
// settle window rather than as a finding, and a preserved ratio would mix fires
// from before and after a cadence change and describe no single regime. The
// carry-over restores redeemability ONLY.
func TestReregistration_StillZeroesTheLifetimeCounters(t *testing.T) {
	s := carryScheduler(t)
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)
	if _, err := s.Add(mailCheck("architect"), now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Five delivered-and-acked cycles.
	at := now
	for i := 0; i < 5; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
		e, _ := s.Get("architect", "mail-check-architect")
		if _, err := s.Ack("architect", "mail-check-architect", e.PendingToken, at.Add(time.Second)); err != nil {
			t.Fatalf("Ack %d: %v", i, err)
		}
	}
	before, _ := s.Get("architect", "mail-check-architect")
	if before.FiresDelivered != 5 || before.FiresCompleted != 5 {
		t.Fatalf("setup: %d/%d, want 5/5", before.FiresCompleted, before.FiresDelivered)
	}

	if _, err := s.Add(mailCheck("architect"), at.Add(time.Hour)); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	after, _ := s.Get("architect", "mail-check-architect")
	if after.FiresDelivered != 0 || after.FiresCompleted != 0 {
		t.Errorf("counters after re-registration = %d/%d, want 0/0 — ackwatch reads the reset as a known-benign event and a preserved lifetime ratio would break that",
			after.FiresCompleted, after.FiresDelivered)
	}
	if after.PendingToken != "" {
		t.Errorf("PendingToken = %q carried across a re-registration with nothing outstanding (the last fire was acked)", after.PendingToken)
	}
}

// TestReregistration_DoesNotCarryAStaleToken. A token past AckStaleWindow would
// be refused by Ack anyway; carrying it would only move the refusal later.
func TestReregistration_DoesNotCarryAStaleToken(t *testing.T) {
	s := carryScheduler(t)
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)
	if _, err := s.Add(mailCheck("pa"), now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	fired := now.Add(10 * time.Minute)
	s.Tick(context.Background(), fired)
	e, _ := s.Get("pa", "mail-check-pa")
	token := e.PendingToken

	if _, err := s.Add(mailCheck("pa"), fired.Add(AckStaleWindow+time.Hour)); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	after, _ := s.Get("pa", "mail-check-pa")
	if after.PendingToken != "" {
		t.Errorf("PendingToken = %q, want empty for a token already past the %s window", after.PendingToken, AckStaleWindow)
	}
	if _, err := s.Ack("pa", "mail-check-pa", token, fired.Add(AckStaleWindow+2*time.Hour)); !errors.Is(err, ErrNoPendingFire) {
		t.Errorf("Ack err = %v, want ErrNoPendingFire", err)
	}
}

// TestReregistration_SupersessionStillWins. Carrying the outstanding fire must
// not make a token immortal: the next fire supersedes it exactly as before, so
// a late ack for fire N-1 cannot mask that fire N also went unanswered.
func TestReregistration_SupersessionStillWins(t *testing.T) {
	s := carryScheduler(t)
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)
	if _, err := s.Add(mailCheck("mayor"), now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	first := now.Add(10 * time.Minute)
	s.Tick(context.Background(), first)
	e, _ := s.Get("mayor", "mail-check-mayor")
	oldToken := e.PendingToken

	if _, err := s.Add(mailCheck("mayor"), first.Add(time.Minute)); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	// A second fire arrives before the agent acks the carried one.
	s.Tick(context.Background(), first.Add(10*time.Minute))

	if _, err := s.Ack("mayor", "mail-check-mayor", oldToken, first.Add(11*time.Minute)); !errors.Is(err, ErrStaleToken) {
		t.Errorf("Ack with the superseded token err = %v, want ErrStaleToken", err)
	}
}
