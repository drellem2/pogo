package heartwatch

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

type sentMail struct{ to, from, subject, body string }

type recorder struct {
	mu     sync.Mutex
	mails  []sentMail
	events []events.Event
	err    error
}

func (r *recorder) mail(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mails = append(r.mails, sentMail{to, from, subject, body})
	return r.err
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) to() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.mails))
	for _, m := range r.mails {
		out = append(out, m.to)
	}
	return out
}

func (r *recorder) typed(t string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.events {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

// report builds a Report with the given rows, filling in the counters the
// watcher reads.
func report(now time.Time, rows ...State) Report {
	rep := Report{
		Now:          now,
		Examined:     len(rows),
		StallAfter:   DefaultStallAfter.String(),
		RestartAfter: DefaultRestartAfter.String(),
		Agents:       rows,
	}
	for _, s := range rows {
		if s.Verdict.Finding() {
			rep.Findings++
		} else {
			rep.Fresh++
		}
	}
	return rep
}

func stale(name string, age time.Duration, now time.Time) State {
	return State{
		Agent: name, Type: "crew", Verdict: VerdictRestartDue,
		Last: now.Add(-age), AgeSecs: age.Seconds(), Path: "/fixture/" + name + "/sweep.log",
	}
}

func fresh(name string, now time.Time) State {
	return State{
		Agent: name, Type: "crew", Verdict: VerdictFresh,
		Last: now.Add(-time.Minute), AgeSecs: 60, Path: "/fixture/" + name + "/sweep.log",
	}
}

func newWatcher(r *recorder, scan ScanFunc) *Watcher {
	return New(Options{
		Enabled:     true,
		Scan:        scan,
		Mail:        r.mail,
		Emit:        r.emit,
		Interval:    time.Nanosecond,
		HoldDown:    -1,
		Coordinator: "mayor",
		HumanBox:    "human",
	})
}

// TestCoordinatorFindingNeverReachesTheCoordinator is this package's one real
// idea, and the build must fail if it inverts — because mailing the coordinator
// is the correct default for every other detector in this tree, so the
// inversion would read as working code.
func TestCoordinatorFindingNeverReachesTheCoordinator(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Report, error) {
		return report(now, stale("mayor", 14*24*time.Hour, now), stale("pm-riemann", 14*24*time.Hour, now)), nil
	})
	w.Check(now)

	got := r.to()
	for _, to := range got {
		if to == "mayor" {
			t.Fatalf("a finding naming the coordinator was mailed TO the coordinator (recipients %v). "+
				"That message arrives only if the claim is false", got)
		}
	}
	if len(got) != 1 || got[0] != "human" {
		t.Fatalf("recipients = %v, want [human]", got)
	}
	body := r.mails[0].body
	if !strings.Contains(body, "<- the coordinator") {
		t.Error("notice does not mark the coordinator row")
	}
	if !strings.Contains(strings.ToUpper(body), "WHY THIS CAME TO HUMAN") {
		t.Error("notice does not explain why it was routed past the coordinator")
	}
	// The coordinator sorts first: it is the row a skimming reader must see.
	if idx := strings.Index(body, "mayor"); idx == -1 || idx > strings.Index(body, "pm-riemann") {
		t.Error("the coordinator row must sort first in the notice")
	}
}

// TestNonCoordinatorFindingGoesToTheCoordinator — the ordinary case, and the
// other half of the routing control. A rule that sent everything to the human
// box would also pass the test above.
func TestNonCoordinatorFindingGoesToTheCoordinator(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Report, error) {
		return report(now, stale("pm-onethird", 14*24*time.Hour, now), fresh("mayor", now)), nil
	})
	w.Check(now)

	if got := r.to(); len(got) != 1 || got[0] != "mayor" {
		t.Fatalf("recipients = %v, want [mayor]", got)
	}
	if body := r.mails[0].body; strings.Contains(strings.ToUpper(body), "WHY THIS CAME TO") {
		t.Error("the routing explanation belongs only on a notice routed past the coordinator")
	}
}

// TestZeroExaminedIsAnInstrumentFailureNotAClear. Zero examined produces zero
// findings, which is the exact shape of green that hid the outage.
func TestZeroExaminedIsAnInstrumentFailureNotAClear(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Report, error) { return report(now), nil })
	w.Check(now)

	if len(r.mails) != 0 {
		t.Errorf("mailed %d notice(s) on an empty population; want none", len(r.mails))
	}
	errs := r.typed(EventError)
	if len(errs) != 1 {
		t.Fatalf("heart_watch_error events = %d, want 1 — an empty population must be loud", len(errs))
	}
	if msg, _ := errs[0].Details["error"].(string); !strings.Contains(msg, "0 agents examined") {
		t.Errorf("error detail = %q, want it to name the empty population", msg)
	}
}

// TestZeroExaminedDoesNotCloseAnOpenEpisode. The dangerous version of the bug
// above is not the missing alarm, it is the false all-clear.
func TestZeroExaminedDoesNotCloseAnOpenEpisode(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	empty := false
	w := newWatcher(r, func(time.Time) (Report, error) {
		if empty {
			return report(now), nil
		}
		return report(now, stale("pm-riemann", 14*24*time.Hour, now)), nil
	})
	w.Check(now)
	if len(r.mails) != 1 {
		t.Fatalf("setup: mails = %d, want 1", len(r.mails))
	}
	empty = true
	w.Check(now.Add(time.Second))

	if len(r.typed(EventClear)) != 0 {
		t.Error("an empty population emitted a clear; it must not close an open episode")
	}
	for _, m := range r.mails[1:] {
		if strings.Contains(m.subject, "fresh again") {
			t.Error("an empty population mailed an all-clear")
		}
	}
}

// TestWakeSuppressionHoldsTheAnnouncementAndSaysSo.
func TestWakeSuppressionHoldsTheAnnouncementAndSaysSo(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Report, error) {
		rep := report(now, stale("pm-riemann", 5*time.Hour, now))
		rep.WakeSuppressed = true
		rep.WokeAt = now.Add(-2 * time.Minute)
		return rep, nil
	})
	w.Check(now)

	if len(r.mails) != 0 {
		t.Errorf("mailed during wake suppression; want none")
	}
	skipped := r.typed(EventSkipped)
	if len(skipped) != 1 {
		t.Fatalf("heart_watch_skipped events = %d, want 1 — a suppressed sample must still be visible", len(skipped))
	}
	if skipped[0].Details["findings"] != 1 {
		t.Errorf("skipped event lost the finding count: %v", skipped[0].Details)
	}
}

// TestScanErrorIsLoudAndMailsNothing.
func TestScanErrorIsLoudAndMailsNothing(t *testing.T) {
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Report, error) { return Report{}, errors.New("registry unreachable") })
	w.Check(time.Now())

	if len(r.mails) != 0 {
		t.Error("mailed on a failed scan")
	}
	if len(r.typed(EventError)) != 1 {
		t.Error("a failed scan must emit heart_watch_error, so a blind detector stays distinguishable from a quiet one")
	}
}

// TestHoldDownAbsorbsOneMissedCheck.
func TestHoldDownAbsorbsOneMissedCheck(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: 10 * time.Minute,
		Coordinator: "mayor", HumanBox: "human",
		Scan: func(time.Time) (Report, error) {
			return report(now, stale("pm-riemann", 3*time.Hour, now)), nil
		},
	})
	w.Check(now)
	if len(r.mails) != 0 {
		t.Fatalf("mailed inside the hold-down; want none")
	}
	w.Check(now.Add(11 * time.Minute))
	if len(r.mails) != 1 {
		t.Fatalf("mails after the hold-down = %d, want 1", len(r.mails))
	}
}

// TestRecoveryClearsAndTellsEveryoneWhoWasAlarmed. A clear that goes to fewer
// mailboxes than the alarm leaves someone holding an open incident forever.
func TestRecoveryClearsAndTellsEveryoneWhoWasAlarmed(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	red := true
	w := newWatcher(r, func(time.Time) (Report, error) {
		if red {
			return report(now, stale("mayor", 14*24*time.Hour, now)), nil
		}
		return report(now, fresh("mayor", now)), nil
	})
	w.Check(now)
	if got := r.to(); len(got) != 1 || got[0] != "human" {
		t.Fatalf("setup recipients = %v, want [human]", got)
	}
	red = false
	w.Check(now.Add(time.Second))

	if len(r.typed(EventClear)) != 1 {
		t.Fatalf("heart_watch_clear events = %d, want 1", len(r.typed(EventClear)))
	}
	clears := map[string]bool{}
	for _, m := range r.mails[1:] {
		clears[m.to] = true
	}
	if !clears["human"] {
		t.Error("the all-clear did not reach the human box, which was the one alarmed")
	}
	if !clears["mayor"] {
		t.Error("the all-clear did not reach the coordinator, which may have been asked to act")
	}
}

// TestNoEpisodeMeansNoClearMail — a fleet that was never red stays quiet.
func TestNoEpisodeMeansNoClearMail(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Report, error) { return report(now, fresh("mayor", now)), nil })
	w.Check(now)
	w.Check(now.Add(time.Second))
	if len(r.mails) != 0 {
		t.Errorf("mailed %d notice(s) over a fresh fleet; want none", len(r.mails))
	}
}

// TestUnchangedRosterStaysQuietUntilRenotify.
func TestUnchangedRosterStaysQuietUntilRenotify(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: -1, RenotifyAfter: time.Hour,
		Coordinator: "mayor", HumanBox: "human",
		Scan: func(time.Time) (Report, error) {
			return report(now, stale("pm-riemann", 3*time.Hour, now)), nil
		},
	})
	w.Check(now)
	w.Check(now.Add(time.Minute))
	if len(r.mails) != 1 {
		t.Fatalf("mails = %d, want 1 — an unchanged roster must not re-mail", len(r.mails))
	}
	w.Check(now.Add(2 * time.Hour))
	if len(r.mails) != 2 {
		t.Fatalf("mails after renotify = %d, want 2", len(r.mails))
	}
}

// TestMailFailureIsRecorded. The fault was detected and could not be reported;
// that is this ticket's bug one level up, so it must not be silent.
func TestMailFailureIsRecorded(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{err: errors.New("no such mailbox")}
	w := newWatcher(r, func(time.Time) (Report, error) {
		return report(now, stale("pm-riemann", 3*time.Hour, now)), nil
	})
	w.Check(now)
	errs := r.typed(EventError)
	if len(errs) != 1 {
		t.Fatalf("heart_watch_error events = %d, want 1", len(errs))
	}
	if errs[0].Details["to"] != "mayor" {
		t.Errorf("error event does not name the mailbox that refused: %v", errs[0].Details)
	}
}

// TestReportOnly: the Watcher has no seam through which it could act. A stale
// heartbeat has two causes that take opposite responses, and pogod distinguishes
// them elsewhere.
func TestReportOnly(t *testing.T) {
	// Options carries only Scan, Mail and Emit as behaviour. If a nudge or
	// restart hook is ever added, this test is where the decision gets made
	// deliberately rather than by a field appearing.
	var o Options
	o.Scan = nil
	o.Mail = nil
	o.Emit = nil
	_ = o
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Report, error) {
		return report(now, stale("pm-riemann", 14*24*time.Hour, now)), nil
	})
	w.Check(now)
	if !strings.Contains(r.mails[0].body, "REPORT-ONLY") {
		t.Error("the notice does not say that nothing was nudged, restarted or stopped")
	}
}

// TestNilWatcherIsSafe — pogod holds a nil *Watcher when the detector is off.
func TestNilWatcherIsSafe(t *testing.T) {
	var w *Watcher
	w.Check(time.Now())
}

// TestNoAllClearWhileAnotherAgentIsInsideItsHoldDown.
//
// This is the remedy committing the defect it remedies, and it is the reason
// the clear is gated on the READING rather than on this sample's confirmed set.
// One agent recovers while another goes late in the same interval: `confirmed`
// is empty because the new one is still inside its hold-down, and a naive clear
// mails "every heartbeat fresh again" over a fleet with a stale heartbeat in it.
func TestNoAllClearWhileAnotherAgentIsInsideItsHoldDown(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	phase := 0
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: 10 * time.Minute,
		Coordinator: "mayor", HumanBox: "human",
		Scan: func(at time.Time) (Report, error) {
			switch phase {
			case 0: // A is red and has been long enough to announce.
				return report(at, stale("pm-a", 3*time.Hour, at)), nil
			default: // A recovered; B has just gone red — inside its hold-down.
				return report(at, fresh("pm-a", at), stale("pm-b", 3*time.Hour, at)), nil
			}
		},
	})
	w.Check(now)
	w.Check(now.Add(11 * time.Minute))
	if len(r.mails) != 1 {
		t.Fatalf("setup: mails = %d, want 1 (pm-a announced after its hold-down)", len(r.mails))
	}

	phase = 1
	w.Check(now.Add(12 * time.Minute))

	for _, m := range r.mails[1:] {
		if strings.Contains(m.subject, "fresh again") {
			t.Fatalf("mailed an all-clear while pm-b was stale and inside its hold-down: %q", m.subject)
		}
	}
	if len(r.typed(EventClear)) != 0 {
		t.Error("emitted heart_watch_clear over a reading that still carried a finding")
	}

	// And the clear DOES arrive once the reading is genuinely clean — without
	// this half, a watcher that never cleared would also pass.
	phase = 2
	w2Scan := func(at time.Time) (Report, error) { return report(at, fresh("pm-a", at), fresh("pm-b", at)), nil }
	w.scan = w2Scan
	w.Check(now.Add(13 * time.Minute))
	if len(r.typed(EventClear)) != 1 {
		t.Errorf("heart_watch_clear events = %d over a genuinely clean reading, want 1", len(r.typed(EventClear)))
	}
}
