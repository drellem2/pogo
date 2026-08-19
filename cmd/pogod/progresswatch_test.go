package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/progresswatch"
)

// TestWorkerReadingReadsTheWorktreeNotTheRoot. gitgc.NewestWrite WALKS, and
// that is the requirement rather than an implementation detail: the root
// directory's mtime does not move when a file three levels down is rewritten,
// so a stat of the root would report a busy worker as silent.
func TestWorkerReadingReadsTheWorktree(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "internal", "pkg")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(deep, "thing.go")
	if err := os.WriteFile(f, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	written := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(f, written, written); err != nil {
		t.Fatal(err)
	}
	// The root's own mtime is deliberately made STALE, so a reader that stats
	// the root instead of walking would report the wrong answer.
	old := time.Now().Add(-9 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	got := workerReading(agent.WorkerProgress{Name: "p1", WorktreeDir: dir}, now)
	if !got.WritesKnown || !got.HasWrites {
		t.Fatalf("a written worktree read as unwritten: %+v", got)
	}
	if got.WriteIdle > 5*time.Minute {
		t.Errorf("write_idle = %s, want ~3m — the walk did not reach the nested file", got.WriteIdle)
	}
}

// TestWorkerWithNoWorktreeIsUnmeasurable, not quiet. A NoWorktree worker's work
// goes somewhere this detector cannot see, and counting its empty tree as
// silence would manufacture a finding out of a design choice.
func TestWorkerWithNoWorktreeIsUnmeasurable(t *testing.T) {
	got := workerReading(agent.WorkerProgress{Name: "p1", Age: time.Hour}, time.Now())
	if got.WritesKnown {
		t.Errorf("a worker with no worktree reported a known write state: %+v", got)
	}
	if got.WritesError == "" {
		t.Error("the reason must travel with the blindness")
	}
}

// TestVanishedWorktreeIsNotQuiet. A tree reaped between the registry read and
// the walk is the fleet making progress; either way it is not evidence of
// silence.
func TestVanishedWorktreeIsNotQuiet(t *testing.T) {
	got := workerReading(agent.WorkerProgress{
		Name: "p1", Age: time.Hour,
		WorktreeDir: filepath.Join(t.TempDir(), "gone"),
	}, time.Now())
	if got.WritesKnown {
		t.Errorf("a vanished worktree reported a known write state: %+v", got)
	}
}

// TestEmptyWorktreeMeasuresFromTheTreeItself. The walk stats the root as well
// as its contents, so an untouched tree reports its own creation rather than a
// zero — which is the same fact stated differently, and the reason this is
// asserted rather than assumed. What must NOT happen is a fresher answer than
// the tree can justify: that would read a worker that has produced nothing as
// having just written something.
func TestEmptyWorktreeMeasuresFromTheTreeItself(t *testing.T) {
	dir := t.TempDir()
	created := time.Now().Add(-20 * time.Minute)
	if err := os.Chtimes(dir, created, created); err != nil {
		t.Fatal(err)
	}
	got := workerReading(agent.WorkerProgress{
		Name: "p1", Age: time.Hour, WorktreeDir: dir,
	}, time.Now())
	if !got.WritesKnown {
		t.Fatalf("an empty but readable worktree must be a measurement: %+v", got)
	}
	if got.WriteIdle < 19*time.Minute {
		t.Errorf("write_idle = %s, want ~20m — an untouched tree must not read as freshly written",
			got.WriteIdle)
	}
}

// TestPTYFactsSurviveTheConversion, including the one that is easy to lose: a
// worker that has never written has an UNMEASURABLE idle time, not a zero one.
func TestPTYFactsSurviveTheConversion(t *testing.T) {
	got := workerReading(agent.WorkerProgress{
		Name: "p1", WorkItemID: "mg-516e", Age: 20 * time.Minute,
		PTYIdle: 90 * time.Second, HasOutput: true,
	}, time.Now())
	if got.Name != "p1" || got.WorkItemID != "mg-516e" || got.Age != 20*time.Minute {
		t.Errorf("identity lost in conversion: %+v", got)
	}
	if !got.HasOutput || got.PTYIdle != 90*time.Second {
		t.Errorf("pty facts lost in conversion: %+v", got)
	}

	silent := workerReading(agent.WorkerProgress{Name: "p2"}, time.Now())
	if silent.HasOutput || silent.PTYIdle != 0 {
		t.Errorf("a never-written PTY must stay unmeasurable: %+v", silent)
	}
}

// TestNoWorkersMeasuresZeroRatherThanGoingBlind. Attributing zero cores to an
// empty worker set is the one case where the zero is certainly right, and
// reporting it as blindness would make an idle daemon emit a permanent
// instrument-failure.
func TestNoWorkersMeasuresZeroRatherThanGoingBlind(t *testing.T) {
	var snap progresswatch.Snapshot
	readWorkerCPU(context.Background(), &snap, nil)
	if !snap.CoresKnown {
		t.Errorf("an empty worker set went blind: %+v", snap)
	}
	if snap.WorkerCores != 0 {
		t.Errorf("worker_cores = %v, want 0", snap.WorkerCores)
	}
	if snap.HostCores <= 0 {
		t.Errorf("host_cores = %d, want the host's core count — a cores figure needs a denominator",
			snap.HostCores)
	}
}

// TestNoProgressSourceIsBlindNotEmpty: with no refinery and no readable work
// items, "nothing landed" is not a fact this daemon established.
func TestNoProgressSourceIsBlindNotEmpty(t *testing.T) {
	// Point the work-item reader at a directory that is not a workspace, so
	// neither source answers.
	t.Setenv("MG_ROOT", filepath.Join(t.TempDir(), "nope"))

	var snap progresswatch.Snapshot
	snap.Now = time.Now()
	readFleetProgress(&snap, nil, time.Now().Add(-time.Hour))
	if snap.ProgressKnown {
		t.Skip("this host's work-item root answered; the no-source path cannot be exercised here")
	}
	if snap.ProgressError == "" {
		t.Error("a source that answered nothing must say why")
	}
	r := progresswatch.Evaluate(snap, progresswatch.Thresholds{})
	if r.Stalled {
		t.Error("an unreadable completion history must not read as no completions")
	}
}

// TestNoteProgressKeepsTheMostRecentAndNamesIt. An orphaned timestamp cannot be
// chased, which is why the label travels with it.
func TestNoteProgressKeepsTheMostRecentAndNamesIt(t *testing.T) {
	base := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	var snap progresswatch.Snapshot
	noteProgress(&snap, base, "merge A landed")
	noteProgress(&snap, base.Add(-time.Hour), "merge B landed")
	noteProgress(&snap, time.Time{}, "never happened")
	noteProgress(&snap, base.Add(time.Minute), "work item mg-516e done")

	if !snap.LastProgress.Equal(base.Add(time.Minute)) {
		t.Errorf("last_progress = %s, want the most recent", snap.LastProgress)
	}
	if snap.LastProgressWhat != "work item mg-516e done" {
		t.Errorf("last_progress_what = %q, want the most recent one's label", snap.LastProgressWhat)
	}
}

// TestSourceWithoutARegistryFailsRatherThanReportingAnIdleFleet. "Nobody is
// working" and "I could not look" must never collapse.
func TestSourceWithoutARegistryFails(t *testing.T) {
	src := fleetProgressSource(nil, nil, time.Now())
	if _, err := src(time.Now()); err == nil {
		t.Fatal("a source with no registry returned a snapshot")
	}
}
