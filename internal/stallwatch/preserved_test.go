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
// of the 2026-08-19 finding: DIRTY, with the work uncommitted.
//
// The outcome is stated rather than left to default. It is the fixture's claim
// about what the probe read, and since mg-fcba there are two things it could
// have read — a tree with the outcome unset is one nobody established anything
// about, which is a third state and not this one.
func heldIn(item, path, branch string, modified, untracked int) PreservedWork {
	return PreservedWork{Items: map[string][]PreservedTree{
		item: {{Path: path, Branch: branch, Outcome: "preserved", Modified: modified, Untracked: untracked}},
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
		Items:     map[string][]PreservedTree{"mg-held": {{Path: "/polecats/pheld", Outcome: "undetermined"}}},
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

// heldCommitsIn builds a snapshot in mg-fcba's shape: a CLEAN tree whose
// polecat committed and never pushed. Every dirty count is zero — that is the
// premise, not an omission — and the finding lives entirely in the commits.
func heldCommitsIn(item, path, branch string, commits int, detached bool) PreservedWork {
	return PreservedWork{Items: map[string][]PreservedTree{
		item: {{
			Path: path, Branch: branch, Outcome: "unpushed",
			Commits: commits, CommitsFinding: "local-only", Detached: detached,
		}},
	}}
}

// TestPriorityWakeDoesNotAdvertiseAnItemWhoseCommitsNeverLeftTheBox is the
// finding this ticket was filed on, on the surface it travelled on.
//
// A polecat was stopped mid-work by the pre-deploy stop. Its work existed only
// in the preserved worktree, and priority-wake advertised the item as "ready,
// claim or dispatch now" FOUR TIMES. Every instrument agreed: the item is
// available, unclaimed, and no branch exists on origin. The only thing standing
// between that advice and duplicated work was a note somebody had written from
// memory eight minutes earlier.
//
// mg-836c's suppression could not cover it: that one is defined over `git
// status`, which is clean once a worker commits.
func TestPriorityWakeDoesNotAdvertiseAnItemWhoseCommitsNeverLeftTheBox(t *testing.T) {
	probe := &fakePreserved{held: heldCommitsIn("mg-hi", "/polecats/phi", "polecat-phi", 3, false)}
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi", "mayor", "high", now.Add(-5*time.Minute))

	w.Check(now)

	for _, cat := range categories(rec) {
		if cat == categoryPriorityWake {
			t.Fatalf("priority-wake advertised an item whose commits exist only in a retained "+
				"worktree: %q", rec.nudges[0].message)
		}
	}
	// And it must not go silent instead. The item needs a DECISION — make the
	// work reachable, rescue it, or rule it spent — and nothing else in the
	// fleet is assigned to ask for one. Trading a wrong instruction for a silent
	// hole is what this package keeps relearning not to ship.
	msgs := strings.Join(nudgeMessages(rec), " ")
	for _, want := range []string{
		"/polecats/phi",                  // the path is what stops the reflex remedy
		"3 commit(s) exist NOWHERE ELSE", // the size of the loss
		"NOT a dispatch request",
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("the notice does not carry %q; got: %s", want, msgs)
		}
	}
	// "clean" must survive into the text. A reader told to look for uncommitted
	// files in a tree git calls clean concludes the check is broken.
	if !strings.Contains(msgs, "clean — no uncommitted files") {
		t.Errorf("the notice does not say the tree is clean, so a reader will look for dirty "+
			"files that are not there; got: %s", msgs)
	}
	// And it states only what git said. "its work is already committed" asserts
	// there IS work, which a clean tree under an UNKNOWN durability verdict has
	// not established — the same overstatement this whole surface refuses to
	// make about an unreadable tree.
	if strings.Contains(msgs, "already committed") {
		t.Errorf("the notice claims work exists on the strength of a clean status; got: %s", msgs)
	}
}

// TestPreservedNoticeNamesADetachedTree. The detached case is the one no ref
// scan can cover — the stranded-work gate reads refs/heads and would see an
// unpushed branch, but a detached worktree's commits are held by that tree's
// own HEAD and by nothing else. Removing the tree orphans them, so the notice
// has to say so where its reader is deciding what to do with the tree.
func TestPreservedNoticeNamesADetachedTree(t *testing.T) {
	probe := &fakePreserved{held: heldCommitsIn("mg-det", "/polecats/pdet", "", 2, true)}
	w, rec, workRoot := preservedEnv(t, baseConfig(), nil, probe)
	now := time.Now()
	writeItem(t, workRoot, "mg-det", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	msgs := strings.Join(nudgeMessages(rec), " ")
	if !strings.Contains(msgs, "NO REF holds them") {
		t.Errorf("the notice does not say the commits are held by no ref; got: %s", msgs)
	}
	if !strings.Contains(msgs, "detached HEAD") {
		t.Errorf("the notice does not name the detached head; got: %s", msgs)
	}
}

// TestPreservedTreeWithNoOutcomeUnderstatesRatherThanLies is the fail-safe
// arm. This struct crosses a package boundary and its zero value used to assert
// "this tree was read" — which, once there are two shapes of finding, renders a
// dirty tree as "clean, its work is already committed". Understating is
// survivable; a confident false sentence about somebody's only copy is not.
func TestPreservedTreeWithNoOutcomeUnderstatesRatherThanLies(t *testing.T) {
	got := PreservedTree{Path: "/polecats/pzero", Modified: 14, Untracked: 2}.summary()
	if !strings.Contains(got, "could NOT be read") {
		t.Errorf("an unset outcome must read as unestablished; got %q", got)
	}
	if strings.Contains(got, "already committed") {
		t.Errorf("an unset outcome rendered as a clean tree — that is a false claim about a tree "+
			"nobody looked at; got %q", got)
	}
}

// TestPreservedNoticeNeverPrintsAZeroCommitCount is this ticket's own defect
// shape turned on the fix. A local-only verdict whose commit list could not be
// re-read arrives here as Commits==0, and "0 commit(s) exist NOWHERE ELSE" is a
// sentence whose NUMBER says the opposite of its verb. The number is the half a
// skimming reader keeps, so a caveat elsewhere in the paragraph does not reach
// them.
func TestPreservedNoticeNeverPrintsAZeroCommitCount(t *testing.T) {
	got := PreservedTree{
		Path: "/polecats/pzero", Outcome: "unpushed", CommitsFinding: "local-only", Commits: 0,
	}.summary()
	if strings.Contains(got, "0 commit") {
		t.Errorf("the notice printed a zero commit count under a local-only verdict; got %q", got)
	}
	if !strings.Contains(got, "how many is UNKNOWN") {
		t.Errorf("an unread count must say so rather than render as none; got %q", got)
	}
}
