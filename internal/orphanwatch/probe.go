package orphanwatch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// This file builds a REAL orphan and asks the real detector about it.
//
// The acceptance bar for anything added under mg-4518 is that it be proven to go
// RED against an orphan someone constructed, not merely GREEN against a clean
// host. A detector is only as good as its failing arm, and a failing arm nobody
// exercises is indistinguishable from one that cannot fire — three controls on
// this fleet were caught inert precisely because somebody finally ran the
// failing side.
//
// So the probe does not stub the process table. It starts two CPU burners in a
// throwaway polecats tree, detaches one from its parent so it genuinely
// reparents to launchd, tells the scan that one owner is dead and the other is
// alive, and checks BOTH arms:
//
//	RED    the burner whose owner is dead is reported
//	GREEN  the burner whose owner is alive is spared
//
// The second arm is not decoration. It is the exact case that killed the ppid
// heuristic: on 2026-08-07 four live workers carried the identical ppid=1,
// high-CPU signature, and a rule without this arm would have destroyed all four.

// probeBurnSeconds bounds each burner's life. Every burner exits on its own
// whether or not the probe gets to kill it, so a probe killed mid-run cannot
// leave behind the very thing it exists to detect.
const probeBurnSeconds = 90

// ProbeResult is what one probe run observed. Every field is a measurement; the
// verdict is derived from them in Passed so a reader can check the derivation.
type ProbeResult struct {
	// DeadOwner and LiveOwner are the two synthetic polecat names.
	DeadOwner string `json:"dead_owner"`
	LiveOwner string `json:"live_owner"`
	// OrphanPID is the burner under the dead owner, DetachedPPID its parent id
	// after its launching shell exited. On a host where reparenting works this
	// is 1, which is the signature the ticket warned against keying on — it is
	// recorded so the report can show the detector reached its verdict without
	// it.
	OrphanPID    int `json:"orphan_pid"`
	DetachedPPID int `json:"detached_ppid"`
	ControlPID   int `json:"control_pid"`
	ControlPPID  int `json:"control_ppid"`
	OrphanCores  float64
	ControlCores float64
	// Reported is true when the scan named OrphanPID. This is the failing arm.
	Reported bool `json:"reported"`
	// Spared is true when the scan did NOT name ControlPID. This is the
	// positive control.
	Spared bool `json:"spared"`
	// Report is the scan itself, so a failing probe can be read without a rerun.
	Report Report `json:"report"`
	// Err is set when the probe could not be conducted at all — which is
	// neither a pass nor a fail, and must be rendered as "measured nothing".
	Err error `json:"-"`
}

// Passed reports whether both arms behaved.
func (p ProbeResult) Passed() bool { return p.Err == nil && p.Reported && p.Spared }

// Summary renders the probe as the two sentences a reader needs.
func (p ProbeResult) Summary() string {
	if p.Err != nil {
		return fmt.Sprintf("probe could not run: %v", p.Err)
	}
	red := "did NOT report"
	if p.Reported {
		red = "REPORTED"
	}
	green := "also reported it (WRONG)"
	if p.Spared {
		green = "spared it"
	}
	return fmt.Sprintf(
		"constructed orphan pid=%d (ppid=%d, owner %s dead, %.2f cores): detector %s it\n"+
			"live-owner control pid=%d (ppid=%d, owner %s alive, %.2f cores): detector %s",
		p.OrphanPID, p.DetachedPPID, p.DeadOwner, p.OrphanCores, red,
		p.ControlPID, p.ControlPPID, p.LiveOwner, p.ControlCores, green)
}

// Probe constructs the population described above under root (a throwaway
// directory the caller owns) and runs the real Scan against it.
//
// It kills only the two pids it started, by pid. Nothing here matches processes
// by name or pattern: an unanchored `pkill -f` has taken this box out before by
// matching every process on the machine, the fleet's own pollers included.
func Probe(root string) ProbeResult {
	res := ProbeResult{DeadOwner: "zzdead", LiveOwner: "zzlive"}

	orphan, err := startDetachedBurner(filepath.Join(root, res.DeadOwner))
	if err != nil {
		res.Err = fmt.Errorf("start orphan burner: %w", err)
		return res
	}
	defer kill(orphan)
	control, err := startDetachedBurner(filepath.Join(root, res.LiveOwner))
	if err != nil {
		res.Err = fmt.Errorf("start control burner: %w", err)
		return res
	}
	defer kill(control)
	res.OrphanPID, res.ControlPID = orphan, control

	// Give both burners a moment to accumulate CPU time and to be reparented,
	// so the first sample of the window already has a reading to difference.
	time.Sleep(750 * time.Millisecond)
	res.DetachedPPID = parentOf(orphan)
	res.ControlPPID = parentOf(control)

	rep, err := Scan(Options{
		PolecatsRoot: root,
		LiveOwners: func() (map[string]bool, error) {
			// The registry's answer, stood in: one owner running, one gone.
			return map[string]bool{res.LiveOwner: true}, nil
		},
	})
	if err != nil {
		res.Err = err
		return res
	}
	res.Report = rep
	for _, o := range rep.Orphans {
		switch o.PID {
		case orphan:
			res.Reported = true
			res.OrphanCores = o.Cores
		case control:
			res.ControlCores = o.Cores
		}
	}
	res.Spared = true
	for _, o := range rep.Orphans {
		if o.PID == control {
			res.Spared = false
		}
	}
	return res
}

// startDetachedBurner creates dir, writes a shell burner into it, and starts it
// so that its launching shell exits immediately — leaving the burner running
// with dir as its working directory and no parent above it.
//
// This reproduces the reported mechanism rather than simulating it: the same
// `nohup ... & ` from a shell that then returns which p00a1 confirmed it uses to
// start parallel searches, and which reparents every worker it launches.
func startDetachedBurner(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	script := filepath.Join(dir, "burn.sh")
	// The inner loop is arithmetic done IN the shell, and that is deliberate.
	// A burner written as `while [ "$(date +%s)" -lt "$end" ]` spends its life
	// blocked in wait() while a forked `date` does the only work, so the process
	// the detector is looking at accumulates almost no CPU of its own and the
	// probe passes or fails on scheduling luck — it did both across two runs
	// before this was changed. The clock is only consulted between batches.
	body := fmt.Sprintf(`#!/bin/sh
end=$(($(date +%%s) + %d))
while :; do
  i=0
  while [ "$i" -lt 200000 ]; do i=$((i+1)); done
  [ "$(date +%%s)" -ge "$end" ] && break
done
`, probeBurnSeconds)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		return 0, err
	}
	pidFile := filepath.Join(dir, "burn.pid")
	// The outer sh records the burner's pid and exits at once; the burner is
	// then reparented. `cd` first so the burner's cwd is the polecat directory,
	// which is the property the detector reads.
	//
	// The braces are load-bearing. Without them `&` backgrounds the whole
	// `cd ... && nohup ...` list, so $! is a subshell that then waits out the
	// burner — the launcher blocks for the burner's full life and nothing is
	// ever orphaned. Grouping backgrounds only the burner.
	launch := fmt.Sprintf(`cd %q && { nohup ./burn.sh >/dev/null 2>&1 & echo $! > %q; }`, dir, pidFile)
	cmd := exec.Command("sh", "-c", launch)
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("burner pid %q unreadable", strings.TrimSpace(string(raw)))
	}
	return pid, nil
}

// parentOf reads a pid's current parent, reported by the probe purely as
// evidence about what the detector did NOT use.
func parentOf(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return ppid
}

// kill signals one pid the probe started. By pid, never by pattern.
func kill(pid int) {
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
