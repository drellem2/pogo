package main

import (
	"errors"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/server"
)

// mg-875d: a condition whose trigger means pogod is unhealthy or the
// coordinator is down must not be addressed ONLY to the coordinator — an agent
// this daemon runs, and in exactly that state not there to read it.

func TestRaise_OutOfBandConditionIsAlsoCopiedToTheBox(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	a.setOutOfBandBox("daniel")
	t0 := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)

	c := testCondition("pogod_log_not_written", "pipe")
	c.OutOfBand = true
	a.Raise(c, t0)
	if len(rec.sent) != 2 || rec.sent[0].to != "mayor" || rec.sent[1].to != "daniel" {
		t.Fatalf("sent %+v, want mayor then daniel", rec.sent)
	}

	// Suppression covers both copies: a second occurrence mails nobody.
	a.Raise(c, t0.Add(time.Minute))
	if len(rec.sent) != 2 {
		t.Fatalf("a suppressed occurrence sent %d mails, want still 2", len(rec.sent))
	}
}

func TestRaise_OrdinaryConditionIsNotCopied(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	a.setOutOfBandBox("daniel")
	a.Raise(testCondition("prompt_refresh_failed", "x"), time.Now())
	if len(rec.sent) != 1 || rec.sent[0].to != "mayor" {
		t.Fatalf("sent %+v, want mayor only — the box is for circular rows, not every row", rec.sent)
	}
}

func TestRaise_OutOfBandBoxEqualToAddresseeMailsOnce(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	a.setOutOfBandBox("mayor")
	c := testCondition("sched", "x")
	c.OutOfBand = true
	a.Raise(c, time.Now())
	if len(rec.sent) != 1 {
		t.Fatalf("sent %d mails to one box, want 1", len(rec.sent))
	}
}

// The remedy's own failure mode: A11 raises every heartbeat tick, so if a
// delivered out-of-band copy were forgotten because the coordinator send
// failed, the box would be re-mailed every 30 seconds.
func TestRaise_DeliveredOutOfBandCopyIsRememberedEvenIfTheCoordinatorSendFailed(t *testing.T) {
	var sent []string
	mail := func(to, from, subject, body string) error {
		if to == "mayor" {
			return errors.New("no_such_mailbox")
		}
		sent = append(sent, to)
		return nil
	}
	a, _ := newTestAnnunciator(t, mail, nil)
	a.setOutOfBandBox("daniel")
	t0 := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	c := testCondition(rowA11HeartbeatWrite, "disk full")
	c.OutOfBand = true
	for i := 0; i < 5; i++ {
		a.Raise(c, t0.Add(time.Duration(i)*30*time.Second))
	}
	if len(sent) != 1 {
		t.Fatalf("out-of-band box mailed %d times over 5 ticks, want 1", len(sent))
	}
}

func TestRaise_EveryAddresseeFailingIsStillRetried(t *testing.T) {
	rec := &condRecorder{err: errors.New("mg not on PATH")}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	a.setOutOfBandBox("daniel")
	c := testCondition("sched", "x")
	c.OutOfBand = true
	t0 := time.Now()
	a.Raise(c, t0)
	rec.err = nil
	a.Raise(c, t0.Add(time.Second))
	if len(rec.sent) != 2 {
		t.Fatalf("after a total failure the next occurrence sent %d, want 2 (both copies)", len(rec.sent))
	}
}

// A store written before mg-875d carries no out_of_band; the first raise after
// the upgrade must reach the box rather than inherit the old quiet window.
func TestRaise_GainingAnOutOfBandBoxReaddresses(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Now()
	c := testCondition("sched", "x")
	c.OutOfBand = true
	a.Raise(c, t0)
	a.setOutOfBandBox("daniel")
	a.Raise(c, t0.Add(time.Minute))
	if len(rec.sent) != 1 {
		t.Fatalf("sent %+v inside the hourly floor, want no re-mail yet", rec.sent)
	}
	a.Raise(c, t0.Add(time.Hour+time.Minute))
	if len(rec.sent) != 3 || rec.sent[2].to != "daniel" {
		t.Fatalf("sent %+v, want the box reached on the first raise past the floor", rec.sent)
	}
}

// The remedy's other failure mode: the coordinator copy lands, the out-of-band
// copy fails. Recording the box as told would lose the one copy these rows
// exist for, silently and for good. It must stay owed and be retried — at the
// hourly floor, not per heartbeat tick.
func TestRaise_FailedOutOfBandCopyStaysOwedAndIsRetriedHourly(t *testing.T) {
	boxDown := true
	var sent []string
	mail := func(to, from, subject, body string) error {
		if to == "daniel" && boxDown {
			return errors.New("no_such_mailbox")
		}
		sent = append(sent, to)
		return nil
	}
	a, _ := newTestAnnunciator(t, mail, nil)
	a.setOutOfBandBox("daniel")
	t0 := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	c := testCondition(rowA11HeartbeatWrite, "disk full")
	c.OutOfBand = true
	for i := 0; i < 10; i++ { // five minutes of 30s ticks
		a.Raise(c, t0.Add(time.Duration(i)*30*time.Second))
	}
	if len(sent) != 1 {
		t.Fatalf("sent %v over 5 minutes with the box refusing, want exactly 1 (mayor) — no per-tick storm", sent)
	}
	boxDown = false
	a.Raise(c, t0.Add(time.Hour+time.Minute))
	if len(sent) != 3 || sent[2] != "daniel" {
		t.Fatalf("sent %v, want the owed out-of-band copy retried past the hourly floor", sent)
	}
	a.Raise(c, t0.Add(time.Hour+2*time.Minute))
	if len(sent) != 3 {
		t.Fatalf("sent %v, want quiet once the box has been told", sent)
	}
}

// The catalogue: which rows are circular. Pinned so a new row has to decide.
func TestCatalogue_OutOfBandRows(t *testing.T) {
	d := logDestination{JobLogPath: "/x", Stderr: "a pipe"}
	cases := []struct {
		name string
		c    pogodCondition
		want bool
	}{
		{"A2 load", conditionSchedulerLoadFailed("mayor", "/s", "x"), true},
		{"A2 no home", conditionSchedulerNoHome("mayor", "x"), true},
		{"A3", conditionAckWatchNotArmed("mayor"), false},
		{"A5 coordinator", conditionAutoStartFailed("mayor", "mayor", "x", true), true},
		{"A5 other", conditionAutoStartFailed("mayor", "pm-pogo", "x", false), false},
		{"A6 coordinator", conditionRestartFailed("mayor", "mayor", "x"), true},
		{"A6 other", conditionRestartFailed("mayor", "pm-pogo", "x"), false},
		{"A11", conditionHeartbeatWriteFailed("mayor", "/h", "x"), true},
		{"A14", conditionLogRotationFailed("mayor", "x"), false},
		{"mg-a19a", conditionLogNotWritten("mayor", d), true},
		{"mg-5af1", conditionOrchestrationLeftStopped("mayor", server.ResumeObligation{}, time.Hour, server.StartReport{}, nil), true},
	}
	for _, tc := range cases {
		if tc.c.OutOfBand != tc.want {
			t.Errorf("%s: OutOfBand = %v, want %v", tc.name, tc.c.OutOfBand, tc.want)
		}
	}
}
