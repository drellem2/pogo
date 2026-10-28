package blindwatch

import (
	"reflect"
	"testing"
	"time"
)

// drellem2/pogo#244: `[blind_watch] mail = false` and `out_of_band = false`
// withhold blind-watch's mail while the runner keeps sampling and every event
// is still emitted — and a withheld send is never recorded as delivered.

func switchedWatcher(r *recorder, mailOff, oobOff bool, blind *string, now time.Time) *Watcher {
	return New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Interval: time.Nanosecond, HoldDown: -1,
		Coordinator: "mayor", HumanBox: "human",
		MailOff: mailOff, OutOfBandOff: oobOff,
		Source: func(at time.Time) (Snapshot, error) {
			s := Snapshot{Detector: "wedge-watch", SampledAt: at, Examined: 5}
			if *blind != "" {
				s.Blind = []Target{{Name: *blind, Since: now.Add(-6 * time.Hour)}}
			}
			return s, nil
		},
	})
}

func TestMailOffSendsNothingAndRecordsNotNotified(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	blind := "pm-riemann"
	w := switchedWatcher(r, true, false, &blind, now)
	w.Check(now)
	blind = ""
	w.Check(now.Add(time.Second))

	if len(r.mails) != 0 {
		t.Fatalf("mailed %v with [blind_watch] mail = false", r.to())
	}
	for _, typ := range []string{EventFinding, EventClear} {
		evs := r.typed(typ)
		if len(evs) != 1 {
			t.Fatalf("%s events = %d, want 1 — mail off must not stop the record", typ, len(evs))
		}
		d := evs[0].Details
		if d["notified"] != false || d["reason"] != "mail_disabled" || d["mail_suppressed"] != true {
			t.Errorf("%s details = %v, want notified=false reason=mail_disabled mail_suppressed=true", typ, d)
		}
	}
}

// A finding routed to the coordinator is untouched by out_of_band = false.
func TestOutOfBandOffKeepsTheCoordinatorCopy(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	blind := "pm-riemann"
	w := switchedWatcher(r, false, true, &blind, now)
	w.Check(now)

	if got := r.to(); !reflect.DeepEqual(got, []string{"mayor"}) {
		t.Fatalf("recipients = %v, want [mayor]", got)
	}
	d := r.typed(EventFinding)[0].Details
	if !reflect.DeepEqual(d["notified"], []string{"mayor"}) || d["mail_suppressed"] != false {
		t.Errorf("details = %v, want notified=[mayor] mail_suppressed=false", d)
	}
}

// A finding routed ONLY to the escalation box becomes event-only, and its
// clear does not reach the box that never heard it.
func TestOutOfBandOffMakesAnEscalationOnlyFindingEventOnly(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	blind := "mayor" // names the coordinator: escalation box alone
	w := switchedWatcher(r, false, true, &blind, now)
	w.Check(now)

	if len(r.mails) != 0 {
		t.Fatalf("mailed %v with [blind_watch] out_of_band = false", r.to())
	}
	fs := r.typed(EventFinding)
	if len(fs) != 1 {
		t.Fatalf("blind_watch_finding events = %d, want 1", len(fs))
	}
	d := fs[0].Details
	if d["notified"] != false || d["reason"] != "mail_disabled" || d["mail_suppressed"] != true ||
		!reflect.DeepEqual(d["suppressed"], []string{"human"}) {
		t.Errorf("details = %v, want notified=false reason=mail_disabled mail_suppressed=true suppressed=[human]", d)
	}

	blind = ""
	w.Check(now.Add(time.Second))
	if got := r.to(); !reflect.DeepEqual(got, []string{"mayor"}) {
		t.Errorf("clear recipients = %v, want [mayor] — the human box was never told", got)
	}
	if len(r.typed(EventClear)) != 1 {
		t.Errorf("blind_watch_clear events = %d, want 1", len(r.typed(EventClear)))
	}
}

// The default (both switches on) still mails the escalation box and records it.
func TestDefaultSwitchesRecordWhoWasMailed(t *testing.T) {
	now := time.Now().UTC()
	r := &recorder{}
	blind := "mayor"
	w := switchedWatcher(r, false, false, &blind, now)
	w.Check(now)

	if got := r.to(); !reflect.DeepEqual(got, []string{"human"}) {
		t.Fatalf("recipients = %v, want [human]", got)
	}
	d := r.typed(EventFinding)[0].Details
	if !reflect.DeepEqual(d["notified"], []string{"human"}) || d["mail_suppressed"] != false {
		t.Errorf("details = %v, want notified=[human] mail_suppressed=false", d)
	}
}
