package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// orphanEnv is flightEnv plus a scripted pid-liveness probe.
func orphanEnv(t *testing.T, workers Workers, alive map[int]bool) (*Watcher, *recorder, string) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	for _, d := range []string{"available", "claimed", "done"} {
		if err := os.MkdirAll(filepath.Join(workRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	w := New(config.StallWatchConfig{Enabled: true, Agent: "mayor"}, Options{
		WorkRoot: workRoot,
		MailRoot: filepath.Join(root, "mail"),
		Nudge:    rec.nudge,
		Emit:     rec.emit,
		Workers:  workers,
		PIDAlive: func(pid int) bool { return alive[pid] },
	})
	return w, rec, workRoot
}

// writeClaim writes claimed/<id>.md.<pid>, the name a claim leaves behind.
func writeClaim(t *testing.T, workRoot, id, assignee string, pid int) {
	t.Helper()
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: %s\nrepo: /src/enth\n---\n# %s\n", id, assignee, id)
	path := filepath.Join(workRoot, "claimed", fmt.Sprintf("%s.md.%d", id, pid))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func orphanFires(rec *recorder) int {
	n := 0
	for _, c := range categories(rec) {
		if c == categoryOrphanedClaim {
			n++
		}
	}
	return n
}

// The incident shape: claims under dead pids, nobody registered. Nothing on
// the first sample (grace), one notice once the grace has elapsed, naming each
// item and its pid.
func TestOrphanedClaimReportedAfterGrace(t *testing.T) {
	w, rec, root := orphanEnv(t, &fakeWorkers{}, map[int]bool{})
	writeClaim(t, root, "mg-1b3f3", "", 50235)
	writeClaim(t, root, "mg-6c0b7", "", 46585)

	t0 := time.Date(2026, 11, 8, 3, 3, 0, 0, time.UTC)
	w.Check(t0)
	if n := orphanFires(rec); n != 0 {
		t.Fatalf("fired %d time(s) on first sight; the grace must hold the first sample back", n)
	}
	w.Check(t0.Add(orphanedClaimGrace - time.Second))
	if n := orphanFires(rec); n != 0 {
		t.Fatalf("fired %d time(s) inside the grace", n)
	}
	w.Check(t0.Add(orphanedClaimGrace))
	if n := orphanFires(rec); n != 1 {
		t.Fatalf("fired %d time(s) after the grace, want 1", n)
	}
	if rec.nudges[0].agent != "mayor" {
		t.Errorf("notice went to %q, want the coordinator", rec.nudges[0].agent)
	}
	msg := rec.nudges[0].message
	for _, want := range []string{"mg-1b3f3 (claim pid 50235, dead)", "mg-6c0b7 (claim pid 46585, dead)", "mg unclaim", "pogo gc --list-preserved"} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice lacks %q:\n%s", want, msg)
		}
	}
	if subj := rec.nudges[0].subject; !strings.Contains(subj, "DEAD pid") {
		t.Errorf("subject %q does not say what is wrong", subj)
	}

	// The per-item backoff holds the very next tick.
	w.Check(t0.Add(orphanedClaimGrace + time.Minute))
	if n := orphanFires(rec); n != 1 {
		t.Fatalf("re-fired inside the cooldown: %d fires", n)
	}
}

// Positive control for the liveness half: the same claim under a LIVE pid is
// never reported, at any age.
func TestOrphanedClaimLivePIDIsNotReported(t *testing.T) {
	w, rec, root := orphanEnv(t, &fakeWorkers{}, map[int]bool{777: true})
	writeClaim(t, root, "mg-aaaa", "", 777)
	writeClaim(t, root, "mg-bbbb", "", 778) // dead: the control that the check runs at all

	t0 := time.Now()
	w.Check(t0)
	w.Check(t0.Add(orphanedClaimGrace))
	if n := orphanFires(rec); n != 1 {
		t.Fatalf("got %d fires, want exactly 1 (for the dead claim)", n)
	}
	msg := rec.nudges[0].message
	if strings.Contains(msg, "mg-aaaa") || !strings.Contains(msg, "mg-bbbb") {
		t.Errorf("notice should name only the dead claim:\n%s", msg)
	}
}

// A dead claim pid with a live worker on the item (a claim stamped by a
// short-lived `mg claim`) is not orphaned.
func TestOrphanedClaimWithLiveWorkerIsNotReported(t *testing.T) {
	workers := &fakeWorkers{flight: workedBy("mg-aaaa", "paaaa", 4242, "registry")}
	w, rec, root := orphanEnv(t, workers, map[int]bool{})
	writeClaim(t, root, "mg-aaaa", "", 99)

	t0 := time.Now()
	w.Check(t0)
	w.Check(t0.Add(2 * orphanedClaimGrace))
	if n := orphanFires(rec); n != 0 {
		t.Fatalf("reported an item a live worker is on (%d fires)", n)
	}
	if workers.calls() == 0 {
		t.Fatal("the in-flight probe was never consulted — the negative above proves nothing")
	}
}

// Dispatch-gated assignees are not this watcher's population: a human's bare
// `mg claim` records mg's own short-lived pid.
func TestOrphanedClaimSkipsGatedAssignees(t *testing.T) {
	w, rec, root := orphanEnv(t, &fakeWorkers{}, map[int]bool{})
	writeClaim(t, root, "mg-human", "human", 99)
	writeClaim(t, root, "mg-parked", "parked", 98)
	writeClaim(t, root, "mg-blocked", "blocked:mayor", 97)

	t0 := time.Now()
	w.Check(t0)
	w.Check(t0.Add(2 * orphanedClaimGrace))
	if n := orphanFires(rec); n != 0 {
		t.Fatalf("reported a dispatch-gated item (%d fires)", n)
	}
}

// A claim that is released (or re-stamped onto a new pid) during the grace
// restarts it: the observation is per (item, pid).
func TestOrphanedClaimGraceRestartsOnRestamp(t *testing.T) {
	w, rec, root := orphanEnv(t, &fakeWorkers{}, map[int]bool{})
	writeClaim(t, root, "mg-aaaa", "", 99)

	t0 := time.Now()
	w.Check(t0)
	// Re-stamp onto another (also dead) pid halfway through.
	if err := os.Rename(filepath.Join(root, "claimed", "mg-aaaa.md.99"), filepath.Join(root, "claimed", "mg-aaaa.md.100")); err != nil {
		t.Fatal(err)
	}
	w.Check(t0.Add(orphanedClaimGrace / 2))
	w.Check(t0.Add(orphanedClaimGrace))
	if n := orphanFires(rec); n != 0 {
		t.Fatalf("fired %d time(s); the re-stamp should have restarted the grace", n)
	}
	w.Check(t0.Add(orphanedClaimGrace/2 + orphanedClaimGrace))
	if n := orphanFires(rec); n != 1 {
		t.Fatalf("fired %d time(s) after the restarted grace, want 1", n)
	}
}

// With no in-flight probe the check still reports — a dead claim pid is the
// stronger half of the evidence — but says the other half went unchecked.
func TestOrphanedClaimWithoutWorkersSaysSo(t *testing.T) {
	w, rec, root := orphanEnv(t, &fakeWorkers{unknown: true}, map[int]bool{})
	writeClaim(t, root, "mg-aaaa", "", 99)

	t0 := time.Now()
	w.Check(t0)
	w.Check(t0.Add(orphanedClaimGrace))
	if n := orphanFires(rec); n != 1 {
		t.Fatalf("got %d fires, want 1", n)
	}
	if !strings.Contains(rec.nudges[0].message, "only the dead claim pid was checked") {
		t.Errorf("notice does not say the live-agent check was skipped:\n%s", rec.nudges[0].message)
	}
}

// A claimed item with no pid suffix (`mg reopen` writes one) is not judged
// here: there is no pid to be dead. reopenguard.go reports that shape.
func TestOrphanedClaimIgnoresPidlessClaims(t *testing.T) {
	w, rec, root := orphanEnv(t, &fakeWorkers{}, map[int]bool{})
	writeItemIn(t, root, "claimed", "mg-aaaa", "", "", time.Now())

	t0 := time.Now()
	w.Check(t0)
	w.Check(t0.Add(2 * orphanedClaimGrace))
	if n := orphanFires(rec); n != 0 {
		t.Fatalf("reported a pid-less claim (%d fires)", n)
	}
}

// The default liveness probe: this process is alive, an absurd pid is not.
func TestDefaultPIDAlive(t *testing.T) {
	if !defaultPIDAlive(os.Getpid()) {
		t.Error("own pid reads as dead")
	}
	if defaultPIDAlive(0) || defaultPIDAlive(-1) {
		t.Error("non-positive pid reads as alive")
	}
	if defaultPIDAlive(99999999) {
		t.Error("pid 99999999 reads as alive")
	}
}
