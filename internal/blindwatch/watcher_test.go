package blindwatch

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/wedgewatch"
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

func newWatcher(r *recorder, src SourceFunc) *Watcher {
	return New(Options{
		Enabled: true, Source: src, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: -1,
		Coordinator: "mayor", HumanBox: "human",
	})
}

// TestStandingBlindnessIsReported is the condition the ticket measured: 2609
// declines to judge over 18 days, into a channel with no consumer.
func TestStandingBlindnessIsReported(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Snapshot, error) {
		return Snapshot{
			Detector: "wedge-watch", SampledAt: now.Add(-time.Minute), Examined: 5,
			Blind: []Target{{
				Name:  "pm-riemann",
				Why:   "no work counter and no event-log fallback; the agent could NOT be judged",
				Since: now.Add(-18 * 24 * time.Hour),
			}},
		}, nil
	})
	w.Check(now)

	if len(r.mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(r.mails))
	}
	if got := r.to(); got[0] != "mayor" {
		t.Errorf("recipient = %q, want mayor — a blind agent that is not the coordinator is the coordinator's to chase", got[0])
	}
	body := r.mails[0].body
	if !strings.Contains(body, "pm-riemann") {
		t.Error("notice does not name the unjudgeable agent")
	}
	if !strings.Contains(body, "not the same as") {
		t.Error("notice does not say that could-not-judge is not health")
	}
	if fs := r.typed(EventFinding); len(fs) != 1 {
		t.Fatalf("blind_watch_finding events = %d, want 1", len(fs))
	}
}

// TestAStoppedDetectorIsItsOwnFinding. wedgewatch emits nothing on a clean
// pass, so "no wedge_watch_error" and "no detector" are the same reading in the
// event log. This arm is the only thing separating them.
func TestAStoppedDetectorIsItsOwnFinding(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: -1, StaleAfter: time.Hour,
		Coordinator: "mayor", HumanBox: "human",
		Source: func(time.Time) (Snapshot, error) {
			// Nobody blind, nothing wrong — and no sample for six hours.
			return Snapshot{Detector: "wedge-watch", SampledAt: now.Add(-6 * time.Hour), Examined: 5}, nil
		},
	})
	w.Check(now)

	if len(r.mails) != 1 {
		t.Fatalf("mails = %d, want 1 — an empty blind set over a stopped detector must not read clean", len(r.mails))
	}
	if got := r.to(); got[0] != "human" {
		t.Errorf("recipient = %q, want human — a broken instrument is not the coordinator's task", got[0])
	}
	if !strings.Contains(strings.ToUpper(r.mails[0].subject), "STOPPED JUDGING") {
		t.Errorf("subject = %q, want it to name the stopped detector", r.mails[0].subject)
	}
}

// TestNeverSampledIsStopped, not clean.
func TestNeverSampledIsStopped(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: -1, StaleAfter: time.Hour,
		Coordinator: "mayor", HumanBox: "human",
		Source: func(time.Time) (Snapshot, error) { return Snapshot{Detector: "wedge-watch"}, nil },
	})
	w.Check(now)
	if len(r.mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(r.mails))
	}
	if !strings.Contains(r.mails[0].body, "never") {
		t.Error("notice does not say the detector has never completed a sample")
	}
}

// TestZeroPopulationIsItsOwnFinding. Zero examined yields zero blind, which is
// the shape of green.
func TestZeroPopulationIsItsOwnFinding(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Snapshot, error) {
		return Snapshot{Detector: "wedge-watch", SampledAt: now.Add(-time.Minute), Examined: 0}, nil
	})
	w.Check(now)
	if len(r.mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(r.mails))
	}
	if got := r.to(); got[0] != "human" {
		t.Errorf("recipient = %q, want human", got[0])
	}
	if !strings.Contains(r.mails[0].subject, "ZERO") {
		t.Errorf("subject = %q, want it to name the empty population", r.mails[0].subject)
	}
}

// TestStoppedSubsumesTheOtherTwo. A detector that is not sampling has an empty
// blind set and a zero population FOR THE SAME REASON; reporting all three
// would be one fault wearing three labels.
func TestStoppedSubsumesTheOtherTwo(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: -1, StaleAfter: time.Hour,
		Coordinator: "mayor", HumanBox: "human",
		Source: func(time.Time) (Snapshot, error) {
			return Snapshot{
				Detector: "wedge-watch", SampledAt: now.Add(-9 * time.Hour), Examined: 0,
				Blind: []Target{{Name: "pm-riemann", Since: now.Add(-9 * time.Hour)}},
			}, nil
		},
	})
	w.Check(now)
	if len(r.mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(r.mails))
	}
	body := r.mails[0].body
	if strings.Contains(body, "population of ZERO") {
		t.Error("a stopped detector also reported EMPTY; that is one fault wearing two labels")
	}
	if strings.Contains(body, "pm-riemann") {
		t.Error("a stopped detector also reported a blind agent; an agent it stopped looking at is unlooked-at, not unjudgeable")
	}
}

// TestCoordinatorBlindnessEscalates.
func TestCoordinatorBlindnessEscalates(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Snapshot, error) {
		return Snapshot{
			Detector: "wedge-watch", SampledAt: now.Add(-time.Minute), Examined: 5,
			Blind: []Target{{Name: "mayor", Since: now.Add(-6 * time.Hour)}},
		}, nil
	})
	w.Check(now)
	for _, to := range r.to() {
		if to == "mayor" {
			t.Fatalf("a finding naming the coordinator was mailed to the coordinator (%v)", r.to())
		}
	}
	if got := r.to(); len(got) != 1 || got[0] != "human" {
		t.Fatalf("recipients = %v, want [human]", got)
	}
	if !strings.Contains(r.mails[0].body, "<- the coordinator") {
		t.Error("notice does not mark the coordinator row")
	}
}

// TestFlickerDoesNotAccumulate. Blindness is expected to flicker; the condition
// worth reporting is the standing one.
func TestFlickerDoesNotAccumulate(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	blind := true
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: 2 * time.Hour,
		Coordinator: "mayor", HumanBox: "human",
		Source: func(at time.Time) (Snapshot, error) {
			s := Snapshot{Detector: "wedge-watch", SampledAt: at, Examined: 5}
			if blind {
				s.Blind = []Target{{Name: "pm-riemann"}}
			}
			return s, nil
		},
	})
	w.Check(now)
	blind = false
	w.Check(now.Add(time.Hour)) // sight regained: the clock must reset
	blind = true
	w.Check(now.Add(90 * time.Minute))
	if len(r.mails) != 0 {
		t.Fatalf("mails = %d, want 0 — a flap must restart the hold-down, not accumulate toward it", len(r.mails))
	}
	w.Check(now.Add(4 * time.Hour))
	if len(r.mails) != 1 {
		t.Fatalf("mails after a standing blindness = %d, want 1", len(r.mails))
	}
}

// TestRecoveryClears.
func TestRecoveryClears(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	blind := true
	w := newWatcher(r, func(at time.Time) (Snapshot, error) {
		s := Snapshot{Detector: "wedge-watch", SampledAt: at, Examined: 5}
		if blind {
			s.Blind = []Target{{Name: "mayor", Since: now.Add(-6 * time.Hour)}}
		}
		return s, nil
	})
	w.Check(now)
	blind = false
	w.Check(now.Add(time.Second))

	if len(r.typed(EventClear)) != 1 {
		t.Fatalf("blind_watch_clear events = %d, want 1", len(r.typed(EventClear)))
	}
	got := map[string]bool{}
	for _, m := range r.mails[1:] {
		got[m.to] = true
	}
	if !got["human"] {
		t.Error("the all-clear did not reach the human box, which was the one alarmed")
	}
	// The clear says nothing about fleet health — only that the instrument answers.
	for _, m := range r.mails[1:] {
		if !strings.Contains(m.body, "Nothing about the fleet's HEALTH is") {
			t.Error("the all-clear overstates what a working instrument proves")
		}
	}
}

// TestSourceErrorIsLoudAndMailsNothing.
//
// This arm is reached by no production source today — WedgeSource cannot fail,
// by design — and Options.Source says so. The test is kept because the arm is
// the contract a future source would inherit, not because it proves anything
// about the shipped wiring.
func TestSourceErrorIsLoudAndMailsNothing(t *testing.T) {
	r := &recorder{}
	w := newWatcher(r, func(time.Time) (Snapshot, error) { return Snapshot{}, errors.New("boom") })
	w.Check(time.Now())
	if len(r.mails) != 0 {
		t.Error("mailed on a failed source read")
	}
	if len(r.typed(EventError)) != 1 {
		t.Error("a failed source read must emit blind_watch_error")
	}
}

// TestWedgeSourceOnANilWatcherReportsStopped. "Not armed" and "armed and
// stopped" are the same fact from a consumer's seat: nothing is judging.
func TestWedgeSourceOnANilWatcherReportsStopped(t *testing.T) {
	snap, err := WedgeSource(nil)(time.Now())
	if err != nil {
		t.Fatalf("WedgeSource(nil) returned an error (%v); the condition belongs in the finding, not the error channel", err)
	}
	if !snap.SampledAt.IsZero() || snap.Examined != 0 {
		t.Fatalf("snapshot = %+v, want a never-sampled reading", snap)
	}
	if snap.Detector == "" {
		t.Error("snapshot does not name the detector")
	}
}

// TestWedgeSourceReadsTheRealWatcher — the adapter must actually be wired to
// wedgewatch's accessor, not to a struct that merely compiles.
func TestWedgeSourceReadsTheRealWatcher(t *testing.T) {
	ww := wedgewatch.New(wedgewatch.Options{Enabled: true, Emit: func(events.Event) {}})
	snap, err := WedgeSource(ww)(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Detector != "wedge-watch" {
		t.Errorf("detector = %q, want wedge-watch", snap.Detector)
	}
	if !snap.SampledAt.IsZero() {
		t.Errorf("SampledAt = %s, want zero for a watcher that has never sampled", snap.SampledAt)
	}
}

// TestNilWatcherIsSafe — pogod holds a nil *Watcher when the detector is off.
func TestNilWatcherIsSafe(t *testing.T) {
	var w *Watcher
	w.Check(time.Now())
}

// TestNoAllClearWhileTheDetectorIsInsideItsHoldDown.
//
// The same defect as internal/heartwatch's, and worse here: `confirmed` is
// empty whenever a condition is inside its hold-down, INCLUDING a detector that
// has just stopped sampling. A clear at that moment mails "wedge-watch can
// judge the fleet again" about a detector that went dark minutes earlier.
func TestNoAllClearWhileTheDetectorIsInsideItsHoldDown(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	stopped := false
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: 2 * time.Hour, StaleAfter: time.Hour,
		Coordinator: "mayor", HumanBox: "human",
		Source: func(at time.Time) (Snapshot, error) {
			if stopped {
				// Sampling stopped six hours ago: past stale_after, but only
				// just entered this consumer's hold-down.
				return Snapshot{Detector: "wedge-watch", SampledAt: now.Add(-6 * time.Hour), Examined: 5}, nil
			}
			return Snapshot{
				Detector: "wedge-watch", SampledAt: at, Examined: 5,
				Blind: []Target{{Name: "pm-riemann", Since: now.Add(-18 * 24 * time.Hour)}},
			}, nil
		},
	})
	// Announce a standing blindness first, so an episode is open to falsely clear.
	w.Check(now)
	w.Check(now.Add(3 * time.Hour))
	if len(r.mails) != 1 {
		t.Fatalf("setup: mails = %d, want 1", len(r.mails))
	}

	stopped = true
	w.Check(now.Add(3*time.Hour + time.Minute))
	for _, m := range r.mails[1:] {
		if strings.Contains(m.subject, "can judge the fleet again") {
			t.Fatalf("mailed an all-clear about a detector that had stopped sampling: %q", m.subject)
		}
	}
	if len(r.typed(EventClear)) != 0 {
		t.Error("emitted blind_watch_clear over a detector that was not sampling")
	}

	// The other half of the control: a genuinely healthy reading DOES clear.
	w.source = func(at time.Time) (Snapshot, error) {
		return Snapshot{Detector: "wedge-watch", SampledAt: at, Examined: 5}, nil
	}
	w.Check(now.Add(3*time.Hour + 2*time.Minute))
	if len(r.typed(EventClear)) != 1 {
		t.Errorf("blind_watch_clear events = %d over a healthy reading, want 1", len(r.typed(EventClear)))
	}
}
