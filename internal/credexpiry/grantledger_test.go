package credexpiry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// --- the grant ledger (mg-2127) --------------------------------------------
//
// The defect these tests pin: `cred_expiry_warned` fires ONLY inside a warning
// tier, so grant issuance is outside its domain by construction. Measured on
// 2026-09-07, all 25 rows in ~/.pogo/events.log carried one grant expiry
// rounded two ways and no transition at all — a clean, complete-looking series
// that could not answer the question being asked of it, with nothing in its
// shape to say so.

// eventRecorder captures emitted events.
type eventRecorder struct{ evs []events.Event }

func (e *eventRecorder) emit(ev events.Event) { e.evs = append(e.evs, ev) }

func (e *eventRecorder) ofType(t string) []events.Event {
	var out []events.Event
	for _, ev := range e.evs {
		if ev.EventType == t {
			out = append(out, ev)
		}
	}
	return out
}

func ledgerWatcher(read Reader, rec *recorder, ev *eventRecorder) *Watcher {
	return New(Options{
		Enabled:  true,
		Read:     read,
		Mail:     rec.send,
		Emit:     ev.emit,
		Interval: time.Minute,
	})
}

func detail(t *testing.T, ev events.Event, key string) any {
	t.Helper()
	v, ok := ev.Details[key]
	if !ok {
		t.Fatalf("%s carries no %q detail; has %v", ev.EventType, key, ev.Details)
	}
	return v
}

// TestGrantTransitionIsRecordedFarFromAnyWarningTier is the load-bearing test.
// A `/login` happens where the warner is silent by design — three weeks out,
// TierNone, no mail. Before mg-2127 that produced no event of any kind, and the
// log therefore held no grant history at all.
func TestGrantTransitionIsRecordedFarFromAnyWarningTier(t *testing.T) {
	oldExpiry := mustTime(t, nextOutage)
	rec := &recorder{}
	ev := &eventRecorder{}
	st := presentAt(oldExpiry)
	w := ledgerWatcher(func(context.Context) Status { return st }, rec, ev)

	// Two healthy samples on the old grant, 24 days out: no tier, no mail.
	first := oldExpiry.Add(-24 * 24 * time.Hour)
	w.Check(context.Background(), first)
	w.Check(context.Background(), first.Add(15*time.Minute))

	// The human runs /login somewhere in the next 15 minutes.
	newExpiry := oldExpiry.Add(30 * 24 * time.Hour)
	st = presentAt(newExpiry)
	seen := first.Add(30 * time.Minute)
	w.Check(context.Background(), seen)

	if len(rec.mails) != 0 {
		t.Fatalf("the warner mailed on a healthy credential: %v", rec.subjects())
	}
	if got := len(ev.ofType(EventWarned)); got != 0 {
		t.Fatalf("got %d %s events far from any tier, want 0 — that is the defect", got, EventWarned)
	}

	rows := ev.ofType(EventGrantObserved)
	if len(rows) != 2 {
		t.Fatalf("got %d %s rows, want 2 (first observation + transition): %v",
			len(rows), EventGrantObserved, ev.evs)
	}

	// Row 1: first observation. It must NOT claim to be a transition.
	if got := detail(t, rows[0], "transition"); got != false {
		t.Errorf("first observation reported transition=%v, want false — otherwise every "+
			"pogod restart looks like a /login", got)
	}
	if got := detail(t, rows[0], "lifetime_at_most"); got != unboundedSpan {
		t.Errorf("first observation reported lifetime_at_most=%v, want %q", got, unboundedSpan)
	}
	if got := detail(t, rows[0], "issuance_after"); got != unknownBound {
		t.Errorf("first observation reported issuance_after=%v, want %q", got, unknownBound)
	}

	// Row 2: the transition, with issuance bracketed by the two samples.
	tr := rows[1]
	if got := detail(t, tr, "transition"); got != true {
		t.Errorf("a changed expiry reported transition=%v, want true", got)
	}
	if got := detail(t, tr, "expires_at"); got != newExpiry.Format(time.RFC3339) {
		t.Errorf("expires_at = %v, want %v", got, newExpiry.Format(time.RFC3339))
	}
	if got := detail(t, tr, "previous_expires_at"); got != oldExpiry.Format(time.RFC3339) {
		t.Errorf("previous_expires_at = %v, want %v", got, oldExpiry.Format(time.RFC3339))
	}
	// The bracket: the old value was still there at first+15m, the new one was
	// there at first+30m, so issuance sits in that 15-minute window.
	wantAfter := first.Add(15 * time.Minute).UTC().Format(time.RFC3339)
	if got := detail(t, tr, "issuance_after"); got != wantAfter {
		t.Errorf("issuance_after = %v, want %v (the last sample that saw the old value)", got, wantAfter)
	}
	if got := detail(t, tr, "issuance_before"); got != seen.UTC().Format(time.RFC3339) {
		t.Errorf("issuance_before = %v, want %v", got, seen.UTC().Format(time.RFC3339))
	}
	if got := detail(t, tr, "issuance_window"); got != "15m" {
		t.Errorf("issuance_window = %v, want 15m", got)
	}
	// And the grant's life is now BOUNDED rather than assumed.
	if got := detail(t, tr, "lifetime_at_most"); got == unboundedSpan {
		t.Errorf("a measured transition still reported lifetime_at_most=%q", unboundedSpan)
	}
}

// TestFirstObservationIsNotATransition is the remedy's own version of the
// defect. A ledger that emitted on "the value differs from what I remember"
// would fire on every pogod restart, because a fresh process remembers the zero
// time — and an analyst differencing consecutive rows would be measuring
// pogod's uptime while believing it was grant lifetime.
func TestFirstObservationIsNotATransition(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	now := expiry.Add(-20 * 24 * time.Hour)

	for i := 0; i < 3; i++ {
		rec := &recorder{}
		ev := &eventRecorder{}
		w := ledgerWatcher(fixedReader(presentAt(expiry)), rec, ev)
		w.Check(context.Background(), now.Add(time.Duration(i)*time.Hour))

		rows := ev.ofType(EventGrantObserved)
		if len(rows) != 1 {
			t.Fatalf("restart %d: got %d ledger rows, want 1", i, len(rows))
		}
		if got := detail(t, rows[0], "transition"); got != false {
			t.Fatalf("restart %d: a fresh process reported transition=%v on the SAME grant — "+
				"consecutive rows would measure daemon uptime, not grant lifetime", i, got)
		}
	}
}

// TestUnchangedGrantEmitsNoLedgerRow keeps the ledger a ledger. A row per
// sample would be 2,880 rows a month and would drown the handful that mean
// something.
func TestUnchangedGrantEmitsNoLedgerRow(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	rec := &recorder{}
	ev := &eventRecorder{}
	w := ledgerWatcher(fixedReader(presentAt(expiry)), rec, ev)

	start := expiry.Add(-20 * 24 * time.Hour)
	for i := 0; i < 10; i++ {
		w.Check(context.Background(), start.Add(time.Duration(i)*15*time.Minute))
	}
	if got := len(ev.ofType(EventGrantObserved)); got != 1 {
		t.Errorf("got %d ledger rows over 10 samples of an unchanged grant, want 1", got)
	}
}

// TestIssuanceBracketNarrowsWithSampling proves the lower bound advances on
// every confirming sample. If it only moved when something changed, the bracket
// would widen to the whole life of the process — a bound that is useless
// exactly when nothing is happening, which is most of the time.
func TestIssuanceBracketNarrowsWithSampling(t *testing.T) {
	oldExpiry := mustTime(t, nextOutage)
	rec := &recorder{}
	ev := &eventRecorder{}
	st := presentAt(oldExpiry)
	w := ledgerWatcher(func(context.Context) Status { return st }, rec, ev)

	start := oldExpiry.Add(-25 * 24 * time.Hour)
	// A day of confirming samples on the same grant.
	for i := 0; i < 96; i++ {
		w.Check(context.Background(), start.Add(time.Duration(i)*15*time.Minute))
	}
	st = presentAt(oldExpiry.Add(30 * 24 * time.Hour))
	w.Check(context.Background(), start.Add(96*15*time.Minute))

	rows := ev.ofType(EventGrantObserved)
	if len(rows) != 2 {
		t.Fatalf("got %d ledger rows, want 2", len(rows))
	}
	if got := detail(t, rows[1], "issuance_window"); got != "15m" {
		t.Errorf("issuance_window = %v after a day of confirming samples, want 15m — "+
			"the lower bound is not advancing on unchanged samples", got)
	}
}

// TestBlindPeriodDoesNotUnobserveThePreviousGrant. reportBlind resets the MAIL
// ratchet on purpose. Sharing that state with the ledger would erase the
// previous grant, and the next readable sample would report a first observation
// — losing the one transition the ledger exists to record.
func TestBlindPeriodDoesNotUnobserveThePreviousGrant(t *testing.T) {
	oldExpiry := mustTime(t, nextOutage)
	rec := &recorder{}
	ev := &eventRecorder{}
	st := presentAt(oldExpiry)
	w := ledgerWatcher(func(context.Context) Status { return st }, rec, ev)

	start := oldExpiry.Add(-25 * 24 * time.Hour)
	w.Check(context.Background(), start)

	// The schema moves for a while: blind.
	st = Status{State: StateUnreadable, Reason: ReasonFieldMissing}
	w.Check(context.Background(), start.Add(15*time.Minute))
	w.Check(context.Background(), start.Add(30*time.Minute))

	// Readable again, with a new grant.
	newExpiry := oldExpiry.Add(30 * 24 * time.Hour)
	st = presentAt(newExpiry)
	w.Check(context.Background(), start.Add(45*time.Minute))

	rows := ev.ofType(EventGrantObserved)
	if len(rows) != 2 {
		t.Fatalf("got %d ledger rows, want 2: %v", len(rows), ev.evs)
	}
	if got := detail(t, rows[1], "transition"); got != true {
		t.Fatalf("the row after a blind spell reported transition=%v — the blind state "+
			"un-observed the previous grant and the transition was lost", got)
	}
	if got := detail(t, rows[1], "previous_expires_at"); got != oldExpiry.Format(time.RFC3339) {
		t.Errorf("previous_expires_at = %v, want %v", got, oldExpiry.Format(time.RFC3339))
	}
	// The bracket correctly widens across the blind window: the last CONFIRMED
	// sighting of the old value is the one before it went blind.
	if got := detail(t, rows[1], "issuance_window"); got != "45m" {
		t.Errorf("issuance_window = %v, want 45m — the bracket must widen across a "+
			"blind spell rather than pretending it saw the old value throughout", got)
	}
}

// TestEveryEventCarriesItsDomain. The field is the half of the fix that acts on
// the rows already being grepped: a `cred_expiry_warned` row read on its own
// must declare that issuance is outside its domain, rather than leaving that to
// be discovered after the wrong conclusion has been relayed to a human.
func TestEveryEventCarriesItsDomain(t *testing.T) {
	cases := []struct {
		name     string
		st       Status
		now      time.Time
		wantType string
		wantDom  string
	}{
		{"warned", presentAt(mustTime(t, nextOutage)), mustTime(t, nextOutage).Add(-time.Hour),
			EventWarned, DomainWarned},
		{"grant", presentAt(mustTime(t, nextOutage)), mustTime(t, nextOutage).Add(-20 * 24 * time.Hour),
			EventGrantObserved, DomainGrantObserved},
		{"blind", Status{State: StateUnreadable, Reason: ReasonFieldMissing}, mustTime(t, nextOutage),
			EventBlind, DomainBlind},
		{"disarmed", Status{State: StateAbsent, Reason: ReasonItemNotFound}, mustTime(t, nextOutage),
			EventDisarmed, DomainDisarmed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			ev := &eventRecorder{}
			w := ledgerWatcher(fixedReader(tc.st), rec, ev)
			w.Check(context.Background(), tc.now)

			rows := ev.ofType(tc.wantType)
			if len(rows) == 0 {
				t.Fatalf("no %s event emitted; got %v", tc.wantType, ev.evs)
			}
			if got := detail(t, rows[0], "domain"); got != tc.wantDom {
				t.Errorf("%s domain = %v, want %q", tc.wantType, got, tc.wantDom)
			}
		})
	}
}

// TestWarnedDomainNamesTheLedger. The pointer is the whole value of the note:
// an analyst who grepped the wrong event must be told which one is right.
func TestWarnedDomainNamesTheLedger(t *testing.T) {
	if !strings.Contains(DomainWarned, EventGrantObserved) {
		t.Errorf("DomainWarned = %q does not name %s, so a reader who grepped the "+
			"wrong event is told it is wrong but not what is right", DomainWarned, EventGrantObserved)
	}
}

// TestFormatSpanRendersABackwardMove. FormatRemaining collapses every
// non-positive duration to "already lapsed", which is right for a warning and
// wrong for a ledger: a grant expiry that moved BACKWARD is a real observation,
// and rendering it as prose about lapsing would hide it.
func TestFormatSpanRendersABackwardMove(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * 24 * time.Hour, "30d 0h"},
		{15 * time.Minute, "15m"},
		{0, "0m"},
		{-2 * time.Hour, "-2h 0m"},
		{-30 * 24 * time.Hour, "-30d 0h"},
	}
	for _, tc := range cases {
		if got := formatSpan(tc.d); got != tc.want {
			t.Errorf("formatSpan(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestBackwardGrantMoveIsStillATransition. A shorter-lived replacement grant is
// unusual, but "unusual" is not "impossible", and a ledger that only records
// moves in one direction has a silence of exactly the kind this item is about.
func TestBackwardGrantMoveIsStillATransition(t *testing.T) {
	expiry := mustTime(t, nextOutage)
	rec := &recorder{}
	ev := &eventRecorder{}
	st := presentAt(expiry)
	w := ledgerWatcher(func(context.Context) Status { return st }, rec, ev)

	start := expiry.Add(-25 * 24 * time.Hour)
	w.Check(context.Background(), start)
	st = presentAt(expiry.Add(-10 * 24 * time.Hour))
	w.Check(context.Background(), start.Add(15*time.Minute))

	rows := ev.ofType(EventGrantObserved)
	if len(rows) != 2 {
		t.Fatalf("got %d ledger rows, want 2", len(rows))
	}
	if got := detail(t, rows[1], "transition"); got != true {
		t.Errorf("a backward expiry move reported transition=%v, want true", got)
	}
	if got := detail(t, rows[1], "expiry_advance"); got != "-10d 0h" {
		t.Errorf("expiry_advance = %v, want -10d 0h", got)
	}
}

// TestLedgerNeverEmitsWithoutACredential. StateAbsent means no claim can be
// made; a ledger row there would be a grant record invented out of nothing.
func TestLedgerNeverEmitsWithoutACredential(t *testing.T) {
	rec := &recorder{}
	ev := &eventRecorder{}
	w := ledgerWatcher(fixedReader(Status{State: StateAbsent, Reason: ReasonNotDarwin}), rec, ev)
	now := mustTime(t, nextOutage)
	for i := 0; i < 5; i++ {
		w.Check(context.Background(), now.Add(time.Duration(i)*time.Hour))
	}
	if got := len(ev.ofType(EventGrantObserved)); got != 0 {
		t.Errorf("got %d ledger rows on a host with no credential, want 0", got)
	}
}
