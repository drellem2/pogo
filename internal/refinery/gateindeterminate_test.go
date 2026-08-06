package refinery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
)

// The defect this file guards (mg-e565).
//
// A gate that was KILLED at its timeout was classified exactly like a gate that
// RAN and returned RED: stage "test" went straight into verdictStages and came
// out ClassDefect, "the test gate ran on this tree and returned a verdict —
// re-running establishes the same fact". None of that is true of a kill.
//
// Measured cost of the misfiling: polecat z37ad's merge failed at exactly
// 1h0m0s three times on a `go env` that was downloading a Go toolchain over a
// link losing its DHCP lease (root cause mg-cdf1). The message it received was
// indistinguishable from a test failure, so it spent hours believing its own
// change was at fault before isolating the suite and proving otherwise.

// TestAKilledGateIsNotReportedAsAVerdictOnTheBranch is the ticket's headline
// requirement. A timeout kill establishes nothing about the branch, so it must
// not arrive carrying the one class that means "a fix is warranted".
func TestAKilledGateIsNotReportedAsAVerdictOnTheBranch(t *testing.T) {
	err := &gateTimeoutError{Gate: "./build.sh", Timeout: time.Hour, Elapsed: time.Hour}

	for _, stage := range []string{"test", "build"} {
		disp := classifyFailure(stage, "", err)

		if disp.Class == ClassDefect {
			t.Errorf("stage %q: a gate KILLED at its timeout was classified %s — "+
				"that is the class whose triage note says a fix is warranted, and the kill "+
				"establishes no fact about the branch at all", stage, disp.Class)
		}
		if disp.Class != ClassIndeterminate {
			t.Errorf("stage %q: class = %s, want %s", stage, disp.Class, ClassIndeterminate)
		}
		// The reason must not assert the event that did not happen. Matched on
		// the affirmative wording only — "never returned a verdict" is the
		// correct sentence and must not trip this.
		if strings.Contains(disp.Reason, "ran on this tree and returned a verdict") {
			t.Errorf("stage %q: reason asserts a run that did not happen: %q", stage, disp.Reason)
		}
		if disp.Signal == "" {
			t.Errorf("stage %q: the evidence that decided the class must be named", stage)
		}
	}
}

// TestAGateThatActuallyRanIsStillADefect is the other half, and the one that
// stops the fix above from being an over-correction. A gate that ran to
// completion and returned non-zero HAS an opinion about the tree, and it is
// exactly as true on a retry.
func TestAGateThatActuallyRanIsStillADefect(t *testing.T) {
	disp := classifyFailure("test", "--- FAIL: TestFoo\nFAIL\n", errors.New("./build.sh failed: exit status 1"))

	if disp.Class != ClassDefect {
		t.Fatalf("a gate that RAN and returned RED must stay %s, got %s", ClassDefect, disp.Class)
	}
	if disp.Retryable {
		t.Error("a real gate verdict must not become retryable")
	}
}

// TestAnIndeterminateGateIsNotRetriedAndSaysWhyNot.
//
// Not retrying is the right call — z37ad's three attempts cost three hours of a
// single-slot refinery and answered nothing — but mg-e5c2's third requirement
// is that the ABSENCE of a retry be legible. An unexplained non-retry reads as
// the refinery being satisfied that the branch is at fault, which is the very
// reading this ticket exists to remove.
func TestAnIndeterminateGateIsNotRetriedAndSaysWhyNot(t *testing.T) {
	disp := classifyFailure("test", "", &gateTimeoutError{Gate: "./build.sh", Timeout: time.Hour})

	if disp.Retryable {
		t.Error("an hour-long timeout must not be retried automatically: it costs another " +
			"hour of the merge slot and cannot answer the question")
	}
	if disp.Reason == "" {
		t.Fatal("a non-retryable failure must state why re-running gives the same answer")
	}
	if !strings.Contains(disp.Reason, "not") {
		t.Errorf("the reason must deny the inference a bare non-retry invites, got: %q", disp.Reason)
	}
}

// TestIndeterminateTriageNoteRefusesBothInferences. The note is what a
// coordinator reads first. It must block "dispatch a fix" AND must not be
// mistaken for infrastructure's "resubmit, nothing to see here" — a gate that
// hung might have hung on the branch.
func TestIndeterminateTriageNoteRefusesBothInferences(t *testing.T) {
	note := ClassIndeterminate.TriageNote()
	if note == "" {
		t.Fatal("every class must carry a triage note")
	}
	if !strings.Contains(note, "INDETERMINATE") {
		t.Errorf("the note must lead with the class, got: %q", note)
	}
	lower := strings.ToLower(note)
	if !strings.Contains(lower, "do not dispatch") && !strings.Contains(lower, "do not") {
		t.Errorf("the note must refuse the dispatch-a-fix inference, got: %q", note)
	}
	if strings.Contains(note, "establishes nothing about the branch") {
		t.Errorf("indeterminate is NOT infrastructure's claim: a gate that hung may have hung "+
			"ON the branch, and the note must not clear it. got: %q", note)
	}
	if note == ClassDefect.TriageNote() || note == ClassInfrastructure.TriageNote() {
		t.Error("the note must be distinguishable from the classes it sits between")
	}
}

// TestAnIndeterminateFailureDoesNotCountAgainstTheAuthor. The author streak
// feeds an escalation whose advice is "consider stopping the polecat or
// reassigning the work item". z37ad would have taken three of those for a
// toolchain download.
func TestAnIndeterminateFailureDoesNotCountAgainstTheAuthor(t *testing.T) {
	if countsAgainstAuthor(ClassIndeterminate) {
		t.Error("a killed gate must not accumulate into a verdict on whoever submitted it")
	}
	if !countsAgainstAuthor(ClassDefect) {
		t.Error("regression: a real defect must still count")
	}
	if !countsAgainstAuthor("") {
		t.Error("regression: an unclassified-by-absence failure must still count")
	}
}

// TestTheKillIsVisibleInTheStatusLabel. StatusLabel is what `pogo refinery
// queue` and the mail subject show. A bare "failed" is what let three kills
// read as three rejections.
func TestTheKillIsVisibleInTheStatusLabel(t *testing.T) {
	mr := &MergeRequest{Status: StatusFailed, FailureClass: ClassIndeterminate}
	label := mr.StatusLabel()
	if !strings.Contains(label, string(ClassIndeterminate)) {
		t.Errorf("StatusLabel = %q, must surface the class rather than a bare failed", label)
	}
}

// TestTheTimeoutMessagePresentsTheFactsAndLetsThemDisagree.
//
// The ticket is explicit that no single signal settles this, and that the
// verdict should present the several facts rather than collapse them into one
// confident sentence. This is the honest-caveat requirement, and it is the part
// a "clean up the wording" pass would quietly undo.
func TestTheTimeoutMessagePresentsTheFactsAndLetsThemDisagree(t *testing.T) {
	// The observed shape of the incident: the runner beating happily while the
	// gate's own stdout had been frozen for 23 minutes and its subtree idle.
	err := &gateTimeoutError{
		Gate:        "./build.sh",
		Timeout:     time.Hour,
		Elapsed:     time.Hour,
		OutputLines: 412,
		SilentFor:   23 * time.Minute,
		EverSpoke:   true,
		Signals: []Signal{
			{Layer: LayerRunner, Name: "heartbeat", State: "alive", Detail: "798ms old, beat 60, every 1m0s"},
			{Layer: LayerGate, Name: "stdout", State: "SILENT", Detail: "412 lines, but nothing for 23m0s of its 1h0m0s"},
			{Layer: LayerGate, Name: "process subtree", State: "IDLE", Detail: "0.00 cores across 3 procs over 30s"},
			{Layer: LayerHost, Name: "load", State: "has capacity", Detail: "load 2.1 over 8 cores"},
		},
	}
	msg := err.Error()

	// 1. The headline must deny the verdict reading outright. A caveat in
	//    paragraph three does not travel; the first line is what gets skimmed
	//    and forwarded.
	head := strings.SplitN(msg, "\n", 2)[0]
	if !strings.Contains(head, "NOT A VERDICT") {
		t.Errorf("the first line must deny that this is a verdict on the branch, got: %q", head)
	}

	// 2. Every signal is present WITH its layer. A reading quoted without its
	//    layer is how a supervisor's heartbeat got reported as the gate's
	//    progress in the first place (mg-48d8).
	for _, s := range err.Signals {
		if !strings.Contains(msg, s.Detail) {
			t.Errorf("signal %q/%q detail missing from the message: %q", s.Layer, s.Name, s.Detail)
		}
		if !strings.Contains(msg, s.Layer) {
			t.Errorf("layer %q missing — a reading without its layer is the defect mg-48d8 fixed", s.Layer)
		}
	}

	// 3. The disagreement must be stated as such, not resolved for the reader.
	if !strings.Contains(msg, "disagree") {
		t.Errorf("the message must say the signals can disagree, got: %s", msg)
	}

	// 4. The two honest caveats, neither of which may be designed away: a
	//    silent gate may be working (22 minutes was observed), and an idle
	//    subtree is also what blocked-on-I/O looks like.
	for _, want := range []string{"22", "silent"} {
		if !strings.Contains(strings.ToLower(msg), want) {
			t.Errorf("the message must carry the observed-silent-but-healthy caveat (%q), got: %s", want, msg)
		}
	}

	// 5. The runner's heartbeat must be disclaimed where it appears, since it
	//    is the freshest number in the report and the least relevant.
	if !strings.Contains(msg, "supervisor") {
		t.Error("the RUNNER heartbeat must be disclaimed as the supervisor's, not the gate's")
	}
}

// TestATimeoutWithNoSignalsStillReads. Signals may be unavailable — the sampler
// can fail, and a gate can die before one is taken. An unmeasurable subtree
// must never render as an idle one, and the message must not sprout an empty
// evidence block.
func TestATimeoutWithNoSignalsStillReads(t *testing.T) {
	err := &gateTimeoutError{Gate: "./build.sh", Timeout: time.Hour, Elapsed: time.Hour}
	msg := err.Error()

	if strings.Contains(msg, "NOT A VERDICT") == false {
		t.Error("the verdict denial does not depend on having signals")
	}
	if strings.Contains(msg, "disagree") {
		t.Error("with no signals there is nothing to disagree; the block must be omitted, not empty")
	}
	if !strings.Contains(msg, "raise [gates] timeout") {
		t.Error("the operator advice must survive")
	}
}

// TestSignalsArePopulatedFromARealHungGate is the POSITIVE CONTROL, and it is
// the check whose absence broke the discriminator this ticket originally
// proposed. That one reported "gate not found" from a `ps | grep` that could
// never have found a gate at all — nothing established it could locate one that
// existed, so its negative reading was worthless.
//
// So this runs a gate that really does hang, kills it on a real timeout, and
// requires that the signals attached to the failure were MEASURED rather than
// defaulted: the gate's own stdout must be represented, and the row must carry
// the line the gate actually printed.
func TestSignalsArePopulatedFromARealHungGate(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	// The observed shape: speaks once, then wedges — z37ad's gate printed
	// "PASS: driver resolves base_url..." and then went quiet for 23 minutes.
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo wedging-now; sleep 30\"]\ntimeout = \"400ms\"\n")

	mr := &MergeRequest{ID: "mr-signals", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("the hung gate must fail")
	}
	var te *gateTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("expected a gateTimeoutError, got %T", err)
	}

	if len(te.Signals) == 0 {
		t.Fatal("POSITIVE CONTROL FAILED: a real gate ran, hung and was killed, and NO signals were " +
			"attached. Until this passes, an empty signal block cannot be read as 'nothing was " +
			"happening' — it is indistinguishable from the measurement never being taken")
	}

	byLayer := map[string][]Signal{}
	for _, s := range te.Signals {
		byLayer[s.Layer] = append(byLayer[s.Layer], s)
		if s.State == "" {
			t.Errorf("signal %s/%s has no state", s.Layer, s.Name)
		}
	}
	// The GATE's own stdout is the row that must exist: it is the one measuring
	// the thing being judged.
	gateRows := byLayer[LayerGate]
	if len(gateRows) == 0 {
		t.Fatalf("no GATE-layer signal attached; got layers %v", byLayer)
	}
	var sawStdout bool
	for _, s := range gateRows {
		if s.Name == "stdout" {
			sawStdout = true
			// It printed exactly one line and then stopped, so the row must
			// show a line count rather than claim silence from the start.
			if !strings.Contains(s.Detail, "1 line") {
				t.Errorf("the stdout row did not measure the line the gate really printed: %q", s.Detail)
			}
		}
	}
	if !sawStdout {
		t.Error("the GATE stdout row is missing — that is the instrument pointed at the thing being judged")
	}

	// And the whole point: this reaches the classifier as a non-verdict.
	if disp := classifyFailure("test", "", err); disp.Class != ClassIndeterminate {
		t.Errorf("end to end, a really-hung gate classified as %s, want %s", disp.Class, ClassIndeterminate)
	}
	// The rendered message a polecat would receive carries the denial.
	if !strings.Contains(err.Error(), "NOT A VERDICT") {
		t.Errorf("the real failure message does not deny the verdict reading:\n%s", err.Error())
	}
}

// TestReadingSignalsDoesNotMintAFreshHeartbeat.
//
// snapshot() stamps Heartbeat=now and bumps Beats. Building the failure report
// with it would mint a zero-age heartbeat out of the act of writing the report,
// and then print it as an observation — the ticket's "heartbeat 798ms old while
// gate output was 9 minutes stale", manufactured rather than merely misread.
func TestReadingSignalsDoesNotMintAFreshHeartbeat(t *testing.T) {
	r := newProgressTestRefinery(t, time.Hour)
	mr := &MergeRequest{ID: "mr-readonly", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	w := startGateWatch(r, mr, "quality-gates", "./build.sh", 1, 1, time.Time{})
	defer w.finish()

	r.mu.Lock()
	before := *mr.Progress
	r.mu.Unlock()

	_ = w.signals(time.Now())

	r.mu.Lock()
	after := *mr.Progress
	r.mu.Unlock()

	if after.Beats != before.Beats {
		t.Errorf("reading the signals bumped the beat count %d -> %d", before.Beats, after.Beats)
	}
	if !after.Heartbeat.Equal(before.Heartbeat) {
		t.Errorf("reading the signals re-stamped the heartbeat %v -> %v", before.Heartbeat, after.Heartbeat)
	}
}

// TestTheContentionClauseSurvives guards mg-1b8c's half of this message, which
// sits in the same string and is easy to drop while rewriting around it.
func TestTheContentionClauseSurvives(t *testing.T) {
	err := &gateTimeoutError{
		Gate: "./build.sh", Timeout: time.Hour, Elapsed: time.Hour,
		Contention: hostload.Summary{Samples: 10},
	}
	if !strings.Contains(err.Error(), "host") && !strings.Contains(err.Error(), "HOST") {
		t.Error("the host-contention clause must still be rendered")
	}
}
