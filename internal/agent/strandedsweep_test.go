package agent

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// deadWitness records a polecat whose process is provably gone: the shape the
// witness store is left in when a pogod dies while a polecat is running and the
// polecat later exits without any registry to see it. It is built from a real
// process rather than a hand-written record, for the reason witness_test.go
// gives — the identity probe is the one thing that must not be faked here.
func deadWitness(t *testing.T, name, workItem, repo string) {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	if err := RecordPolecatWitness(name, cmd.Process.Pid, workItem, repo); err != nil {
		t.Fatalf("RecordPolecatWitness(%s): %v", name, err)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait() // reap, so the pid stops answering signal 0
}

// TestStartupSweepReportsAPolecatTheReleaseGateCanNeverReach is the acceptance
// test for the second deliverable.
//
// THE STATE UNDER TEST. reportStrandedWorkOnRelease has exactly one caller, and
// that caller needs an *Agent out of the registry. The registry is in-memory
// with no adopt path, so a polecat that was running when its pogod restarted has
// no entry in the successor and never will — neither of the gate's two doors is
// ever reachable for it again, graceful stop included. It is un-instrumented for
// the rest of its life, and this fleet restarts pogod nightly.
//
// The fixture is that state exactly: a witness record with no registry entry,
// and a branch with pushed unmerged work.
func TestStartupSweepReportsAPolecatTheReleaseGateCanNeverReach(t *testing.T) {
	sandboxWitness(t)
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-a564", "deliver.md", "feat(deliver): finished (mg-a564)")
	deadWitness(t, "a564", "mg-a564", repo)

	reg := newDrainTestRegistry(t) // empty, as it is on every boot
	rep := reg.ReportStrandedWorkAcrossRestart()

	if rep.Err != nil {
		t.Fatalf("sweep could not enumerate its population: %v", rep.Err)
	}
	if rep.Candidates != 1 || rep.Stranded != 1 {
		t.Fatalf("sweep report = %+v, want 1 candidate and 1 stranded — this polecat is exactly the "+
			"population the release gate cannot reach (mg-be37)", rep)
	}

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("the sweep sent %d mails, want 1", len(sent))
	}
	if sent[0].Route != RouteRestartSweep {
		t.Errorf("alert route = %q, want %q", sent[0].Route, RouteRestartSweep)
	}
	if sent[0].StillAlive {
		t.Error("a dead polecat was reported as still running")
	}
	if _, body := sent[0].Message(); !strings.Contains(body, "pogo refinery submit polecat-a564") ||
		!strings.Contains(body, "DO NOT DISPATCH A WORKER AT mg-a564") {
		t.Errorf("the sweep's mail lost the remedy or the prohibition:\n%s", body)
	}

	ev := findEvent(readEventLines(t, logPath), "work_item_stranded_push", "cat-a564")
	if ev == nil {
		t.Fatal("the sweep mailed but put nothing on the event spine; the two outputs must agree")
	}
	details, _ := ev["details"].(map[string]any)
	if got, _ := details["route"].(string); got != RouteRestartSweep {
		t.Errorf("event details.route = %q, want %q — a consumer must be able to tell which "+
			"detector spoke", got, RouteRestartSweep)
	}
}

// TestStartupSweepIgnoresPolecatsTheRegistryStillHolds. The population is
// defined by ABSENCE from the registry, and this is the half that keeps it
// narrow. A pogod that restarts while nothing was running has an empty registry
// and an empty witness store, and must report nothing at all — the failure mode
// here is a sweep that fires on every polecat branch in the repo, which is the
// design this ticket forbids twice.
func TestStartupSweepIgnoresPolecatsTheRegistryStillHolds(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-a564", "deliver.md", "feat(deliver): finished (mg-a564)")

	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	if err := RecordPolecatWitness("a564", cmd.Process.Pid, "mg-a564", repo); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	reg := newDrainTestRegistry(t)
	a := livePolecat("a564", "mg-a564")
	a.SourceRepo = repo
	reg.agents["a564"] = a

	rep := reg.ReportStrandedWorkAcrossRestart()
	if rep.Candidates != 0 || rep.Stranded != 0 {
		t.Fatalf("sweep report = %+v, want zero candidates — this polecat IS in the registry, so the "+
			"release gate will report it when it stops; sweeping it too is the duplicate detector "+
			"this ticket rules out", rep)
	}
	if sent := mail(); len(sent) != 0 {
		t.Fatalf("a registered polecat produced %d mail(s): %+v", len(sent), sent)
	}
}

// TestStartupSweepSaysNothingWhenTheBranchIsMerged. The other polarity of the
// instrument. `git cherry` compares patch ids, and the refinery merges by
// rebase, so every commit on a landed branch is rewritten — a naive
// `git rev-list --count main..<branch>` calls a successfully merged branch
// stranded forever, and the ticket's filer nearly reported 65 strandings that
// way. A merged branch must produce nothing.
func TestStartupSweepSaysNothingWhenTheBranchIsMerged(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-a564", "deliver.md", "feat(deliver): finished (mg-a564)")
	// Land it the way the refinery does: rebase onto main, so every commit gets
	// a new sha but the same patch id.
	gitRun(t, repo, "checkout", "-q", "main")
	gitRun(t, repo, "cherry-pick", "polecat-a564")
	gitRun(t, repo, "push", "-q", "origin", "main")

	deadWitness(t, "a564", "mg-a564", repo)

	reg := newDrainTestRegistry(t)
	rep := reg.ReportStrandedWorkAcrossRestart()
	if rep.Candidates != 1 {
		t.Fatalf("sweep report = %+v, want 1 candidate (the control: this polecat WAS swept)", rep)
	}
	if rep.Stranded != 0 || rep.Clean != 1 {
		t.Fatalf("sweep report = %+v, want 0 stranded and 1 clean — the branch landed under new shas "+
			"and `git cherry` must see through the rebase", rep)
	}
	if sent := mail(); len(sent) != 0 {
		t.Fatalf("a merged branch produced %d mail(s): %+v", len(sent), sent)
	}
}

// TestStartupSweepCountsAnUnreadableBranchAsUnjudgedNotClean is the polarity
// control this ticket demanded, aimed at its own remedy.
//
// The natural shell spelling of this predicate —
// `git cherry origin/main FETCH_HEAD | grep -q '^+'` — answers LANDED whenever
// git FAILS, because a failed git prints nothing and no output is exactly how
// the predicate spells clean. For a stranded-work sweep, clean is the PERMISSIVE
// answer: it means this branch needs nothing. So a transient git or network
// failure would silently convert a stranded branch into an all-clear row, which
// is the failure this ticket exists to prevent occurring inside the detector
// built to prevent it. doctor measured ~40-minute connectivity waves the night
// this was written, so it is not hypothetical.
//
// The fixture is an unreadable repo. The assertion is that it lands in NEITHER
// Clean nor Stranded.
func TestStartupSweepCountsAnUnreadableBranchAsUnjudgedNotClean(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	logs := captureLog(t)
	mail := captureStrandedMail(t)

	deadWitness(t, "a564", "mg-a564", t.TempDir()) // not a git repository

	reg := newDrainTestRegistry(t)
	rep := reg.ReportStrandedWorkAcrossRestart()

	if rep.Unjudged != 1 {
		t.Fatalf("sweep report = %+v, want Unjudged=1 — an unanswerable branch must be a third "+
			"outcome (mg-b6d1)", rep)
	}
	if rep.Clean != 0 {
		t.Fatalf("sweep report = %+v: an unreadable branch was counted CLEAN. Clean is the permissive "+
			"answer here, so this is a stranded branch reported as all-clear — the ticket's own "+
			"defect inside its remedy", rep)
	}
	if rep.Stranded != 0 {
		t.Fatalf("sweep report = %+v: an unreadable branch was counted STRANDED, which cries wolf on "+
			"every connectivity blip", rep)
	}
	if rep.Judged() {
		t.Error("Judged() reported a complete answer over an unjudged candidate")
	}
	if sent := mail(); len(sent) != 0 {
		t.Fatalf("an unjudged candidate produced %d mail(s): %+v", len(sent), sent)
	}
	if out := logs(); !strings.Contains(out, "UNJUDGED") {
		t.Errorf("the unjudged case was not distinguishable in the log; got: %s", out)
	}
}

// TestStartupSweepDoesNotReadAnUnreadableStoreAsEmpty. "We cannot see who is out
// there" and "nobody is out there" are different facts, and rendering the first
// as the second is the same polarity error one level up from the branch check.
func TestStartupSweepDoesNotReadAnUnreadableStoreAsEmpty(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	logs := captureLog(t)
	if err := os.WriteFile(WitnessPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newDrainTestRegistry(t)
	rep := reg.ReportStrandedWorkAcrossRestart()
	if rep.Err == nil {
		t.Fatal("an unparseable witness store produced a clean report; that is 'cannot see' rendered " +
			"as 'none' (mg-0b77, mg-76e5)")
	}
	if rep.Judged() {
		t.Error("Judged() reported a complete answer over an unreadable population")
	}
	if out := logs(); !strings.Contains(out, "NOT a report that none exist") {
		t.Errorf("the log does not say what the failure means; got: %s", out)
	}
}

// TestStartupSweepSkipsAPolecatWithNoRepoWithoutCallingItClean. A --no-worktree
// polecat, or a witness written before the repo field existed, cannot be
// checked. No question was asked, so no answer may be recorded.
func TestStartupSweepSkipsAPolecatWithNoRepoWithoutCallingItClean(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	deadWitness(t, "a564", "mg-a564", "")

	reg := newDrainTestRegistry(t)
	rep := reg.ReportStrandedWorkAcrossRestart()
	if rep.Skipped != 1 || rep.Clean != 0 || rep.Stranded != 0 {
		t.Fatalf("sweep report = %+v, want Skipped=1 and nothing else — an unattributable polecat is "+
			"not a clean one", rep)
	}
	if sent := mail(); len(sent) != 0 {
		t.Fatalf("an unattributable polecat produced %d mail(s): %+v", len(sent), sent)
	}
}

// TestStartupSweepMailsALiveOrphanWithTheRightWarning. A polecat that outlived
// the restart may still be RUNNING. Its pushed work is just as invisible — the
// gate can never fire for it again — so it is reported; but the branch may still
// grow, and submitting under a working agent takes its work away mid-flight. The
// mail must say which case this is.
func TestStartupSweepMailsALiveOrphanWithTheRightWarning(t *testing.T) {
	sandboxWitness(t)
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-a564", "deliver.md", "feat(deliver): finished (mg-a564)")

	pid := liveProcess(t)
	if err := RecordPolecatWitness("a564", pid, "mg-a564", repo); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	reg := newDrainTestRegistry(t) // empty: the survivor has no entry, by construction
	rep := reg.ReportStrandedWorkAcrossRestart()
	if rep.Stranded != 1 {
		t.Fatalf("sweep report = %+v, want 1 stranded — a live orphan's pushed work is exactly as "+
			"un-instrumented as a dead one's", rep)
	}
	sent := mail()
	if len(sent) != 1 || !sent[0].StillAlive {
		t.Fatalf("alerts = %+v, want one with StillAlive=true", sent)
	}
	if _, body := sent[0].Message(); !strings.Contains(body, "THE POLECAT IS STILL RUNNING") {
		t.Errorf("the mail did not warn that the branch may still grow:\n%s", body)
	}
}

// TestStartupSweepRecordsThatItRan is this ticket's lesson applied to its own
// remedy. A clean sweep and a sweep that never executed are the same absence
// unless the clean one says so — and the only other trace is a log.Printf into
// pogod's stderr, which is exactly where this feature's output went unread for
// three months.
func TestStartupSweepRecordsThatItRan(t *testing.T) {
	sandboxWitness(t)
	logPath := useTempEventLog(t)

	reg := newDrainTestRegistry(t)
	rep := reg.ReportStrandedWorkAcrossRestart()
	if rep.Candidates != 0 || rep.Err != nil {
		t.Fatalf("sweep report = %+v, want an empty clean run", rep)
	}

	ev := findEvent(readEventLines(t, logPath), "work_item_stranded_sweep", "pogod")
	if ev == nil {
		t.Fatal("a sweep that found nothing left no trace on the event spine; 'nothing was stranded' " +
			"and 'the sweep is not running' must not be the same silence (mg-be37)")
	}
	details, _ := ev["details"].(map[string]any)
	if judged, _ := details["judged"].(bool); !judged {
		t.Errorf("event details.judged = false on a clean complete run: %v", details)
	}
}
