package orphanwatch

import (
	"strings"
	"testing"
)

// These are the unit tests for the line mg-db12 draws: which scan outcomes are a
// VERDICT on the detector and which are a fact about the host.
//
// They exist as their own tests, on a synthetic Report, because the live probe
// cannot reach these states on demand — the one that failed a merge gate on
// 2026-08-10 needed a load of 33 and an lsof that would not answer. A rule that
// can only be exercised by loading the machine is a rule nobody checks.

// probeArms builds a result whose scan put the two constructed pids in the given
// buckets, then reads the arms out of it exactly as Probe does.
func probeArms(t *testing.T, orphanBucket, controlBucket Disposition, reported bool) ProbeResult {
	t.Helper()
	const orphanPID, controlPID = 111, 222

	rep := Report{
		Floor:          probeFloor,
		CandidateFloor: DefaultCandidateFloor,
		Dispositions:   map[int]Disposition{},
		Rates:          map[int]float64{},
	}
	if orphanBucket != "" {
		rep.Dispositions[orphanPID] = orphanBucket
		rep.Rates[orphanPID] = 0.9
	}
	if controlBucket != "" {
		rep.Dispositions[controlPID] = controlBucket
		rep.Rates[controlPID] = 0.9
	}
	rep.Busy = len(rep.Dispositions)
	if reported {
		rep.Orphans = append(rep.Orphans, Orphan{PID: orphanPID, Owner: "zzdead", Cores: 0.9})
	}
	if controlBucket == DispositionOrphan {
		rep.Orphans = append(rep.Orphans, Orphan{PID: controlPID, Owner: "zzlive", Cores: 0.9})
	}

	res := ProbeResult{
		DeadOwner: "zzdead", LiveOwner: "zzlive",
		OrphanPID: orphanPID, ControlPID: controlPID,
		Attempts: 1, Report: rep,
	}
	res.readArms(orphanPID, controlPID)
	return res
}

// TestProbeFloorSitsBetweenTheTwoDefaults pins the invariant probeFloor was
// introduced with and did not state anywhere enforceable (mg-5aac).
//
// Both bounds are load-bearing and both fail SILENTLY at the point of use. A
// probeFloor at or below DefaultCandidateFloor makes Scan REFUSE the whole run
// with ErrCandidateFloorAboveFloor, so the probe stops being a verdict on the
// detector and starts being an instrument failure — while looking, from the
// constant's definition, entirely reasonable. Above DefaultFloor it would make
// the probe stricter than the detector it is checking, which is the original
// defect with a larger number.
//
// This is the guard against the remedy carrying its own defect: probeFloor is a
// new constant whose correctness is a RELATION to two others, and nothing about
// editing either of those brings a reader here.
func TestProbeFloorSitsBetweenTheTwoDefaults(t *testing.T) {
	if probeFloor <= DefaultCandidateFloor {
		t.Errorf("probeFloor = %.3f is at or below DefaultCandidateFloor %.3f; Scan REFUSES that "+
			"pairing (ErrCandidateFloorAboveFloor), so every probe run would report an instrument "+
			"failure rather than a verdict", probeFloor, DefaultCandidateFloor)
	}
	if probeFloor > DefaultFloor {
		t.Errorf("probeFloor = %.3f is above DefaultFloor %.3f; the probe would be asserting a "+
			"stricter threshold than the detector ships with", probeFloor, DefaultFloor)
	}
}

// TestProbeIsBlindWhenTheHostWouldNotLetItBeObserved is the defect this ticket
// was filed for, reproduced as a unit: the constructed orphan WAS seen, and was
// binned cwd_unreadable because lsof would not answer for it. The old test read
// `Reported == false` and failed the branch. It must now measure nothing.
func TestProbeIsBlindWhenTheHostWouldNotLetItBeObserved(t *testing.T) {
	for _, tc := range []struct {
		name         string
		orphanBucket Disposition
		wantIn       string
	}{
		{"cwd unreadable", DispositionCwdUnreadable, "cwd could not be read"},
		{"never met the floor", "", "never met the per-process candidate floor"},
		// mg-5aac. The attribution all worked and only the MAGNITUDE fell short,
		// which is the host declining to give a burner a share of a contended
		// box rather than the rule getting anything wrong. It used to read as a
		// verdict and failed 4 gate-equivalent runs out of 4.
		{"granted too little CPU to clear the floor", DispositionBelowOwnerFloor, "the host granted it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := probeArms(t, tc.orphanBucket, DispositionLiveOwner, false)
			if res.Blind == "" {
				t.Fatalf("Blind is empty; %s is a fact about the host and must not read as a detector failure", tc.name)
			}
			if !strings.Contains(res.Blind, tc.wantIn) {
				t.Errorf("Blind = %q, want it to mention %q", res.Blind, tc.wantIn)
			}
			if !strings.Contains(res.Blind, "constructed orphan") {
				t.Errorf("Blind = %q, want it to name WHICH arm went blind", res.Blind)
			}
			if res.Passed() {
				t.Error("Passed() is true on a blind run; a green light nothing exercised is the failure this family keeps rediscovering")
			}
			if !res.InstrumentFailure() {
				t.Error("InstrumentFailure() is false on a blind run")
			}
		})
	}
}

// TestProbeStillFailsWhenTheRULEGetsAConstructedInputWrong is the other half,
// and the one that keeps mg-db12 from being a softening. The two buckets that
// mean the attribution step ran and reached the WRONG answer must still fail —
// they are not host state, they are the detector being wrong about an input
// somebody built on purpose.
func TestProbeStillFailsWhenTheRULEGetsAConstructedInputWrong(t *testing.T) {
	for _, bucket := range []Disposition{DispositionUnattributable, DispositionLiveOwner} {
		t.Run(string(bucket), func(t *testing.T) {
			res := probeArms(t, bucket, DispositionLiveOwner, false)
			if res.Blind != "" {
				t.Fatalf("Blind = %q; %q is the rule reaching a verdict, not the host refusing to be observed", res.Blind, bucket)
			}
			if res.Reported {
				t.Fatal("Reported is true but the orphan was not in the report")
			}
			if res.Passed() {
				t.Errorf("Passed() is true with the constructed orphan binned %q", bucket)
			}
		})
	}
}

// TestProbePassesOnlyWhenBOTHArmsWereConducted pins the positive control against
// the way a retry-and-skip mechanism usually rots: the RED arm behaves, the
// control is never observed at all, and the run reports itself green on one arm.
func TestProbePassesOnlyWhenBOTHArmsWereConducted(t *testing.T) {
	good := probeArms(t, DispositionOrphan, DispositionLiveOwner, true)
	if good.Blind != "" {
		t.Fatalf("Blind = %q on a fully conducted run", good.Blind)
	}
	if !good.Reported || !good.Spared || !good.Passed() {
		t.Fatalf("both arms behaved but Passed() = %v (reported=%v spared=%v)", good.Passed(), good.Reported, good.Spared)
	}

	halfBlind := probeArms(t, DispositionOrphan, DispositionCwdUnreadable, true)
	if halfBlind.Blind == "" {
		t.Error("the control arm was never conducted and the run does not say so")
	}
	if !strings.Contains(halfBlind.Blind, "live-owner control") {
		t.Errorf("Blind = %q, want it to name the control arm", halfBlind.Blind)
	}
	if halfBlind.Passed() {
		t.Error("Passed() is true with the positive control unobserved — Spared was trivially true, " +
			"which is exactly the state a ppid-keyed sweep was in before it destroyed four live workers")
	}

	// And the control being WRONGLY reported is a failure, never a blindness.
	violated := probeArms(t, DispositionOrphan, DispositionOrphan, true)
	if violated.Blind != "" {
		t.Errorf("Blind = %q; a control that was reported is a verdict", violated.Blind)
	}
	if violated.Spared || violated.Passed() {
		t.Error("the control was named in the report and the probe did not fail")
	}
}

// TestReadArmsOverwritesAStaleAttempt covers the retry loop's one sharp edge:
// readArms runs once per scan against the same result value, so a reading left
// over from a blind attempt must not survive into a later one. If it did, a
// probe that went blind and then recovered could report the earlier attempt's
// arms — which is the same class of defect as the one being fixed, arriving
// through the fix itself.
func TestReadArmsOverwritesAStaleAttempt(t *testing.T) {
	const orphanPID, controlPID = 111, 222

	// Attempt 1: blind, and (impossibly, but this is the point) carrying state.
	res := ProbeResult{OrphanPID: orphanPID, ControlPID: controlPID}
	res.Report = Report{Dispositions: map[int]Disposition{
		orphanPID:  DispositionOrphan,
		controlPID: DispositionOrphan,
	}, Rates: map[int]float64{
		orphanPID:  0.9,
		controlPID: 0.8,
	}, Orphans: []Orphan{
		{PID: orphanPID, Cores: 0.9},
		{PID: controlPID, Cores: 0.8},
	}}
	res.readArms(orphanPID, controlPID)
	if !res.Reported || res.Spared || res.OrphanCores == 0 || res.ControlCores == 0 {
		t.Fatalf("fixture did not take: %+v", res)
	}
	if res.OrphanRate == 0 || res.ControlRate == 0 {
		t.Fatalf("fixture did not take (rates): %+v", res)
	}

	// Attempt 2: a scan that examined neither pid. Everything must reset.
	res.Report = Report{Dispositions: map[int]Disposition{}}
	res.readArms(orphanPID, controlPID)
	if res.Reported {
		t.Error("Reported survived a scan that did not report it")
	}
	if res.OrphanCores != 0 || res.ControlCores != 0 {
		t.Errorf("cores survived a scan that measured none: orphan=%.2f control=%.2f", res.OrphanCores, res.ControlCores)
	}
	// The rates are the reading mg-5aac ADDED, so they are exactly the field a
	// stale-attempt bug would arrive through next: a probe that went blind and
	// recovered would otherwise print the earlier attempt's grant beside this
	// attempt's verdict, which is a wrong number in the one sentence a reader
	// uses to decide whether the host or the rule was at fault.
	if res.OrphanRate != 0 || res.ControlRate != 0 {
		t.Errorf("granted rates survived a scan that measured none: orphan=%.3f control=%.3f",
			res.OrphanRate, res.ControlRate)
	}
	if res.OrphanDisposition != "" || res.ControlDisposition != "" {
		t.Errorf("dispositions survived: orphan=%q control=%q", res.OrphanDisposition, res.ControlDisposition)
	}
	if res.Blind == "" {
		t.Error("the second attempt examined neither pid and is not reported blind")
	}
}
