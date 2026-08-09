package refinery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The rules that decide whether a live gate-watch run PROVES the instrument is
// measuring a real subtree's CPU — and why not one of them is an absolute
// number of cores.
//
// # What went wrong with the absolute floor (mg-6c90)
//
// The live control used to assert that a gate holding one spinner peaks above
// 0.5 cores. That assertion is about the BOX, not about gatewatch. Cores are a
// shared resource: what a spinner is granted in a 400ms window is whatever the
// scheduler had left over, so the same binary measured
//
//	load 4.6 - 5.3     4 / 4   PASS
//	load 52 - 106     13 / 13  FAIL
//
// on byte-identical binaries built from main and from the branch under review.
// It went on to fail four further innocent branches in one evening, each
// costing a gate run and an exoneration — and, worse, it taught the fleet's
// coordinator to read `FAIL internal/refinery` as noise before checking, which
// is how a real refinery regression would merge unnoticed.
//
// The failure is one of KIND, not of magnitude, so no replacement threshold is
// correct: an ABSOLUTE floor over a SHARED resource is unmeetable by
// construction under contention. Widening it to 0.25 would only buy silence,
// and a control tuned until it stops firing has stopped measuring anything.
//
// # The rule this file adopts
//
// An assertion over a shared resource must be RELATIVE — a comparison between
// two arms measured on the same box minutes apart — never a minimum share of
// that resource inside a fixed window. Concretely, the arms are:
//
//	idle   a gate that sleeps                    -> must measure ~nothing
//	one    a gate holding 1 spinner              -> must measure above idle
//	many   a gate holding N spinners             -> must measure above one
//
// and the load-bearing assertions are the two INEQUALITIES between them, plus
// the requirement that `many` be a MULTIPLE of `one` rather than merely larger.
// That last one is what makes the control strong: it proves the number is a
// function of the injected work, so a constant-returning instrument, an
// instrument that measures the wrong subtree, and an instrument that lost the
// descendants all fail it.
//
// The arms are run CONCURRENTLY — see runGateArms for why that, and not the
// choice of ratio, is what makes the comparison hold on a moving box.
//
// # Measured across an 11x range of contention, which is the claim that matters
//
// Both Linux CFS and Darwin schedule per runnable thread, so a 1-spinner and a
// 4-spinner subtree hold a 1:4 share of whatever the box has left, however
// little that is. Measured directly on this 10-core host, by running the arms
// against a known number of competing spinners:
//
//	competitors   one     many(4)   ratio
//	     0        0.989    3.900    3.94x
//	    16        0.378    1.645    4.35x
//	    40        0.206    0.811    3.95x
//	    90        0.090    0.381    4.23x
//
// The one-spinner arm — the exact quantity the retired 0.5 floor keyed on —
// fell by a factor of ELEVEN and crossed that floor three times over. The
// verdict did not move: the ratio stayed inside 3.94x-4.35x throughout, never
// approaching the 1.5x the rule asks for. That is the whole argument for this
// change, measured rather than asserted.
//
// # Load AVERAGE does not predict the share, and that resolves the puzzle
//
// It is tempting to model the share as cores/(load+1). It does not hold, and
// the error matters: on this box at a load average of 18-23 a single spinner
// still measured ~1.0 cores, while 90 deliberately-runnable competitors pushed
// it to 0.09. Load average counts threads that are runnable OR blocked, so a
// box can carry a load of 150 in mostly-blocked work and still hand a spinner a
// whole core.
//
// This is what explains the observation that looked contradictory when this was
// filed — that the old test PASSED at load 154 in isolation while failing at
// load 52-106 during a gate. It was never the load average. It was how many
// threads were actually RUNNABLE inside the measurement window, which is a
// quantity no absolute threshold can be chosen against, because nothing in the
// test can see it.
//
// # The one floor that remains, and its measured margin
//
// `one` must still classify as SubtreeBusy at least once, which requires it to
// clear subtreeIdleCores (0.02). That is not a number invented here — it is the
// production classifier's own definition of "measurable", and an instrument
// that calls a real spinner idle is reporting a real defect whatever the load.
// Its margin is stated rather than assumed, in the manner mg-a465 settled on:
// at 90 competing spinners on 10 cores — nine times oversubscribed, well past
// anything this fleet has produced — the arm measured 0.090 cores, still 4.5x
// clear of the floor. Reaching 0.02 needs roughly 500 runnable threads, at
// which point the box is failing at everything. The `many` arm carries N times
// that margin, which is why the ceiling and the tracking rules hang off it.

const (
	// oneSpinnerCeiling bounds a single spinner's measurement from ABOVE.
	// This direction is safe to state absolutely: contention can only push a
	// measurement down, so an upper bound is never made unmeetable by a busy
	// host. It catches the opposite defect — a CPU column too coarse for the
	// window reports nothing for several windows and then a whole tick at
	// once, which reads as a multi-core burst. Precision loss in the
	// inflating direction is what put this signal in CI blind (mg-79e3).
	oneSpinnerCeiling = 2.0

	// trackingMargin is the multiple of the one-spinner arm's mean that the
	// many-spinner arm's mean must reach. With cpuArmSpinners = 4 the ideal is
	// 4.0x, and the measured range on this host is 3.94x-4.35x across an 11x
	// span of contention (see the table above).
	//
	// 1.5x is therefore not a threshold tuned until the test passed — it was
	// chosen from what the rule must CATCH. Every broken instrument this
	// control is for reports a ratio at or near 1.0x: a constant returns
	// exactly 1.0, an instrument measuring the whole host returns ~1.0, and one
	// that finds the gate but loses its descendants returns ~1.0. 1.5x sits
	// between that failure signature and the 3.9x-4.4x a working instrument
	// produces, with roughly equal room on each side. Widening it toward 1.0
	// would blind it to those defects; tightening it toward 4.0 would buy
	// nothing, since no observed defect lands in between.
	trackingMargin = 1.5

	// cpuArmSpinners is the parallelism of the injected-work arm. Four is
	// enough for the ideal ratio to sit well clear of trackingMargin while
	// staying inside the core count of any host that runs this suite; the arm
	// is capped at the host's core count below, because N spinners cannot
	// exceed N cores' worth of work no matter how the box is scheduled.
	cpuArmSpinners = 4
)

// subtreeCPUArms is what one live discrimination run measured. It is a plain
// value rather than a pile of assertions inside the test so that the rules can
// be exercised against a deliberately broken instrument — see
// TestSubtreeCPURulesSurviveContentionAndCatchABlindInstrument. A control that
// has only ever been run against a working instrument has not been shown to be
// able to fail, and "everyone assumes red means load" is exactly the state
// mg-6c90 was filed about.
type subtreeCPUArms struct {
	// IdlePeak is the highest rate the sleeping gate measured.
	IdlePeak float64
	// IdleReadings is how many settled readings the sleeping gate produced,
	// and IdleBusyReadings how many of those classified as anything other
	// than idle.
	IdleReadings     int
	IdleBusyReadings int

	// OnePeak and OneMean are the highest and the average rate the one-spinner
	// gate measured over OneReadings settled readings; OneSawBusy records
	// whether any of them classified as busy. Both statistics are kept because
	// the rules need different ones — see the tracking rule below for why a
	// peak is the wrong estimator for a ratio.
	OnePeak     float64
	OneMean     float64
	OneReadings int
	OneSawBusy  bool

	// ManySpinners is the injected-work arm's parallelism. ManySpinners of 0
	// or 1 means the arm was not run — a single-core host cannot inject
	// parallel work — and the tracking rule is then skipped rather than
	// weakened.
	ManySpinners int
	ManyPeak     float64
	ManyMean     float64
	ManyReadings int
	ManySawBusy  bool
}

// complaints returns one sentence per rule broken, empty when the instrument
// proved itself. Every rule is either a comparison BETWEEN arms or an upper
// bound; none of them asks the host to grant a minimum share of a shared
// resource inside a fixed window.
func (a subtreeCPUArms) complaints() []string {
	var out []string

	// A measurement that reports busy unconditionally cannot report the state
	// it exists to report. This is checked on EVERY settled reading, not one,
	// because "at least one idle reading" would be satisfied by noise.
	if a.IdleBusyReadings > 0 {
		out = append(out, fmt.Sprintf(
			"a sleeping gate classified as busy in %d of its %d settled readings (peak %.2f cores) — "+
				"the measurement is reporting busy unconditionally",
			a.IdleBusyReadings, a.IdleReadings, a.IdlePeak))
	}

	// The subtree walk has to FIND the descendant. `sh -c` forks for a
	// compound command, so an instrument that samples only the gate's own pid
	// sees a shell waiting on sleep and reports ~0.
	if a.OnePeak <= a.IdlePeak {
		out = append(out, fmt.Sprintf(
			"a computing gate peaked at %.2f cores and a sleeping one at %.2f — the subtree walk is not "+
				"finding the work, or the number is not a measurement of this subtree",
			a.OnePeak, a.IdlePeak))
	}
	if !a.OneSawBusy {
		out = append(out, fmt.Sprintf(
			"a gate holding a spinner classified as busy in none of its %d settled readings (peak %.2f cores, "+
				"idle floor %.2f) — the work did not clear the classifier's own threshold",
			a.OneReadings, a.OnePeak, subtreeIdleCores))
	}
	if a.OnePeak > oneSpinnerCeiling {
		out = append(out, fmt.Sprintf(
			"a gate holding ONE spinner peaked at %.2f cores, over the %.1f ceiling — the instrument is "+
				"over-reporting, most likely quantising work into bursts",
			a.OnePeak, oneSpinnerCeiling))
	}

	if a.ManySpinners > 1 {
		if !a.ManySawBusy {
			out = append(out, fmt.Sprintf(
				"a gate holding %d spinners classified as busy in none of its %d settled readings "+
					"(peak %.2f cores)", a.ManySpinners, a.ManyReadings, a.ManyPeak))
		}
		// The load-bearing rule: the number must TRACK the work. Everything
		// above can be satisfied by an instrument that merely distinguishes
		// "some process is running" from "nothing is"; only this one shows the
		// rate is a function of how much work was injected.
		//
		// It compares MEANS and not the peaks the other rules use, because a
		// peak is a biased estimator and the two arms are not biased equally.
		// The one-spinner arm's readings are noisy, so its highest of ~8 sits
		// above its centre; the four-spinner arm has already averaged four
		// spinners, so its highest sits close to its centre. Taking the peak of
		// both therefore compresses the ratio — measured over the runs in the
		// table above at 3.52x-4.25x, against the mean's tighter 3.94x-4.35x.
		// Both would have passed; the mean is used because it is the estimator
		// whose spread does not widen as the readings get noisier, and a margin
		// that erodes with contention is the defect this file exists to remove.
		if want := trackingMargin * a.OneMean; a.ManyMean < want {
			ratio := 0.0
			if a.OneMean > 0 {
				ratio = a.ManyMean / a.OneMean
			}
			out = append(out, fmt.Sprintf(
				"%d spinners averaged %.3f cores against one spinner's %.3f — %.2fx, where %d spinners "+
					"should measure about %dx and the rule requires %.1fx. The measurement is not tracking "+
					"the injected work",
				a.ManySpinners, a.ManyMean, a.OneMean, ratio, a.ManySpinners, a.ManySpinners, trackingMargin))
		}
		if ceiling := float64(a.ManySpinners) * oneSpinnerCeiling; a.ManyPeak > ceiling {
			out = append(out, fmt.Sprintf(
				"a gate holding %d spinners peaked at %.2f cores, over the %.1f ceiling — the instrument "+
					"is over-reporting", a.ManySpinners, a.ManyPeak, ceiling))
		}
	}
	return out
}

// spinGate builds a gate that holds n spinners for two seconds and then exits
// cleanly. The spinners are subshells, so the work lives in DESCENDANTS of the
// gate process and only a subtree walk finds it; the loops use shell builtins
// only, so the work shows up as CPU time rather than as process churn — these
// arms have to prove the CPU path, not the churn fallback.
//
// Both arms are built from this one template so that the ONLY difference
// between them is n. A ratio between two arms that differed in any other way
// would not be a measurement of injected work.
func spinGate(n int) string {
	starts := make([]string, 0, n)
	pids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		starts = append(starts, fmt.Sprintf("(while :; do :; done) & p%d=$!", i))
		pids = append(pids, fmt.Sprintf("$p%d", i))
	}
	return strings.Join(starts, "; ") + "; sleep 2; kill " + strings.Join(pids, " ") + "; exit 0"
}

// runGateArms executes several gates AT THE SAME TIME and returns, per gate,
// every progress record the refinery published while it was still running.
//
// # Concurrent and not sequential, because that is what makes the ratio hold
//
// The arms are compared to each other, so what matters is that they meet the
// SAME contention — and running them one after another does not guarantee
// that on a box whose load is moving. Measured here: run sequentially while
// this host's one-minute load ramped from 6 to 62, the one-spinner arm was
// sampled on a quiet box and the four-spinner arm on a saturated one, and the
// ratio collapsed to 2.06x against a rule needing 1.5x. On the same host
// minutes later, at a steady load of 18-23, six sequential runs measured
// 3.68x to 4.05x. Nothing about the instrument changed between those; only
// how much the box moved between one arm and the next.
//
// Started together, the arms are squeezed by the same competitors over the
// same windows, and the scheduler splits what is available per runnable
// thread — so the one-spinner and four-spinner subtrees hold a 1:4 share of
// whatever they get, however little that is. THAT is the sense in which this
// control is load-independent by construction, and sequential arms did not
// have it.
//
// The records are read LIVE and not from the sealed one: the sealed record of
// a finished gate honestly reports SubtreeGone, so whether an assertion on it
// saw a live subtree came down to whether the last heartbeat landed before or
// after the gate exited — on darwin it did, on Linux it did not (mg-79e3).
// One polling loop on the test's own goroutine samples every arm at the same
// instants, which also keeps t.Fatalf on the goroutine that may call it.
func runGateArms(t *testing.T, heartbeat time.Duration, gates ...string) [][]StepProgress {
	t.Helper()

	type arm struct {
		gate string
		r    *Refinery
		mr   *MergeRequest
		wt   string
		seen []StepProgress
	}
	// Built before any gate starts: t.TempDir and writeGateConfig belong to
	// the test goroutine, and setup work must not land inside the window the
	// arms are measured over.
	arms := make([]*arm, len(gates))
	for i, gate := range gates {
		r := newProgressTestRefinery(t, heartbeat)
		wtDir := t.TempDir()
		writeGateConfig(t, wtDir, "quality_gate = "+quoteTOML(gate))
		mr := &MergeRequest{ID: fmt.Sprintf("mr-cpu-%d", i), Status: StatusProcessing}
		r.byID[mr.ID] = mr
		arms[i] = &arm{gate: gate, r: r, mr: mr, wt: wtDir}
	}

	var wg sync.WaitGroup
	errs := make([]error, len(arms))
	for i, a := range arms {
		wg.Add(1)
		go func(i int, a *arm) {
			defer wg.Done()
			_, _, errs[i] = a.r.runQualityGates(context.Background(), a.wt, a.wt, a.mr)
		}(i, a)
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()

	poll := time.NewTicker(heartbeat / 4)
	defer poll.Stop()
	for {
		select {
		case <-allDone:
			out := make([][]StepProgress, len(arms))
			for i, a := range arms {
				if errs[i] != nil {
					t.Fatalf("gate %q should have passed: %v", a.gate, errs[i])
				}
				if len(a.seen) == 0 {
					t.Fatalf("gate %q published no record while it ran", a.gate)
				}
				out[i] = a.seen
			}
			return out
		case <-poll.C:
			for _, a := range arms {
				a.r.mu.Lock()
				if a.mr.Progress != nil && a.mr.Progress.EndTime.IsZero() {
					a.seen = append(a.seen, *a.mr.Progress)
				}
				a.r.mu.Unlock()
			}
		}
	}
}

// settledCPU keeps the readings taken over a window in which the subtree
// neither gained nor lost a process. Churn counts as work by design — a gate
// forking short-lived workers IS working — so the startup and teardown windows
// say "busy" for every arm and can discriminate nothing. Excluding them is
// what forces each arm to prove itself on the CPU path, which is the path that
// was blind.
func settledCPU(rs []StepProgress) []StepProgress {
	var out []StepProgress
	for _, p := range rs {
		if !p.CPUSampledAt.IsZero() && p.CPUProcs > 0 && p.CPUChurn == 0 {
			out = append(out, p)
		}
	}
	return out
}

// peakCores is the highest rate in a set of readings. The PEAK and not the
// mean, because an arm's job is to show what the instrument can resolve when
// the scheduler does grant it a slice; averaging in the windows where a loaded
// host gave it nothing measures the host.
func peakCores(rs []StepProgress) float64 {
	hi := 0.0
	for _, p := range rs {
		if p.CPUCores > hi {
			hi = p.CPUCores
		}
	}
	return hi
}

// meanCores is the average rate over a set of readings. Unlike peakCores it is
// an UNBIASED estimator of what the arm was granted, which is what the tracking
// ratio needs: peak-of-N overstates a noisy arm more than a smooth one, and the
// one-spinner arm is the noisy one.
func meanCores(rs []StepProgress) float64 {
	if len(rs) == 0 {
		return 0
	}
	sum := 0.0
	for _, p := range rs {
		sum += p.CPUCores
	}
	return sum / float64(len(rs))
}

// logArm prints one arm's readings so a red run can be diagnosed from the log
// alone, without a rerun on a machine whose load has since changed.
func logArm(t *testing.T, name string, rs []StepProgress) {
	t.Helper()
	for _, p := range rs {
		t.Logf("  %s: cores=%.2f procs=%d churn=%d window=%s unavailable=%q",
			name, p.CPUCores, p.CPUProcs, p.CPUChurn, p.CPUWindow, p.CPUUnavailable)
	}
}

// TestSubtreeCPURulesSurviveContentionAndCatchABlindInstrument is the control
// over the control. It feeds the rules the shapes a real run produces — from a
// quiet box, from a box at the loads that used to fail this suite, and from
// four ways the instrument can be broken — and asserts which of them are
// supposed to be red.
//
// This exists because of the specific way the old assertion failed: it went red
// so often for environmental reasons that a red was read as "the box is busy"
// before anyone checked. The defence against that is not a better threshold,
// it is a demonstration that red still means something — so the broken
// instruments below are the positive control, and the contended hosts are the
// negative one. The contended rows are the shares MEASURED on this host against
// a known number of competing spinners (see the table in this file's header),
// not shares inferred from a load average — which, as that header explains,
// does not predict them.
func TestSubtreeCPURulesSurviveContentionAndCatchABlindInstrument(t *testing.T) {
	// working describes an instrument that measures correctly, on a host with
	// `share` cores available to a single spinner. Everything scales with
	// share, which is the property under test: the verdict must not.
	working := func(share float64) subtreeCPUArms {
		return subtreeCPUArms{
			IdlePeak: 0, IdleReadings: 6, IdleBusyReadings: 0,
			OnePeak: share, OneMean: 0.8 * share, OneReadings: 6, OneSawBusy: share >= subtreeIdleCores,
			ManySpinners: 4, ManyPeak: 3.9 * share, ManyMean: 3.2 * share, ManyReadings: 6, ManySawBusy: true,
		}
	}

	cases := []struct {
		name    string
		arms    subtreeCPUArms
		wantRed bool
		// mentions is a fragment the complaint must carry, so a rule that
		// fires for the wrong reason is not mistaken for the right one.
		mentions string
	}{
		{
			name: "quiet host: measured 0.99 cores, the arm the old floor was tuned on",
			arms: working(0.98),
		}, {
			name: "16 competing spinners: measured 0.38 cores — BELOW the old 0.5 floor",
			arms: working(0.38),
		}, {
			name: "40 competing spinners: measured 0.21 cores",
			arms: working(0.21),
		}, {
			name: "90 competing spinners, 9x oversubscribed: measured 0.09 cores",
			arms: working(0.09),
		}, {
			name: "0.03 cores, past anything measured — still above the classifier's floor",
			arms: working(0.03),
		}, {
			name: "BROKEN: the walk samples only the gate's own pid, so it sees a waiting shell",
			arms: subtreeCPUArms{
				IdlePeak: 0, IdleReadings: 6,
				OnePeak: 0, OneMean: 0, OneReadings: 6, OneSawBusy: false,
				ManySpinners: 4, ManyPeak: 0, ManyMean: 0, ManyReadings: 6,
			},
			wantRed:  true,
			mentions: "not finding the work",
		}, {
			name: "BROKEN: a constant, reported whatever the subtree is doing",
			arms: subtreeCPUArms{
				IdlePeak: 1.0, IdleReadings: 6, IdleBusyReadings: 6,
				OnePeak: 1.0, OneMean: 1.0, OneReadings: 6, OneSawBusy: true,
				ManySpinners: 4, ManyPeak: 1.0, ManyMean: 1.0, ManyReadings: 6, ManySawBusy: true,
			},
			wantRed:  true,
			mentions: "not tracking the injected work",
		}, {
			name: "BROKEN: measures the whole host, so the idle gate reads busy too",
			arms: subtreeCPUArms{
				IdlePeak: 0.9, IdleReadings: 6, IdleBusyReadings: 6,
				OnePeak: 1.0, OneMean: 0.9, OneReadings: 6, OneSawBusy: true,
				ManySpinners: 4, ManyPeak: 1.1, ManyMean: 1.0, ManyReadings: 6, ManySawBusy: true,
			},
			wantRed:  true,
			mentions: "reporting busy unconditionally",
		}, {
			name: "BROKEN: a column too coarse for the window, quantising into bursts",
			arms: subtreeCPUArms{
				IdlePeak: 0, IdleReadings: 6,
				OnePeak: 6.0, OneMean: 2.0, OneReadings: 6, OneSawBusy: true,
				ManySpinners: 4, ManyPeak: 6.0, ManyMean: 2.0, ManyReadings: 6, ManySawBusy: true,
			},
			wantRed:  true,
			mentions: "over-reporting",
		}, {
			name: "BROKEN: finds the descendants but not all of them, so 4 spinners read as 1",
			arms: subtreeCPUArms{
				IdlePeak: 0, IdleReadings: 6,
				OnePeak: 0.98, OneMean: 0.80, OneReadings: 6, OneSawBusy: true,
				ManySpinners: 4, ManyPeak: 1.05, ManyMean: 0.85, ManyReadings: 6, ManySawBusy: true,
			},
			wantRed:  true,
			mentions: "not tracking the injected work",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.arms.complaints()
			if tc.wantRed && len(got) == 0 {
				t.Fatalf("a broken instrument produced no complaint — the control cannot fail: %+v", tc.arms)
			}
			if !tc.wantRed && len(got) > 0 {
				t.Fatalf("a working instrument was called broken: %s", strings.Join(got, "; "))
			}
			if tc.mentions != "" && !strings.Contains(strings.Join(got, "\n"), tc.mentions) {
				t.Errorf("the complaint fired for the wrong reason; want a mention of %q, got:\n%s",
					tc.mentions, strings.Join(got, "\n"))
			}
		})
	}

	// And the property the whole change rests on, asserted directly rather
	// than inferred from the rows above: the verdict must be invariant under
	// scaling. A 40x range of host contention, one answer.
	for _, share := range []float64{1.0, 0.5, 0.25, 0.1, 0.05, 0.025} {
		if c := working(share).complaints(); len(c) > 0 {
			t.Errorf("a correct instrument on a host granting %.3f cores was called broken — the rules "+
				"are still load-dependent: %s", share, strings.Join(c, "; "))
		}
	}
}

// retiredCoreFloorAccepts reports what the OLD assertion would have said about
// a run: a spinning gate had to peak at 0.5 cores or more, under the ceiling,
// classify busy, and out-measure a sleeping gate that never read busy.
//
// It is kept, unused by any production path, purely so the claim that replaced
// it can be checked rather than believed.
func retiredCoreFloorAccepts(a subtreeCPUArms) bool {
	const retiredFloor = 0.5
	return a.OnePeak >= retiredFloor &&
		a.OnePeak <= oneSpinnerCeiling &&
		a.OneSawBusy &&
		a.IdleBusyReadings == 0 &&
		a.OnePeak > a.IdlePeak
}

// TestTheReplacedFloorWasBothWEAKERANDFlakier is the argument for this change,
// made executable. A replacement control is only worth the churn if it is
// STRICTLY stronger, and "stronger" is a claim that can be checked: run the
// retired rule and the new rules over the same arms and see where they differ.
//
// Both directions matter, and they are different failures:
//
//   - The retired floor said RED to correct instruments, because it measured
//     the box. Those are the five innocent branches this ticket was filed over.
//   - The retired floor said GREEN to broken ones, because a floor only asks
//     for a number that is big enough, never for a number that is RIGHT. That
//     cost is invisible and therefore worse: nobody files a ticket about a gate
//     that passed.
func TestTheReplacedFloorWasBothWEAKERAndFlakier(t *testing.T) {
	// Arms measured on this 10-core host against a known number of competing
	// spinners — a correct instrument every time. See this file's header.
	contended := []struct {
		competitors int
		arms        subtreeCPUArms
	}{
		{0, subtreeCPUArms{IdleReadings: 8, OnePeak: 1.00, OneMean: 0.989, OneReadings: 8, OneSawBusy: true,
			ManySpinners: 4, ManyPeak: 3.90, ManyMean: 3.900, ManyReadings: 8, ManySawBusy: true}},
		{16, subtreeCPUArms{IdleReadings: 8, OnePeak: 0.40, OneMean: 0.378, OneReadings: 8, OneSawBusy: true,
			ManySpinners: 4, ManyPeak: 1.70, ManyMean: 1.645, ManyReadings: 8, ManySawBusy: true}},
		{40, subtreeCPUArms{IdleReadings: 8, OnePeak: 0.25, OneMean: 0.206, OneReadings: 8, OneSawBusy: true,
			ManySpinners: 4, ManyPeak: 0.88, ManyMean: 0.811, ManyReadings: 8, ManySawBusy: true}},
		{90, subtreeCPUArms{IdleReadings: 8, OnePeak: 0.10, OneMean: 0.090, OneReadings: 8, OneSawBusy: true,
			ManySpinners: 4, ManyPeak: 0.40, ManyMean: 0.381, ManyReadings: 8, ManySawBusy: true}},
	}
	firedOnAWorkingInstrument := 0
	for _, c := range contended {
		if len(c.arms.complaints()) > 0 {
			t.Errorf("%d competing spinners: the new rules called a correct instrument broken (one=%.3f "+
				"many=%.3f) — they are still load-dependent: %s",
				c.competitors, c.arms.OneMean, c.arms.ManyMean, strings.Join(c.arms.complaints(), "; "))
		}
		if !retiredCoreFloorAccepts(c.arms) {
			firedOnAWorkingInstrument++
			t.Logf("%d competing spinners: one spinner measured %.2f cores — the retired 0.5 floor would "+
				"have failed this run, on a correct instrument", c.competitors, c.arms.OnePeak)
		}
	}
	// Three of the four contended rows sit under 0.5 cores. If a future change
	// to the arms makes them all comfortable again, this test stops
	// demonstrating anything and should be re-measured rather than deleted.
	if firedOnAWorkingInstrument == 0 {
		t.Error("no measured row falls under the retired floor, so this test no longer shows the flake " +
			"it was written to show — re-measure the arms under contention")
	}

	// The other direction: defects a floor cannot see. Both of these clear
	// 0.5 cores comfortably and would have sailed through the retired rule.
	missed := []struct {
		name string
		arms subtreeCPUArms
	}{{
		name: "a constant, reported whatever the subtree is doing",
		arms: subtreeCPUArms{IdleReadings: 8, OnePeak: 1.0, OneMean: 1.0, OneReadings: 8, OneSawBusy: true,
			ManySpinners: 4, ManyPeak: 1.0, ManyMean: 1.0, ManyReadings: 8, ManySawBusy: true},
	}, {
		name: "finds the gate's own children but loses the grandchildren, so 4 spinners read as 1",
		arms: subtreeCPUArms{IdleReadings: 8, OnePeak: 0.98, OneMean: 0.80, OneReadings: 8, OneSawBusy: true,
			ManySpinners: 4, ManyPeak: 1.05, ManyMean: 0.85, ManyReadings: 8, ManySawBusy: true},
	}}
	for _, m := range missed {
		t.Run(m.name, func(t *testing.T) {
			if !retiredCoreFloorAccepts(m.arms) {
				t.Fatalf("this row is meant to show a defect the retired floor MISSED, but the floor "+
					"catches it — the row no longer makes its point: %+v", m.arms)
			}
			got := m.arms.complaints()
			if len(got) == 0 {
				t.Fatalf("the retired floor passed this instrument and so do the new rules — the "+
					"replacement is not stronger here: %+v", m.arms)
			}
			t.Logf("retired floor: PASS. new rules: %s", strings.Join(got, "; "))
		})
	}
}

// TestGateWatchRulesGoRedWhenTheWalkMissesTheSubtree is the LIVE positive
// control: a real gate, a real spinner, a real process table, and only the
// subtree walk crippled.
//
// psSnapshot is rewritten so every process looks like an orphaned group leader.
// sampleSubtree then finds the gate shell and nothing else — which is precisely
// the instrument pogo had before mg-0c51, and precisely the defect
// TestGateWatchMeasuresARealSubtreesCPU exists to catch. The rules must go red,
// and they must go red on THIS host at whatever load it happens to be under,
// because a control that only fails on a quiet box is not a control.
func TestGateWatchRulesGoRedWhenTheWalkMissesTheSubtree(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real gates for a couple of seconds")
	}
	const heartbeat = 400 * time.Millisecond
	if !procSource.CanResolve(heartbeat) {
		t.Skipf("subtree CPU is not measurable here: %s cannot resolve a %s window", procSource, heartbeat)
	}

	// Blind the walk: with every row its own group leader and parented to
	// init, the group sweep matches only the root and the ppid walk finds no
	// children.
	prev := psSnapshot
	psSnapshot = func() ([]procRow, error) {
		rows, err := prev()
		if err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].PPID = 1
			rows[i].PGID = rows[i].PID
		}
		return rows, nil
	}
	t.Cleanup(func() { psSnapshot = prev })

	blinded := runGateArms(t, heartbeat, spinGate(1), spinGate(cpuArmSpinners))
	one, many := settledCPU(blinded[0]), settledCPU(blinded[1])
	logArm(t, "blind-one", one)
	logArm(t, "blind-many", many)
	if len(one) == 0 || len(many) == 0 {
		t.Fatalf("the blinded arms produced no settled readings (one=%d many=%d) — the control cannot "+
			"speak to the rules", len(one), len(many))
	}

	arms := subtreeCPUArms{
		IdleReadings: len(one),
		OnePeak:      peakCores(one), OneMean: meanCores(one), OneReadings: len(one),
		ManySpinners: cpuArmSpinners, ManyPeak: peakCores(many), ManyMean: meanCores(many),
		ManyReadings: len(many),
	}
	for _, p := range one {
		if p.Subtree() == SubtreeBusy {
			arms.OneSawBusy = true
		}
	}
	for _, p := range many {
		if p.Subtree() == SubtreeBusy {
			arms.ManySawBusy = true
		}
	}

	got := arms.complaints()
	t.Logf("blinded instrument: one=%.2f many=%.2f cores; %d complaints", arms.OnePeak, arms.ManyPeak, len(got))
	for _, c := range got {
		t.Logf("  complaint: %s", c)
	}
	if len(got) == 0 {
		t.Fatalf("a gate watch that samples ONLY the gate's own pid drew no complaint (one=%.2f "+
			"many=%.2f cores over %d/%d settled readings) — TestGateWatchMeasuresARealSubtreesCPU "+
			"would pass against an instrument that measures nothing",
			arms.OnePeak, arms.ManyPeak, len(one), len(many))
	}
	if !strings.Contains(strings.Join(got, "\n"), "not finding the work") {
		t.Errorf("the blinded walk must be named as such; got:\n%s", strings.Join(got, "\n"))
	}
}
