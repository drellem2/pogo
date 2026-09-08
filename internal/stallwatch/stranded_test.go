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
//
// QueueConsulted is TRUE here, so these snapshots describe the ordinary case: the
// refinery queue was asked and this branch is not in it. A default of false would
// make every test below assert over a snapshot that says it could not tell
// (mg-64bb), and the note that fires on that state would be in every message.
func strandedIn(item, branch string, unmerged int) StrandedWork {
	return StrandedWork{QueueConsulted: true, Items: map[string][]StrandedBranch{
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

// queuedIn is strandedIn plus the fact that changes the instruction: this
// branch's merge is ALREADY RUNNING.
func queuedIn(item, branch string, unmerged int, mr, status string) StrandedWork {
	work := strandedIn(item, branch, unmerged)
	b := work.Items[item][0]
	b.Queued = &QueuedMerge{MR: mr, Status: status}
	work.Items[item] = []StrandedBranch{b}
	return work
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

// TestQueuedBranchIsStillWithheldFromPriorityWake is mg-64bb's headline
// requirement, and it is deliberately the SAME assertion as
// TestPriorityWakeDoesNotAdvertiseAStrandedItem: a branch in the merge queue is
// work that already exists outside the item, so the item is not "ready" whatever
// the board says. On 2026-09-03 mg-a19a was advertised four times across ~36
// minutes with mr-dacudtqtjv1hjkm21420 in the queue throughout.
func TestQueuedBranchIsStillWithheldFromPriorityWake(t *testing.T) {
	probe := &fakeStranded{work: queuedIn("mg-a19a", "polecat-pa19a", 1, "mr-dacudtqtjv1hjkm21420", "processing")}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a19a", "mayor", "high", "/Users/daniel/dev/pogo", now.Add(-5*time.Minute))

	w.Check(now)

	for _, cat := range categories(rec) {
		if cat == categoryPriorityWake {
			t.Fatalf("priority-wake advertised an item whose merge is already running: %q", rec.nudges[0].message)
		}
	}
	msg := strings.Join(nudgeMessages(rec), " ")
	if !strings.Contains(msg, "mg-a19a") {
		t.Fatalf("the item went unreported entirely — suppressing it drops the do-not-dispatch "+
			"instruction with it, which is the exclusion mg-4bf1 had to undo: %v", nudgeMessages(rec))
	}
}

// TestQueuedBranchGetsNoSubmitLine is this fix checked against the defect it
// repairs, in the direction that is NOT caught by the pushed/local-only guard.
//
// `pogo refinery submit` refuses a branch that is not on origin, so mg-586d's
// remedy fails loudly. A queued branch is on origin: the command RUNS, and what
// it produces is a duplicate merge request for work whose merge is in flight,
// because the refinery has no dedup. The reader is told to wait instead, and the
// MR is named so the wait is checkable.
func TestQueuedBranchGetsNoSubmitLine(t *testing.T) {
	probe := &fakeStranded{work: queuedIn("mg-a19a", "polecat-pa19a", 2, "mr-dacudtqtjv1hjkm21420", "processing")}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a19a", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	msg := strings.Join(nudgeMessages(rec), " ")
	// The BRANCH ARGUMENT is what makes a line paste-ready, and it is what this
	// forbids. A sentence that names the command in order to prohibit it is the
	// opposite of the defect, so the assertion is on `submit <branch>` rather
	// than on the command's name appearing anywhere.
	if strings.Contains(msg, "pogo refinery submit polecat-pa19a") {
		t.Errorf("a paste-ready submit was printed for a branch already in the merge queue — the "+
			"refinery has no dedup, so that command merges the same work twice: %q", msg)
	}
	for _, want := range []string{
		"mr-dacudtqtjv1hjkm21420",
		"processing",
		"MERGE QUEUE",
		"do NOT resubmit",
		"Do NOT re-submit it",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice = %q, want it to contain %q", msg, want)
		}
	}
}

// TestMixedQueuedAndStrandedSpeaksNeutrally. A headline is the half that gets
// skimmed and forwarded, so a notice covering one queued branch and one nobody
// has submitted must not say "already in the merge queue" — that is true of one
// half and false of the other, and the false half is the one that needs somebody
// to act.
func TestMixedQueuedAndStrandedSpeaksNeutrally(t *testing.T) {
	work := queuedIn("mg-a19a", "polecat-pa19a", 1, "mr-dacudtqtjv1hjkm21420", "queued")
	other := strandedIn("mg-a854", "polecat-pa854", 1)
	work.Items["mg-a854"] = other.Items["mg-a854"]

	probe := &fakeStranded{work: work}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a19a", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))
	writeItemForRepo(t, workRoot, "mg-a854", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var subj, msg string
	for _, n := range rec.nudges {
		if strings.Contains(n.message, "ALREADY EXISTS") {
			subj, msg = n.subject, n.message
		}
	}
	if msg == "" {
		t.Fatalf("no stranded notice fired: %+v", rec.nudges)
	}
	if strings.Contains(subj, "ALREADY IN THE MERGE QUEUE") {
		t.Errorf("subject = %q claims the whole set is in the queue while mg-a854 is not "+
			"submitted at all", subj)
	}
	if !strings.Contains(msg, "mr-dacudtqtjv1hjkm21420") {
		t.Errorf("the queued half lost its MR id in a mixed notice: %q", msg)
	}
	if strings.Contains(msg, "pogo refinery submit polecat-pa19a") {
		t.Errorf("the queued branch was handed a submit line in a mixed notice: %q", msg)
	}
}

// TestAllQueuedNoticeSaysWaitInTheHeadline. When every branch in the notice is
// in the queue there is nothing to submit and nothing to dispatch, and the
// subject is where a reader who reads no further learns it.
func TestAllQueuedNoticeSaysWaitInTheHeadline(t *testing.T) {
	probe := &fakeStranded{work: queuedIn("mg-a19a", "polecat-pa19a", 1, "mr-dacudtqtjv1hjkm21420", "queued")}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a19a", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var subj, msg string
	for _, n := range rec.nudges {
		if strings.Contains(n.message, "ALREADY EXISTS") {
			subj, msg = n.subject, n.message
		}
	}
	if !strings.Contains(subj, "ALREADY IN THE MERGE QUEUE") {
		t.Errorf("subject = %q, want it to say where the work already is", subj)
	}
	if !strings.Contains(msg, "closes itself when it lands") {
		t.Errorf("the notice does not say the item resolves on its own — the reader is left "+
			"looking for an action that does not exist: %q", msg)
	}
}

// TestUnconsultedQueueIsStatedOnTheStrandedNotice. With the queue unasked every
// branch reads as un-submitted, which is the state that gets a submit line — so
// the reader being handed that remedy is the one who has to be told the remedy
// might already be running. mg-8baa's collapse, in the direction where the
// damage lands on the remedy rather than on the finding.
func TestUnconsultedQueueIsStatedOnTheStrandedNotice(t *testing.T) {
	work := strandedIn("mg-a19a", "polecat-pa19a", 1)
	work.QueueConsulted = false

	probe := &fakeStranded{work: work}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a19a", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	msg := strings.Join(nudgeMessages(rec), " ")
	if !strings.Contains(msg, "refinery queue was NOT consulted") {
		t.Errorf("notice = %q, want it to say the queue was not asked", msg)
	}

	// Positive control: consulted snapshots must NOT carry the sentence, or it
	// is decoration rather than a signal.
	probe2 := &fakeStranded{work: strandedIn("mg-a854", "polecat-pa854", 1)}
	w2, rec2, workRoot2 := strandedEnv(t, baseConfig(), nil, nil, probe2)
	writeItemForRepo(t, workRoot2, "mg-a854", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))
	w2.Check(now)
	if msg2 := strings.Join(nudgeMessages(rec2), " "); strings.Contains(msg2, "NOT consulted") {
		t.Errorf("a consulted snapshot printed the unconsulted-queue note: %q", msg2)
	}
}

// TestQueuedAttributionIsStampedOnTheEvent, so "held because its work was pushed
// and abandoned" and "held because its merge is running" are countable apart in
// events.log. They are the same exclusion and different emergencies: one needs
// somebody to act, the other needs everybody not to.
func TestQueuedAttributionIsStampedOnTheEvent(t *testing.T) {
	probe := &fakeStranded{work: queuedIn("mg-a19a", "polecat-pa19a", 1, "mr-dacudtqtjv1hjkm21420", "processing")}
	w, rec, workRoot := strandedEnv(t, baseConfig(), nil, nil, probe)
	now := time.Now()
	writeItemForRepo(t, workRoot, "mg-a19a", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 1 {
		t.Fatalf("events = %d, want 1", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Details["queue_consulted"] != true {
		t.Errorf("queue_consulted = %#v, want true — with it absent a counter over these events "+
			"reads 'no item was ever held for a merge in flight'", ev.Details["queue_consulted"])
	}
	rows, ok := ev.Details["branches"].([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("branches detail = %#v, want one row", ev.Details["branches"])
	}
	if rows[0]["queued_mr"] != "mr-dacudtqtjv1hjkm21420" || rows[0]["queued_status"] != "processing" {
		t.Errorf("queued attribution = %#v", rows[0])
	}

	// And an ordinary stranded branch carries NO queue keys, so their presence
	// means something.
	probe2 := &fakeStranded{work: strandedIn("mg-a854", "polecat-pa854", 1)}
	w2, rec2, workRoot2 := strandedEnv(t, baseConfig(), nil, nil, probe2)
	writeItemForRepo(t, workRoot2, "mg-a854", "mayor", "", "/Users/daniel/dev/pogo", now.Add(-20*time.Minute))
	w2.Check(now)
	rec2.mu.Lock()
	defer rec2.mu.Unlock()
	rows2, ok := rec2.events[0].Details["branches"].([]map[string]any)
	if !ok || len(rows2) != 1 {
		t.Fatalf("branches detail = %#v", rec2.events[0].Details["branches"])
	}
	if _, present := rows2[0]["queued_mr"]; present {
		t.Errorf("a branch in no queue was stamped with a merge request: %#v", rows2[0])
	}
}
