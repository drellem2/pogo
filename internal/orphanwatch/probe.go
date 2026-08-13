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
// plus one cwd read and starts no new processes. It is worth having because
// every way this probe goes blind is a property of the HOST rather than of the
// detector — an lsof that refused a pid this second, a burner the scheduler
// starved through one 2s window — so a retry re-measures the same constructed
// input against a host that may have moved on. On 2026-08-10 the first of those
// failed a merge gate on a branch that had never touched this package.
//
// Retrying is NOT what makes the probe conductible under contention, and reading
// it that way is how mg-5aac was missed for a while: sustained contention is not
// transient, and three attempts against a host that is loaded for the whole gate
// return the same answer three times. What makes the arm conductible is running
// it at a floor the constructed input can actually reach — see probeFloor. The
// retry is for the second-scale flicker on top of that.
const probeScanAttempts = 3

// probeFloor is the REPORTING floor this probe runs the scan under, standing in
// for DefaultFloor the same way LiveOwners stands in for the registry — and for
// the same reason: the probe has to be able to construct the input the arm needs.
//
// DefaultFloor is 0.20 cores, and that is a policy constant about how much
// compute a dead owner must be holding before a human is told. It is NOT a
// property the constructed input can guarantee, because what a shell burner
// achieves is the host's to decide: N runnable processes on C cores get about
// C/N each. Measured on this 10-core box, one burner got 0.24-0.29 cores
// isolated at load 53, and 0.105-0.160 cores inside `go test ./...` — every
// in-gate sample below 0.20, and the isolated ones clearing it by only 1.2-1.45x.
// That is a dose-response curve with the verdict sitting on the steep part of it
// (mg-5aac).
//
// The failure that produced was not a skip. The orphan was attributed correctly
// — right cwd, right owner, right liveness answer — and only the magnitude
// comparison fell short, so it came back `below_owner_floor`, which is a VERDICT
// bucket. The test read `Reported == false` and reported the detector broken, on
// a branch that had never touched this package. Measured: 4 runs out of 4 FAILED
// with 30 additional competing processes on a box already at load 53, against 6
// out of 6 passing on the same box without them, minutes earlier.
//
// 0.05 is chosen to be clear of both edges. It is 2.5x DefaultCandidateFloor,
// so a process that reaches it is measured rather than rounded, and it is well
// above the ~0.00-core blocked-process class the floor exists to exclude; and
// every in-gate sample above cleared it by 2.1-3.2x. By the C/N arithmetic that
// margin holds to a load of about 200 on this box, which is past anything this
// fleet has produced — though that last figure is arithmetic, not a measurement.
//
// What it costs: this probe no longer witnesses the VALUE 0.20. It never was the
// instrument for that — a live probe cannot pin a constant whose satisfaction it
// does not control — and the floor arithmetic is pinned deterministically
// against a stubbed table by TestSubdividingComputeCannotGetUNDERTheOwnerFloor,
// TestTheOwnerFloorSumsWITHINAnOwnerNotAcrossThem and
// TestASwarmOfSMALLProcessesIsSTILLOneDeadOwnersCompute. What this probe
// uniquely witnesses is the PATH: real process table, real cwd reader, real
// attribution, real liveness answer, RED at the end of it. Every term of that is
// unchanged.
//
// The alternative considered and rejected was to construct MORE burners under
// the dead owner until their sum cleared 0.20. It works arithmetically — N
// burners sum to ~CN/(L+N), so two clear 0.20 at load 84 — but it answers host
// load by ADDING host load, inside a merge gate, on the box whose contention is
// the problem. This fleet has already had one branch failed by a polecat's
// synthetic load (mg-c675); a probe that does the same thing on purpose is not a
// fix.
const probeFloor = 0.05

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
	// OrphanRate and ControlRate are what the host actually GRANTED each burner
	// over the sampling window, read out of the scan whatever bucket the process
	// landed in. OrphanCores above is carried only on a finding, so it is 0 for
	// every run that did not report — including the runs where the interesting
	// question is precisely how much the process was getting (mg-5aac).
	OrphanRate  float64 `json:"orphan_rate_cores"`
	ControlRate float64 `json:"control_rate_cores"`
	// Reported is true when the scan named OrphanPID. This is the failing arm.
	Reported bool `json:"reported"`
	// Spared is true when the scan did NOT name ControlPID. This is the
	// positive control.
	Spared bool `json:"spared"`
	// OrphanDisposition and ControlDisposition are the buckets the scan put each
	// constructed process in. Empty means the process never met the per-process
	// CANDIDATE floor, so it was never examined at all — which is one of the ways
	// an arm can measure nothing, and is distinct from being examined and binned
	// below_owner_floor.
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
	// The three ways in, all contention-driven and all observed:
	//
	//	the pid never met the CANDIDATE    a shell burner sharing 10 cores with
	//	floor                              ~68 packages' worth of test processes
	//	                                   can miss even 0.02 cores for a whole
	//	                                   2s window (mg-db12).
	//	cwd_unreadable                     lsof refused or timed out on the pid.
	//	                                   Measured live: the constructed orphan
	//	                                   was binned here, the test read only
	//	                                   `Reported == false`, and the gate
	//	                                   classed it as a DEFECT of an unrelated
	//	                                   branch (mg-db12).
	//	below_owner_floor                  the pid was attributed correctly and
	//	                                   only the MAGNITUDE fell short of
	//	                                   probeFloor. Added under mg-5aac, where
	//	                                   this bucket — a verdict bucket, and so
	//	                                   read as the detector being wrong —
	//	                                   failed 4 gate-equivalent runs out of 4.
	//	                                   See probeFloor for why it is a fact
	//	                                   about the host.
	//
	// The discriminator for all three is CONTENTION IN THE SAMPLING WINDOW, not
	// the host's 1-minute load average, and the difference is not academic: this
	// probe passed 6 of 6 isolated at load 47 and 0 of 2 inside `go test ./...`
	// on the same box (p60eb, 2026-08-13). A remedy keyed on load average would
	// have skipped a quiet-but-contended gate and fired on a busy idle box.
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
				"constructed orphan pid=%d disposition=%q granted %.3f cores, "+
				"live-owner control pid=%d disposition=%q granted %.3f cores\n"+
				"(an empty disposition means the process never met the %.2f-core candidate floor; "+
				"the reporting floor this probe ran at is %.2f)",
			p.Attempts, p.Blind,
			p.OrphanPID, p.OrphanDisposition, p.OrphanRate,
			p.ControlPID, p.ControlDisposition, p.ControlRate,
			p.Report.CandidateFloor, p.Report.Floor)
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
		"constructed orphan pid=%d (ppid=%d, owner %s dead, granted %.3f cores against a %.2f floor): detector %s it\n"+
			"live-owner control pid=%d (ppid=%d, owner %s alive, granted %.3f cores): detector %s",
		p.OrphanPID, p.DetachedPPID, p.DeadOwner, p.OrphanRate, p.Report.Floor, red,
		p.ControlPID, p.ControlPPID, p.LiveOwner, p.ControlRate, green)
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
			Floor:        probeFloor,
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
	p.OrphanRate, _ = p.Report.RateOf(orphan)
	p.ControlRate, _ = p.Report.RateOf(control)
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
	if why := armBlindness("the constructed orphan", orphan, p.OrphanDisposition, p.OrphanRate); why != "" {
		blind = append(blind, why)
	}
	if why := armBlindness("the live-owner control", control, p.ControlDisposition, p.ControlRate); why != "" {
		blind = append(blind, why)
	}
	p.Blind = strings.Join(blind, "; ")
}

// armBlindness names why one arm was not conducted, or returns "" when it was.
//
// `unattributable` and `live_owner` count as CONDUCTED: an orphan that comes
// back in either is the detector getting a constructed input wrong, and must
// fail rather than be excused. The remaining three are properties of the HOST,
// and each is reported with the rate the host granted, because "did not clear a
// floor" is a comparison and the number is what a reader can act on.
func armBlindness(what string, pid int, d Disposition, rate float64) string {
	switch d {
	case "":
		return fmt.Sprintf("%s pid=%d never met the per-process candidate floor of %.2f cores, "+
			"so the scan never examined it", what, pid, DefaultCandidateFloor)
	case DispositionCwdUnreadable:
		return fmt.Sprintf("%s pid=%d was examined but its cwd could not be read, so it could not be attributed", what, pid)
	case DispositionBelowOwnerFloor:
		// The attribution ALL worked — right cwd, right owner, right liveness
		// answer — and the host simply would not grant the process enough CPU to
		// clear the floor that arm is being run at. Nothing about the rule is on
		// trial in that comparison; see probeFloor.
		return fmt.Sprintf("%s pid=%d was attributed to its owner correctly, but the host granted it "+
			"only %.3f cores against this probe's %.2f-core floor, so the owner never became reportable",
			what, pid, rate, probeFloor)
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
