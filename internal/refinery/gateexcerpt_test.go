package refinery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestRunningGateTextIsReadableWhileItStillRuns is the positive control the
// ticket asks for, and it is the whole test file's reason to exist.
//
// The defect was not that gate text was unavailable — it is on the MergeRequest
// the moment the merge resolves. It was that the text is unreachable for
// exactly as long as anyone wants it: while the gate is running and the
// question "why is this slow" is live. A test that reads the output after
// completion would have PASSED against the old behaviour and proved nothing.
//
// So the assertion is deliberately about the instant, not the content: the
// gate's own words must be readable from the persisted record while EndTime is
// still zero.
func TestRunningGateTextIsReadableWhileItStillRuns(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	wtDir := t.TempDir()
	// A gate that states what it resolved on its first line and then works —
	// the shape of the real gate whose opening line refuted a wrong hypothesis
	// about it while being unreadable for 77 minutes.
	writeGateConfig(t, wtDir, `quality_gate = "echo 'watchlist consistent: 17 paths'; sleep 1"`)

	mr := &MergeRequest{ID: "mr-live", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	done := make(chan error, 1)
	go func() {
		_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
		done <- err
	}()

	var seen *GateExcerpt
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && seen == nil {
		r.mu.Lock()
		if p := mr.Progress; p != nil && p.EndTime.IsZero() && p.OutputExcerpt.Spoke() {
			// The snapshot the record holds is never mutated after it is
			// installed, so holding on to it past the lock is safe.
			seen = p.OutputExcerpt
		}
		r.mu.Unlock()
		if seen == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("gate should have passed: %v", err)
	}
	if seen == nil {
		t.Fatal("a running gate's text was never readable from its progress record — " +
			"this is the defect: the output exists, and nothing exposes it until the merge resolves")
	}
	if got := seen.First(); got != "watchlist consistent: 17 paths" {
		t.Errorf("the running gate's first line must be readable verbatim, got %q", got)
	}
	if !seen.Complete() {
		t.Errorf("a two-line gate must report a complete excerpt, got %d elided", seen.Elided)
	}
}

// TestExcerptKeepsTheOpeningLinesAfterTheTailRollsPast is the constraint that
// gets designed out. "Show me the last N lines" is what everyone asks for and
// it would have left the motivating incident fully intact: the refuting line
// was the gate's FIRST, and by minute 77 no tail reaches it.
func TestExcerptKeepsTheOpeningLinesAfterTheTailRollsPast(t *testing.T) {
	e := newExcerptBuffer()
	e.write([]byte("watchlist consistent: 17 paths; import closure 10 modules\n"))
	for i := 1; i <= 500; i++ {
		e.write([]byte(fmt.Sprintf("ok  package-%d\n", i)))
	}
	x := e.snapshot()

	if got := x.First(); got != "watchlist consistent: 17 paths; import closure 10 modules" {
		t.Errorf("the opening line must survive 500 lines of later output, got %q", got)
	}
	if x.Lines != 501 {
		t.Errorf("Lines must count what the GATE produced, not what was kept: got %d, want 501", x.Lines)
	}
	if x.Latest() != "ok  package-500" {
		t.Errorf("the most recent line must also be present, got %q", x.Latest())
	}
	if want := 501 - len(x.Head) - len(x.Tail); x.Elided != want {
		t.Errorf("Elided must account for every line not in the record: got %d, want %d", x.Elided, want)
	}
	if x.Complete() {
		t.Error("a bounded excerpt must not report itself complete")
	}
}

// TestExcerptStatesItsBoundWheneverItBit is the caution this arc has already
// been bitten by: a bounded read that does not say it is bounded manufactures
// an absence, and a confident wrong diagnosis follows.
func TestExcerptStatesItsBoundWheneverItBit(t *testing.T) {
	e := newExcerptBuffer()
	for i := 1; i <= 300; i++ {
		e.write([]byte(fmt.Sprintf("line-%d\n", i)))
	}
	x := e.snapshot()

	head := x.Header()
	for _, want := range []string{"300 lines", "NOT shown", "head 25", "tail 40"} {
		if !strings.Contains(head, want) {
			t.Errorf("the header must state the bound; %q missing from: %s", want, head)
		}
	}
	report := strings.Join(x.Report(), "\n")
	if !strings.Contains(report, "not shown here") {
		t.Errorf("the gap between head and tail must be visible IN the text, got:\n%s", report)
	}
	// The gate's own numbering, so the jump across the gap cannot be read as a
	// contiguous transcript.
	if !strings.Contains(report, " 25 | line-25") || !strings.Contains(report, "261 | line-261") {
		t.Errorf("kept lines must carry their original line numbers, got:\n%s", report)
	}
}

// TestCompleteExcerptSaysSoTooIsTheOtherHalf: stating the bound only when it
// bit is not enough. A reader has to be able to tell "this is everything the
// gate said" from "this is a window onto what it said", and the difference is
// what makes the excerpt usable as evidence at all.
func TestCompleteExcerptSaysSoTooIsTheOtherHalf(t *testing.T) {
	e := newExcerptBuffer()
	for i := 1; i <= 30; i++ {
		e.write([]byte(fmt.Sprintf("line-%d\n", i)))
	}
	x := e.snapshot()
	if !x.Complete() || x.Elided != 0 {
		t.Fatalf("30 lines fit inside head+tail; got %d elided", x.Elided)
	}
	if !strings.Contains(x.Header(), "all shown") {
		t.Errorf("a complete excerpt must say it is complete, got: %s", x.Header())
	}
	// And it must be a transcript, not a doubled one: the head window covers
	// lines 1-25 and the tail window would otherwise repeat some of them.
	report := strings.Join(x.Report(), "\n")
	if n := strings.Count(report, "| line-25"); n != 1 {
		t.Errorf("line 25 must appear exactly once, appeared %d times in:\n%s", n, report)
	}
	if !strings.Contains(report, " 30 | line-30") {
		t.Errorf("the last line must be present, got:\n%s", report)
	}
}

// TestExcerptExposesTheLineBeingWritten covers the mid-write case, which is
// also the case a polecat lost three gate timeouts to: its gate halted at
// "Building pogod into the sandbox..." and that text was not reachable from any
// tooling. An unterminated line is not a line, and dropping it for that reason
// would discard the newest thing the gate said.
func TestExcerptExposesTheLineBeingWritten(t *testing.T) {
	e := newExcerptBuffer()
	e.write([]byte("=== Running: ./build.sh\n"))
	e.write([]byte("Building pogod into the sandbox..."))
	x := e.snapshot()

	if x.Lines != 1 {
		t.Errorf("an unterminated line must not be counted as a complete one, got %d lines", x.Lines)
	}
	if !x.Spoke() {
		t.Error("a gate mid-line has spoken")
	}
	if x.Pending != "Building pogod into the sandbox..." {
		t.Errorf("the line in progress must be readable, got %q", x.Pending)
	}
	if x.Latest() != "Building pogod into the sandbox..." {
		t.Errorf("the line in progress is the newest thing said, got %q", x.Latest())
	}
	report := strings.Join(x.Report(), "\n")
	if !strings.Contains(report, "still being written") {
		t.Errorf("an unterminated line must be labelled as one, got:\n%s", report)
	}

	// Finishing the line promotes it, and it stops being pending.
	e.write([]byte(" done\n"))
	x = e.snapshot()
	if x.Pending != "" {
		t.Errorf("a completed line must no longer be pending, got %q", x.Pending)
	}
	if x.Latest() != "Building pogod into the sandbox... done" {
		t.Errorf("the completed line must be the latest, got %q", x.Latest())
	}
}

// TestExcerptBoundsASingleLineAndSaysWhere: bounding the line COUNT does not
// bound memory. One `base64 < bigfile` in a gate is a single line of arbitrary
// size, and a record that silently kept its first 500 bytes would be reporting
// a truncated line as the gate's words.
func TestExcerptBoundsASingleLineAndSaysWhere(t *testing.T) {
	e := newExcerptBuffer()
	e.write([]byte("prefix: " + strings.Repeat("x", 4000) + "\n"))
	x := e.snapshot()

	if x.TruncatedLines != 1 {
		t.Errorf("expected 1 truncated line, got %d", x.TruncatedLines)
	}
	line := x.First()
	if !strings.HasPrefix(line, "prefix: xxx") {
		t.Errorf("the start of a long line is what carries its meaning and must be kept, got %.40q", line)
	}
	if !strings.Contains(line, "bytes cut at the 500-byte line bound") {
		t.Errorf("a cut line must say so IN the line — the reader who needs to know is looking at it; got %.120q", line)
	}
	if !strings.Contains(x.Header(), "cut at 500 bytes") {
		t.Errorf("the header must state that a line bound bit, got: %s", x.Header())
	}
	if len(line) > 600 {
		t.Errorf("a bounded line must actually be bounded, got %d bytes", len(line))
	}
}

// TestSilentGateExcerptIsAMeasurementNotAnAbsence. "The gate has said nothing"
// and "nothing here captures what the gate said" lead to opposite actions, and
// an empty record that does not distinguish them is how a bounded read
// manufactures an absence.
func TestSilentGateExcerptIsAMeasurementNotAnAbsence(t *testing.T) {
	x := newExcerptBuffer().snapshot()
	if x == nil {
		t.Fatal("a silent gate must still produce a record — nil means 'not captured', which is a different claim")
	}
	if x.Spoke() || x.Lines != 0 {
		t.Errorf("a silent gate reports zero, got %d lines / pending %q", x.Lines, x.Pending)
	}
	if !strings.Contains(x.Header(), "NOTHING YET") || !strings.Contains(x.Header(), "not a bound hiding one") {
		t.Errorf("silence must be stated as a measurement, got: %s", x.Header())
	}

	var missing *GateExcerpt
	if !strings.Contains(missing.Header(), "NOT RECORDED") {
		t.Errorf("an absent record must say it is absent, got: %s", missing.Header())
	}
	if missing.Spoke() || missing.Complete() {
		t.Error("an absent record must not claim the gate was silent or that it is complete")
	}
}

// TestExcerptReassemblesLinesAcrossWrites: a gate's bytes arrive in whatever
// chunks the pipe hands over, and a line split across two writes is the normal
// case, not the edge one.
func TestExcerptReassemblesLinesAcrossWrites(t *testing.T) {
	e := newExcerptBuffer()
	e.write([]byte("=== watched pa"))
	e.write([]byte("ths changed:\n    .github/workflows/script-cont"))
	e.write([]byte("rols.yml\nnext\n"))
	x := e.snapshot()

	if x.Lines != 3 {
		t.Fatalf("expected 3 reassembled lines, got %d: %v", x.Lines, x.Head)
	}
	if x.Head[0] != "=== watched paths changed:" {
		t.Errorf("line 1 was not reassembled: %q", x.Head[0])
	}
	if x.Head[1] != "    .github/workflows/script-controls.yml" {
		t.Errorf("line 2 was not reassembled: %q", x.Head[1])
	}
}

// TestExcerptStripsCarriageReturns. A gate drawing a progress bar writes them,
// and left in place they return the terminal to column zero and overwrite the
// excerpt with itself — a rendering that hides the text this exists to show.
func TestExcerptStripsCarriageReturns(t *testing.T) {
	e := newExcerptBuffer()
	e.write([]byte("building\r\ntests 1/9\r"))
	x := e.snapshot()
	if x.Head[0] != "building" {
		t.Errorf("CRLF must not leave a stray carriage return, got %q", x.Head[0])
	}
	if x.Pending != "tests 1/9" {
		t.Errorf("a pending line must be stripped too, got %q", x.Pending)
	}
}

// TestExcerptSurvivesStateRoundTrip. The reader who most needs this is the one
// reading a state file written by a process that is now gone, so the record has
// to mean the same thing after a marshal as before it.
func TestExcerptSurvivesStateRoundTrip(t *testing.T) {
	e := newExcerptBuffer()
	for i := 1; i <= 200; i++ {
		e.write([]byte(fmt.Sprintf("line-%d\n", i)))
	}
	e.write([]byte("half a li"))
	mr := &MergeRequest{ID: "mr-rt", Progress: &StepProgress{OutputExcerpt: e.snapshot()}}

	data, err := json.Marshal(mr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"output_excerpt"`) {
		t.Error("the excerpt must travel under its own field name, not inside last_output")
	}
	var back MergeRequest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Progress.OutputExcerpt
	if got.Lines != 200 || got.Elided != mr.Progress.OutputExcerpt.Elided {
		t.Errorf("counts changed across the round trip: %d lines / %d elided", got.Lines, got.Elided)
	}
	if got.First() != "line-1" || got.Latest() != "half a li" {
		t.Errorf("text changed across the round trip: first=%q latest=%q", got.First(), got.Latest())
	}
	if got.HeadLimit != excerptHeadLines || got.TailLimit != excerptTailLines {
		t.Error("the bounds must travel with the record — a reader must not have to consult the source to judge truncation")
	}
}

// TestGateExcerptTracksEachGateSeparately: the record is per-gate, so gate 2's
// excerpt must not carry gate 1's words. Reading the previous gate's header as
// the current one's is the same class of error as reading the runner's
// heartbeat as the gate's progress.
func TestGateExcerptTracksEachGateSeparately(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo from-gate-one\", \"echo from-gate-two\"]\n")

	mr := &MergeRequest{ID: "mr-two-gates", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatalf("gates should have passed: %v", err)
	}
	x := mr.Progress.OutputExcerpt
	if x.Latest() != "from-gate-two" {
		t.Errorf("the record must hold the CURRENT gate's text, got %q", x.Latest())
	}
	if strings.Contains(strings.Join(x.Head, "\n"), "from-gate-one") {
		t.Error("gate 2's excerpt must not carry gate 1's output")
	}
}
