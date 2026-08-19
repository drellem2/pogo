package refinery

import (
	"context"
	"fmt"
	"strings"
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
// Ratios are load-independent by construction. Both Linux CFS and Darwin
// schedule per runnable thread, so N spinners receive about N times a single
// spinner's share whatever else the box is doing. The measurement above bears
// this out exactly: at load 42 on 10 cores one spinner measured 0.25 cores,
// against the 10/43 = 0.23 that fair share predicts.
//
// # The one floor that remains, and its measured margin
//
// `one` must still classify as SubtreeBusy at least once, which requires it to
// clear subtreeIdleCores (0.02). That is not a number invented here — it is the
// production classifier's own definition of "measurable", and an instrument
// that calls a real spinner idle is reporting a real defect whatever the load.
// Its margin is stated rather than assumed, in the manner mg-a465 settled on:
// on this 10-core host a single spinner receives about 10/(load+1) cores, so
// the arm sits at 0.19 cores at load 52 and 0.055 at load 180 — the worst this
// box has recorded in a night — against a 0.02 floor. It goes under only past
// load ~500, at which point the box is failing at everything. The `many` arm
// carries N times that margin, which is why the ceiling and the tracking rules
// hang off it.

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
	// 4.0x, so 1.5x leaves a 2.6x allowance for the two arms meeting different
	// contention — they run seconds apart rather than simultaneously.
	//
	// It is a margin on a RATIO, which is the whole point: scaling both arms
	// down for a loaded host leaves the ratio, and therefore the verdict,
	// untouched. Measured on this 10-core host over eight runs spanning load
	// 17 to load 248, the mean ratio ran 2.90x to 3.86x against an ideal of
	// 4x — while the one-spinner arm the old floor keyed on fell from 0.33 to
	// 0.06 cores over the same span, a factor of five. That is the property
	// that distinguishes this rule from the absolute floor it replaced: the
	// quantity being compared moved by 5x and the verdict did not move at all.
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
		// well above its centre; the four-spinner arm has already averaged
		// four spinners, so its highest sits close to its centre. Taking the
		// peak of both therefore COMPRESSES the ratio, and it compresses it
		// more the busier the box is: the peak ratio measured 3.3-4.0x at load
		// 42-49 but only 1.8-2.5x at load 174-210, where the mean ratio held
		// at 2.9-3.9x. A margin that erodes with load is the very defect this
		// file exists to remove, so the estimator was fixed rather than the
		// threshold lowered to accommodate it.
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

// runGateArm executes a gate and returns every progress record the refinery
// published while it was still running. The records are read LIVE and not from
// the sealed one: the sealed record of a finished gate honestly reports
// SubtreeGone, so whether an assertion on it saw a live subtree came down to
// whether the last heartbeat landed before or after the gate exited — on
// darwin it did, on Linux it did not (mg-79e3).
func runGateArm(t *testing.T, heartbeat time.Duration, gate string) []StepProgress {
	t.Helper()
	r := newProgressTestRefinery(t, heartbeat)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "quality_gate = "+quoteTOML(gate))
	mr := &MergeRequest{ID: "mr-cpu", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	done := make(chan error, 1)
	go func() {
		_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
		done <- err
	}()

	var seen []StepProgress
	poll := time.NewTicker(heartbeat / 4)
	defer poll.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("gate %q should have passed: %v", gate, err)
			}
			if len(seen) == 0 {
				t.Fatalf("gate %q published no record while it ran", gate)
			}
			return seen
		case <-poll.C:
			r.mu.Lock()
			if mr.Progress != nil && mr.Progress.EndTime.IsZero() {
				seen = append(seen, *mr.Progress)
			}
			r.mu.Unlock()
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
// instruments below are the positive control, and the loaded hosts are the
// negative one. The loaded rows are the actual measurements from the ticket,
// scaled by fair share: at load 42 on this 10-core host a single spinner
// measured 0.25 cores against the 0.23 that 10/43 predicts.
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
			name: "quiet host, load ~5: the arm the old floor was tuned on",
			arms: working(0.98),
		}, {
			name: "load 42: measured on this host, 0.25 cores — HALF the old 0.5 floor",
			arms: working(0.25),
		}, {
			name: "load 106: the top of the band that failed 13/13",
			arms: working(0.09),
		}, {
			name: "load 180: the worst this box has recorded in a night",
			arms: working(0.055),
		}, {
			name: "load 154 in isolation: passed the old floor, must still pass",
			arms: working(1.0),
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

	one := settledCPU(runGateArm(t, heartbeat, spinGate(1)))
	logArm(t, "blind-one", one)
	many := settledCPU(runGateArm(t, heartbeat, spinGate(cpuArmSpinners)))
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
