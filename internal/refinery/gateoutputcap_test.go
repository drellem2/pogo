package refinery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// elisionRE pulls both counts back out of the marker, so a test can assert on
// what the record CLAIMS about itself rather than on its length alone.
var elisionRE = regexp.MustCompile(`gate output elided: (\d+) of (\d+) bytes removed`)

func TestCapGateOutputLeavesSmallOutputByteIdentical(t *testing.T) {
	for _, s := range []string{"", "\n", "gate ok\n", strings.Repeat("x", gateOutputRecordCap)} {
		if got := capGateOutput(s); got != s {
			t.Errorf("capGateOutput mutated output of %d bytes (cap %d)", len(s), gateOutputRecordCap)
		}
		if gateOutputWasCapped(s) {
			t.Errorf("uncapped output reported as capped: %q", s)
		}
	}
}

func TestCapGateOutputKeepsHeadAndTailAndSaysWhatItRemoved(t *testing.T) {
	head := "watchlist consistent: 17 paths\n"
	tail := "\nFAIL github.com/drellem2/pogo/internal/refinery\n"
	full := head + strings.Repeat("noise\n", 200000) + tail

	got := capGateOutput(full)

	if len(got) > gateOutputRecordCap {
		t.Fatalf("capped output is %d bytes, over the %d cap", len(got), gateOutputRecordCap)
	}
	// The head is what answers "why is this slow" and the tail is what answers
	// "what failed"; a cut that kept only one of them loses a question the
	// record exists to answer (same argument as GateExcerpt).
	if !strings.HasPrefix(got, head) {
		t.Errorf("the gate's opening lines were dropped: %q", firstLine(got))
	}
	if !strings.HasSuffix(got, tail) {
		t.Errorf("the gate's last lines were dropped: %q", lastLine(got))
	}

	m := elisionRE.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("the cut does not say it happened — a bounded record whose bound is unstated "+
			"renders 'never printed' and 'printed outside the window' identically. Got:\n%s", got)
	}
	removed, _ := strconv.Atoi(m[1])
	total, _ := strconv.Atoi(m[2])
	if total != len(full) {
		t.Errorf("marker reports the original as %d bytes, actual %d", total, len(full))
	}
	if removed <= 0 {
		t.Errorf("marker reports %d bytes removed, want > 0", removed)
	}
	// Kept + removed must account for the whole original: an elision count that
	// under-reports would understate the gap it exists to declare.
	if removed+len(got) < len(full) {
		t.Errorf("marker under-reports: %d removed + %d kept < %d original", removed, len(got), len(full))
	}
	if !gateOutputWasCapped(got) {
		t.Error("gateOutputWasCapped does not recognise its own marker")
	}
}

func TestCapGateOutputNeverSplitsARune(t *testing.T) {
	// A wall of 3-byte runes: every naive byte cut lands mid-rune.
	full := strings.Repeat("あ", 20000)
	got := capGateOutputTo(full, 4096)
	if len(got) > 4096 {
		t.Fatalf("capped output is %d bytes, over the 4096 cap", len(got))
	}
	head, _, ok := strings.Cut(got, "\n\n... [")
	if !ok {
		t.Fatalf("no marker in capped output")
	}
	for _, r := range head {
		if r == '�' {
			t.Fatal("head cut split a UTF-8 rune")
		}
	}
	_, tail, _ := strings.Cut(got, "] ...\n\n")
	for _, r := range tail {
		if r == '�' {
			t.Fatal("tail cut split a UTF-8 rune")
		}
	}
}

func TestCapGateOutputWithATinyCapStillExplainsItself(t *testing.T) {
	got := capGateOutputTo(strings.Repeat("x", 5000), 20)
	if !gateOutputWasCapped(got) {
		t.Errorf("a cap too small to excerpt must still say what happened, got %q", got)
	}
	if !strings.Contains(got, "5000") {
		t.Errorf("the degenerate case must still report the original size, got %q", got)
	}
}

// TestGateOutputIsCappedBeforeItReachesPersistedState is the wiring half of
// mg-538e change (1), end to end through a real failing gate and out to the
// file that was measured at 6.3 MB — 5.83 MB of which (93%) was
// history[].gate_output, largest single entry 518 KB.
//
// It asserts on the persisted BYTES, not on the in-memory record, because the
// file is what the fsync was paying for.
func TestGateOutputIsCappedBeforeItReachesPersistedState(t *testing.T) {
	const emit = 400000
	originDir, branch := setupRepoWithDeploy(t,
		gateToml(fmt.Sprintf("echo GATE-HEADER-LINE; yes gate-noise | head -c %d; echo GATE-LAST-LINE; exit 1", emit), "120s"))

	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		StatePath:    statePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.setLoadSampler(nil)

	id, err := r.Submit(MergeRequest{RepoPath: originDir, Branch: branch, TargetRef: "main", Author: "mg-538e"})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()
	r.flushState()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR vanished")
	}
	if mr.Status != StatusFailed {
		t.Fatalf("expected the gate to fail the merge, got %s (%s)", mr.Status, mr.Error)
	}

	// Not vacuous: the gate really did produce far more than the cap. Without
	// this the "≤ cap" assertion below would also pass on a gate that said
	// nothing.
	m := elisionRE.FindStringSubmatch(mr.GateOutput)
	if m == nil {
		t.Fatalf("gate output carries no elision marker, so nothing was capped — "+
			"len=%d, cap=%d", len(mr.GateOutput), gateOutputRecordCap)
	}
	total, _ := strconv.Atoi(m[2])
	if total < 10*gateOutputRecordCap {
		t.Fatalf("the gate produced only %d bytes: this test is not exercising the condition it measures", total)
	}
	if len(mr.GateOutput) > gateOutputRecordCap {
		t.Errorf("record gate_output is %d bytes, over the %d cap", len(mr.GateOutput), gateOutputRecordCap)
	}
	if !strings.Contains(mr.GateOutput, "GATE-HEADER-LINE") {
		t.Error("the gate's opening line is missing from the record")
	}
	if !strings.Contains(mr.GateOutput, "GATE-LAST-LINE") {
		t.Error("the gate's final line is missing from the record")
	}

	// The file itself — the thing whose size set the marshal and fsync cost.
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	var persisted *MergeRequest
	for _, h := range st.History {
		if h != nil && h.ID == id {
			persisted = h
		}
	}
	if persisted == nil {
		t.Fatalf("MR %s not in persisted history", id)
	}
	if len(persisted.GateOutput) > gateOutputRecordCap {
		t.Errorf("persisted gate_output is %d bytes, over the %d cap", len(persisted.GateOutput), gateOutputRecordCap)
	}

	// Capping ONE field is only a fix if no sibling field carries the same
	// bulk — that is the check the repair could most easily skip, because it
	// would still look like it worked. Bound the WHOLE record, which is what
	// the marshal and the fsync actually pay for. Measured with this gate:
	// 400 KB of output produces an ~11.5 KB record (gate_output 8192 at the
	// cap, error 96, raw_error 96, progress ~1456).
	rec, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if max := 4 * gateOutputRecordCap; len(rec) > max {
		t.Errorf("the persisted record is %d bytes (gate_output %d): some OTHER field is carrying the "+
			"output the cap was supposed to bound", len(rec), len(persisted.GateOutput))
	}
	if len(data) > total/4 {
		t.Errorf("state file is %d bytes for one merge whose gate printed %d: the cap is not reaching disk",
			len(data), total)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
