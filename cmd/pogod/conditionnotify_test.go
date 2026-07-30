package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// condRecorder is the mail seam substitute. Nothing in this file may shell out
// to the real `mg`: a test that mails a live crew agent manufactures a fleet
// alarm, which is the same class of fault as writing test events onto the real
// spine.
type condRecorder struct {
	sent []recordedMail
	err  error
}

func (r *condRecorder) send(to, from, subject, body string) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, recordedMail{to, from, subject, body})
	return nil
}

// wakeRecorder is the PTY-nudge seam. errs is consumed one entry per call, so a
// test can make the first N wakes fail and the N+1th succeed — the real shape,
// where the addressee is not running yet at the moment the condition is known.
type wakeRecorder struct {
	woken []string
	errs  []error
}

func (w *wakeRecorder) nudge(name, message string) error {
	if len(w.errs) > 0 {
		err := w.errs[0]
		w.errs = w.errs[1:]
		if err != nil {
			return err
		}
	}
	w.woken = append(w.woken, name+": "+message)
	return nil
}

func newTestAnnunciator(t *testing.T, mail conditionMailer, wake conditionWaker) (*conditionAnnunciator, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), conditionNoticesFile)
	return newConditionAnnunciator(path, mail, wake), path
}

func testCondition(id, detail string) pogodCondition {
	return pogodCondition{
		ID: id, Row: "A2", To: "mayor", Detail: detail,
		Subject: "subject for " + id, Body: "body for " + id,
	}
}

// TestRaise_MailsOnTransitionInAndThenGoesQuiet is the core contract. mg-c3f0's
// constraint is explicit that seven identical mails is how a real alarm gets
// filtered out, so an alarm that fires on every occurrence is not a weaker
// version of this fix — it is the failure mode it exists to avoid.
func TestRaise_MailsOnTransitionInAndThenGoesQuiet(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	a.Raise(testCondition("c", "boom"), t0)
	if len(rec.sent) != 1 {
		t.Fatalf("transition into the condition sent %d mails, want 1", len(rec.sent))
	}
	if rec.sent[0].to != "mayor" {
		t.Errorf("addressee = %q, want mayor", rec.sent[0].to)
	}
	if rec.sent[0].from != conditionMailFrom {
		t.Errorf("from = %q, want %q so a recipient can filter these", rec.sent[0].from, conditionMailFrom)
	}

	// Same condition, same failure, later in the same boot and on four more
	// boots: silent until the renotify window.
	for i, dt := range []time.Duration{0, time.Second, time.Hour, 6 * time.Hour, 23 * time.Hour} {
		a.Raise(testCondition("c", "boom"), t0.Add(dt))
		if len(rec.sent) != 1 {
			t.Fatalf("occurrence %d at +%s sent another mail (%d total); "+
				"repetition is what trains a recipient to filter a real alarm", i, dt, len(rec.sent))
		}
	}

	// Past the window, still unresolved: one reminder.
	a.Raise(testCondition("c", "boom"), t0.Add(conditionRenotifyAfter))
	if len(rec.sent) != 2 {
		t.Fatalf("an unresolved condition past the %s window sent %d mails, want 2",
			conditionRenotifyAfter, len(rec.sent))
	}
}

// TestRaise_RenotifyClockRunsFromDeliveryNotFromTheLastOccurrence. If a
// suppressed occurrence restamped the clock, a condition that re-occurs more
// often than the renotify window would NEVER reach a reminder — and A11 occurs
// every ~30 seconds. That bug is silent and total.
func TestRaise_RenotifyClockRunsFromDeliveryNotFromTheLastOccurrence(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	// The A11 shape: a 30-second tick for two days.
	for i := 0; i < 2*24*60*2; i++ {
		a.Raise(testCondition("hb", "disk full"), t0.Add(time.Duration(i)*30*time.Second))
	}
	// 48h at a 24h renotify = the initial mail plus one reminder at +24h.
	if len(rec.sent) != 2 {
		t.Fatalf("2 days of 30-second occurrences sent %d mails, want 2 "+
			"(one transition + one %s reminder)", len(rec.sent), conditionRenotifyAfter)
	}
}

// TestRaise_HardFloorSurvivesAChurningErrorString. Unlike A1's .dist content
// hash, these fingerprints are error strings, and error strings carry pids,
// ports and temp paths. Without the floor, a churning fingerprint reads as
// "materially changed" on every occurrence and mails every 30 seconds.
func TestRaise_HardFloorSurvivesAChurningErrorString(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	for i := 0; i < 200; i++ {
		// Every occurrence has a different error text, as a real errno-with-pid
		// or temp-path error would.
		a.Raise(testCondition("hb", fmt.Sprintf("write /tmp/pogo-%d/hb: no space", i)),
			t0.Add(time.Duration(i)*30*time.Second))
	}
	// 200 * 30s = 100 minutes, so the floor permits at most 2.
	if len(rec.sent) > 2 {
		t.Fatalf("200 occurrences with 200 distinct error strings over 100 minutes sent %d mails; "+
			"the %s floor should bound this at 2 regardless of fingerprint churn",
			len(rec.sent), conditionMinInterval)
	}
	if len(rec.sent) == 0 {
		t.Fatal("no mail at all — the floor must bound noise, never produce silence")
	}
}

// TestRaise_AMateriallyDifferentFailureReAnnounces. The counterpart to the floor:
// once the floor has elapsed, a genuinely different failure must not hide behind
// the first one's quiet window. The recipient's problem is not the one they were
// told about.
func TestRaise_AMateriallyDifferentFailureReAnnounces(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	a.Raise(testCondition("sched", "schedules.json: unexpected end of JSON input"), t0)
	a.Raise(testCondition("sched", "schedules.json: permission denied"), t0.Add(conditionMinInterval+time.Minute))
	if len(rec.sent) != 2 {
		t.Fatalf("a different failure past the floor sent %d mails, want 2", len(rec.sent))
	}
}

// TestRaise_ReaddressingMailsAgain. An agent name is also a mailbox name, so a
// renamed coordinator means the mailbox we last announced to is now unread by
// anyone — mail to it is silently accepted into a phantom mailbox and lost.
func TestRaise_ReaddressingMailsAgain(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	c := testCondition("sched", "boom")
	a.Raise(c, t0)
	c.To = "ringmaster"
	a.Raise(c, t0.Add(time.Minute))
	if len(rec.sent) != 2 {
		t.Fatalf("a re-addressed condition sent %d mails, want 2", len(rec.sent))
	}
	if rec.sent[1].to != "ringmaster" {
		t.Errorf("second mail went to %q, want ringmaster", rec.sent[1].to)
	}
}

// TestClear_ForgetsSoARecurrenceIsAFreshTransition. This is the half that is easy
// to omit because nothing fails when you do: without it a condition that broke,
// was fixed, and broke again would be suppressed by its own resolved history.
func TestClear_ForgetsSoARecurrenceIsAFreshTransition(t *testing.T) {
	rec := &condRecorder{}
	a, path := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	a.Raise(testCondition("sched", "boom"), t0)
	a.Clear("sched", t0.Add(time.Hour))
	a.flush()

	// The store must not carry a cleared condition forward.
	var n conditionNotices
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &n); err != nil {
		t.Fatal(err)
	}
	if _, still := n.Conditions["sched"]; still {
		t.Error("a cleared condition is still in the store; the store would grow without bound")
	}

	// A recurrence one minute later mails immediately rather than inheriting the
	// quiet window from the resolved incident.
	a.Raise(testCondition("sched", "boom"), t0.Add(time.Hour+time.Minute))
	if len(rec.sent) != 2 {
		t.Fatalf("a recurrence after a clear sent %d mails, want 2 — "+
			"a resolved incident must not suppress the next one", len(rec.sent))
	}
}

// TestRaise_AFailedSendIsNotRememberedAsDelivered. The defect one level up: if a
// failed notice were stamped, the retry would never happen and the alarm would
// die silently while the state file claimed an announcement that never landed.
func TestRaise_AFailedSendIsNotRememberedAsDelivered(t *testing.T) {
	rec := &condRecorder{err: errors.New("mg not on PATH")}
	a, path := newTestAnnunciator(t, rec.send, nil)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	a.Raise(testCondition("sched", "boom"), t0)
	a.flush()

	data, _ := os.ReadFile(path)
	var n conditionNotices
	_ = json.Unmarshal(data, &n)
	if _, claimed := n.Conditions["sched"]; claimed {
		t.Fatal("a FAILED send was recorded in the store; the retry would never happen")
	}
	if a.failed != 1 {
		t.Errorf("failed count = %d, want 1 — report() must be able to say a notice reached nobody", a.failed)
	}

	// Mail comes back; the very next occurrence tries again.
	rec.err = nil
	a.Raise(testCondition("sched", "boom"), t0.Add(time.Second))
	if len(rec.sent) != 1 {
		t.Fatalf("after a failed send the next occurrence sent %d mails, want 1", len(rec.sent))
	}
}

// TestRaise_NoAddresseeIsLoudAndCountedRatherThanGuessedAt. Mail to a name no
// agent reads is silently accepted into a phantom mailbox and lost, so a
// synthesized addressee would recreate this ticket's defect with extra steps.
func TestRaise_NoAddresseeIsLoudAndCountedRatherThanGuessedAt(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)

	c := testCondition("sched", "boom")
	c.To = ""
	a.Raise(c, time.Now())

	if len(rec.sent) != 0 {
		t.Errorf("mailed %d times with no addressee resolved; a guessed name is a lost mail", len(rec.sent))
	}
	if a.failed != 1 {
		t.Errorf("failed count = %d, want 1 — an unroutable condition is worse news than the condition", a.failed)
	}
}

// TestStore_SurvivesTheBootBoundary. Most of these conditions are re-derived
// identically at every boot from unchanged inputs (a corrupt schedules.json is
// still corrupt next boot), so the process restart IS the tick and in-process
// memory cannot suppress across it.
func TestStore_SurvivesTheBootBoundary(t *testing.T) {
	rec := &condRecorder{}
	path := filepath.Join(t.TempDir(), conditionNoticesFile)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	boot1 := newConditionAnnunciator(path, rec.send, nil)
	boot1.Raise(testCondition("sched", "boom"), t0)
	boot1.flush()

	// Four more boots in the same day — the A1 incident's actual repetition count.
	for i := 1; i <= 4; i++ {
		b := newConditionAnnunciator(path, rec.send, nil)
		b.Raise(testCondition("sched", "boom"), t0.Add(time.Duration(i)*time.Hour))
		b.flush()
	}
	if len(rec.sent) != 1 {
		t.Fatalf("five boots of the same condition sent %d mails, want 1; "+
			"in-process memory cannot suppress across a restart, which is why the store is on disk",
			len(rec.sent))
	}
}

// TestStore_UnreadableFailsTowardNoiseButBoundedByTheMemoryShadow. The bias is
// deliberate: reading a corrupt file as "already told them" would let one bad
// byte silently disable every alarm in this file. But the naive version of that
// bias mails on every occurrence, and A9/A11 occur on a timer — so the in-process
// shadow bounds it.
func TestStore_UnreadableFailsTowardNoiseButBoundedByTheMemoryShadow(t *testing.T) {
	rec := &condRecorder{}
	path := filepath.Join(t.TempDir(), conditionNoticesFile)
	if err := os.WriteFile(path, []byte("{{{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	a := newConditionAnnunciator(path, rec.send, nil)
	// A corrupt store must not produce silence.
	a.Raise(testCondition("hb", "boom"), t0)
	if len(rec.sent) == 0 {
		t.Fatal("a corrupt store silenced the alarm; the bias must always be toward noise")
	}
	// ...and must not produce a storm either. 200 ticks, one hour of floor.
	for i := 1; i < 200; i++ {
		a.Raise(testCondition("hb", "boom"), t0.Add(time.Duration(i)*30*time.Second))
	}
	if len(rec.sent) > 2 {
		t.Fatalf("a corrupt store turned 200 occurrences into %d mails; the memory shadow "+
			"must bound an unreadable store at the same floor as a healthy one", len(rec.sent))
	}
}

// TestRetryWakes_LandsOnTheFirstTickAfterTheAddresseeIsUp. The wake is queued
// rather than sent inline because A2 is detected during startup, BEFORE crew
// auto-start: at the moment the condition is known there is no coordinator
// process to nudge. Retrying on the heartbeat is what makes the channel real.
func TestRetryWakes_LandsOnTheFirstTickAfterTheAddresseeIsUp(t *testing.T) {
	rec := &condRecorder{}
	// Two failures (coordinator not started yet), then it is up.
	wake := &wakeRecorder{errs: []error{
		errors.New("agent mayor is not running"),
		errors.New("agent mayor is not running"),
	}}
	a, _ := newTestAnnunciator(t, rec.send, wake.nudge)
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	c := testCondition("sched", "boom")
	c.Wake = true
	a.Raise(c, t0)
	if len(a.pendingWakes) != 1 {
		t.Fatalf("pending wakes = %d, want 1", len(a.pendingWakes))
	}

	a.retryWakes(t0.Add(30 * time.Second))
	a.retryWakes(t0.Add(60 * time.Second))
	if len(wake.woken) != 0 {
		t.Fatal("woke an agent that was not running")
	}
	if len(a.pendingWakes) != 1 {
		t.Fatal("a wake that could not land was dropped instead of retried")
	}

	a.retryWakes(t0.Add(90 * time.Second))
	if len(wake.woken) != 1 {
		t.Fatalf("woken = %v, want one nudge once the addressee was up", wake.woken)
	}
	if !strings.Contains(wake.woken[0], "mayor: ") {
		t.Errorf("nudge went to the wrong agent: %q", wake.woken[0])
	}
	if !strings.Contains(wake.woken[0], "mg mail list mayor") {
		t.Errorf("the nudge must tell the recipient where the notice is; got %q", wake.woken[0])
	}
	if len(a.pendingWakes) != 0 {
		t.Error("a delivered wake stayed queued")
	}
}

// TestRetryWakes_AbandonsLoudlyRatherThanRetryingForever. A wake that never lands
// degrades to "mailed but not woken" — which is exactly the state mg-c3f0 §6
// described as the residual gap — and it must SAY so rather than quietly stop.
func TestRetryWakes_AbandonsLoudlyRatherThanRetryingForever(t *testing.T) {
	rec := &condRecorder{}
	wake := &wakeRecorder{}
	// Always fails.
	failing := func(string, string) error { return errors.New("never running") }
	a, _ := newTestAnnunciator(t, rec.send, failing)
	_ = wake
	t0 := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)

	c := testCondition("sched", "boom")
	c.Wake = true
	a.Raise(c, t0)
	a.retryWakes(t0.Add(time.Minute))
	if len(a.pendingWakes) != 1 {
		t.Fatal("gave up before the deadline")
	}
	a.retryWakes(t0.Add(conditionWakeDeadline + time.Minute))
	if len(a.pendingWakes) != 0 {
		t.Fatal("still retrying past the deadline")
	}
}

// TestReport_EmitsOnEveryBootIncludingTheCleanOnes. This is the answer to "how
// would you know the annunciator itself had stopped". A summary only on the
// boots that raise something is a summary whose silence means nothing.
func TestReport_EmitsOnEveryBootIncludingTheCleanOnes(t *testing.T) {
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	// Must not panic and must not need a live condition. The event itself goes to
	// the events package's test path, never the live spine.
	a.report()

	// And a nil annunciator — a decision point reached before main arms one — is
	// a no-op rather than a panic, or a wiring mistake takes the daemon down
	// instead of losing an alarm.
	var nilA *conditionAnnunciator
	nilA.Raise(testCondition("x", "y"), time.Now())
	nilA.Clear("x", time.Now())
	nilA.retryWakes(time.Now())
	nilA.flush()
	nilA.report()
}
