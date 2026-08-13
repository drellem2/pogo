package refinery

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
)

// The defect this file guards (mg-0502).
//
// A gate killed by `signal: terminated` was classified DEFECT, with the
// no-retry reason "the build gate ran on this tree and returned a verdict —
// re-running establishes the same fact". A terminated gate returned nothing,
// and that sentence asserts the opposite of what happened.
//
// Measured instance: mr-d9uqe8atjv1j0e4isn1g, branch polecat-pc26d, 2026-08-13.
// The gate died at 18.84s wall against a 20m per-package budget, burning 0.38
// cores, with zero saturated contention samples — so it was neither a timeout
// nor a starved host. The refinery's own deadline path kills with SIGKILL on
// the process group (gaterun.go), so the SIGTERM cannot have been its doing.
//
// The load-bearing tests here are the POSITIVE CONTROLS at the bottom: a gate
// that really is killed by a real SIGTERM, and a gate that really does exit
// 128+N of its own accord. Without both, "we detect signal kills" and "we do
// not detect anything at all" are indistinguishable, and so are "we detect
// signal kills" and "we now excuse every high exit status".

// TestASignalKilledGateIsNotReportedAsAVerdictOnTheBranch is the ticket's
// headline requirement, stated against the classifier.
func TestASignalKilledGateIsNotReportedAsAVerdictOnTheBranch(t *testing.T) {
	err := &gateSignalError{Gate: "./build.sh", Signal: syscall.SIGTERM, Elapsed: 18840 * time.Millisecond, Timeout: time.Hour}

	for _, stage := range []string{"test", "build"} {
		disp := classifyFailure(stage, "", err)

		if disp.Class == ClassDefect {
			t.Errorf("stage %q: a gate killed by SIGTERM was classified %s — that is the class whose "+
				"triage note says a fix is warranted, and a signalled gate never reached an exit status",
				stage, disp.Class)
		}
		if disp.Class != ClassIndeterminate {
			t.Errorf("stage %q: class = %s, want %s", stage, disp.Class, ClassIndeterminate)
		}
		// The exact sentence the ticket is about. Matched on the affirmative
		// wording only — "never returned a verdict" is correct and must not trip.
		if strings.Contains(disp.Reason, "ran on this tree and returned a verdict") {
			t.Errorf("stage %q: the no-retry reason asserts a verdict that was never returned: %q", stage, disp.Reason)
		}
		if disp.Signal == "" {
			t.Errorf("stage %q: the evidence that decided the class must be named", stage)
		}
		if !strings.Contains(disp.Signal, "SIGTERM") {
			t.Errorf("stage %q: the signal that decided this must name WHICH signal, got %q", stage, disp.Signal)
		}
	}
}

// TestASignalKilledGateIsNotRetriedAutomaticallyButAsksForOneReRun.
//
// The two halves are separate claims and both matter. Not retrying
// automatically is right — a gate that signals its own process group is
// re-killed every attempt. But the ticket's point is that re-running IS
// informative here, unlike a red gate, and the reason has to say so or the
// bare non-retry reproduces the original misreading.
func TestASignalKilledGateIsNotRetriedAutomaticallyButAsksForOneReRun(t *testing.T) {
	disp := classifyFailure("build", "", &gateSignalError{Gate: "./build.sh", Signal: syscall.SIGTERM})

	if disp.Retryable {
		t.Error("a signal kill must not be retried automatically: a gate signalling its own " +
			"process group would be re-killed on every attempt")
	}
	if disp.Reason == "" {
		t.Fatal("a non-retryable failure must state why")
	}
	lower := strings.ToLower(disp.Reason)
	if !strings.Contains(lower, "re-run") {
		t.Errorf("the reason must ask for the one deliberate re-run, since unlike a red gate this is "+
			"not reproduced by re-running, got: %q", disp.Reason)
	}
	if !strings.Contains(lower, "not") {
		t.Errorf("the reason must deny the inference a bare non-retry invites, got: %q", disp.Reason)
	}
}

// TestASignalKilledGateDoesNotCountAgainstTheAuthor. The streak feeds an
// escalation advising that the polecat be stopped or the work reassigned — a
// judgement about a person, off the back of a signal nobody has explained.
func TestASignalKilledGateDoesNotCountAgainstTheAuthor(t *testing.T) {
	if countsAgainstAuthor(classifyFailure("build", "", &gateSignalError{Signal: syscall.SIGTERM}).Class) {
		t.Error("a gate stopped by a signal must not accumulate into a verdict on whoever submitted it")
	}
}

// TestTheIndeterminateTriageNoteDoesNotAssertATimeout.
//
// The note is what a coordinator reads first, and it used to say the gate "was
// KILLED at its timeout". For a signal kill that is false in exactly the way
// "returned a verdict" was false — a confident sentence about an event that did
// not happen. Fixing one and leaving the other would move the defect, not
// remove it.
func TestTheIndeterminateTriageNoteDoesNotAssertATimeout(t *testing.T) {
	note := ClassIndeterminate.TriageNote()
	if strings.Contains(note, "KILLED at its timeout") {
		t.Errorf("the note asserts a timeout as THE cause, but a gate killed by an outside signal "+
			"lands in this class too: %q", note)
	}
	if !strings.Contains(strings.ToLower(note), "signal") {
		t.Errorf("the note must admit the signal kill it now covers, got: %q", note)
	}
	// mg-e565's requirements on this note, re-asserted so widening it does not
	// quietly drop them.
	if !strings.Contains(note, "INDETERMINATE") {
		t.Errorf("the note must lead with the class, got: %q", note)
	}
	if strings.Contains(note, "establishes nothing about the branch") {
		t.Errorf("indeterminate is NOT infrastructure's claim: a gate can signal its own process "+
			"group, so the note must not clear the branch. got: %q", note)
	}
}

// TestTheSignalMessageDeniesTheVerdictInItsFirstLine. The part of a failure
// that travels is its headline — onto the MR, into the failure mail subject
// area, into what the author skims. A caveat in paragraph three does not reach
// them.
func TestTheSignalMessageDeniesTheVerdictInItsFirstLine(t *testing.T) {
	err := &gateSignalError{
		Gate: "./build.sh", Signal: syscall.SIGTERM,
		Elapsed: 18840 * time.Millisecond, Timeout: time.Hour,
		OutputLines: 12, SilentFor: 3 * time.Second, EverSpoke: true,
	}
	msg := err.Error()

	head := strings.SplitN(msg, "\n", 2)[0]
	if !strings.Contains(head, "NOT A VERDICT") {
		t.Errorf("the first line must deny that this is a verdict on the branch, got: %q", head)
	}
	if !strings.Contains(head, "SIGTERM") {
		t.Errorf("the first line must name the signal — `signal: terminated` is the string nobody "+
			"recognised as a kill. got: %q", head)
	}
	if !strings.Contains(msg, roundDur(err.Elapsed).String()) {
		t.Errorf("the elapsed time must be reported: it is what makes 'nowhere near the deadline' "+
			"readable rather than asserted. got:\n%s", msg)
	}
}

// TestTheSignalReportNamesWhatItRulesOut.
//
// The ticket says this half is probably the more valuable one: an author told
// "your gate was killed" still cannot act. Each line below is a fact about the
// run or about code in this repo, so each is checkable — which is the property
// that separates this from a list of guesses.
func TestTheSignalReportNamesWhatItRulesOut(t *testing.T) {
	// The measured incident: SIGTERM, 18.84s, against a 60m bound.
	term := (&gateSignalError{
		Gate: "./build.sh", Signal: syscall.SIGTERM,
		Elapsed: 18840 * time.Millisecond, Timeout: time.Hour,
	}).Error()

	if !strings.Contains(term, "RULED OUT") {
		t.Fatalf("the report must say what the evidence eliminates, got:\n%s", term)
	}
	// The refinery's own kill paths, eliminated by the mechanism rather than
	// asserted: both send SIGKILL and both are reported as other errors.
	if !strings.Contains(term, "SIGKILL on the process group") {
		t.Errorf("the report must name HOW the refinery kills, so 'not the refinery' is checkable "+
			"rather than a claim, got:\n%s", term)
	}
	// A kill at 18.84s against a 60m bound is not a deadline of ours.
	if !strings.Contains(term, "nowhere near") {
		t.Errorf("a kill three orders of magnitude short of the bound must say so, got:\n%s", term)
	}
	// The kernel OOM killer sends SIGKILL, so a SIGTERM eliminates it. The
	// wording differs by platform on purpose — see the SIGKILL half below and
	// the caveat this pair exists to keep honest.
	if !strings.Contains(term, "RULED OUT  an out-of-memory kill") &&
		!strings.Contains(term, "RULED OUT  the KERNEL out-of-memory killer") {
		t.Errorf("SIGTERM eliminates the kernel OOM killer, which sends SIGKILL — the report must say so, got:\n%s", term)
	}
	// ...and off darwin it must NOT overclaim: earlyoom and friends send SIGTERM
	// by default, so a bare "ruled out" there would be this ticket's own defect
	// committed inside its remedy.
	if runtime.GOOS != "darwin" && !strings.Contains(term, "earlyoom") {
		t.Errorf("off darwin a userspace OOM daemon can send SIGTERM; the report must not rule "+
			"out every OOM kill, got:\n%s", term)
	}
	// And the case that keeps this out of INFRASTRUCTURE must be named as OPEN,
	// not quietly omitted.
	if !strings.Contains(term, "kill 0") || !strings.Contains(term, "pkill") {
		t.Errorf("the self-signal case is why this class is not INFRASTRUCTURE; the report must "+
			"name it so the author checks their own scripts, got:\n%s", term)
	}

	// A SIGKILL must NOT claim the OOM killer is eliminated — that would be this
	// ticket's own defect, committed in the report that fixes it.
	kill := (&gateSignalError{
		Gate: "./build.sh", Signal: syscall.SIGKILL,
		Elapsed: 18840 * time.Millisecond, Timeout: time.Hour,
	}).Error()
	if strings.Contains(kill, "RULED OUT  an out-of-memory kill") ||
		strings.Contains(kill, "RULED OUT  the KERNEL out-of-memory killer") {
		t.Errorf("a SIGKILL cannot rule out the OOM killer, which sends exactly that signal:\n%s", kill)
	}
	if !strings.Contains(kill, "OPEN       an out-of-memory kill") {
		t.Errorf("a SIGKILL must leave the OOM reading open and say where to look, got:\n%s", kill)
	}
}

// TestAnUnsampledHostGetsNeitherAnExcuseNorAnAccusation. The same rule
// gateTimeoutError applies to its contention reading: a run whose host was
// never sampled must not acquire a sentence about the host. Zero samples is not
// a measurement of an idle box.
func TestAnUnsampledHostGetsNeitherAnExcuseNorAnAccusation(t *testing.T) {
	unsampled := (&gateSignalError{Gate: "./build.sh", Signal: syscall.SIGTERM}).Error()
	if strings.Contains(unsampled, "saturated") {
		t.Errorf("with no host samples the report must say nothing about saturation, got:\n%s", unsampled)
	}
	sampled := (&gateSignalError{
		Gate: "./build.sh", Signal: syscall.SIGTERM,
		Contention: hostload.Summary{Samples: 10},
	}).Error()
	if !strings.Contains(sampled, "saturated") {
		t.Errorf("a sampled host must be reported, got:\n%s", sampled)
	}
}

// TestSignalThatKilledReadsTheWaitStatusNotTheEnglish.
//
// Matching the STRING `signal: terminated` against gate output is the
// speculative text-matching failureclass.go refuses: a test that printed those
// words would have its author's defect taken away from them. The wait status is
// the kernel's answer about the process the refinery itself started, and a gate
// cannot forge it.
func TestSignalThatKilledReadsTheWaitStatusNotTheEnglish(t *testing.T) {
	if _, ok := signalThatKilled(errors.New("FAIL: TestFoo printed \"signal: terminated\"")); ok {
		t.Error("a plain error whose TEXT says `signal: terminated` must not be read as a kill — " +
			"gate output is arbitrary and a test may print anything")
	}
	if _, ok := signalThatKilled(nil); ok {
		t.Error("a nil error is not a kill")
	}
}

// TestARealSignalKilledGateReachesTheClassifierAsANonVerdict is the POSITIVE
// CONTROL, and it reproduces the incident's exact signature: `sh -c` killed by
// a real SIGTERM, rendered by os/exec as `signal: terminated`.
//
// It uses the self-signal shape on purpose (`kill -TERM $$`), because that is
// the one case the ticket leaves open as a possible real defect — so the
// control is also a demonstration that the class is INDETERMINATE rather than a
// clearance.
func TestARealSignalKilledGateReachesTheClassifierAsANonVerdict(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	// Speaks first, then is signalled — so the report has real output to count
	// and cannot be passing by rendering a default.
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo working-then-killed; kill -TERM $$\"]\ntimeout = \"60m\"\n")

	mr := &MergeRequest{ID: "mr-signalled", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("the signalled gate must fail")
	}

	// The bare os/exec error is still reachable, and it is the incident's string.
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("the underlying *exec.ExitError must stay reachable, got %T", err)
	}
	if ee.Error() != "signal: terminated" {
		t.Fatalf("POSITIVE CONTROL DID NOT REPRODUCE THE SIGNATURE: os/exec said %q, want "+
			"%q — this test is not exercising the reported condition", ee.Error(), "signal: terminated")
	}

	var se *gateSignalError
	if !errors.As(err, &se) {
		t.Fatalf("a real SIGTERM kill did not produce a gateSignalError, got %T: %v", err, err)
	}
	if se.Signal != syscall.SIGTERM {
		t.Errorf("signal recorded as %v, want SIGTERM", se.Signal)
	}
	if !se.EverSpoke || se.OutputLines == 0 {
		t.Errorf("the gate printed a line before dying; the report counted %d lines (everSpoke=%v) — "+
			"the observation is defaulted, not measured", se.OutputLines, se.EverSpoke)
	}

	// The headline the author actually receives, unprefixed by a package list.
	// `./build.sh failed [cmd/pogo, +46 more]` in front of the denial points the
	// reader straight back at the packages.
	head := strings.SplitN(err.Error(), "\n", 2)[0]
	if !strings.HasPrefix(head, "gate ") {
		t.Errorf("the kill must not be prefixed with a gate-failure summary naming packages; got: %q", head)
	}
	if !strings.Contains(head, "NOT A VERDICT") {
		t.Errorf("the real failure message does not deny the verdict reading:\n%s", err.Error())
	}

	// End to end, at both stages the incident could have been recorded under.
	for _, stage := range []string{gateStage(ran), "test"} {
		disp := classifyFailure(stage, "", err)
		if disp.Class != ClassIndeterminate {
			t.Errorf("stage %q: end to end, a really-signalled gate classified as %s, want %s",
				stage, disp.Class, ClassIndeterminate)
		}
		// Matched on the affirmative wording only, as mg-e565's test is: "never
		// returned a verdict" is the correct sentence and must not trip this.
		if strings.Contains(disp.Reason, "ran on this tree and returned a verdict") {
			t.Errorf("stage %q: the reason is the one the ticket is about: %q", stage, disp.Reason)
		}
	}
}

// TestAGateThatChoseItsOwnHighExitStatusIsStillADefect is the NEGATIVE control,
// and it is what stops the fix above from becoming an amnesty.
//
// `exit 143` is the status a shell reports for a child killed by SIGTERM, and a
// gate that exits 143 of its own accord has RUN and returned it. It is not
// signalled — Signaled() is false — so it must stay a defect. Without this, a
// fix that keyed off the exit NUMBER instead of the wait status would look
// correct and would excuse a whole band of real failures.
func TestAGateThatChoseItsOwnHighExitStatusIsStillADefect(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo ran-and-judged; exit 143\"]\ntimeout = \"60m\"\n")

	mr := &MergeRequest{ID: "mr-exit143", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("the failing gate must fail")
	}
	var se *gateSignalError
	if errors.As(err, &se) {
		t.Fatalf("exit 143 is a status the gate CHOSE, not a signal it died of; classifying it as a "+
			"kill takes a real defect away from its author: %v", err)
	}
	if disp := classifyFailure(gateStage(ran), "", err); disp.Class != ClassDefect {
		t.Errorf("a gate that ran and exited 143 classified as %s, want %s", disp.Class, ClassDefect)
	}
}

// TestTheRefinerysOwnKillIsStillReportedAsATimeout.
//
// The refinery kills an overrunning gate with SIGKILL, so every timeout is ALSO
// a signal death at the wait-status level. If the new branch ran first, mg-e565's
// timeout report — the per-layer signal block, the observed-silent-but-healthy
// caveat, the "raise [gates] timeout" advice — would silently disappear and be
// replaced by a report that says the deadline is ruled out. That is the failure
// mode of this change, so it is the test.
func TestTheRefinerysOwnKillIsStillReportedAsATimeout(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo wedging-now; sleep 30\"]\ntimeout = \"400ms\"\n")

	mr := &MergeRequest{ID: "mr-timeout-not-signal", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("the hung gate must fail")
	}
	var se *gateSignalError
	if errors.As(err, &se) {
		t.Fatalf("the refinery's OWN deadline kill was reported as an outside signal (%v) — the "+
			"timeout report and its advice are gone", se.Signal)
	}
	var te *gateTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("expected a gateTimeoutError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "raise [gates] timeout") {
		t.Errorf("mg-e565's operator advice must survive:\n%s", err.Error())
	}
}

// TestOperatorCancellationIsStillReportedAsCancellation. The other kill path
// through the same context, guarded for the same reason.
func TestOperatorCancellationIsStillReportedAsCancellation(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"sleep 30\"]\ntimeout = \"60m\"\n")

	mr := &MergeRequest{ID: "mr-cancelled", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel(errCancelRequested)
	}()
	_, _, err := r.runQualityGates(ctx, wtDir, wtDir, mr)
	cancel(errCancelRequested)
	if err == nil {
		t.Fatal("the cancelled gate must fail")
	}
	var se *gateSignalError
	if errors.As(err, &se) {
		t.Fatalf("an operator cancellation was reported as an outside signal (%v)", se.Signal)
	}
	if !isCancelled(err) {
		t.Errorf("cancellation must stay recognisable as cancellation, got %T: %v", err, err)
	}
}
