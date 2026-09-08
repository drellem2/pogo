package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// fakeStranded is a scripted Stranded probe. It counts calls so a test can
// assert the snapshot is taken once per TICK rather than once per check, and it
// records what it was ASKED — the repo travels with the id here, and a probe
// handed no repo cannot answer at all.
type fakeStranded struct {
	mu         sync.Mutex
	work       StrandedWork
	unknown    bool
	callCount  int
	askedItems []StrandedItem
}

func (f *fakeStranded) Branches(items []StrandedItem) (StrandedWork, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	f.askedItems = append(f.askedItems, items...)
	if f.unknown {
		return StrandedWork{}, false
	}
	return f.work, true
}

func (f *fakeStranded) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

// strandedIn builds a snapshot naming one PUSHED branch on one item, in the
// shape of the 2026-09-08 00:54Z finding: the polecat is gone, the work is on
// origin, and the item is back in available/ describing itself as untouched.
func strandedIn(item, branch string, unmerged int) StrandedWork {
	return StrandedWork{Items: map[string][]StrandedBranch{
		item: {{
			Branch:   branch,
			Ref:      "refs/remotes/origin/" + branch,
			Pushed:   true,
			Unmerged: unmerged,
			Target:   "refs/remotes/origin/main",
			Repo:     "/Users/daniel/dev/pogo",
		}},
	}}
}

// strandedEnv is preservedEnv plus a stranded probe.
func strandedEnv(t *testing.T, cfg config.StallWatchConfig, workers Workers, preserved Preserved, stranded Stranded) (*Watcher, *recorder, string) {
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
		Stranded:  stranded,
	})
	return w, rec, workRoot
}

// writeItemForRepo writes an available work item that names a repository, which
// is what the stranded probe needs to look for a branch at all. It is separate
// from repocap_test.go's writeRepoItem, which pins the assignee to pm-pogo.
func writeItemForRepo(t *testing.T, workRoot, id, assignee, priority, repo string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(workRoot, "available")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: %s\nrepo: %s\n", id, assignee, repo)
	if priority != "" {
		content += fmt.Sprintf("priority: %s\n", priority)
	}
	content += fmt.Sprintf("---\n# %s\n", id)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// TestPriorityWakeDoesNotAdvertiseAStrandedItem is the defect verbatim, and it
// is the surface that mattered. At 2026-09-08 00:54Z priority-wake said, with no
// capacity clause, "1 high-priority work item(s) are ready and unclaimed — claim
// or dispatch now: mg-a932", while pogod's own `[stranded-push]` mail about
// mg-a932 sat in the same inbox naming polecat-ta932, pushed=true, do NOT
// dispatch. Both signals were pogod's and both were confident.
func TestPriorityWakeDoesNotAdvertiseAStrandedItem(t *testing.T) {
	probe := &fakeStranded{work: strandedIn("mg-a932", "polecat-ta932", 1)}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a932", "mayor", "high", "/Users/daniel/dev/pogo", now.Add(-5*time.Minute))

	w.Check(now)

	for _, cat := range categories(rec) {
		if cat == categoryPriorityWake {
			t.Fatalf("priority-wake advertised an item whose work is already pushed and unmerged: %q",
				rec.nudges[0].message)
		}
	}
	msg := strings.Join(nudgeMessages(rec), " ")
	if !strings.Contains(msg, "ALREADY EXISTS") {
		t.Fatalf("the item went unreported entirely. Suppressing it would fix the wrong dispatch "+
			"and leave the do-not-dispatch instruction with nowhere to live: %v", nudgeMessages(rec))
	}
	for _, want := range []string{
		"mg-a932",
		"polecat-ta932",
		"PUSHED",
		"NOT a dispatch request",
		"[stranded-push]",
		"pogo refinery submit polecat-ta932 --repo=/Users/daniel/dev/pogo --author=mg-a932",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice = %q, want it to contain %q", msg, want)
		}
	}
}

// TestStrandedNoticeRepeats is the whole point of the fix, and it is the one
// property the existing detector did not have.
//
// pogod's [stranded-push] mail is sent ONCE, at release. priority-wake repeats
// on a backoff. On the night of 2026-09-07 that asymmetry decided the
// arbitration: mg-daf4 was advertised three times between 22:44Z and 22:55Z and
// prohibited once. This check is re-derived from available/ every tick, so the
// prohibition now repeats on the same shape of schedule the recommendation used
// to win with.
func TestStrandedNoticeRepeats(t *testing.T) {
	probe := &fakeStranded{work: strandedIn("mg-daf4", "polecat-pdaf4", 1)}
	cfg := baseConfig()
	w, rec, workRoot := strandedEnv(t, cfg, nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-daf4", "mayor", "high", "/Users/daniel/dev/pogo", now.Add(-5*time.Minute))

	w.Check(now)
	// Past the per-item cooldown, which is what the recommendation's own repeats
	// were paced by.
	w.Check(now.Add(cfg.NudgeCooldown + time.Minute))

	fired := 0
	for _, cat := range categories(rec) {
		if cat == categoryStrandedPush {
			fired++
		}
	}
	if fired != 2 {
		t.Fatalf("the do-not-dispatch notice fired %d time(s) across two ticks, want 2 — a "+
			"prohibition sent once loses to a recommendation sent on a schedule, which is the "+
			"mechanism this fix exists to reverse. categories=%v", fired, categories(rec))
	}
	if !strings.Contains(nudgeMessages(rec)[1], "[repeat]") {
		t.Errorf("the second notice does not identify itself as a repeat: %q", nudgeMessages(rec)[1])
	}
}

// TestUnstrandedItemStillFires is the positive control. Without it this change
// could be a detector that stopped detecting, which reads identically from
// outside — the failure this package has already shipped twice.
func TestUnstrandedItemStillFires(t *testing.T) {
	probe := &fakeStranded{work: strandedIn("mg-other", "polecat-pother", 1)}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-alone", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want exactly [%s] — an item with no branch is still neglected "+
			"and must still be reported", got, categoryUnclaimedItems)
	}
}

// TestStrandedProbeIsAskedOncePerTick, and asked with the REPO. Three checks
// read the same available/ listing; if each probed separately an item could read
// as stranded to one and free to another within one sample, which is the
// disagreement this whole mechanism exists to remove rather than reproduce
// internally.
func TestStrandedProbeIsAskedOncePerTick(t *testing.T) {
	probe := &fakeStranded{work: strandedIn("mg-a932", "polecat-ta932", 1)}
	w, _, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a932", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))
	writeItemForRepo(t, workRoot, "mg-hi", "mayor", "high", "/Users/daniel/dev/other", now.Add(-5*time.Minute))

	w.Check(now)

	if got := probe.calls(); got != 1 {
		t.Fatalf("Stranded probe called %d times in one tick, want 1", got)
	}
	probe.mu.Lock()
	asked := append([]StrandedItem(nil), probe.askedItems...)
	probe.mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("probe asked about %v, want both listed items", asked)
	}
	for _, a := range asked {
		if a.Repo == "" {
			t.Errorf("item %s was asked about with no repo — a branch lives in a repository "+
				"nothing but the item names, so the probe cannot answer", a.ID)
		}
	}
}

// TestUnknownStrandedProbeKeepsThePreFixBehaviour. known=false is "the question
// could not be answered", never "nothing is stranded". The loud direction is
// chosen deliberately: a false "dispatch this" is self-correcting — the
// spawn-time stranded gate refuses it — while a false silence looks like a
// healthy queue.
func TestUnknownStrandedProbeKeepsThePreFixBehaviour(t *testing.T) {
	probe := &fakeStranded{unknown: true}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a932", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want exactly [%s] — an unanswerable probe must not silence the "+
			"standard notice", got, categoryUnclaimedItems)
	}
}

// TestNoStrandedProbeIsExactlyThePreFixWatcher. Left unwired the watcher must
// behave as it did before this fix: the option is optional, and a daemon that
// cannot answer the question must not pretend to.
func TestNoStrandedProbeIsExactlyThePreFixWatcher(t *testing.T) {
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, nil)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a932", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryUnclaimedItems {
		t.Fatalf("categories = %v, want exactly [%s]", got, categoryUnclaimedItems)
	}
}

// TestUncertainStrandedSnapshotTravelsWithTheDispatchAdvice. An incomplete
// snapshot can only cause a stranded item to be MISSED, never invented — so the
// caveat rides on the notice that says "dispatch these", which is the one it
// qualifies, and not on this file's own notice.
func TestUncertainStrandedSnapshotTravelsWithTheDispatchAdvice(t *testing.T) {
	probe := &fakeStranded{work: StrandedWork{
		Items:     map[string][]StrandedBranch{},
		Uncertain: "/Users/daniel/dev/pogo could not be listed",
	}}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-alone", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	msg := strings.Join(nudgeMessages(rec), " ")
	if !strings.Contains(msg, "could not be listed") || !strings.Contains(msg, "pogo check-stranded") {
		t.Errorf("the dispatch notice does not disclose that the stranded snapshot may be "+
			"incomplete: %q", msg)
	}
}

// TestLiveWorkerOutranksAStrandedBranch keeps the three do-not-dispatch checks
// DISJOINT. A running polecat's branch has unmerged commits because that is what
// work in progress IS, so without this the healthy majority of the fleet would
// draw a stranded notice every tick — a finding that fires on the steady state
// is one readers learn to skip.
func TestLiveWorkerOutranksAStrandedBranch(t *testing.T) {
	workers := &fakeWorkers{flight: workedBy("mg-a932", "ta932", 4242, "registry")}
	probe := &fakeStranded{work: strandedIn("mg-a932", "polecat-ta932", 1)}
	w, rec, workRoot := strandedEnv(t, baseConfig(), workers, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a932", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryWorkedUnclaimed {
		t.Fatalf("categories = %v, want exactly [%s] — two notices saying the same thing about "+
			"one item is how a channel gets skimmed", got, categoryWorkedUnclaimed)
	}
}

// TestPreservedWorktreeOutranksAStrandedBranch, for the same disjointness and
// for one further reason: the preserved row's work exists in ONE place that a gc
// reap destroys, while this row's work is on origin. When both fit, the more
// urgent one is the row to keep.
func TestPreservedWorktreeOutranksAStrandedBranch(t *testing.T) {
	held := &fakePreserved{held: heldIn("mg-a932", "/polecats/ta932", "polecat-ta932", 3, 1)}
	probe := &fakeStranded{work: strandedIn("mg-a932", "polecat-ta932", 1)}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, held, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a932", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	if got := categories(rec); len(got) != 1 || got[0] != categoryPreservedWorktree {
		t.Fatalf("categories = %v, want exactly [%s]", got, categoryPreservedWorktree)
	}
}

// TestLocalOnlyStrandedBranchPrintsNoSubmit is this fix checked against the
// defect it remedies. `pogo refinery submit` REFUSES a branch that is not on
// origin (mg-586d), so an unconditional submit line would tell the reader two
// false things at once — that the work is durable, and that a command which
// cannot run is the remedy. That is mg-bfe0's defect, and a remedy is an
// artifact of the same kind as the defect it repairs.
func TestLocalOnlyStrandedBranchPrintsNoSubmit(t *testing.T) {
	work := strandedIn("mg-d788", "polecat-pd788", 2)
	b := work.Items["mg-d788"][0]
	b.Pushed = false
	b.Ref = "refs/heads/polecat-pd788"
	work.Items["mg-d788"] = []StrandedBranch{b}

	probe := &fakeStranded{work: work}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-d788", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	msg := strings.Join(nudgeMessages(rec), " ")
	if strings.Contains(msg, "pogo refinery submit") {
		t.Errorf("a paste-ready submit was printed for a branch that is NOT on origin, and the "+
			"refinery refuses those: %q", msg)
	}
	if !strings.Contains(msg, "LOCAL-ONLY") {
		t.Errorf("the notice does not say the work is not on origin, which is the half that "+
			"changes how urgent this is: %q", msg)
	}
}

// TestPreRegistrationTravelsOnTheStrandedNotice. It is the one fact that changes
// what a reader must NOT do rather than merely how urgent the item is: a worker
// based on the target writes its predictions after seeing the results, and the
// artifact looks identical to a valid one.
func TestPreRegistrationTravelsOnTheStrandedNotice(t *testing.T) {
	work := strandedIn("mg-9a19", "polecat-q9a19", 3)
	b := work.Items["mg-9a19"][0]
	b.PreRegistration = "0640bc7ab12"
	work.Items["mg-9a19"] = []StrandedBranch{b}

	probe := &fakeStranded{work: work}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-9a19", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	msg := strings.Join(nudgeMessages(rec), " ")
	if !strings.Contains(msg, "PRE-REGISTRATION") || !strings.Contains(msg, "0640bc7ab12") {
		t.Errorf("the notice does not name the unmerged pre-registration commit: %q", msg)
	}
}

// TestStrandedAttributionIsStampedOnTheEvent, so "aging because nobody
// dispatched it" and "aging because its work is already written" are countable
// apart in events.log rather than only distinguishable by reading prose.
func TestStrandedAttributionIsStampedOnTheEvent(t *testing.T) {
	probe := &fakeStranded{work: strandedIn("mg-a932", "polecat-ta932", 1)}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a932", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 1 {
		t.Fatalf("events = %d, want 1", len(rec.events))
	}
	rows, ok := rec.events[0].Details["branches"].([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("branches detail = %#v, want one row", rec.events[0].Details["branches"])
	}
	if rows[0]["branch"] != "polecat-ta932" || rows[0]["pushed"] != true {
		t.Errorf("branch attribution = %#v", rows[0])
	}
}
