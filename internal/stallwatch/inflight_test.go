package stallwatch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// fakeWorkers is a scripted Workers probe. It counts calls so a test can assert
// the snapshot is taken once per tick rather than once per check — three checks
// disagreeing about who is alive within one sample is the shape this fix exists
// to remove, not to reproduce internally.
type fakeWorkers struct {
	mu        sync.Mutex
	flight    WorkInFlight
	unknown   bool
	callCount int
}

func (f *fakeWorkers) InFlight() (WorkInFlight, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	if f.unknown {
		return WorkInFlight{}, false
	}
	return f.flight, true
}

func (f *fakeWorkers) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

// workedBy builds a snapshot naming one worker on one item.
func workedBy(item, name string, pid int, evidence string) WorkInFlight {
	return WorkInFlight{Items: map[string]InFlightWorker{
		item: {Name: name, PID: pid, Evidence: evidence},
	}}
}

// flightEnv is testEnv plus an in-flight probe.
func flightEnv(t *testing.T, cfg config.StallWatchConfig, workers Workers) (*Watcher, *recorder, string) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	mailRoot := filepath.Join(root, "mail")
	for _, d := range []string{"available", "claimed", "done"} {
		if err := os.MkdirAll(filepath.Join(workRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	w := New(cfg, Options{
		WorkRoot: workRoot,
		MailRoot: mailRoot,
		Nudge:    rec.nudge,
		Emit:     rec.emit,
		Workers:  workers,
	})
	return w, rec, workRoot
}

// categories returns the category of every event the recorder saw, in order.
func categories(rec *recorder) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, 0, len(rec.events))
	for _, e := range rec.events {
		cat, _ := e.Details["category"].(string)
		out = append(out, cat)
	}
	return out
}

// TestAgedItemWithALiveWorkerIsNotCalledNeglected is the defect verbatim. On
// 2026-08-07 a claim-at-spawn failed open, the item stayed in available/, and
// the standard notice went on reporting it as unclaimed while a polecat worked
// it — which is what invites a coordinator to dispatch a second one.
func TestAgedItemWithALiveWorkerIsNotCalledNeglected(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-worked", "pc-fronted", 4242, "registry")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-worked", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryWorkedUnclaimed {
		t.Fatalf("categories = %v, want exactly [%s] — the dispatch notice must not fire on a worked item",
			got, categoryWorkedUnclaimed)
	}
	msg := rec.nudges[0].message
	for _, want := range []string{"mg-worked", "pc-fronted", "pid 4242", "NOT a dispatch request"} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice = %q, want it to contain %q", msg, want)
		}
	}
	// The repair must name the WORKER's pid: a bare `mg claim` stamps the
	// caller's, and pogo reads that pid as the polecat's in two places.
	if !strings.Contains(msg, "mg claim mg-worked --pid 4242") {
		t.Errorf("notice = %q, want the repair command to carry the worker's pid", msg)
	}
}

// TestPriorityWakeDoesNotUrgeDispatchOfWorkedItem covers the surface the harm
// actually travelled on: "claim or dispatch now" is the most imperative wording
// the component emits and has the shortest cooldown, so a high-priority item
// whose claim failed open drew the fastest push toward a second polecat.
func TestPriorityWakeDoesNotUrgeDispatchOfWorkedItem(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-hi", "pc-busy", 77, "registry")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi", "mayor", "high", now.Add(-5*time.Minute))

	w.Check(now)

	for _, cat := range categories(rec) {
		if cat == categoryPriorityWake {
			t.Fatalf("priority-wake fired on an item a live worker holds: %q", rec.nudges[0].message)
		}
	}
	if !strings.Contains(strings.Join(nudgeMessages(rec), " "), "LIVE WORKER") {
		t.Errorf("the item went unreported entirely; it must be re-reported with the opposite remedy: %v",
			nudgeMessages(rec))
	}
}

// TestUnworkedItemStillFires is the positive control. Without it this fix could
// be a detector that stopped detecting, which reads identically from outside.
func TestUnworkedItemStillFires(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-other", "pc-elsewhere", 5, "registry")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-alone", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want [%s] — an item nobody is on is still neglected",
			got, categoryUnclaimedItems)
	}
	if !strings.Contains(rec.nudges[0].message, "mg-alone") {
		t.Errorf("notice = %q, want it to name the aging item", rec.nudges[0].message)
	}
}

// TestUnknownProbeKeepsThePreFixBehaviour pins the failure DIRECTION. A probe
// that cannot answer must leave every check exactly as it was: a false
// "dispatch this" is self-correcting (the coordinator checks `pogo agent list`,
// or the spawn is refused), while a false silence looks like a healthy queue —
// the mistake this package has made twice (mg-4bd4, mg-1693).
func TestUnknownProbeKeepsThePreFixBehaviour(t *testing.T) {
	probe := &fakeWorkers{unknown: true}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-unknowable", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want [%s] — an unanswerable probe must not silence the queue",
			got, categoryUnclaimedItems)
	}
}

// TestNoProbeWiredIsTheOldWatcherExactly: a daemon that wires no Workers probe
// keeps its pre-mg-1a8a behaviour, the same way a watcher with no Capacity probe
// keeps its pre-mg-dd77 wording. A notice that cannot see who is working must
// not claim to.
func TestNoProbeWiredIsTheOldWatcherExactly(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-nowire", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want [%s]", got, categoryUnclaimedItems)
	}
}

// TestUncertainAttributionIsSaidInTheDispatchNotice. An unreadable witness means
// the snapshot may MISS a worked item, so the imperative it prints could still
// be aimed at one. The uncertainty rides with the advice rather than replacing
// it — the same treatment RepoCapacity.Uncertain gets.
func TestUncertainAttributionIsSaidInTheDispatchNotice(t *testing.T) {
	probe := &fakeWorkers{flight: WorkInFlight{Uncertain: "the witness could not be read"}}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-maybe", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("nudges = %d, want 1", rec.nudgeCount())
	}
	msg := rec.nudges[0].message
	if !strings.Contains(msg, "may be incomplete") || !strings.Contains(msg, "the witness could not be read") {
		t.Errorf("notice = %q, want it to carry the attribution uncertainty", msg)
	}
}

// TestWorkedNoticeStampsTheAttribution: events.log must be able to tell "aging
// because nobody dispatched it" from "aging because its claim failed open"
// without reading prose. mg-1693 cost a night to a distinction that existed only
// in message text.
func TestWorkedNoticeStampsTheAttribution(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-stamp", "pc-witnessed", 0, "witness")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-stamp", "mayor", now.Add(-1*time.Minute))

	w.Check(now)

	if rec.eventCount() != 1 {
		t.Fatalf("events = %d, want 1", rec.eventCount())
	}
	details := rec.events[0].Details
	if details["category"] != categoryWorkedUnclaimed {
		t.Fatalf("category = %v, want %s", details["category"], categoryWorkedUnclaimed)
	}
	workers, ok := details["workers"].([]map[string]any)
	if !ok || len(workers) != 1 {
		t.Fatalf("workers detail = %#v, want one entry", details["workers"])
	}
	if workers[0]["item_id"] != "mg-stamp" || workers[0]["polecat"] != "pc-witnessed" {
		t.Errorf("workers[0] = %#v, want the item and its worker", workers[0])
	}
	// A witness-only attribution is weaker evidence than a registry one, and a
	// reader deciding whether to dispatch is entitled to the difference.
	if workers[0]["evidence"] != "witness" {
		t.Errorf("evidence = %v, want it recorded", workers[0]["evidence"])
	}
	if _, present := workers[0]["pid"]; present {
		t.Errorf("an unknown pid was stamped as one: %#v", workers[0])
	}
	if !strings.Contains(rec.nudges[0].message, "<the worker's pid>") {
		t.Errorf("notice = %q, want the repair to admit the pid is unknown", rec.nudges[0].message)
	}
}

// TestWorkedNoticeFiresWithoutAnAgeThreshold. The two dispatch checks wait out
// an age because a freshly-filed item is not yet evidence of anything; a
// worked-but-unclaimed item is an anomaly the instant it exists, and its cost —
// a second dispatch — is highest in the first minutes, before either polecat has
// pushed anything.
func TestWorkedNoticeFiresWithoutAnAgeThreshold(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-fresh", "pc-new", 9, "registry")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-fresh", "mayor", now.Add(-5*time.Second))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryWorkedUnclaimed {
		t.Fatalf("categories = %v, want [%s] on a seconds-old item", got, categoryWorkedUnclaimed)
	}
}

// TestWorkedNoticeBacksOffPerItem: this check shares selectDue, so a standing
// anomaly must not re-notify every 30s tick. mg-1693's lesson applies to a new
// category the day it ships, not after it draws 22 notices in a night.
func TestWorkedNoticeBacksOffPerItem(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-stuck", "pc-stuck", 3, "registry")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-stuck", "mayor", now.Add(-1*time.Minute))

	for i := 0; i < 6; i++ {
		w.Check(now.Add(time.Duration(i) * 30 * time.Second))
	}
	if rec.nudgeCount() != 1 {
		t.Fatalf("nudges = %d, want 1 — the 5m cooldown must hold a standing anomaly", rec.nudgeCount())
	}
	w.Check(now.Add(6 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("nudges = %d, want 2 past the cooldown", rec.nudgeCount())
	}
	if !strings.Contains(rec.nudges[1].message, "[repeat]") {
		t.Errorf("second notice = %q, want it marked as a repeat", rec.nudges[1].message)
	}
}

// TestWorkedItemThatFinishesReturnsToTheDispatchPopulation. The worker exits,
// its claim is released, and the item is genuinely available again — the
// dispatch notice must come back. A filter that latched would trade a
// double-dispatch for a permanently invisible item, which is the worse failure.
func TestWorkedItemThatFinishesReturnsToTheDispatchPopulation(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-handover", "pc-leaving", 11, "registry")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-handover", "mayor", now.Add(-20*time.Minute))

	w.Check(now)
	if got := categories(rec); len(got) != 1 || got[0] != categoryWorkedUnclaimed {
		t.Fatalf("categories = %v, want [%s] while the worker is live", got, categoryWorkedUnclaimed)
	}

	probe.mu.Lock()
	probe.flight = WorkInFlight{}
	probe.mu.Unlock()

	w.Check(now.Add(6 * time.Minute))
	got := categories(rec)
	if len(got) != 2 || got[1] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want the dispatch notice back once the worker is gone", got)
	}
}

// TestGatedItemStaysSilentEvenWhenWorked keeps the three populations disjoint. A
// `human`/`parked` item is held precisely so it stops generating traffic, and a
// worker on one does not change that — it would be a dispatch-shaped notice
// about an item the daemon refuses to dispatch.
func TestGatedItemStaysSilentEvenWhenWorked(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-gated", "pc-hand", 1, "registry")}
	w, rec, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-gated", "human", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("nudges = %d, want 0 for a gated item: %v", rec.nudgeCount(), nudgeMessages(rec))
	}
}

// TestProbeIsAskedOncePerTick. Three checks read the same available/ listing;
// they must read the same in-flight snapshot too, or a polecat can be in flight
// to one of them and absent to another within one sample.
func TestProbeIsAskedOncePerTick(t *testing.T) {
	probe := &fakeWorkers{flight: workedBy("mg-x", "pc-x", 1, "registry")}
	w, _, workRoot := flightEnv(t, baseConfig(), probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-x", "mayor", now.Add(-20*time.Minute))
	writePriorityItem(t, workRoot, "mg-y", "mayor", "high", now.Add(-20*time.Minute))

	w.Check(now)

	if got := probe.calls(); got != 1 {
		t.Errorf("probe calls = %d, want 1 per tick", got)
	}
}

// nudgeMessages returns every message the recorder saw, for assertions that care
// about what was said across a batch rather than which one said it.
func nudgeMessages(rec *recorder) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, 0, len(rec.nudges))
	for _, n := range rec.nudges {
		out = append(out, n.message)
	}
	return out
}
