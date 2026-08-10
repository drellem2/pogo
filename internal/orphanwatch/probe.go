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

// probeScanAttempts bounds how many times one probe re-runs the SCAN against the
// same two burners before giving up and reporting itself blind.
//
// Re-scanning is cheap and re-constructing is not: the burners are already
// running for probeBurnSeconds, so another attempt costs one sampling window
// plus one cwd read and starts no new processes. It is worth having because both
// ways this probe goes blind are TRANSIENT properties of the host — an lsof that
// refused a pid this second, a burner the scheduler starved through one 2s
// window — and neither says anything about the detector. On 2026-08-10 the first
// of those failed a merge gate on a branch that had never touched this package.
const probeScanAttempts = 3

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
	// OrphanDisposition and ControlDisposition are the buckets the scan put each
	// constructed process in. Empty means the process never met the rate floor,
	// which is the other way an arm can measure nothing.
	OrphanDisposition  Disposition `json:"orphan_disposition,omitempty"`
	ControlDisposition Disposition `json:"control_disposition,omitempty"`
	// Attempts is how many scans it took, out of probeScanAttempts.
	Attempts int `json:"attempts"`
	// Report is the scan itself, so a failing probe can be read without a rerun.
	Report Report `json:"report"`
	// Blind is set when the probe was CONSTRUCTED but one of its arms was not
	// conducted — the scan could say nothing about a process the probe made. It
	// is neither a pass nor a fail of the detector; it is the probe reporting
	// that it measured nothing, and it is a fact about the HOST.
	//
	// The two ways in (mg-db12), both load-driven and both observed:
	//
	//	the pid never met the rate floor   a shell burner sharing 10 cores with
	//	                                   a load of 33 can miss 0.20 cores for
	//	                                   a whole 2s window.
	//	cwd_unreadable                     lsof refused or timed out on the pid.
	//	                                   Measured live: the constructed orphan
	//	                                   was binned here, the test read only
	//	                                   `Reported == false`, and the gate
	//	                                   classed it as a DEFECT of an unrelated
	//	                                   branch.
	//
	// Err, by contrast, means the probe could not be BUILT at all.
	Blind string `json:"blind,omitempty"`
	// Err is set when the probe could not be conducted at all — which is
	// neither a pass nor a fail, and must be rendered as "measured nothing".
	Err error `json:"-"`
}

// InstrumentFailure reports whether the probe proved nothing either way, whether
// because it could not be built (Err) or because an arm was not conducted
// (Blind). Neither is a verdict on the detector.
func (p ProbeResult) InstrumentFailure() bool { return p.Err != nil || p.Blind != "" }

// Passed reports whether both arms were CONDUCTED and both behaved. A blind run
// has not passed — that is the same rule verdictwatch.ProbeResult.Passed
// applies, and for the same reason: a green light earned by a fixture that did
// not fire is the failure this family keeps rediscovering.
func (p ProbeResult) Passed() bool {
	return !p.InstrumentFailure() && p.Reported && p.Spared
}

// Summary renders the probe as the two sentences a reader needs.
func (p ProbeResult) Summary() string {
	if p.Err != nil {
		return fmt.Sprintf("probe could not run: %v", p.Err)
	}
	if p.Blind != "" {
		return fmt.Sprintf(
			"probe measured NOTHING about the detector after %d scan(s): %s\n"+
				"constructed orphan pid=%d disposition=%q, live-owner control pid=%d disposition=%q\n"+
				"(an empty disposition means the process never met the %.2f-core floor)",
			p.Attempts, p.Blind,
			p.OrphanPID, p.OrphanDisposition, p.ControlPID, p.ControlDisposition,
			p.Report.Floor)
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

	// Scan, and re-scan while the result says nothing about the detector. The
	// burners outlive every attempt by construction, so a retry re-measures the
	// SAME population rather than building a new one — which is what makes it
	// legitimate to retry at all: nothing about the constructed input changes
	// between attempts, only the host's willingness to let it be observed.
	for attempt := 1; attempt <= probeScanAttempts; attempt++ {
		res.Attempts = attempt
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
		res.readArms(orphan, control)
		if res.Blind == "" {
			break
		}
	}
	return res
}

// readArms reads both arms out of the scan just recorded, and decides whether
// what came back is a verdict on the detector or a fact about the host.
//
// Every field it sets is overwritten from scratch, because it runs once per
// attempt and a stale reading from a blind attempt would be indistinguishable
// from a fresh one.
func (p *ProbeResult) readArms(orphan, control int) {
	p.Reported, p.Spared = false, true
	p.OrphanCores, p.ControlCores = 0, 0
	for _, o := range p.Report.Orphans {
		switch o.PID {
		case orphan:
			p.Reported = true
			p.OrphanCores = o.Cores
		case control:
			p.Spared = false
			p.ControlCores = o.Cores
		}
	}
	p.OrphanDisposition, _ = p.Report.DispositionOf(orphan)
	p.ControlDisposition, _ = p.Report.DispositionOf(control)

	var blind []string
	if why := armBlindness("the constructed orphan", orphan, p.OrphanDisposition); why != "" {
		blind = append(blind, why)
	}
	if why := armBlindness("the live-owner control", control, p.ControlDisposition); why != "" {
		blind = append(blind, why)
	}
	p.Blind = strings.Join(blind, "; ")
}

// armBlindness names why one arm was not conducted, or returns "" when it was.
//
// The two verdict buckets and the blind-spot bucket all count as CONDUCTED: an
// orphan that came back `unattributable` or `live_owner` is the detector getting
// a constructed input wrong, and must fail rather than be excused. Only the
// absent disposition (never met the rate floor) and cwd_unreadable (the cwd
// reader refused) are properties of the host rather than of the rule.
func armBlindness(what string, pid int, d Disposition) string {
	switch d {
	case "":
		return fmt.Sprintf("%s pid=%d never met the CPU rate floor, so the scan never examined it", what, pid)
	case DispositionCwdUnreadable:
		return fmt.Sprintf("%s pid=%d was examined but its cwd could not be read, so it could not be attributed", what, pid)
	default:
		return ""
	}
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
