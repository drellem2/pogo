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

// fakePreserved is a scripted Preserved probe. It counts calls so a test can
// assert the snapshot is taken once per TICK rather than once per check — the
// same property fakeWorkers pins, for the same reason: an item must not read as
// held by one check and free to another within one sample.
type fakePreserved struct {
	mu        sync.Mutex
	held      PreservedWork
	unknown   bool
	callCount int
	askedIDs  []string
}

func (f *fakePreserved) Retained(ids []string) (PreservedWork, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.askedIDs = append(f.askedIDs, ids...)
	if f.unknown {
		return PreservedWork{}, false
	}
	return f.held, true
}

func (f *fakePreserved) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

// heldIn builds a snapshot naming one retained tree on one item, in the shape
// of the 2026-08-19 finding.
func heldIn(item, path, branch string, modified, untracked int) PreservedWork {
	return PreservedWork{Items: map[string][]PreservedTree{
		item: {{Path: path, Branch: branch, Modified: modified, Untracked: untracked}},
	}}
}

// preservedEnv is flightEnv plus a preserved probe.
func preservedEnv(t *testing.T, cfg config.StallWatchConfig, workers Workers, preserved Preserved) (*Watcher, *recorder, string) {
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
		WorkRoot:  workRoot,
		MailRoot:  mailRoot,
		Nudge:     rec.nudge,
		Emit:      rec.emit,
		Workers:   workers,
		Preserved: preserved,
	})
	return w, rec, workRoot
}

// TestAgedItemWithAPreservedWorktreeIsNotAdvertised is the defect verbatim.
// After the 2026-08-14→19 outage, five open items sat in available/ with their
// polecats' uncommitted work still on disk and zero commits ahead of origin.
// The board went on calling them neglected, which is what invites a dispatch
// that re-derives the work and lets the next gc reap destroy the original.
func TestAgedItemWithAPreservedWorktreeIsNotAdvertised(t *testing.T) {
	probe := &fakePreserved{held: heldIn("mg-516e", "/Users/x/.pogo/polecats/p516e", "polecat-p516e", 14, 2)}
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-516e", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryPreservedWorktree {
		t.Fatalf("categories = %v, want exactly [%s] — the dispatch notice must not advertise an "+
			"item whose work is already written and uncommitted", got, categoryPreservedWorktree)
	}
	msg := rec.nudges[0].message
	for _, want := range []string{
		"mg-516e",
		"/Users/x/.pogo/polecats/p516e", // the path is what stops the reflex remedy
		"14 modified, 2 untracked",
		"NOT a dispatch request",
		"DO NOT clear this by removing the worktree",
		"pogo gc --list-preserved",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice = %q, want it to contain %q", msg, want)
		}
	}
}

// TestPriorityWakeDoesNotAdvertiseAPreservedItem covers the surface the harm
// actually travelled on. mg-516e was flagged HIGH-PRIORITY unclaimed while
// sixteen uncommitted files — including a whole new command and its tests — sat
// in its retained worktree. "Claim or dispatch now" is the most imperative
// wording this component emits and the shortest-cooldown one.
func TestPriorityWakeDoesNotAdvertiseAPreservedItem(t *testing.T) {
	probe := &fakePreserved{held: heldIn("mg-hi", "/polecats/phi", "polecat-phi", 1, 1)}
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi", "mayor", "high", now.Add(-5*time.Minute))

	w.Check(now)

	for _, cat := range categories(rec) {
		if cat == categoryPriorityWake {
			t.Fatalf("priority-wake advertised an item whose work sits uncommitted in a retained "+
				"worktree: %q", rec.nudges[0].message)
		}
	}
	if !strings.Contains(strings.Join(nudgeMessages(rec), " "), "UNCOMMITTED") {
		t.Errorf("the item went unreported entirely. Suppressing it would fix the wrong dispatch "+
			"and hide the decision nobody is assigned to make: %v", nudgeMessages(rec))
	}
}

// TestUnheldItemStillFires is the positive control. Without it this change
// could be a detector that stopped detecting, which reads identically from
// outside — the failure this package has already shipped twice.
func TestUnheldItemStillFires(t *testing.T) {
	probe := &fakePreserved{held: heldIn("mg-other", "/polecats/pother", "polecat-pother", 1, 0)}
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-alone", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want exactly [%s] — an item with no retained tree is still "+
			"neglected and must still be reported", got, categoryUnclaimedItems)
	}
}

// TestPreservedProbeIsAskedOncePerTick. Three checks read the same available/
// listing; if each probed separately an item could read as held by one and free
// to another within one sample, which is the disagreement this whole mechanism
// exists to remove rather than reproduce internally.
func TestPreservedProbeIsAskedOncePerTick(t *testing.T) {
	probe := &fakePreserved{held: heldIn("mg-516e", "/polecats/p516e", "polecat-p516e", 1, 1)}
	w, _, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-516e", "mayor", now.Add(-20*time.Minute))
	writePriorityItem(t, workRoot, "mg-hi", "mayor", "high", now.Add(-5*time.Minute))

	w.Check(now)

	if got := probe.calls(); got != 1 {
		t.Fatalf("Preserved probe called %d times in one tick, want 1", got)
	}
	// And it must be asked about the items actually listed, not a wildcard: a
	// probe handed no ids would have to answer for everything, and a guard that
	// widened to every item refuses dispatches nobody asked about.
	probe.mu.Lock()
	asked := append([]string(nil), probe.askedIDs...)
	probe.mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("probe asked about %v, want both listed items", asked)
	}
}

// TestUnknownPreservedProbeKeepsThePreFixBehaviour. known=false is "the
// question could not be answered", never "nothing is retained". The loud
// direction is chosen deliberately: a false "dispatch this" is self-correcting
// — the spawn-time gate refuses it — while a false silence looks like a healthy
// queue.
func TestUnknownPreservedProbeKeepsThePreFixBehaviour(t *testing.T) {
	probe := &fakePreserved{unknown: true}
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-516e", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want exactly [%s] — an unanswerable probe must not silence the "+
			"standard notice", got, categoryUnclaimedItems)
	}
}

// TestNoPreservedProbeIsExactlyThePreFixWatcher. Left unwired the watcher must
// behave as it did before this fix: the option is optional, and a daemon that
// cannot read a polecats dir must not lose its stall notices.
func TestNoPreservedProbeIsExactlyThePreFixWatcher(t *testing.T) {
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, nil)
	now := time.Now()
	writeItem(t, workRoot, "mg-516e", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want exactly [%s]", got, categoryUnclaimedItems)
	}
}

// TestPreservedAndWorkedPopulationsStayDisjoint. A live worker's tree is that
// worker's working copy, and checkWorkedButUnclaimed already tells the reader
// not to dispatch. Two notices saying the same thing about one item is how a
// channel gets skimmed — which is the failure this whole ticket is an instance
// of.
func TestPreservedAndWorkedPopulationsStayDisjoint(t *testing.T) {
	workers := &fakeWorkers{flight: workedBy("mg-both", "pc-live", 99, "registry")}
	preserved := &fakePreserved{held: heldIn("mg-both", "/polecats/pboth", "polecat-pboth", 3, 0)}
	w, rec, workRoot := preservedEnv(t, baseConfig(), workers, preserved)
	now := time.Now()
	writeItem(t, workRoot, "mg-both", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryWorkedUnclaimed {
		t.Fatalf("categories = %v, want exactly [%s] — an item a LIVE worker holds belongs to the "+
			"worked-unclaimed notice, not to both", got, categoryWorkedUnclaimed)
	}
}

// TestPreservedHoldRespectsTheDispatchGate. A `human`, `parked` or
// `blocked:<agent>` item is nobody's dispatch candidate, so it is not this
// check's business either. The three populations that read available/ share one
// watchedForDispatch gate precisely so they cannot disagree about who owns an
// item.
func TestPreservedHoldRespectsTheDispatchGate(t *testing.T) {
	probe := &fakePreserved{held: heldIn("mg-parked", "/polecats/pparked", "polecat-pparked", 2, 1)}
	cfg := baseConfig()
	cfg.NonDispatchableAssignees = []string{"human", "parked"}
	w, rec, workRoot := preservedEnv(t, cfg, nil, probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-parked", "parked", now.Add(-20*time.Minute))

	w.Check(now)

	for _, cat := range categories(rec) {
		if cat == categoryPreservedWorktree {
			t.Fatalf("the preserved-worktree notice fired on a gated item: %q", rec.nudges[0].message)
		}
	}
}

// TestPreservedUncertaintyTravelsWithTheDispatchAdvice. An incomplete snapshot
// can only cause a held item to be MISSED, never invented — so the caveat
// belongs on the notice that might be wrong, and never replaces it.
func TestPreservedUncertaintyTravelsWithTheDispatchAdvice(t *testing.T) {
	probe := &fakePreserved{held: PreservedWork{
		Items:     map[string][]PreservedTree{"mg-held": {{Path: "/polecats/pheld", Unread: true}}},
		Uncertain: "the polecats directory could not be fully read",
	}}
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-held", "mayor", now.Add(-20*time.Minute))
	writeItem(t, workRoot, "mg-free", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	msgs := strings.Join(nudgeMessages(rec), "\n")
	if !strings.Contains(msgs, "Preserved-worktree attribution may be incomplete") {
		t.Errorf("the dispatch notice did not carry the incompleteness caveat: %s", msgs)
	}
	// The unread tree is still reported, and still described as unread rather
	// than as a positively-read empty one.
	if !strings.Contains(msgs, "could NOT be read") {
		t.Errorf("an unread retained tree must be reported as unread: %s", msgs)
	}
}
