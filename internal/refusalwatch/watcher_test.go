package refusalwatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refusalstreak"
)

var t0 = time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC)

// streaking is the reading a stopped agent produces.
func streakingReport(now time.Time, streak int, reason refusalstreak.Reason) refusalstreak.Report {
	return refusalstreak.Report{
		State: refusalstreak.StateStreaking, Streak: streak, Reason: reason,
		Reasons: map[refusalstreak.Reason]int{reason: streak},
		Detail:  "Please run /login · API Error: 403 The socket connection was closed unexpectedly",
		First:   now.Add(-30 * time.Minute), Last: now,
		MinStreak: refusalstreak.DefaultMinStreak, ScannedAt: now,
	}
}

func workingReport(now time.Time) refusalstreak.Report {
	return refusalstreak.Report{State: refusalstreak.StateWorking, ScannedAt: now, MinStreak: 3}
}

// recorder collects emitted events without touching the spine.
type recorder struct {
	mu  sync.Mutex
	all []events.Event
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, e)
}

func (r *recorder) ofType(t string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.all {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

// fakeSink records what it was handed and reports whatever it was told to.
type fakeSink struct {
	name      string
	confirm   bool
	mu        sync.Mutex
	delivered []Alarm
}

func (f *fakeSink) Name() string { return f.name }

func (f *fakeSink) Deliver(a Alarm) (Receipt, error) {
	f.mu.Lock()
	f.delivered = append(f.delivered, a)
	f.mu.Unlock()
	if !f.confirm {
		return Receipt{Sink: f.name, Err: "constructed failure"}, errors.New("constructed failure")
	}
	return Receipt{Sink: f.name, Ref: "constructed", Confirmed: true}, nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.delivered)
}

// newWatcher builds a watcher whose reader is a table, so the tests are about
// the ALARM and not about transcript parsing (which refusalstreak's own tests
// cover).
func newWatcher(t *testing.T, rec *recorder, sinks []Sink, reports map[string]refusalstreak.Report, opts Options) *Watcher {
	t.Helper()
	opts.Emit = rec.emit
	opts.Sinks = sinks
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	opts.Home = t.TempDir()
	if opts.Globs == nil {
		opts.Globs = func(workdir string) []string { return []string{"whatever"} }
	}
	if opts.Targets == nil {
		names := make([]string, 0, len(reports))
		for n := range reports {
			names = append(names, n)
		}
		opts.Targets = func() []Target {
			out := make([]Target, 0, len(names))
			for _, n := range names {
				out = append(out, Target{Name: n, Identity: "crew-" + n, Workdir: "/w/" + n})
			}
			return out
		}
	}
	if opts.Scan == nil {
		opts.Scan = func(home string, globs []string, o refusalstreak.Options) refusalstreak.Report {
			// The table is keyed by the glob the Globs func returned, because the
			// workdir is not carried into Scan.
			return reports[strings.TrimPrefix(globs[0], "glob:")]
		}
	}
	return New(opts)
}

// perAgent wires Globs so each target's reading is looked up by name.
func perAgent(reports map[string]refusalstreak.Report) (func(string) []string, func(string, []string, refusalstreak.Options) refusalstreak.Report) {
	globs := func(workdir string) []string { return []string{"glob:" + filepath.Base(workdir)} }
	scan := func(home string, g []string, o refusalstreak.Options) refusalstreak.Report {
		return reports[strings.TrimPrefix(g[0], "glob:")]
	}
	return globs, scan
}

// -------------------------------------------------- THE ACCEPTANCE CRITERION

// A notification mechanism verified against a healthy fleet is verified in the
// one condition where it is not needed. So this test constructs the DOWN
// condition — every agent streaking, no agent capable of carrying a message, a
// macguffin store with nothing in it but the `human` mailbox — and asserts the
// bytes land in the maildir the out-of-process notifier polls.
func TestTheAlarmReachesTheHumanMailboxWithTheFLEETDOWN(t *testing.T) {
	bin := requireMG(t)
	root := newStore(t, bin)
	mustMG(t, bin, root, "mail", "register", "human")

	reports := map[string]refusalstreak.Report{
		"mayor":   streakingReport(t0, 16, refusalstreak.ReasonLoginPrompt),
		"pm-pogo": streakingReport(t0, 11, refusalstreak.ReasonLoginPrompt),
		"pa":      streakingReport(t0, 11, refusalstreak.ReasonLoginPrompt),
	}
	globs, scan := perAgent(reports)
	rec := &recorder{}
	w := newWatcher(t, rec, []Sink{MailSink{Root: root, Bin: bin, To: "human", From: "pogod"}}, nil, Options{
		Globs: globs, Scan: scan,
		Targets: func() []Target {
			return []Target{
				{Name: "mayor", Workdir: "/w/mayor"},
				{Name: "pm-pogo", Workdir: "/w/pm-pogo"},
				{Name: "pa", Workdir: "/w/pa"},
			}
		},
	})

	w.Check(t0)

	// The receipt is this package's own claim. The maildir is the notifier's
	// input, and it is the only thing that makes this a delivery.
	entries := maildir(t, root, "human")
	if len(entries) != 1 {
		t.Fatalf("%d message(s) in <root>/mail/human/new, want 1 — with the fleet down this is the ONLY path to a person", len(entries))
	}
	body, err := os.ReadFile(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{"FLEET STOPPED", "mayor", "pm-pogo", "pa", "16 consecutive failing turns"} {
		if !strings.Contains(got, want) {
			t.Errorf("the delivered message does not contain %q; it is what a woken human reads:\n%s", want, got)
		}
	}

	// And the event names WHO took it, so "did anyone learn" is answerable from
	// the log rather than reconstructed.
	alarms := rec.ofType(EventDetected)
	if len(alarms) != 1 {
		t.Fatalf("%d %s event(s), want 1", len(alarms), EventDetected)
	}
	routed, _ := alarms[0].Details["routed_to"].([]string)
	if len(routed) != 1 || !strings.HasPrefix(routed[0], "mg-mail:") {
		t.Errorf("routed_to = %v, want the mail sink named", alarms[0].Details["routed_to"])
	}
	if got := rec.ofType(EventUndelivered); len(got) != 0 {
		t.Errorf("%d undelivered event(s) on a delivery that landed", len(got))
	}
}

// The matched control for the test above. If a healthy fleet also produced a
// message in that maildir, the positive arm would be green for a reason that has
// nothing to do with the detector.
func TestAHealthyFleetProducesNoAlarmAtAll(t *testing.T) {
	bin := requireMG(t)
	root := newStore(t, bin)
	mustMG(t, bin, root, "mail", "register", "human")

	reports := map[string]refusalstreak.Report{"mayor": workingReport(t0), "pa": workingReport(t0)}
	globs, scan := perAgent(reports)
	rec := &recorder{}
	w := newWatcher(t, rec, []Sink{MailSink{Root: root, Bin: bin, To: "human", From: "pogod"}}, nil, Options{
		Globs: globs, Scan: scan,
		Targets: func() []Target {
			return []Target{{Name: "mayor", Workdir: "/w/mayor"}, {Name: "pa", Workdir: "/w/pa"}}
		},
	})
	w.Check(t0)

	if n := len(maildir(t, root, "human")); n != 0 {
		t.Fatalf("%d message(s) delivered for a working fleet", n)
	}
	if n := len(rec.ofType(EventDetected)) + len(rec.ofType(EventUndelivered)); n != 0 {
		t.Fatalf("%d alarm event(s) for a working fleet", n)
	}
}

// ------------------------------------------------- the routed_to=nobody state

// mg-3222 measured what a fleet does with an alarm nobody takes: sixteen correct
// detections over 3h55m, every one "routed_to": "nobody", inside a normal-looking
// fired event. So a failed delivery here is its OWN event type, it is retried on
// every scan with no floor, and it carries how long it has been silent.
func TestAnUndeliveredAlarmGetsItsOwnEventTypeAndIsRetriedEveryScan(t *testing.T) {
	reports := map[string]refusalstreak.Report{"mayor": streakingReport(t0, 4, refusalstreak.ReasonEntitlement)}
	globs, scan := perAgent(reports)
	rec := &recorder{}
	dead := &fakeSink{name: "dead-channel", confirm: false}
	w := newWatcher(t, rec, []Sink{dead}, nil, Options{
		Globs: globs, Scan: scan, Interval: time.Minute,
		Targets: func() []Target { return []Target{{Name: "mayor", Workdir: "/w/mayor"}} },
	})

	w.Check(t0)
	w.Check(t0.Add(2 * time.Minute))
	w.Check(t0.Add(4 * time.Minute))

	if dead.count() != 3 {
		t.Fatalf("the sink was tried %d time(s) across 3 scans; an alarm that has reached NOBODY must not be "+
			"floored — that floor is what made sixteen detections sixteen silences", dead.count())
	}
	und := rec.ofType(EventUndelivered)
	if len(und) != 3 {
		t.Fatalf("%d %s event(s), want 3", len(und), EventUndelivered)
	}
	if got := rec.ofType(EventDetected); len(got) != 0 {
		t.Fatalf("%d success event(s) emitted for deliveries that never landed", len(got))
	}
	last := und[2].Details
	if last["routed_to"] != "nobody" {
		t.Errorf("routed_to = %v, want %q", last["routed_to"], "nobody")
	}
	if got, _ := last["attempts"].(int); got != 3 {
		t.Errorf("attempts = %v, want 3", last["attempts"])
	}
	if got, _ := last["silent_seconds"].(int); got != 240 {
		t.Errorf("silent_seconds = %v, want 240 — the duration of a silent alarm must be a number, not something "+
			"a reader reconstructs from repeated log lines", last["silent_seconds"])
	}
}

// The floor exists, and it applies ONLY once something has actually taken the
// alarm. This is the pair of the test above: the two branches must differ.
func TestTheFloorAppliesToADeliveredAlarmAndNotToAnUndeliveredOne(t *testing.T) {
	reports := map[string]refusalstreak.Report{"mayor": streakingReport(t0, 4, refusalstreak.ReasonEntitlement)}
	globs, scan := perAgent(reports)
	live := &fakeSink{name: "live-channel", confirm: true}
	w := newWatcher(t, &recorder{}, []Sink{live}, nil, Options{
		Globs: globs, Scan: scan, Interval: time.Minute, MinAlarmInterval: time.Hour,
		Targets: func() []Target { return []Target{{Name: "mayor", Workdir: "/w/mayor"}} },
	})
	w.Check(t0)
	w.Check(t0.Add(2 * time.Minute))
	w.Check(t0.Add(30 * time.Minute))
	if live.count() != 1 {
		t.Fatalf("the sink was tried %d time(s) inside the floor, want 1", live.count())
	}
	w.Check(t0.Add(90 * time.Minute))
	if live.count() != 2 {
		t.Fatalf("the sink was tried %d time(s) after the floor elapsed, want 2", live.count())
	}
}

// A second agent joining is new information about the SCOPE of the outage, and
// the floor must not swallow it.
func TestARosterChangeBypassesTheFloor(t *testing.T) {
	reports := map[string]refusalstreak.Report{
		"mayor": streakingReport(t0, 4, refusalstreak.ReasonEntitlement),
		"pa":    workingReport(t0),
	}
	globs, scan := perAgent(reports)
	live := &fakeSink{name: "live-channel", confirm: true}
	w := newWatcher(t, &recorder{}, []Sink{live}, nil, Options{
		Globs: globs, Scan: scan, Interval: time.Minute, MinAlarmInterval: time.Hour,
		Targets: func() []Target {
			return []Target{{Name: "mayor", Workdir: "/w/mayor"}, {Name: "pa", Workdir: "/w/pa"}}
		},
	})
	w.Check(t0)
	if live.count() != 1 {
		t.Fatalf("first alarm not sent (count=%d)", live.count())
	}
	reports["pa"] = streakingReport(t0.Add(2*time.Minute), 3, refusalstreak.ReasonEntitlement)
	w.Check(t0.Add(2 * time.Minute))
	if live.count() != 2 {
		t.Fatalf("the sink was tried %d time(s) after the roster grew inside the floor, want 2", live.count())
	}
	got := live.delivered[1]
	if len(got.Agents) != 2 || got.Agents[0] != "mayor" || got.Agents[1] != "pa" {
		t.Errorf("second alarm roster = %v, want [mayor pa] sorted", got.Agents)
	}
}

// ------------------------------------------------------------- the bounds

// A run is positional and has no window, so the ONE thing time can say about it
// is whether it is still going. 90m clears every gap observed between two
// consecutive failing turns in the corpus (max 83m4s); a run whose last failure
// is hours old is history.
func TestAStaleRunIsHistoryAndDoesNotAlarm(t *testing.T) {
	stale := streakingReport(t0.Add(-4*time.Hour), 300, refusalstreak.ReasonSpendLimit)
	reports := map[string]refusalstreak.Report{"mayor": stale}
	globs, scan := perAgent(reports)
	live := &fakeSink{name: "live-channel", confirm: true}
	w := newWatcher(t, &recorder{}, []Sink{live}, nil, Options{
		Globs: globs, Scan: scan,
		Targets: func() []Target { return []Target{{Name: "mayor", Workdir: "/w/mayor"}} },
	})
	w.Check(t0)
	if live.count() != 0 {
		t.Fatalf("alarmed on a run whose last failing turn was 4h old (count=%d)", live.count())
	}
	// The positive control: the same 300-turn run, still being written.
	reports["mayor"] = streakingReport(t0, 300, refusalstreak.ReasonSpendLimit)
	w.Check(t0.Add(10 * time.Minute))
	if live.count() != 1 {
		t.Fatalf("did NOT alarm on the same run when it was live (count=%d); the freshness bound is swallowing "+
			"real outages, not just history", live.count())
	}
}

func TestClearingEmitsWhenEveryAgentIsWorkingAgain(t *testing.T) {
	reports := map[string]refusalstreak.Report{"mayor": streakingReport(t0, 4, refusalstreak.ReasonTimeout)}
	globs, scan := perAgent(reports)
	rec := &recorder{}
	w := newWatcher(t, rec, []Sink{&fakeSink{name: "live", confirm: true}}, nil, Options{
		Globs: globs, Scan: scan, Interval: time.Minute,
		Targets: func() []Target { return []Target{{Name: "mayor", Workdir: "/w/mayor"}} },
	})
	w.Check(t0)
	reports["mayor"] = workingReport(t0.Add(10 * time.Minute))
	w.Check(t0.Add(10 * time.Minute))

	cleared := rec.ofType(EventCleared)
	if len(cleared) != 1 {
		t.Fatalf("%d %s event(s), want 1", len(cleared), EventCleared)
	}
	if got, _ := cleared[0].Details["was_delivered"].(bool); !got {
		t.Error("the clear does not record whether the alarm it closes ever reached anybody")
	}
	// And it does not repeat on subsequent quiet ticks.
	w.Check(t0.Add(20 * time.Minute))
	if n := len(rec.ofType(EventCleared)); n != 1 {
		t.Errorf("%d clear events after two quiet ticks, want 1", n)
	}
}

// An agent that leaves the registry leaves the roster: its transcript is no
// longer evidence about a live process.
func TestADepartedAgentLeavesTheRoster(t *testing.T) {
	reports := map[string]refusalstreak.Report{
		"mayor": streakingReport(t0, 4, refusalstreak.ReasonTimeout),
		"pa":    streakingReport(t0, 4, refusalstreak.ReasonTimeout),
	}
	globs, scan := perAgent(reports)
	live := &fakeSink{name: "live", confirm: true}
	present := []Target{{Name: "mayor", Workdir: "/w/mayor"}, {Name: "pa", Workdir: "/w/pa"}}
	w := newWatcher(t, &recorder{}, []Sink{live}, nil, Options{
		Globs: globs, Scan: scan, Interval: time.Minute, MinAlarmInterval: time.Hour,
		Targets: func() []Target { return present },
	})
	w.Check(t0)
	present = present[:1]
	w.Check(t0.Add(2 * time.Minute))
	if live.count() != 2 {
		t.Fatalf("the sink was tried %d time(s), want 2 — a shrinking roster is a roster change", live.count())
	}
	if got := live.delivered[1].Agents; len(got) != 1 || got[0] != "mayor" {
		t.Errorf("second alarm roster = %v, want [mayor]", got)
	}
}

// The watcher must be inert rather than panicking when pogod could not wire it.
func TestAnUnwiredWatcherIsInert(t *testing.T) {
	w := New(Options{Emit: func(events.Event) { t.Error("an unwired watcher emitted") }})
	w.Check(t0)
	if _, ok := w.Report("anyone"); ok {
		t.Error("an unwired watcher holds a report")
	}
}

// Describe is what an operator reads to answer "why was I not alarmed". Every
// bound that can make this channel say LESS must be in it.
func TestDescribeStatesEveryBound(t *testing.T) {
	w := New(Options{Sinks: []Sink{&fakeSink{name: "mg-mail:human"}, LedgerSink{}}})
	got := w.Describe()
	for _, want := range []string{"min-streak=3", "interval=5m", "freshness=1h30m", "alarm-floor=1h", "mg-mail:human", "ledger"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, want %q in it", got, want)
		}
	}
}

// ------------------------------------------------------------------ helpers

func maildir(t *testing.T, root, box string) []string {
	t.Helper()
	dir := filepath.Join(root, "mail", box, "new")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// An episode that clears having never reached anybody is this item's own defect
// closing quietly. Nothing can escalate it — the channels are what failed — so
// the one thing that must survive is that the case is COUNTABLE.
func TestAnEpisodeThatClearedWithoutEverReachingAnybodyIsRecordedAsSuch(t *testing.T) {
	reports := map[string]refusalstreak.Report{"mayor": streakingReport(t0, 9, refusalstreak.ReasonTimeout)}
	globs, scan := perAgent(reports)
	rec := &recorder{}
	var logged []string
	w := newWatcher(t, rec, []Sink{&fakeSink{name: "dead"}}, nil, Options{
		Globs: globs, Scan: scan, Interval: time.Minute,
		Logf:    func(f string, a ...any) { logged = append(logged, f) },
		Targets: func() []Target { return []Target{{Name: "mayor", Workdir: "/w/mayor"}} },
	})
	w.Check(t0)
	reports["mayor"] = workingReport(t0.Add(10 * time.Minute))
	w.Check(t0.Add(10 * time.Minute))

	cleared := rec.ofType(EventCleared)
	if len(cleared) != 1 {
		t.Fatalf("%d clear event(s), want 1", len(cleared))
	}
	if got, _ := cleared[0].Details["was_delivered"].(bool); got {
		t.Fatal("the clear claims the alarm was delivered; nothing ever confirmed it")
	}
	var loud bool
	for _, l := range logged {
		if strings.Contains(l, "CLEARED WITHOUT EVER REACHING ANYBODY") {
			loud = true
		}
	}
	if !loud {
		t.Errorf("the daemon log does not distinguish an episode that cleared unheard from one that was acted on; "+
			"lines were %v", logged)
	}
}

// pogod calls Check in a goroutine on every ~30s tick and a sink shells out to
// `mg`, so two ticks can be inside a delivery at once — and the undelivered path
// has no floor by design, so nothing else would stop them. The fix for a
// duplicate page must not be a floor on the undelivered path.
func TestConcurrentChecksDoNotDoubleDeliverTheSameAlarm(t *testing.T) {
	reports := map[string]refusalstreak.Report{"mayor": streakingReport(t0, 5, refusalstreak.ReasonTimeout)}
	globs, scan := perAgent(reports)
	release := make(chan struct{})
	slow := &blockingSink{name: "slow", gate: release}
	w := newWatcher(t, &recorder{}, []Sink{slow}, nil, Options{
		Globs: globs, Scan: scan, Interval: time.Nanosecond,
		Targets: func() []Target { return []Target{{Name: "mayor", Workdir: "/w/mayor"}} },
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); w.Check(t0.Add(time.Duration(i) * time.Second)) }(i)
	}
	// Let the goroutines pile up on the gate, then let them all go.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := slow.count(); n > 1 {
		t.Fatalf("%d concurrent deliveries of one episode, want 1 — a slow channel must not become duplicate pages", n)
	}
}

// blockingSink holds its first delivery open until the gate closes, so several
// Checks are genuinely in flight at once.
type blockingSink struct {
	name string
	gate <-chan struct{}
	mu   sync.Mutex
	n    int
}

func (b *blockingSink) Name() string { return b.name }

func (b *blockingSink) Deliver(Alarm) (Receipt, error) {
	b.mu.Lock()
	b.n++
	b.mu.Unlock()
	<-b.gate
	return Receipt{Sink: b.name, Confirmed: true}, nil
}

func (b *blockingSink) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}
