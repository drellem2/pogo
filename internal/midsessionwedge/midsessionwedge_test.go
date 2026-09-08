package midsessionwedge

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// harness drives the Watcher with a scripted fleet and records everything it
// did. Nothing here touches a registry, a PTY or the clock.
type harness struct {
	mu       sync.Mutex
	readings []Reading
	srcErr   error

	emitted   []events.Event
	recovered []string
	mails     []string

	// submits is the count RegistrySubmits would return, keyed by agent. The
	// recover hook can move it, which is how a test says "the bare return
	// worked".
	submits    map[string]int
	recoverErr error
	// recoverSubmits, when non-zero, is what the agent's receipt count becomes
	// after a successful bare return.
	recoverSubmits map[string]int
	noSubmitsProbe bool
}

func newHarness() *harness {
	return &harness{submits: map[string]int{}, recoverSubmits: map[string]int{}}
}

func (h *harness) opts() Options {
	o := Options{
		Enabled: true,
		Source: func(time.Time) ([]Reading, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.srcErr != nil {
				return nil, h.srcErr
			}
			out := make([]Reading, len(h.readings))
			copy(out, h.readings)
			return out, nil
		},
		Recover: func(name string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.recovered = append(h.recovered, name)
			if h.recoverErr != nil {
				return h.recoverErr
			}
			if n, ok := h.recoverSubmits[name]; ok {
				h.submits[name] = n
			}
			return nil
		},
		Submits: func(name string) (int, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.submits[name], nil
		},
		Mail: func(to, from, subject, body string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.mails = append(h.mails, to+"|"+subject+"|"+body)
			return nil
		},
		NotifyTo: "mayor",
		From:     "pogod",
		Emit: func(e events.Event) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.emitted = append(h.emitted, e)
		},
		Settle: time.Millisecond,
	}
	if h.noSubmitsProbe {
		o.Submits = nil
	}
	return o
}

func (h *harness) set(rs ...Reading) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.readings = rs
}

func (h *harness) events(typ string) []events.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []events.Event
	for _, e := range h.emitted {
		if e.EventType == typ {
			out = append(out, e)
		}
	}
	return out
}

func (h *harness) nRecovered() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.recovered)
}

func (h *harness) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitted = nil
	h.recovered = nil
	h.mails = nil
}

const t0seed = "2026-09-08T12:00:00Z"

func base(t *testing.T) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, t0seed)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// owed builds a reading for an agent that owes a submit.
func owed(name, digest string, at time.Time) Reading {
	return Reading{Name: name, Kind: "polecat", Digest: digest, Submits: 7,
		HasReceipt: true, Owed: true, OwedAt: at, OwedSubmits: 7}
}

// run drives the watcher across n ticks spaced by step, calling mutate before
// each tick so a test can change the fleet under it.
func run(w *Watcher, start time.Time, step time.Duration, n int, mutate func(i int, now time.Time)) time.Time {
	now := start
	for i := 0; i < n; i++ {
		if mutate != nil {
			mutate(i, now)
		}
		w.Check(now)
		now = now.Add(step)
	}
	return now
}

// ---------------------------------------------------------------------------
// The two readings the ticket says must be established separately.
// ---------------------------------------------------------------------------

// TestParkedAgentWithNothingOwedIsNeverJudged is the false positive that sinks
// the quiescence-only design, and the reason the criterion is a conjunction.
//
// A polecat that has finished its work and is holding for the coordinator is
// parked at an empty composer with a byte-identical ring FOREVER, by design. No
// threshold excludes it, because there is no duration a healthy hold does not
// reach. Only "a submit is owed" separates it from the wedge.
func TestParkedAgentWithNothingOwedIsNeverJudged(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	w := New(o)

	// Same digest for four hours. Nothing owed.
	h.set(Reading{Name: "parked", Kind: "polecat", Digest: "static", Submits: 3, HasReceipt: true})
	run(w, base(t), time.Minute, 240, nil)

	if n := h.nRecovered(); n != 0 {
		t.Fatalf("delivered %d bare returns to an agent that owes no submit; a finished "+
			"polecat holding for the coordinator is quiescent forever and must never be typed into", n)
	}
	if got := h.events(EventFired); len(got) != 0 {
		t.Fatalf("fired %d times over a parked agent with nothing owed", len(got))
	}
}

// TestSpinningAgentIsExoneratedHoweverLongItOwesASubmit is the spinner trap.
//
// A spinner IS PTY output, so the ring hash of a busy agent changes on every
// sample. That is the reading the detector must treat as conclusive LIVENESS —
// a mid-turn agent legitimately holds a queued prompt until its turn ends, and
// typing a bare return at it would submit into the middle of a turn.
func TestSpinningAgentIsExoneratedHoweverLongItOwesASubmit(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	w := New(o)

	start := base(t)
	run(w, start, 30*time.Second, 400, func(i int, now time.Time) {
		// Every sample a different ring: an animating spinner with an elapsed
		// counter in it, which is what Claude Code actually draws.
		h.set(owed("spinner", fmt.Sprintf("frame-%d", i), start))
	})

	if n := h.nRecovered(); n != 0 {
		t.Fatalf("delivered %d bare returns to an agent whose ring changed on every "+
			"sample; a changed ring is conclusive liveness and must only ever exonerate", n)
	}
}

// TestTheConjunctionFires is the target case: healthy start, turn over, ring
// byte-identical, worktree still, and a submit owed that never landed.
func TestTheConjunctionFires(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = 5 * time.Minute
	w := New(o)

	start := base(t)
	h.set(owed("wedged", "static", start))
	// Ten minutes of a byte-identical ring at one-minute ticks.
	run(w, start, time.Minute, 10, nil)

	if n := h.nRecovered(); n == 0 {
		t.Fatal("never delivered a bare return to an agent that owed a submit and " +
			"whose ring had been byte-identical for twice the quiescence window")
	}
	fired := h.events(EventFired)
	if len(fired) == 0 {
		t.Fatal("no midsession_wedge_fired event")
	}
	if got := fired[0].Details["action"]; got != "bare_return" {
		t.Errorf("action = %v, want bare_return — a bare return is the only payload that "+
			"cannot duplicate a message that already landed", got)
	}
	if _, ok := fired[0].Details["quiet_for"]; !ok {
		t.Error("fired event carries no quiet_for; the reading that justified it must be in the record")
	}
}

// TestQuiescenceIsMeasuredFromTheLastCHANGE, not from the first sight of the
// agent: a watcher that started its clock at spawn would judge an agent on a
// window it never observed.
func TestQuiescenceIsMeasuredFromTheLastChange(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = 5 * time.Minute
	w := New(o)

	start := base(t)
	// Four minutes quiet, then one change, then four more minutes quiet.
	// Neither run reaches five minutes, so nothing may fire.
	run(w, start, time.Minute, 4, func(i int, now time.Time) {
		h.set(owed("flapper", "a", start))
	})
	run(w, start.Add(4*time.Minute), time.Minute, 5, func(i int, now time.Time) {
		if i == 0 {
			h.set(owed("flapper", "b", start))
		}
	})
	if n := h.nRecovered(); n != 0 {
		t.Fatalf("fired %d times although no unbroken quiet run reached the quiescence "+
			"window; the timer must restart on every change", n)
	}
}

// ---------------------------------------------------------------------------
// Exonerations, in cost order.
// ---------------------------------------------------------------------------

// TestReceiptAdvanceClearsAndReportsRecovery: the cheapest and only POSITIVE
// observation in the chain — the harness itself recorded a submit.
func TestReceiptAdvanceClearsAndReportsRecovery(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = 2 * time.Minute
	// The bare return works: the receipt count moves.
	h.recoverSubmits["late"] = 8
	w := New(o)

	start := base(t)
	h.set(owed("late", "static", start))
	run(w, start, time.Minute, 4, nil)
	if h.nRecovered() == 0 {
		t.Fatal("expected a bare return")
	}
	// The harness recorded the submit, so the next reading is no longer owed.
	h.set(Reading{Name: "late", Kind: "polecat", Digest: "static", Submits: 8, HasReceipt: true})
	w.Check(start.Add(10 * time.Minute))

	if got := h.events(EventRecovered); len(got) == 0 {
		t.Fatal("no midsession_wedge_recovered event after the composer drained")
	}
	before := h.nRecovered()
	run(w, start.Add(11*time.Minute), time.Minute, 30, nil)
	if h.nRecovered() != before {
		t.Errorf("kept typing at an agent whose composer had drained: %d -> %d", before, h.nRecovered())
	}
}

// TestWorktreeMovementExoneratesAndIsPositiveOnly.
//
// Both halves matter. Movement inside the quiet run must clear the alarm — that
// is the signal that settled a live case on 2026-09-08. And a STILL worktree
// must contribute nothing: an agent thinking hard between commits is
// byte-identical to an agent wedged between commits.
func TestWorktreeMovementExoneratesAndIsPositiveOnly(t *testing.T) {
	start := base(t)

	moved := newHarness()
	mo := moved.opts()
	mo.Interval = time.Second
	mo.Quiescence = 2 * time.Minute
	mo.Worktree = func(string, time.Time) (Movement, bool) {
		return Movement{Moved: true, At: start.Add(3 * time.Minute), Path: "logs/HEAD"}, true
	}
	w := New(mo)
	moved.set(owed("committing", "static", start))
	run(w, start, time.Minute, 8, nil)
	if n := moved.nRecovered(); n != 0 {
		t.Errorf("typed at an agent whose worktree moved inside the quiet run (%d bare returns)", n)
	}
	if got := moved.events(EventExonerated); len(got) == 0 {
		t.Error("worktree exoneration left no event; the reason an alarm was dropped must be recorded")
	} else {
		if got[0].Details["positive_only"] != true {
			t.Error("exoneration event does not mark itself positive_only")
		}
		if got[0].Details["moved_path"] != "logs/HEAD" {
			t.Errorf("exoneration names moved_path %v, want logs/HEAD — a path that "+
				"exonerates for a reason unrelated to the agent's progress must be "+
				"visible in the log as the same filename every time, not silent",
				got[0].Details["moved_path"])
		}
	}

	still := newHarness()
	so := still.opts()
	so.Interval = time.Second
	so.Quiescence = 2 * time.Minute
	so.Worktree = func(string, time.Time) (Movement, bool) {
		return Movement{}, true
	}
	w2 := New(so)
	still.set(owed("quiet", "static", start))
	run(w2, start, time.Minute, 8, nil)
	if still.nRecovered() == 0 {
		t.Error("a still worktree suppressed the finding; stillness proves nothing and " +
			"must not be readable as an exoneration")
	}
}

// TestNoWorktreeIsNotAnExoneration: ok=false means there is nothing to read —
// a crew agent, or a polecat spawned with --no-worktree — and must not be
// mistaken for "did not move".
func TestNoWorktreeIsNotAnExoneration(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = 2 * time.Minute
	o.Worktree = func(string, time.Time) (Movement, bool) { return Movement{}, false }
	w := New(o)
	start := base(t)
	h.set(owed("crewish", "static", start))
	run(w, start, time.Minute, 8, nil)
	if h.nRecovered() == 0 {
		t.Fatal("an agent with no worktree to read was never judged; absence of the probe " +
			"is not an exoneration")
	}
}

// ---------------------------------------------------------------------------
// Declining loudly.
// ---------------------------------------------------------------------------

// TestAgentWithoutReceiptSignalIsSkippedLoudly. An agent with no
// submission-receipt hook cannot own a submit, so the whole conjunction is
// unreadable for it. It must be declined WITH AN EVENT — internal/blindwatch's
// founding bug is an instrument that could not judge reading as clean.
func TestAgentWithoutReceiptSignalIsSkippedLoudly(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	w := New(o)
	start := base(t)
	r := owed("blind", "static", start)
	r.HasReceipt = false
	h.set(r)
	run(w, start, time.Minute, 10, nil)

	if n := h.nRecovered(); n != 0 {
		t.Fatalf("typed %d bare returns at an agent whose submits cannot be read", n)
	}
	sk := h.events(EventSkipped)
	if len(sk) == 0 {
		t.Fatal("an unjudgeable agent produced no midsession_wedge_skipped event; " +
			"could-not-judge must never read the same as healthy")
	}
	if sk[0].Details["reason"] != "no_receipt_signal" {
		t.Errorf("skip reason = %v, want no_receipt_signal", sk[0].Details["reason"])
	}
}

// TestUnreadableRingIsADeclineNotAQuiescence. An empty digest means the ring
// could not be read. Treating it as "identical to last time" would let a broken
// reader accumulate a quiet run and fire on it.
func TestUnreadableRingIsADeclineNotAQuiescence(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = 2 * time.Minute
	w := New(o)
	start := base(t)
	r := owed("dark", "", start)
	h.set(r)
	run(w, start, time.Minute, 20, nil)
	if n := h.nRecovered(); n != 0 {
		t.Fatalf("fired %d times on an agent whose ring never read at all", n)
	}
}

// TestSourceErrorIsReportedNotSwallowed.
func TestSourceErrorIsReportedNotSwallowed(t *testing.T) {
	h := newHarness()
	h.srcErr = errors.New("registry gone")
	o := h.opts()
	o.Interval = time.Second
	w := New(o)
	w.Check(base(t))
	if got := h.events(EventError); len(got) == 0 {
		t.Fatal("a fleet that could not be read produced no error event")
	}
}

// ---------------------------------------------------------------------------
// Bounding the action.
// ---------------------------------------------------------------------------

// TestAttemptsAreBoundedAndExhaustionIsMailed. A dead agent — or one whose work
// was cancelled out from under it — must not draw an unbounded stream of stray
// keystrokes.
func TestAttemptsAreBoundedAndExhaustionIsMailed(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	o.MaxAttempts = 3
	w := New(o)
	start := base(t)
	h.set(owed("dead", "static", start))
	run(w, start, time.Minute, 200, nil)

	if n := h.nRecovered(); n != 3 {
		t.Fatalf("delivered %d bare returns over 200 ticks, want exactly MaxAttempts=3", n)
	}
	h.mu.Lock()
	mails := append([]string(nil), h.mails...)
	h.mu.Unlock()
	if len(mails) != 1 {
		t.Fatalf("exhausted recovery produced %d mails, want exactly 1", len(mails))
	}
	if !strings.Contains(mails[0], "mayor|") {
		t.Errorf("notice went to %q, want the configured NotifyTo", mails[0])
	}
	if !strings.Contains(mails[0], "restart destroys the transcript") {
		t.Error("the notice does not warn that a restart destroys the transcript that " +
			"would explain the wedge")
	}
}

// TestANewOwedSubmitGetsAFreshAttemptBudget: attempts belong to one owed
// submit. A later delivery is a different thing sitting in the composer and
// must not inherit a spent budget.
func TestANewOwedSubmitGetsAFreshAttemptBudget(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	o.MaxAttempts = 2
	w := New(o)
	start := base(t)
	h.set(owed("twice", "static", start))
	run(w, start, time.Minute, 30, nil)
	if n := h.nRecovered(); n != 2 {
		t.Fatalf("first budget spent %d, want 2", n)
	}
	// A new mid-turn delivery lands. Same digest, same unmoved receipt count.
	later := start.Add(time.Hour)
	h.set(owed("twice", "static", later))
	run(w, later, time.Minute, 30, nil)
	if n := h.nRecovered(); n != 4 {
		t.Fatalf("after a NEW owed submit the total is %d, want 4 (a fresh budget of 2)", n)
	}
}

// TestReportOnlyWithoutRecover: a watcher with no Recover still detects, and
// says in the event that it did nothing.
func TestReportOnlyWithoutRecover(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	o.Recover = nil
	w := New(o)
	start := base(t)
	h.set(owed("watched", "static", start))
	run(w, start, time.Minute, 5, nil)
	fired := h.events(EventFired)
	if len(fired) == 0 {
		t.Fatal("report-only watcher detected nothing")
	}
	if got := fired[0].Details["action"]; got != "none_report_only" {
		t.Errorf("action = %v, want none_report_only", got)
	}
}

// TestUnconfirmedRecoveryIsRecordedAsUnrecovered. Writing to a PTY master
// succeeds whether or not anything is listening, so a nudge returning nil is
// never a recovery.
func TestUnconfirmedRecoveryIsRecordedAsUnrecovered(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	o.MaxAttempts = 1
	w := New(o)
	start := base(t)
	h.set(owed("mute", "static", start)) // recoverSubmits unset: count never moves
	run(w, start, time.Minute, 5, nil)
	if got := h.events(EventUnrecovered); len(got) == 0 {
		t.Fatal("a bare return that produced no receipt was not recorded as unrecovered")
	}
	if got := h.events(EventRecovered); len(got) != 0 {
		t.Fatal("recorded a recovery with no submission receipt behind it")
	}
}

// TestRecoveryWithoutASubmitsProbeCannotClaimSuccess.
func TestRecoveryWithoutASubmitsProbeCannotClaimSuccess(t *testing.T) {
	h := newHarness()
	h.noSubmitsProbe = true
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	o.MaxAttempts = 1
	w := New(o)
	start := base(t)
	h.set(owed("unprovable", "static", start))
	run(w, start, time.Minute, 5, nil)
	ev := h.events(EventUnrecovered)
	if len(ev) == 0 {
		t.Fatal("a delivery that cannot be confirmed was not recorded as unrecovered")
	}
	if s, _ := ev[0].Details["error"].(string); !strings.Contains(s, "CANNOT be confirmed") {
		t.Errorf("error = %q, want it to say the delivery cannot be confirmed", s)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle.
// ---------------------------------------------------------------------------

// TestWarmupSuppressesJudgementAfterAPogodRestart. A restart leaves no hash
// history, so the first observation of a digest establishes only that it is
// current — not how long it has been.
func TestWarmupSuppressesJudgementAfterAPogodRestart(t *testing.T) {
	h := newHarness()
	start := base(t)
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = 5 * time.Minute
	o.StartedAt = start
	w := New(o)
	h.set(owed("early", "static", start.Add(-2*time.Hour)))
	// Ticks inside the warm-up window, each already past the quiescence
	// threshold as measured from the owed timestamp.
	run(w, start, time.Minute, 5, nil)
	if n := h.nRecovered(); n != 0 {
		t.Fatalf("fired %d times inside the post-restart warm-up window", n)
	}
	// Past the warm-up, with a quiet run the watcher has actually observed.
	run(w, start.Add(5*time.Minute), time.Minute, 6, nil)
	if h.nRecovered() == 0 {
		t.Fatal("never fired after the warm-up window elapsed")
	}
}

// TestIntervalThrottlesSampling.
func TestIntervalThrottlesSampling(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = 5 * time.Minute
	o.Quiescence = time.Minute
	w := New(o)
	start := base(t)
	h.set(owed("throttled", "static", start))
	// Sixty one-second ticks inside one interval: at most one sample.
	run(w, start, time.Second, 60, nil)
	if n := h.nRecovered(); n > 1 {
		t.Fatalf("sampled %d times inside one interval", n)
	}
}

// TestDisabledWatcherAndNilReceiverAreInert.
func TestDisabledWatcherAndNilReceiverAreInert(t *testing.T) {
	var nilw *Watcher
	nilw.Check(base(t)) // must not panic
	if _, ok := nilw.Quiet("x", base(t)); ok {
		t.Error("nil watcher reported a quiet run")
	}
	h := newHarness()
	o := h.opts()
	o.Enabled = false
	w := New(o)
	h.set(owed("off", "static", base(t)))
	run(w, base(t), time.Minute, 30, nil)
	if h.nRecovered() != 0 {
		t.Error("a disabled watcher acted")
	}
}

// TestDepartedAgentsAreForgotten keeps the track map from growing with every
// polecat the fleet has ever run.
func TestDepartedAgentsAreForgotten(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	w := New(o)
	start := base(t)
	h.set(Reading{Name: "gone", Kind: "polecat", Digest: "d", HasReceipt: true})
	w.Check(start)
	if _, ok := w.Quiet("gone", start); !ok {
		t.Fatal("no track for an agent that was just read")
	}
	h.set()
	w.Check(start.Add(time.Minute))
	if _, ok := w.Quiet("gone", start.Add(time.Minute)); ok {
		t.Error("kept a track for an agent that left the fleet")
	}
}

// TestQuiescenceZeroMeansDefaultAndNegativeMeansOff. A config that simply omits
// the key must not silently disable the requirement: a watcher with no
// quiescence requirement types a bare return into every agent that owes a
// submit the instant it owes one, which is the state every healthy mid-turn
// agent is in.
func TestQuiescenceZeroMeansDefaultAndNegativeMeansOff(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Quiescence = 0
	if got := New(o).quiescence; got != DefaultQuiescence {
		t.Errorf("zero quiescence = %s, want the default %s", got, DefaultQuiescence)
	}
	o.Quiescence = -1
	if got := New(o).quiescence; got != 0 {
		t.Errorf("negative quiescence = %s, want it disabled (0)", got)
	}
}

// TestTheNoReceiptDeclineIsLatchedNotRepeated. It must be said — an
// unjudgeable agent that reads as nothing is the failure one level out — but
// once per agent, not once per tick: a fleet of hookless agents would otherwise
// bury the event spine under the same sentence forever.
func TestTheNoReceiptDeclineIsLatchedNotRepeated(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	w := New(o)
	r := Reading{Name: "hookless", Kind: "polecat", Digest: "static", HasReceipt: false}
	h.set(r)
	run(w, base(t), time.Minute, 50, nil)
	if got := h.events(EventSkipped); len(got) != 1 {
		t.Fatalf("emitted %d skip events over 50 ticks, want exactly 1", len(got))
	}
	// A hook appearing (a respawn onto a provider that has one) re-arms the
	// latch, so a LATER loss of the signal is announced again.
	h.reset()
	r.HasReceipt = true
	h.set(r)
	w.Check(base(t).Add(time.Hour))
	r.HasReceipt = false
	h.set(r)
	w.Check(base(t).Add(2 * time.Hour))
	if got := h.events(EventSkipped); len(got) != 1 {
		t.Fatalf("a receipt signal that came back and went away again produced %d skip "+
			"events, want 1 — the latch must re-arm", len(got))
	}
}

// TestSnapshotMakesAnArmedAndSILENTDetectorReadable.
//
// This detector fires only on an agent that OWES a submit, and a submit becomes
// owed only when pogod writes into a mid-turn agent and gets no receipt. On a
// box where that never happens it runs forever, judges nobody, emits nothing,
// and is indistinguishable from a box with no wedges. That is the same shape as
// the failure it detects, one level out, so the armed-and-silent case has to be
// readable rather than inferred from absence.
func TestSnapshotMakesAnArmedAndSilentDetectorReadable(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	w := New(o)

	if got := w.Snapshot(); !got.Armed || !got.LastSample.IsZero() {
		t.Errorf("before the first sample: armed=%v last=%v, want armed with no sample",
			got.Armed, got.LastSample)
	}

	start := base(t)
	h.set(
		owed("owes", "static", start),
		Reading{Name: "idle", Kind: "polecat", Digest: "static", HasReceipt: true},
		Reading{Name: "hookless", Kind: "polecat", Digest: "static", HasReceipt: false},
		Reading{Name: "dark", Kind: "polecat", Digest: "", HasReceipt: true},
	)
	w.Check(start)

	got := w.Snapshot()
	if !got.Armed {
		t.Error("armed=false for an enabled watcher with a source")
	}
	if got.LastSample.IsZero() {
		t.Error("LastSample is zero after a completed sample; a detector that stopped " +
			"sampling would be indistinguishable from one finding nothing")
	}
	if got.Judged != 2 {
		t.Errorf("Judged = %d, want 2 (owes + idle)", got.Judged)
	}
	if got.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (no receipt signal, unreadable ring)", got.Skipped)
	}
	if got.Owed != 1 {
		t.Errorf("Owed = %d, want 1 — this is the population the detector can fire on "+
			"at all, and a long run of zero is the reading that says it is covering nobody",
			got.Owed)
	}
	if got.Quiescence != DefaultQuiescence {
		t.Errorf("Quiescence = %s, want the measured default %s", got.Quiescence, DefaultQuiescence)
	}

	// A discharged obligation leaves the population, so Owed tracks the live
	// state rather than accumulating.
	h.set(Reading{Name: "owes", Kind: "polecat", Digest: "static", Submits: 8,
		HasReceipt: true, Owed: true, OwedAt: start, OwedSubmits: 7})
	w.Check(start.Add(time.Minute))
	if got := w.Snapshot().Owed; got != 0 {
		t.Errorf("Owed = %d after the receipt moved, want 0", got)
	}
}

// TestExhaustionMailIsFlooredPerAgent.
//
// The attempt budget is per OWED SUBMIT and a new owed submit arrives with
// every nudge, so an agent on the fleet's */10 mail-check cadence exhausts a
// fresh budget every ten minutes, forever. Without a floor that is six mails an
// hour about one agent, and this tree has already watched a correct detector
// fire every five minutes for thirteen hours into an inbox nobody read.
//
// The bare returns themselves must stay UNFLOORED: they are harmless keystrokes
// and each new delivery is a genuinely new thing to try to submit.
func TestExhaustionMailIsFlooredPerAgent(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Second
	o.Quiescence = time.Minute
	o.MaxAttempts = 1
	o.RenotifyAfter = time.Hour
	w := New(o)

	start := base(t)
	// Six fresh owed submits, ten minutes apart — the */10 cadence.
	for i := 0; i < 6; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Minute)
		h.set(owed("stuck", "static", at))
		run(w, at, time.Minute, 6, nil)
	}

	if n := h.nRecovered(); n != 6 {
		t.Errorf("delivered %d bare returns over six fresh owed submits, want 6 — the "+
			"keystroke must not be floored, only the mail", n)
	}
	h.mu.Lock()
	mails := len(h.mails)
	h.mu.Unlock()
	if mails != 1 {
		t.Fatalf("mailed %d times about one agent inside the renotify floor, want 1", mails)
	}

	// Past the floor, it speaks again.
	late := start.Add(3 * time.Hour)
	h.set(owed("stuck", "static", late))
	run(w, late, time.Minute, 6, nil)
	h.mu.Lock()
	mails = len(h.mails)
	h.mu.Unlock()
	if mails != 2 {
		t.Errorf("mails = %d after the renotify floor elapsed, want 2 — a floor that never "+
			"lifts is a detector that reported once and went quiet", mails)
	}
}

// TestRenotifyZeroMeansDefaultAndNegativeMeansOff.
func TestRenotifyZeroMeansDefaultAndNegativeMeansOff(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.RenotifyAfter = 0
	if got := New(o).renotifyAfter; got != DefaultRenotifyAfter {
		t.Errorf("zero renotify = %s, want %s", got, DefaultRenotifyAfter)
	}
	o.RenotifyAfter = -1
	if got := New(o).renotifyAfter; got != 0 {
		t.Errorf("negative renotify = %s, want it disabled (0)", got)
	}
}

// TestSamplesDoNotOverlap. The recovery pass waits out a settle window per
// agent, so a sample over several wedged agents can outlast the interval. Two
// overlapping samples judge the same agent against the same attempt count and
// deliver more bare returns than MaxAttempts allows — a bound that is not a
// bound, which is worse than no bound because it is one somebody will cite.
func TestSamplesDoNotOverlap(t *testing.T) {
	h := newHarness()
	o := h.opts()
	o.Interval = time.Nanosecond // no throttle at all: only the in-flight guard
	o.Quiescence = time.Minute
	o.MaxAttempts = 2
	o.Settle = 40 * time.Millisecond
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	o.Recover = func(name string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		h.mu.Lock()
		h.recovered = append(h.recovered, name)
		h.mu.Unlock()
		return nil
	}
	w := New(o)
	start := base(t)
	h.set(owed("slow", "static", start))

	// Prime the quiet run — this tick is inside the quiescence window, so it
	// establishes the digest without firing. The NEXT one fires and blocks
	// inside recovery, which is the state the guard has to survive.
	w.Check(start)
	go w.Check(start.Add(2 * time.Minute))
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery never started")
	}

	// While that one is still inside its recovery, hammer Check.
	for i := 0; i < 50; i++ {
		w.Check(start.Add(time.Duration(3+i) * time.Minute))
	}
	close(release)
	time.Sleep(200 * time.Millisecond)

	if n := h.nRecovered(); n > o.MaxAttempts {
		t.Errorf("delivered %d bare returns with MaxAttempts=%d; overlapping samples "+
			"judged the same agent against the same attempt count", n, o.MaxAttempts)
	}
}
