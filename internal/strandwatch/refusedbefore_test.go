package strandwatch

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The mg-441f battery: a stranded branch the refinery has ALREADY refused must
// not be handed the bare `refinery submit` line, and the cases where that cannot
// be established must say so rather than falling back to it silently.
//
// The record below is transcribed from the live refinery on 2026-08-19 —
// `curl /refinery/history | jq` over the retained window — and every field is
// real. 6 of the 15 failed requests then retained carried this exact stage,
// class and reason, one of them for polecat-p5058, which is the branch the
// ticket was filed about. The reason string is quoted rather than paraphrased
// because it is the substance of the row: it already says what has to happen.
const liveRebaseRefusal = "the rebase reached the tree and the tree disagreed — exactly as true on " +
	"the next attempt. Resubmitting unchanged re-runs the same conflict forever; the branch has to " +
	"be rebased and the conflict resolved by hand before it can land"

// refinery builds a History source over a fixed set of records.
//
// floorOffset is how far BEFORE the newest record the window's floor sits;
// a window whose floor is older than every fixture branch answers conclusively,
// which is the state most of this battery wants to hold fixed while it varies
// the thing under test.
func refinery(floor time.Time, records map[string]PriorSubmission) func() (RefineryHistory, error) {
	return func() (RefineryHistory, error) {
		return RefineryHistory{
			Latest:    records,
			Floor:     floor,
			Records:   len(records),
			Retention: "max 100 entries / 168h0m0s",
		}, nil
	}
}

// refused is the record the ticket is about: failed at rebase, class defect,
// not retried, and submitted AFTER the branch's tip so it is about what is on
// the branch now.
func refused(repo, branch string, at time.Time) map[string]PriorSubmission {
	return map[string]PriorSubmission{
		QueueKey(repo, branch): {
			MR:          "mr-d9v65uqtjv1j0e4isog0",
			Status:      "failed",
			Stage:       "rebase",
			Class:       "defect",
			Target:      "main",
			Reason:      liveRebaseRefusal,
			Triage:      "DEFECT — establishes a fact about the branch: re-running establishes the SAME fact.",
			Attempts:    1,
			SubmittedAt: at,
			FinishedAt:  at.Add(time.Minute),
			Repeats:     true,
		},
	}
}

// TestRefusedBranchIsNotHandedTheBareSubmit is the ticket, minimally: the tool
// printed, with confidence and as its single recommended action, the one command
// that provably cannot work.
//
// The assertion is on the ABSENCE of the submit string for the same reason
// mg-aed4's is: the defect was never a missing warning, it was a present command,
// and a report that prints both is the state that was measured.
func TestRefusedBranchIsNotHandedTheBareSubmit(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: the work the refinery could not rebase")
	r.push("polecat-p5058")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir, Title: "an audit"}),
		LiveAgents: fleet(),
		History:    refinery(hourAgo(t, 48), refused(r.dir, "polecat-p5058", time.Now())),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-5058")
	if !ok {
		t.Fatalf("no row for mg-5058; a refused branch IS still stranded and must still be "+
			"reported — what changes is the remedy\n%s", Render(rep, true))
	}
	if row.Kind != KindRefusedBefore {
		t.Fatalf("Kind = %q, want %q\n%s", row.Kind, KindRefusedBefore, Render(rep, true))
	}
	if row.Prior == nil {
		t.Fatal("Prior is nil on a branch the fixture gave a failed merge request")
	}
	if row.PriorStale {
		t.Error("PriorStale = true for a refusal submitted after the branch's last commit")
	}
	if row.HistoryGap != "" {
		t.Errorf("HistoryGap = %q on a conclusive answer", row.HistoryGap)
	}
	if strings.Contains(row.Remedy(), "refinery submit") {
		t.Errorf("Remedy() = %q still prints the command the refinery has already refused; an "+
			"unchanged resubmit re-runs the same failure", row.Remedy())
	}
	if out := Render(rep, false); strings.Contains(out, "refinery submit") {
		t.Errorf("the rendered report still carries a submit line:\n%s", out)
	}
	if !rep.Actionable() {
		t.Error("Actionable() = false; the row is still a finding")
	}
}

// TestRefusedRowNamesTheStageAndTheRefinerysOwnReason. The ticket asks for the
// failure STAGE by name. Withholding a command without saying what already
// happened is how a reader concludes the tool is broken and runs it anyway.
func TestRefusedRowNamesTheStageAndTheRefinerysOwnReason(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: the work the refinery could not rebase")
	r.push("polecat-p5058")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History:    refinery(hourAgo(t, 48), refused(r.dir, "polecat-p5058", time.Now())),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	out := Render(rep, false)
	for _, want := range []string{
		"ALREADY SUBMITTED",
		"mr-d9v65uqtjv1j0e4isog0",
		"stage=rebase",
		"class=defect",
		"against main",
		"re-runs the same conflict forever",
		"1 ALREADY-REFUSED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}
	row, _ := rowFor(rep, "mg-5058")
	if !strings.Contains(row.Remedy(), "stage=rebase") {
		t.Errorf("Remedy() = %q does not name the stage the refinery refused at", row.Remedy())
	}
	if !strings.Contains(row.Remedy(), "on purpose") {
		t.Errorf("Remedy() = %q does not say the submit line was withheld deliberately", row.Remedy())
	}
}

// TestRefusalOlderThanTheBranchDoesNotSuppressTheRemedy is the control this
// change most needed, and it is this ticket's own defect in the opposite
// direction: a branch that was refused, FIXED and pushed again is an ordinary
// resubmit. A remedy computed from an expired fact is no better than one computed
// from no fact at all.
func TestRefusalOlderThanTheBranchDoesNotSuppressTheRemedy(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: the work the refinery could not rebase")
	r.commit("audit.go", "fix: resolve the conflict the refinery hit")
	r.push("polecat-p5058")
	r.checkout("main")

	// The refusal is dated well before the branch's commits, which the fixture
	// makes now.
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History:    refinery(hourAgo(t, 48), refused(r.dir, "polecat-p5058", hourAgo(t, 24))),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-5058")
	if !ok {
		t.Fatalf("no row for mg-5058\n%s", Render(rep, true))
	}
	if row.Kind != KindStranded {
		t.Fatalf("Kind = %q, want %q — the refusal is about content this branch no longer has\n%s",
			row.Kind, KindStranded, Render(rep, true))
	}
	if !row.PriorStale {
		t.Error("PriorStale = false for a refusal submitted before the branch's last commit")
	}
	if !strings.Contains(row.Remedy(), "refinery submit") {
		t.Errorf("Remedy() = %q withheld the submit line from a branch that was FIXED after the "+
			"refusal; that is the correct remedy and it must still be printed", row.Remedy())
	}
	out := Render(rep, false)
	if !strings.Contains(out, "PREDATES this branch's last commit") {
		t.Errorf("the report does not say the record is stale, so a reader who saw ALREADY "+
			"SUBMITTED would stop there:\n%s", out)
	}
}

// TestRetryableFailureKeepsItsSubmitLine. Only a class whose own table commits to
// REPRODUCING suppresses the remedy. An infrastructure failure establishes nothing
// about the branch and the refinery's own triage note says to resubmit — a check
// that suppressed on "it failed once" would withhold the one remedy that works.
func TestRetryableFailureKeepsItsSubmitLine(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p9a19", "main")
	r.commit("audit.go", "feat: an audit whose merge lost its network")
	r.push("polecat-p9a19")
	r.checkout("main")

	now := time.Now()
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History: refinery(hourAgo(t, 48), map[string]PriorSubmission{
			QueueKey(r.dir, "polecat-p9a19"): {
				MR: "mr-infra", Status: "failed", Stage: "fetch", Class: "infrastructure",
				Triage:      "INFRASTRUCTURE — establishes nothing about the branch. Resubmit; do NOT dispatch a fix.",
				SubmittedAt: now, FinishedAt: now, Repeats: false,
			},
		}),
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-9a19")
	if !ok {
		t.Fatalf("no row for mg-9a19\n%s", Render(rep, true))
	}
	if row.Kind != KindStranded {
		t.Fatalf("Kind = %q, want %q — an infrastructure failure establishes nothing about the "+
			"branch and its correct remedy IS the resubmit\n%s", row.Kind, KindStranded, Render(rep, true))
	}
	if !strings.Contains(row.Remedy(), "refinery submit") {
		t.Errorf("Remedy() = %q withheld the submit from a failure whose own triage note says to "+
			"resubmit", row.Remedy())
	}
	out := Render(rep, false)
	if !strings.Contains(out, "mr-infra") || !strings.Contains(out, "establishes nothing about the branch") {
		t.Errorf("the prior failure is not reported at all; a reader deciding whether to resubmit "+
			"wants to know it has already failed once:\n%s", out)
	}
}

// TestPrunedWindowIsNotAnAllClear is mg-8baa's lesson in this instrument. The
// refinery's history is a WINDOW: a branch's absence from it means either "never
// submitted" or "the record was deleted", and those license opposite remedies.
// The row must not fall back silently to the bare submit.
func TestPrunedWindowIsNotAnAllClear(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: work committed long before the window opened")
	r.push("polecat-p5058")
	r.checkout("main")

	// The window's floor is AFTER the branch's tip, so any refusal in between has
	// been pruned. The map is non-empty — other branches' records survive — which
	// is what makes the absence look like an answer.
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History:    refinery(time.Now().Add(time.Hour), refused(r.dir, "polecat-somebody-else", time.Now())),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-5058")
	if !ok {
		t.Fatalf("no row for mg-5058\n%s", Render(rep, true))
	}
	if row.HistoryGap == "" {
		t.Fatal("HistoryGap is empty for a branch older than the retained window's floor — " +
			"\"no record\" and \"the record was pruned\" must not render alike")
	}
	if !strings.Contains(row.HistoryGap, "pruned") {
		t.Errorf("HistoryGap = %q does not say the record could have been pruned", row.HistoryGap)
	}
	out := Render(rep, false)
	if !strings.Contains(out, "REMEDY NOT CHECKED AGAINST REFINERY HISTORY") {
		t.Errorf("the row does not warn that its remedy is unchecked:\n%s", out)
	}
}

// TestFreshBranchInsideTheWindowIsConclusive is the other half of the one above,
// and it is what keeps the caveat from being printed on every row forever. A
// TRUNCATED window still answers conclusively for a branch committed to since the
// window opened: every submission of what is on it now would be inside the window.
func TestFreshBranchInsideTheWindowIsConclusive(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: work committed just now")
	r.push("polecat-p5058")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History:    refinery(hourAgo(t, 168), refused(r.dir, "polecat-somebody-else", time.Now())),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-5058")
	if !ok {
		t.Fatalf("no row for mg-5058\n%s", Render(rep, true))
	}
	if row.HistoryGap != "" {
		t.Errorf("HistoryGap = %q on a branch whose tip is INSIDE the retained window; the "+
			"window covers every submission that could have been made since, so this is an "+
			"answer and not a gap", row.HistoryGap)
	}
	if row.Kind != KindStranded {
		t.Errorf("Kind = %q, want %q", row.Kind, KindStranded)
	}
	if !strings.Contains(row.Remedy(), "refinery submit") {
		t.Errorf("Remedy() = %q; a branch with no record and a covering window is an ordinary "+
			"stranded row", row.Remedy())
	}
	if out := Render(rep, false); strings.Contains(out, "REMEDY NOT CHECKED") {
		t.Errorf("a conclusive row carries the unchecked caveat anyway; printed on every row it "+
			"teaches readers to skip it:\n%s", out)
	}
}

// TestUnreadableHistorySaysSoRatherThanFallingBack. The ticket names this
// explicitly: a branch whose history could not be read must say THAT rather than
// falling back silently to the bare submit line.
func TestUnreadableHistorySaysSoRatherThanFallingBack(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: work whose history could not be read")
	r.push("polecat-p5058")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History: func() (RefineryHistory, error) {
			return RefineryHistory{}, errors.New("dial tcp 127.0.0.1:10000: connect: connection refused")
		},
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v — an unreadable history is stated, not fatal: the rest of the answer "+
			"is still worth having", err)
	}
	if rep.HistoryUnreadable == "" {
		t.Error("Report.HistoryUnreadable is empty after the history source returned an error")
	}
	row, ok := rowFor(rep, "mg-5058")
	if !ok {
		t.Fatalf("no row for mg-5058\n%s", Render(rep, true))
	}
	if !strings.Contains(row.HistoryGap, "COULD NOT BE READ") {
		t.Errorf("HistoryGap = %q does not say the history was unreadable", row.HistoryGap)
	}
	out := Render(rep, false)
	for _, want := range []string{
		"refinery merge history UNREADABLE",
		"connection refused",
		"REMEDY NOT CHECKED AGAINST REFINERY HISTORY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}
}

// TestHistoryNotConsultedIsDistinctFromAnEmptyWindow. Nil means "not asked" and
// an empty window means "asked and it remembers nothing". They are the same zero
// value and they license the same fallback, which is exactly how a silent
// fallback survives.
func TestHistoryNotConsultedIsDistinctFromAnEmptyWindow(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: work nobody asked the refinery about")
	r.push("polecat-p5058")
	r.checkout("main")

	base := Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	}
	unasked, err := Scan(base)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if unasked.HistoryConsulted {
		t.Error("HistoryConsulted = true with Options.History nil")
	}
	row, _ := rowFor(unasked, "mg-5058")
	if !strings.Contains(row.HistoryGap, "NOT consulted") {
		t.Errorf("HistoryGap = %q with no history source", row.HistoryGap)
	}
	if out := Render(unasked, false); !strings.Contains(out, "NOT CONSULTED") {
		t.Errorf("the report does not say the history was never consulted:\n%s", out)
	}

	withEmpty := base
	withEmpty.History = refinery(time.Time{}, nil)
	empty, err := Scan(withEmpty)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !empty.HistoryConsulted {
		t.Error("HistoryConsulted = false after the history source answered")
	}
	erow, _ := rowFor(empty, "mg-5058")
	if !strings.Contains(erow.HistoryGap, "EMPTY") {
		t.Errorf("HistoryGap = %q on an empty retained window; it observes nothing, which is not "+
			"the same as observing no submission", erow.HistoryGap)
	}
	if erow.HistoryGap == row.HistoryGap {
		t.Error("an unconsulted history and an empty one produce the SAME row text; they are " +
			"different facts and a reader acts differently on each")
	}
}

// TestRescueOutranksRefused. The two causes are independent — a rescue branch has
// typically never been submitted at all — but where they coincide the rescue label
// must survive: its failure mode is a PASSING gate merging unreviewed code, which
// is worse than a wasted gate run.
func TestRescueOutranksRefused(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commit("rescued.go", liveRescueSubject)
	r.push("polecat-p516e")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-516e", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History:    refinery(hourAgo(t, 168), refused(r.dir, "polecat-p516e", time.Now())),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-516e")
	if !ok {
		t.Fatalf("no row for mg-516e\n%s", Render(rep, true))
	}
	if row.Kind != KindRescueUnbuilt {
		t.Fatalf("Kind = %q, want %q — the never-built claim is the more dangerous one\n%s",
			row.Kind, KindRescueUnbuilt, Render(rep, true))
	}
	if row.Prior == nil {
		t.Error("Prior was dropped when the rescue kind displaced the refused one; the refusal " +
			"is still a fact a reader of this row needs")
	}
	out := Render(rep, false)
	if !strings.Contains(out, "ALREADY SUBMITTED") || !strings.Contains(out, "RESCUE COMMIT") {
		t.Errorf("the row does not carry both facts:\n%s", out)
	}
	if strings.Contains(out, "refinery submit") {
		t.Errorf("a submit line survived on a row that is both rescue and refused:\n%s", out)
	}
}

// TestRefusedRankedAheadOfStranded. A reader who stops at the first row must have
// read the one whose ordinary remedy is wrong.
func TestRefusedRankedAheadOfStranded(t *testing.T) {
	if KindRefusedBefore.Rank() >= KindStranded.Rank() {
		t.Errorf("refused_before ranks %d and stranded ranks %d; the row whose printed remedy "+
			"cannot work has to be read first",
			KindRefusedBefore.Rank(), KindStranded.Rank())
	}
	if KindRescueUnbuilt.Rank() >= KindRefusedBefore.Rank() {
		t.Errorf("rescue_unbuilt ranks %d and refused_before ranks %d; a merge of unreviewed "+
			"code outranks a wasted gate run",
			KindRescueUnbuilt.Rank(), KindRefusedBefore.Rank())
	}
	seen := map[int]Kind{}
	for _, k := range []Kind{
		KindRescueUnbuilt, KindRefusedBefore, KindStranded, KindUnjudged,
		KindRepoUnreadable, KindConflictSuspect, KindOrphanBranch,
	} {
		if other, dup := seen[k.Rank()]; dup {
			t.Errorf("%s and %s share rank %d, so their order is whatever the sort happens to do",
				k, other, k.Rank())
		}
		seen[k.Rank()] = k
	}
}

// TestCoversNeedsEvidenceInBothDirections. Covers is what turns a truncated
// window into a conclusive answer, so its two "no evidence" cases have to answer
// NO — a zero floor or an undated branch that answered YES would manufacture
// coverage out of a missing field.
func TestCoversNeedsEvidenceInBothDirections(t *testing.T) {
	now := time.Now()
	if (RefineryHistory{}).Covers(now) {
		t.Error("an empty window claims to cover a real time; it observes nothing")
	}
	if (RefineryHistory{Floor: now.Add(-time.Hour)}).Covers(time.Time{}) {
		t.Error("a window claims to cover an UNDATED branch; the zero time is not the epoch here")
	}
	h := RefineryHistory{Floor: now.Add(-time.Hour)}
	if !h.Covers(now) {
		t.Error("a window does not cover a branch committed to after its floor")
	}
	if h.Covers(now.Add(-2 * time.Hour)) {
		t.Error("a window claims to cover a branch older than its floor")
	}
}

// TestFrameStatesTheHistoryWindow. An instrument that uses a bounded source and
// does not name the bound gets read as a census — the reading mg-8baa and mg-ded2
// were both filed about.
func TestFrameStatesTheHistoryWindow(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p5058", "main")
	r.commit("audit.go", "feat: something")
	r.push("polecat-p5058")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-5058", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History:    refinery(hourAgo(t, 168), refused(r.dir, "polecat-p5058", time.Now())),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	frame := strings.Join(rep.Frame, "\n")
	for _, want := range []string{"merge history", "PRUNED", "max 100 entries"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not contain %q:\n%s", want, frame)
		}
	}
}

// hourAgo is a fixture time N hours in the past.
func hourAgo(t *testing.T, hours int) time.Time {
	t.Helper()
	return time.Now().Add(-time.Duration(hours) * time.Hour)
}

// TestMergedPriorDoesNotInventAFailure. Row.Prior is the LATEST completed record
// whatever its outcome, and it is printed on any kind that carries it — including
// landed_not_closed, where naming the merge request that landed the work is
// useful. A merged request has no stage, and a phrase built for a failure would
// render "MERGED at an unrecorded stage": a sentence that invents a failure out
// of a missing field, on the row whose remedy is `mg done`.
func TestMergedPriorDoesNotInventAFailure(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p65d2", "main")
	r.commit("relay.go", "feat: the representative relay (mg-65d2)")
	r.push("polecat-p65d2")
	r.landCleanly("polecat-p65d2")

	now := time.Now()
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-65d2", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		History: refinery(hourAgo(t, 168), map[string]PriorSubmission{
			QueueKey(r.dir, "polecat-p65d2"): {
				MR: "mr-landed", Status: "merged", Target: "main",
				SubmittedAt: now, FinishedAt: now,
			},
		}),
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-65d2")
	if !ok {
		t.Fatalf("no row for mg-65d2\n%s", Render(rep, true))
	}
	if row.Kind != KindLandedNotClosed {
		t.Fatalf("Kind = %q, want %q — a merged prior must not change what the row IS\n%s",
			row.Kind, KindLandedNotClosed, Render(rep, true))
	}
	out := Render(rep, false)
	if strings.Contains(out, "unrecorded stage") || strings.Contains(out, "MERGED at stage") {
		t.Errorf("a merged record was rendered as though it had failed somewhere:\n%s", out)
	}
	if !strings.Contains(out, "mr-landed MERGED against main") {
		t.Errorf("the merged record is not named on the row that should carry it:\n%s", out)
	}
	if !strings.Contains(row.Remedy(), "mg done") {
		t.Errorf("Remedy() = %q; a landed row's remedy is unchanged by its history", row.Remedy())
	}
}
