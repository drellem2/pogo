package turnwatch

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/turnlog"
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

// report builds a reading in which each named agent is red with the given
// verdict and every other agent is live.
func report(now time.Time, red map[string]turnlog.Verdict, live ...string) turnlog.Report {
	rep := turnlog.Report{Dir: "/x/turnlog", Now: now, MaxAge: "3h0m0s"}
	started := now.Add(-24 * time.Hour) // well outside any grace window
	for name, v := range red {
		st := turnlog.State{Agent: name, Type: "crew", Verdict: v, Started: started}
		if v == turnlog.VerdictStale {
			st.Last = now.Add(-22 * time.Hour)
			st.AgeSecs = (22 * time.Hour).Seconds()
		}
		rep.Agents = append(rep.Agents, st)
		rep.Findings++
	}
	for _, name := range live {
		rep.Agents = append(rep.Agents, turnlog.State{
			Agent: name, Type: "crew", Verdict: turnlog.VerdictLive,
			Started: started, Last: now.Add(-time.Minute), AgeSecs: 60,
		})
		rep.Live++
	}
	return rep
}

func newTestWatcher(r *recorder, rep func(now time.Time) (turnlog.Report, error)) *Watcher {
	return New(Options{
		Enabled: true, Scan: rep, Mail: r.mail, Emit: r.emit,
		Coordinator: "mayor", HumanBox: "human",
		HoldDown: -1, // announce on the first confirmed observation
		Interval: time.Nanosecond,
	})
}

// TestCoordinatorFindingNeverReachesTheCoordinator is the load-bearing test in
// this package.
//
// Delivering "mayor has completed no turn in 22 hours" to mayor is a message
// that arrives only when the claim is false. It is also the natural thing to
// write — every other detector in this tree mails the coordinator, because for
// every other finding the coordinator is the agent that can act. The amendment
// that created this package exists because that default, applied here, would
// reproduce the exact circularity that let a 22-hour outage read green: every
// fleet-wide check is mayor-owned, so nothing routed through mayor can report
// mayor.
func TestCoordinatorFindingNeverReachesTheCoordinator(t *testing.T) {
	now := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	r := &recorder{}
	w := newTestWatcher(r, func(time.Time) (turnlog.Report, error) {
		return report(now, map[string]turnlog.Verdict{"mayor": turnlog.VerdictStale}, "pa", "architect"), nil
	})
	w.Check(now)

	got := r.to()
	for _, to := range got {
		if to == "mayor" {
			t.Fatalf("a finding ABOUT the coordinator was mailed TO the coordinator (recipients=%v). "+
				"That notice arrives only if the finding is false, which is the circularity this "+
				"package exists to break", got)
		}
	}
	if len(got) != 1 || got[0] != "human" {
		t.Fatalf("recipients = %v, want exactly [human]", got)
	}
	if !strings.Contains(r.mails[0].subject, "mayor") {
		t.Errorf("subject does not name the coordinator: %q", r.mails[0].subject)
	}
	if !strings.Contains(r.mails[0].body, "cannot report") {
		t.Errorf("notice does not explain why it did not go to the coordinator:\n%s", r.mails[0].body)
	}
}

// TestNonCoordinatorFindingGoesToTheCoordinator: the routing rule is a
// carve-out, not a blanket redirect. mayor is the agent that acts on a stalled
// pa, and sending everything to the human box would make this detector
// indistinguishable from a pager.
func TestNonCoordinatorFindingGoesToTheCoordinator(t *testing.T) {
	now := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	r := &recorder{}
	w := newTestWatcher(r, func(time.Time) (turnlog.Report, error) {
		return report(now, map[string]turnlog.Verdict{"pa": turnlog.VerdictSilent}, "mayor", "architect"), nil
	})
	w.Check(now)
	if got := r.to(); len(got) != 1 || got[0] != "mayor" {
		t.Fatalf("recipients = %v, want exactly [mayor]", got)
	}
}

// TestCoordinatorSortsFirst. A notice naming four agents is skimmed, and the
// coordinator is the only one whose failure hides itself from every other
// detector on the machine.
func TestCoordinatorSortsFirst(t *testing.T) {
	now := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	r := &recorder{}
	w := newTestWatcher(r, func(time.Time) (turnlog.Report, error) {
		return report(now, map[string]turnlog.Verdict{
			"architect": turnlog.VerdictSilent,
			"mayor":     turnlog.VerdictSilent,
			"pa":        turnlog.VerdictSilent,
		}), nil
	})
	w.Check(now)
	body := r.mails[0].body
	if strings.Index(body, "mayor") > strings.Index(body, "architect") {
		t.Errorf("the coordinator is not listed first:\n%s", body)
	}
}

// TestGraceWindowSpareschecksAgentThatJustStarted. An agent thirty seconds old
// with no completed turn has not failed to complete one; it has not had time.
// Judging it makes every spawn a finding, which is how a detector becomes
// something people filter out.
func TestGraceWindowSparesAgentThatJustStarted(t *testing.T) {
	now := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Coordinator: "mayor", HumanBox: "human", HoldDown: -1, Interval: time.Nanosecond,
		Grace: 45 * time.Minute,
		Scan: func(time.Time) (turnlog.Report, error) {
			return turnlog.Report{Dir: "/x", Agents: []turnlog.State{{
				Agent: "architect", Verdict: turnlog.VerdictSilent, Started: now.Add(-30 * time.Second),
			}}, Findings: 1}, nil
		},
	})
	w.Check(now)
	if got := r.to(); len(got) != 0 {
		t.Errorf("a 30-second-old agent was reported: %v", got)
	}
}

// TestScanErrorIsNotACleanFleet. Losing the reading is a real failure, and it
// must be distinguishable in the event log from a fleet that is fine. That
// distinction — "quiet" versus "blind" — is the founding bug of this lineage.
func TestScanErrorIsNotACleanFleet(t *testing.T) {
	r := &recorder{}
	w := newTestWatcher(r, func(time.Time) (turnlog.Report, error) {
		return turnlog.Report{}, fmt.Errorf("registry unreachable")
	})
	w.Check(time.Now())
	if len(r.to()) != 0 {
		t.Errorf("a failed scan produced a finding mail: %v", r.to())
	}
	var sawError bool
	for _, e := range r.events {
		if e.EventType == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("a failed scan emitted no %s event; a blind detector is indistinguishable from a quiet one", EventError)
	}
}

// TestHoldDownDelaysTheFirstNotice, then the finding is announced once it has
// persisted. Without it, every pogod restart announces the whole fleet in the
// gap between spawn and the first completed turn.
func TestHoldDownDelaysTheFirstNotice(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	r := &recorder{}
	scan := func(now time.Time) (turnlog.Report, error) {
		return report(now, map[string]turnlog.Verdict{"pa": turnlog.VerdictStale}, "mayor"), nil
	}
	w := New(Options{
		Enabled: true, Scan: scan, Mail: r.mail, Emit: r.emit,
		Coordinator: "mayor", HumanBox: "human",
		HoldDown: 30 * time.Minute, Interval: time.Minute,
	})
	w.Check(t0)
	if len(r.to()) != 0 {
		t.Fatalf("announced inside the hold-down: %v", r.to())
	}
	w.Check(t0.Add(31 * time.Minute))
	if got := r.to(); len(got) != 1 {
		t.Fatalf("nothing announced after the hold-down elapsed: %v", got)
	}
}

// TestRenotifyThrottlesAnUnchangedRoster. An alert that repeats every interval
// is an alert people filter, which is the failure this detector's own subject
// matter keeps producing one level up.
func TestRenotifyThrottlesAnUnchangedRoster(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Coordinator: "mayor", HumanBox: "human", HoldDown: -1,
		Interval: time.Minute, RenotifyAfter: 6 * time.Hour,
		Scan: func(now time.Time) (turnlog.Report, error) {
			return report(now, map[string]turnlog.Verdict{"pa": turnlog.VerdictStale}, "mayor"), nil
		},
	})
	w.Check(t0)
	w.Check(t0.Add(2 * time.Minute))
	w.Check(t0.Add(4 * time.Minute))
	if got := len(r.to()); got != 1 {
		t.Errorf("mailed %d times for an unchanged roster, want 1", got)
	}
	// A changed roster is news immediately.
	w2 := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Coordinator: "mayor", HumanBox: "human", HoldDown: -1,
		Interval: time.Minute, RenotifyAfter: 6 * time.Hour,
		Scan: func(now time.Time) (turnlog.Report, error) {
			if now.After(t0) {
				return report(now, map[string]turnlog.Verdict{
					"pa": turnlog.VerdictStale, "architect": turnlog.VerdictSilent}, "mayor"), nil
			}
			return report(now, map[string]turnlog.Verdict{"pa": turnlog.VerdictStale}, "mayor"), nil
		},
	})
	before := len(r.to())
	w2.Check(t0)
	w2.Check(t0.Add(2 * time.Minute))
	if got := len(r.to()) - before; got != 2 {
		t.Errorf("a changed roster mailed %d times, want 2 (the change is news)", got)
	}
}

// TestPogodStartSuppressesOneHoldDown. A pogod restart bounces the fleet, so
// every agent legitimately has no completed turn for a while afterwards.
func TestPogodStartSuppressesOneHoldDown(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	r := &recorder{}
	w := New(Options{
		Enabled: true, Mail: r.mail, Emit: r.emit,
		Coordinator: "mayor", HumanBox: "human",
		HoldDown: 30 * time.Minute, Interval: time.Nanosecond, StartedAt: t0,
		Scan: func(now time.Time) (turnlog.Report, error) {
			return report(now, map[string]turnlog.Verdict{"pa": turnlog.VerdictSilent}, "mayor"), nil
		},
	})
	w.Check(t0.Add(5 * time.Minute))
	if len(r.to()) != 0 {
		t.Errorf("announced within one hold-down of pogod start: %v", r.to())
	}
}

// TestDisabledWatcherIsInert and a nil one does not panic — pogod builds these
// conditionally.
func TestDisabledWatcherIsInert(t *testing.T) {
	var nilW *Watcher
	nilW.Check(time.Now()) // must not panic

	r := &recorder{}
	w := New(Options{Enabled: false, Mail: r.mail, Emit: r.emit,
		Scan: func(time.Time) (turnlog.Report, error) {
			t.Fatal("scanned while disabled")
			return turnlog.Report{}, nil
		}})
	w.Check(time.Now())
}
