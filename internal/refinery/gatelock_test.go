package refinery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests cover mg-58f3. A pogod restart left a gate running in its own
// process group, and the new pogod started a second gate in the same worktree
// beside it. Each test puts a REAL process group into a worktree, because the
// defect is about processes that outlive the code that tracked them, and a
// stubbed process table cannot outlive anything.

// newLockedTree returns a worktree-shaped directory: gatelock keeps its lock
// under .git.
func newLockedTree(t *testing.T) string {
	t.Helper()
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wt
}

// startGroup starts `sleep` as the leader of its own process group, the way
// runGate starts a gate. Its cwd is wt, as a real gate's would be. The cleanup
// kills the group whatever the test did to it.
func startGroup(t *testing.T, wt string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.Dir = wt
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	// Reap is observed through groupAlive, which needs the zombie collected.
	// The goroutine above does that, so a killed group becomes invisible.
	return cmd
}

// deadPID returns a pid that no longer names a process.
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

func fastReap(t *testing.T) {
	t.Helper()
	term, kill, poll := gateReapTermGrace, gateReapKillGrace, gateReapPoll
	gateReapTermGrace, gateReapKillGrace, gateReapPoll = 2*time.Second, 2*time.Second, 10*time.Millisecond
	t.Cleanup(func() { gateReapTermGrace, gateReapKillGrace, gateReapPoll = term, kill, poll })
}

func waitGone(t *testing.T, pgid int) bool {
	t.Helper()
	return waitGroupGone(pgid, 3*time.Second)
}

// The incident, reduced. The lock names a gate whose pogod is dead, which is
// what the new pogod found on 2026-09-26. The guard must end the orphan before
// the tree is handed to anyone.
func TestGuardReapsAGateOrphanedByADeadPogod(t *testing.T) {
	fastReap(t)
	wt := newLockedTree(t)
	orphan := startGroup(t, wt)
	pgid := orphan.Process.Pid
	writeGateLock(wt, gateLock{PGID: pgid, PogodPID: deadPID(t), MRID: "mr-dag4", Branch: "polecat-t4d59",
		Gate: "./build.sh", Started: time.Now().Add(-time.Second)})

	if !groupAlive(pgid) {
		t.Fatal("POSITIVE CONTROL: the orphan's group is not alive before the guard runs — the test proves nothing")
	}
	r := &Refinery{byID: map[string]*MergeRequest{}}
	if err := r.guardGateWorktree(wt, "mr-dag4"); err != nil {
		t.Fatalf("an orphan of a dead pogod must be reaped, not refused: %v", err)
	}
	if !waitGone(t, pgid) {
		t.Fatalf("the orphan's process group %d is still alive after the guard returned nil", pgid)
	}
	if _, err := os.Stat(gateLockPath(wt)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the lock must be removed once its group is reaped (stat err %v)", err)
	}
}

// The startup sweep finds the same orphan with no MR involved. That covers
// recovery, and also an orphan whose MR never reached the state file.
func TestStartupSweepReapsOrphansInEveryWorktree(t *testing.T) {
	fastReap(t)
	root := t.TempDir()
	wt := filepath.Join(root, "pogo")
	if err := os.MkdirAll(filepath.Join(wt, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := startGroup(t, wt)
	writeGateLock(wt, gateLock{PGID: orphan.Process.Pid, PogodPID: deadPID(t), Started: time.Now().Add(-time.Second)})

	r := &Refinery{cfg: Config{WorktreeDir: root}, byID: map[string]*MergeRequest{}}
	r.sweepGateWorktrees()
	if !waitGone(t, orphan.Process.Pid) {
		t.Fatal("the startup sweep left the previous pogod's gate running")
	}
}

// A live gate of THIS pogod, belonging to another MR that is still in flight,
// shares the tree when two repos share a basename. Killing it would fail an
// unrelated merge, so the guard refuses and names the gate.
func TestGuardRefusesAndNamesALiveGateOfThisPogod(t *testing.T) {
	fastReap(t)
	wt := newLockedTree(t)
	live := startGroup(t, wt)
	pgid := live.Process.Pid
	writeGateLock(wt, gateLock{PGID: pgid, PogodPID: os.Getpid(), MRID: "mr-other", Branch: "b-other",
		Started: time.Now().Add(-time.Second)})

	r := &Refinery{byID: map[string]*MergeRequest{"mr-other": {ID: "mr-other", Status: StatusProcessing}}}
	err := r.guardGateWorktree(wt, "mr-me")
	if !errors.Is(err, errWorktreeOccupied) {
		t.Fatalf("want errWorktreeOccupied, got %v", err)
	}
	for _, want := range []string{strconv.Itoa(pgid), "mr-other", "b-other", wt} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the occupant; %q missing from: %v", want, err)
		}
	}
	if !groupAlive(pgid) {
		t.Error("a refusal must not kill the live gate")
	}

	// The same lock, the MR no longer in flight, means what is left is residue
	// from a gate this pogod already finished, and it gets reaped.
	r.byID["mr-other"].Status = StatusMerged
	if err := r.guardGateWorktree(wt, "mr-me"); err != nil {
		t.Fatalf("leftovers of a finished gate must be reaped: %v", err)
	}
	if !waitGone(t, pgid) {
		t.Error("leftover group still alive")
	}
}

// A lock naming the asking MR itself is that MR's own earlier gate. Its lane
// is the only thing that runs its gates, so the guard must never refuse it.
// Refusing would wedge a retry against its own residue.
func TestGuardReapsTheAskingMRsOwnResidue(t *testing.T) {
	fastReap(t)
	wt := newLockedTree(t)
	g := startGroup(t, wt)
	writeGateLock(wt, gateLock{PGID: g.Process.Pid, PogodPID: os.Getpid(), MRID: "mr-me", Started: time.Now().Add(-time.Second)})
	r := &Refinery{byID: map[string]*MergeRequest{"mr-me": {ID: "mr-me", Status: StatusProcessing}}}
	if err := r.guardGateWorktree(wt, "mr-me"); err != nil {
		t.Fatalf("got %v", err)
	}
	if !waitGone(t, g.Process.Pid) {
		t.Error("residue not reaped")
	}
}

// Another pogod that is still RUNNING owns the gate, for example a sandbox
// daemon pointed at the same worktree dir. That gate is not ours to kill.
func TestGuardRefusesAGateOwnedByAnotherLivePogod(t *testing.T) {
	wt := newLockedTree(t)
	other := startGroup(t, t.TempDir()) // stands in for the other daemon
	gate := startGroup(t, wt)
	writeGateLock(wt, gateLock{PGID: gate.Process.Pid, PogodPID: other.Process.Pid, Started: time.Now().Add(time.Second)})

	r := &Refinery{byID: map[string]*MergeRequest{}}
	err := r.guardGateWorktree(wt, "mr-me")
	if !errors.Is(err, errWorktreeOccupied) || !strings.Contains(err.Error(), strconv.Itoa(other.Process.Pid)) {
		t.Fatalf("want a refusal naming pogod pid %d, got %v", other.Process.Pid, err)
	}
	if !groupAlive(gate.Process.Pid) {
		t.Error("another live daemon's gate was killed")
	}
}

// Pid reuse. The group the lock named is led by a process that started AFTER
// the lock was written, so that number now belongs to a stranger. Nothing may
// be killed on the strength of a recycled number.
func TestGuardDoesNotKillAStrangerThatReusedTheLeaderPID(t *testing.T) {
	wt := newLockedTree(t)
	stranger := startGroup(t, t.TempDir())
	writeGateLock(wt, gateLock{PGID: stranger.Process.Pid, PogodPID: deadPID(t),
		Started: time.Now().Add(-time.Hour)})

	r := &Refinery{byID: map[string]*MergeRequest{}}
	if err := r.guardGateWorktree(wt, "mr-me"); err != nil {
		t.Fatalf("a reused pid is a stale lock, not an occupant: %v", err)
	}
	if !groupAlive(stranger.Process.Pid) {
		t.Fatal("the guard killed a process that merely reused the recorded pid")
	}
	if _, err := os.Stat(gateLockPath(wt)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the stale lock should have been removed")
	}
}

// A lock whose group is gone is stale, and it is cleared without fuss.
func TestGuardClearsALockWhoseGroupIsGone(t *testing.T) {
	wt := newLockedTree(t)
	writeGateLock(wt, gateLock{PGID: deadPID(t), PogodPID: deadPID(t), Started: time.Now()})
	r := &Refinery{byID: map[string]*MergeRequest{}}
	if err := r.guardGateWorktree(wt, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gateLockPath(wt)); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale lock not removed")
	}
}

// runGate writes the lock while the gate runs. The gate itself reads the lock,
// so the test observes it from inside the occupancy it describes. runGate
// removes the lock when the group is empty.
func TestRunGateHoldsTheLockForExactlyTheGatesLifetime(t *testing.T) {
	wt := newLockedTree(t)
	w := &gateWatch{mr: &MergeRequest{ID: "mr-lock", Branch: "polecat-lock"}, excerpt: newExcerptBuffer()}
	out, err := runGate(context.Background(), wt, "cat .git/"+gateLockFile, 0, w)
	if err != nil {
		t.Fatalf("gate failed: %v\n%s", err, out)
	}
	var l gateLock
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &l); err != nil {
		t.Fatalf("the gate could not read its own lock (%v); output: %q", err, out)
	}
	if l.PGID != int(w.pid.Load()) || l.PogodPID != os.Getpid() || l.MRID != "mr-lock" || l.Branch != "polecat-lock" {
		t.Errorf("lock does not describe the running gate: %+v (gate pid %d)", l, w.pid.Load())
	}
	if _, err := os.Stat(gateLockPath(wt)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the lock outlived a gate whose whole group had exited")
	}
}

// A gate shell that returns while a descendant keeps running still occupies
// the tree. The lock must survive the shell so the next guard reaps the
// descendant. The observed orphan was a descendant chain several levels deep.
func TestRunGateKeepsTheLockWhileItsGroupOutlivesTheShell(t *testing.T) {
	fastReap(t)
	wt := newLockedTree(t)
	w := &gateWatch{excerpt: newExcerptBuffer()}
	if _, err := runGate(context.Background(), wt, "sleep 60 >/dev/null 2>&1 &", 0, w); err != nil {
		t.Fatal(err)
	}
	pgid := int(w.pid.Load())
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	if !groupAlive(pgid) {
		t.Fatal("POSITIVE CONTROL: the backgrounded descendant is not alive — the test proves nothing")
	}
	if _, ok, _ := readGateLock(wt); !ok {
		t.Fatal("the lock was released while the gate's group still had a live member")
	}
	r := &Refinery{byID: map[string]*MergeRequest{}}
	if err := r.guardGateWorktree(wt, "mr-next"); err != nil {
		t.Fatal(err)
	}
	// The descendant was reparented away from us, so nothing here collects
	// it, and a zombie still counts as a group member until init reaps it.
	// Wait for init, which is prompt, rather than asserting instantly.
	if !waitGone(t, pgid) {
		t.Error("the descendant survived the guard")
	}
}

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:05":       5 * time.Second,
		"12:34":       12*time.Minute + 34*time.Second,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:02:03":  49*time.Hour + 2*time.Minute + 3*time.Second,
		"10-00:00:00": 240 * time.Hour,
	} {
		if got, ok := parseEtime(in); !ok || got != want {
			t.Errorf("parseEtime(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "5", "a:b", "1:2:3:4", "x-01:02"} {
		if _, ok := parseEtime(bad); ok {
			t.Errorf("parseEtime(%q) accepted garbage", bad)
		}
	}
}
