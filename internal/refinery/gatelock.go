package refinery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// This file makes a refinery worktree remember WHICH gate is running in it, so
// that a gate outliving the pogod that started it is found and dealt with
// instead of being run alongside (mg-58f3).
//
// # What happened
//
// Measured live on 2026-09-26: pogod was restarted while a gate for
// mr-dag4ms2tjv1hjkm214r0 was running. runGate starts every gate with Setpgid,
// so the gate's process group is not pogod's, and a signal aimed at pogod's
// group does not reach it. The gate survived, reparented to launchd, with
// nothing left to collect its result. The new pogod recovered the MR from the
// state file, re-queued it, and started a SECOND `./build.sh` with the same
// cwd, so two `go test ./...` runs shared one worktree. Recovery also runs
// `git reset --hard` in that worktree, under the orphan's feet.
//
// Nothing could have noticed. The state file does carry the old gate's pid in
// Progress.GatePID, but recovery resets Progress when it re-queues, and nothing
// consulted the pid first. And a worktree held no record of its occupant at
// all.
//
// # The lock
//
// runGate writes gateLockFile into the clone's .git directory the moment the
// gate process exists. It is in .git so `git reset --hard` and `git clean`
// cannot remove it, and so it lives as long as the worktree does. It records
// the gate's process group, which pogod started it (by pid), and which MR it
// belongs to. It is removed when the gate's whole process group is gone, not
// when the gate shell exits: a gate whose shell returned while a descendant
// kept running is still occupying the tree.
//
// # Who reads it, and what they do
//
// ensureWorktree reads it before handing the tree to anything, so it guards
// both callers — processMerge, before a gate, and recovery, before its reset.
// Start also sweeps every worktree once, before recovery, so an orphan is found
// on startup even when its MR was not in the state file.
//
//   - the lock's group is gone          => stale; remove it, proceed.
//   - its pogod is not this process and
//     is no longer running              => ORPHAN. The only reader of its
//     result died, so it can never be consumed; the MR it belonged to will be
//     re-run by this pogod. Reap it: SIGTERM the group, then SIGKILL, and say
//     so, naming the pids. Reaping is the right call and not merely a safe
//     one, because leaving it is exactly the collision this ticket is about.
//   - its pogod is THIS process, and the
//     MR it names is still processing   => REFUSE. A live gate of this pogod
//     is using the tree (two repos with the same basename share a clone).
//     Killing it would fail an unrelated merge.
//   - its pogod is another LIVE process => REFUSE. A second daemon is using
//     this worktree directory; it is not ours to kill.
//   - its pogod is THIS process and the
//     MR is not in flight               => leftover descendants of a gate
//     this pogod already finished. Reap.
//
// A refusal is an error from ensureWorktree, and it NAMES the occupant — the
// pgid, the pogod pid, the MR and the branch — so the failure that surfaces on
// the MR says what is in the way instead of presenting as a defect in the
// branch.
//
// # Pid reuse
//
// Killing by a recorded number is only safe if the number still means what it
// meant. POSIX does not reuse a process-group id while the group has members,
// so a live group whose leader is dead is necessarily the original group. The
// one hazard is a live LEADER: the original group emptied, the pid was reused,
// and the new process made itself a leader. That process started after the
// lock was written, so a leader younger than the lock is treated as a stranger
// and the lock as stale. The same test decides whether the recorded pogod pid
// still names that pogod.

// gateLockFile is the lock's name inside the clone's .git directory.
const gateLockFile = "pogo-gate.lock"

// gateLockClockSlack absorbs the gap between cmd.Start and the lock write, and
// the whole-second resolution of `ps -o etime`: a leader that started this
// much after the lock's timestamp is still the one the lock names.
const gateLockClockSlack = 3 * time.Second

// Reap timings. Vars so tests need not wait out a real grace period.
var (
	gateReapTermGrace = 5 * time.Second
	gateReapKillGrace = 2 * time.Second
	gateReapPoll      = 50 * time.Millisecond
)

// gateLock is the on-disk record of a worktree's occupant.
type gateLock struct {
	// PGID is the gate's process group. runGate's Setpgid makes it equal to
	// the gate shell's pid.
	PGID int `json:"pgid"`
	// PogodPID is the pid of the pogod that started the gate — the only
	// process that could ever collect its result.
	PogodPID int       `json:"pogod_pid"`
	MRID     string    `json:"mr_id,omitempty"`
	Repo     string    `json:"repo,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Gate     string    `json:"gate,omitempty"`
	Started  time.Time `json:"started"`
}

func (l gateLock) describe() string {
	s := fmt.Sprintf("gate process group %d started by pogod pid %d at %s", l.PGID, l.PogodPID,
		l.Started.UTC().Format(time.RFC3339))
	if l.MRID != "" {
		s += fmt.Sprintf(" for MR %s", l.MRID)
	}
	if l.Branch != "" {
		s += fmt.Sprintf(" (branch %s)", l.Branch)
	}
	if l.Gate != "" {
		s += fmt.Sprintf(", running %q", l.Gate)
	}
	return s
}

func gateLockPath(wtDir string) string {
	return filepath.Join(wtDir, ".git", gateLockFile)
}

// writeGateLock records the gate now occupying wtDir. Best-effort: a failed
// write costs only the orphan detection for this run, and must not fail a gate
// that is otherwise fine — but it is logged, because a silent failure here is
// the whole defect coming back.
func writeGateLock(wtDir string, l gateLock) {
	data, err := json.Marshal(l)
	if err == nil {
		err = os.WriteFile(gateLockPath(wtDir), data, 0o644)
	}
	if err != nil {
		log.Printf("refinery: could not record gate lock in %s (%v) — an orphan of this gate would go undetected", wtDir, err)
	}
}

// releaseGateLock removes wtDir's lock if it still names pgid and that group
// has no members left. A group that outlived its shell keeps the lock, so the
// next caller of ensureWorktree finds and reaps what remains.
func releaseGateLock(wtDir string, pgid int) {
	l, ok, _ := readGateLock(wtDir)
	if !ok || l.PGID != pgid {
		return
	}
	if groupAlive(pgid) {
		log.Printf("refinery: gate shell exited but its process group %d still has members in %s — keeping the gate lock so the next run reaps them", pgid, wtDir)
		return
	}
	_ = os.Remove(gateLockPath(wtDir))
}

// readGateLock loads wtDir's lock. ok is false when there is none; an
// unreadable lock is an error, and is treated by the caller as no lock with a
// log line, because it can name no process to act on.
func readGateLock(wtDir string) (gateLock, bool, error) {
	data, err := os.ReadFile(gateLockPath(wtDir))
	if errors.Is(err, os.ErrNotExist) {
		return gateLock{}, false, nil
	}
	if err != nil {
		return gateLock{}, false, err
	}
	var l gateLock
	if err := json.Unmarshal(data, &l); err != nil {
		return gateLock{}, false, fmt.Errorf("parse %s: %w", gateLockPath(wtDir), err)
	}
	if l.PGID <= 1 {
		return gateLock{}, false, fmt.Errorf("%s names no usable process group (pgid %d)", gateLockPath(wtDir), l.PGID)
	}
	return l, true, nil
}

// groupAlive reports whether any process is still in group pgid. EPERM means
// the group exists and belongs to someone else — alive, and not ours.
func groupAlive(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// pidAlive reports whether a process with this pid exists.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processStartTime returns when pid started, from `ps -o etime=`. A var so
// tests can stand in a reused pid.
var processStartTime = func(pid int) (time.Time, bool) {
	out, err := exec.Command("ps", "-o", "etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	age, ok := parseEtime(strings.TrimSpace(string(out)))
	if !ok {
		return time.Time{}, false
	}
	return time.Now().Add(-age), true
}

// parseEtime reads ps's elapsed-time column: [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	var days int
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var secs int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, true
}

// stillNames reports whether a live pid is the process that existed when the
// lock was written, rather than a later process that reused the number. An
// unreadable start time is taken as "still names it": the cost of that error
// is a refusal or a reap of a group that POSIX says is the original anyway.
func stillNames(pid int, lockStarted time.Time) bool {
	started, ok := processStartTime(pid)
	if !ok {
		return true
	}
	return !started.After(lockStarted.Add(gateLockClockSlack))
}

// errWorktreeOccupied marks a refusal to use a worktree another live gate
// holds.
var errWorktreeOccupied = errors.New("refinery worktree occupied by a live gate")

// guardGateWorktree deals with whatever gate a previous run left in wtDir,
// before the tree is used. It returns nil when the tree is free (including
// after a successful reap), and an error naming the occupant when it is not.
//
// forMR is the merge request about to use the tree, "" for the startup sweep.
// A lock naming forMR itself, from this pogod, is that MR's own earlier gate
// and never a reason to refuse: the MR's lane is the only thing that runs its
// gates, so whatever is left of them is leftover.
func (r *Refinery) guardGateWorktree(wtDir, forMR string) error {
	l, ok, err := readGateLock(wtDir)
	if err != nil {
		log.Printf("refinery: ignoring unreadable gate lock in %s: %v", wtDir, err)
		_ = os.Remove(gateLockPath(wtDir))
		return nil
	}
	if !ok {
		return nil
	}
	if !groupAlive(l.PGID) {
		_ = os.Remove(gateLockPath(wtDir))
		return nil
	}
	if pidAlive(l.PGID) && !stillNames(l.PGID, l.Started) {
		// The leader pid was reused by a process younger than the lock, so
		// the group the lock named is gone and this is a stranger's.
		log.Printf("refinery: gate lock in %s names pgid %d, now led by a process started after the lock — stale, removing", wtDir, l.PGID)
		_ = os.Remove(gateLockPath(wtDir))
		return nil
	}

	self := os.Getpid()
	switch {
	case l.PogodPID == self && l.MRID != forMR && r.mrInFlight(l.MRID):
		return fmt.Errorf("%w: %s is still running in %s under this pogod — refusing to start another gate in the same tree",
			errWorktreeOccupied, l.describe(), wtDir)
	case l.PogodPID != self && pidAlive(l.PogodPID) && stillNames(l.PogodPID, l.Started):
		return fmt.Errorf("%w: %s is running in %s, and pogod pid %d is still alive — refusing to share the tree with another daemon's gate",
			errWorktreeOccupied, l.describe(), wtDir, l.PogodPID)
	}

	why := "ORPHANED: the pogod that started it is gone, so nothing can ever consume its result"
	if l.PogodPID == self {
		why = "left over from a gate this pogod already finished"
	}
	log.Printf("refinery: reaping %s in %s — %s", l.describe(), wtDir, why)
	outcome, err := reapGateGroup(l.PGID)
	emitOrphanGateReaped(wtDir, l, why, outcome, err)
	if err != nil {
		return fmt.Errorf("%w: %s in %s is %s and could not be reaped: %v",
			errWorktreeOccupied, l.describe(), wtDir, why, err)
	}
	log.Printf("refinery: reaped gate process group %d in %s (%s)", l.PGID, wtDir, outcome)
	_ = os.Remove(gateLockPath(wtDir))
	return nil
}

// ensureGateWorktree is ensureWorktree for a caller about to act on mr in the
// tree: it additionally clears — or refuses, naming — a gate still occupying it.
func (r *Refinery) ensureGateWorktree(mr *MergeRequest) (string, error) {
	wtDir, err := r.ensureWorktree(mr.RepoPath)
	if err != nil {
		return "", err
	}
	if err := r.guardGateWorktree(wtDir, mr.ID); err != nil {
		return "", err
	}
	return wtDir, nil
}

// mrInFlight reports whether this refinery is currently processing id.
func (r *Refinery) mrInFlight(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	mr, ok := r.byID[id]
	return ok && mr.Status == StatusProcessing
}

// reapGateGroup terminates process group pgid: SIGTERM, a grace period, then
// SIGKILL. It returns which signal ended it.
func reapGateGroup(pgid int) (string, error) {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", fmt.Errorf("SIGTERM group %d: %w", pgid, err)
	}
	if waitGroupGone(pgid, gateReapTermGrace) {
		return "ended by SIGTERM", nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", fmt.Errorf("SIGKILL group %d: %w", pgid, err)
	}
	if waitGroupGone(pgid, gateReapKillGrace) {
		return "ended by SIGKILL after ignoring SIGTERM for " + gateReapTermGrace.String(), nil
	}
	return "", fmt.Errorf("group %d still has members after SIGKILL", pgid)
}

func waitGroupGone(pgid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if !groupAlive(pgid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(gateReapPoll)
	}
}

// sweepGateWorktrees runs the guard over every refinery worktree once. Called
// from Start before recovery, so an orphan from the previous pogod is found at
// startup whether or not its MR survived in the state file. A refusal here is
// only logged: the merge that next wants the tree will hit the same guard and
// fail with the occupant named.
func (r *Refinery) sweepGateWorktrees() {
	entries, err := os.ReadDir(r.cfg.WorktreeDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wtDir := filepath.Join(r.cfg.WorktreeDir, e.Name())
		if err := r.guardGateWorktree(wtDir, ""); err != nil {
			log.Printf("refinery: startup sweep: %v", err)
		}
	}
}

// emitOrphanGateReaped records a reap in the event log, success or not.
func emitOrphanGateReaped(wtDir string, l gateLock, why, outcome string, err error) {
	d := map[string]any{
		"worktree":         wtDir,
		"pgid":             l.PGID,
		"pogod_pid":        l.PogodPID,
		"merge_request_id": l.MRID,
		"branch":           l.Branch,
		"gate":             l.Gate,
		"gate_started":     l.Started.UTC().Format(time.RFC3339),
		"reason":           why,
		"outcome":          outcome,
	}
	if err != nil {
		d["error"] = summarizeReason(err)
	}
	events.Emit(context.Background(), events.Event{
		EventType: "refinery_orphan_gate_reaped",
		Agent:     "refinery",
		Repo:      l.Repo,
		Details:   d,
	})
}
