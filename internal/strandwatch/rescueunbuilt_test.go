package strandwatch

import (
	"fmt"
	"strings"
	"testing"
)

// The mg-aed4 battery: a stranded branch whose unmerged work is a RESCUE commit
// must not be handed a paste-ready `pogo refinery submit`, and it must be
// DISTINGUISHABLE in the report from a branch that is genuinely ready to submit.
//
// The subject below is transcribed from the mg-51bf rescue of 2026-08-19: five
// branches printed under a submit line while each of their items' own bodies said
// in bold not to submit them. That rescue is one of two spellings of the marker
// and the smaller one — see strandedwork.RescuePrefix for the measured
// population, and the strandedwork tests for the other form.
// The two ids are DIFFERENT and both are transcribed: RESCUE(mg-516e) is the item
// whose work was recovered, and the trailing (mg-51bf) is the rescue that
// recovered it. Collapsing them into one id in a fixture makes a report that
// prints the wrong one of the two pass.
const liveRescueSubject = "RESCUE(mg-516e): fleet-progress detector recovered from preserved " +
	"worktree p516e — UNREVIEWED, not this committer's work (mg-51bf)"

// TestRescueBranchIsNotHandedASubmitCommand is the ticket, minimally.
//
// The assertion is on the ABSENCE of a string, which is unusual and deliberate:
// the defect was not a missing warning, it was a present command. A report that
// prints the warning AND the command is the state that was measured, because a
// prose caveat beside a runnable command loses to the command.
func TestRescueBranchIsNotHandedASubmitCommand(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commit("fleetprogress.go", liveRescueSubject)
	r.push("polecat-p516e")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-516e", Status: "available", Repo: r.dir, Title: "fleet-progress detector"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-516e")
	if !ok {
		t.Fatalf("no row for mg-516e; a rescue branch IS stranded and must still be reported — "+
			"what changes is the remedy, not whether it appears.\n%s", Render(rep, true))
	}
	if row.Kind != KindRescueUnbuilt {
		t.Fatalf("Kind = %q, want %q\n%s", row.Kind, KindRescueUnbuilt, Render(rep, true))
	}
	if row.Rescue == nil {
		t.Fatal("Rescue is nil on a branch whose unmerged commit carries the RESCUE marker")
	}
	if got := row.Rescue.RescueTracker(); got != "mg-51bf" {
		t.Errorf("RescueTracker() = %q, want mg-51bf — a reader asking WHY this was committed "+
			"unbuilt has to be able to chase the rescue", got)
	}
	if strings.Contains(row.Remedy(), "refinery submit") {
		t.Errorf("Remedy() = %q still prints a paste-ready submit for work that has never been "+
			"built; if the gate passed, half-implemented unreviewed code merges to main", row.Remedy())
	}
	if out := Render(rep, false); strings.Contains(out, "refinery submit") {
		t.Errorf("the rendered report still carries a submit line:\n%s", out)
	}
	if !rep.Actionable() {
		t.Error("Actionable() = false; the row is still a finding")
	}
}

// TestRescueRowSaysWhyItWithheldTheSubmit. Withholding a command without saying
// so is how a reader concludes the tool is broken and reaches for the command
// anyway. The row has to state the fact (never built) and the mechanism (the
// hook was bypassed on purpose).
func TestRescueRowSaysWhyItWithheldTheSubmit(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commit("fleetprogress.go", liveRescueSubject)
	r.push("polecat-p516e")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-516e", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	out := Render(rep, false)
	for _, want := range []string{
		"RESCUE COMMIT",
		"NEVER BUILT",
		"--no-verify",
		"mg-51bf",
		"1 RESCUE-UNBUILT",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}
	row, _ := rowFor(rep, "mg-516e")
	if !strings.Contains(row.Remedy(), "on purpose") {
		t.Errorf("Remedy() = %q does not say the submit line was withheld deliberately", row.Remedy())
	}
}

// TestRescueAndOrdinaryStrandedRowsAreDistinguishable is the ticket's actual
// claim. It was never "the report has no caveat" — the closing caveat was there
// and still is. It was that the five unbuildable branches rendered IDENTICALLY
// to a branch genuinely ready to submit, so a reader who trusted the caveat had
// nothing telling them which rows it applied to.
//
// So the fixture is a MIXED population, and both halves are asserted: the
// ordinary branch keeps its submit, the rescue branch does not have one.
func TestRescueAndOrdinaryStrandedRowsAreDistinguishable(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commit("fleetprogress.go", liveRescueSubject)
	r.push("polecat-p516e")
	r.checkout("main")
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery, all five cases caught (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	rep, err := Scan(Options{
		Items: board(
			Item{ID: "mg-516e", Status: "available", Repo: r.dir, Title: "fleet-progress detector"},
			Item{ID: "mg-9a19", Status: "available", Repo: r.dir, Title: "drift battery"},
		),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	rescue, ok := rowFor(rep, "mg-516e")
	if !ok {
		t.Fatalf("no rescue row\n%s", Render(rep, true))
	}
	ordinary, ok := rowFor(rep, "mg-9a19")
	if !ok {
		t.Fatalf("no ordinary stranded row\n%s", Render(rep, true))
	}
	if rescue.Kind == ordinary.Kind {
		t.Fatalf("both rows are Kind %q — that is the defect verbatim: unbuilt rescue work and "+
			"work genuinely ready to submit are rendered identically", rescue.Kind)
	}
	if ordinary.Kind != KindStranded {
		t.Errorf("the ordinary branch's Kind = %q, want %q — the fix must not reclassify "+
			"branches that have nothing to do with a rescue", ordinary.Kind, KindStranded)
	}
	if !strings.Contains(ordinary.Remedy(), "pogo refinery submit polecat-q9a19") {
		t.Errorf("the ordinary branch LOST its submit remedy: %q", ordinary.Remedy())
	}
	if strings.Contains(rescue.Remedy(), "refinery submit") {
		t.Errorf("the rescue branch kept a submit remedy: %q", rescue.Remedy())
	}

	out := Render(rep, false)
	if !strings.Contains(out, "pogo refinery submit polecat-q9a19") {
		t.Errorf("the report lost the ordinary branch's submit line:\n%s", out)
	}
	if strings.Contains(out, "refinery submit polecat-p516e") {
		t.Errorf("the report still prints a submit line for the rescue branch:\n%s", out)
	}
	if !strings.Contains(out, "1 RESCUE-UNBUILT, 1 stranded") {
		t.Errorf("the findings header does not separate the two counts:\n%s", out)
	}
}

// TestRescueRowOutranksOrdinaryStranded. Both rows are on `available` items, so
// statusRank cannot separate them and Kind.Rank decides. The row whose ordinary
// remedy is destructive has to be the one a reader who stops after the first row
// has read.
func TestRescueRowOutranksOrdinaryStranded(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")
	r.branch("polecat-p516e", "main")
	r.commit("fleetprogress.go", liveRescueSubject)
	r.push("polecat-p516e")
	r.checkout("main")

	rep, err := Scan(Options{
		Items: board(
			Item{ID: "mg-9a19", Status: "available", Repo: r.dir},
			Item{ID: "mg-516e", Status: "available", Repo: r.dir},
		),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("got %d rows, want 2\n%s", len(rep.Rows), Render(rep, true))
	}
	if rep.Rows[0].Kind != KindRescueUnbuilt {
		t.Errorf("first row is %q (%s); the rescue row must lead, because the remedy a reader "+
			"would otherwise reach for on it is the destructive one",
			rep.Rows[0].Kind, rep.Rows[0].Item.ID)
	}
}

// TestMergedRescueCommitIsNotAnUnbuiltRow is the negative control on the
// PREDICATE, and it is the one that keeps this from becoming a permanent label.
// Only an UNMERGED rescue commit is evidence of unbuilt work outside the target:
// one that already landed was built by whatever gate merged it, and the row's
// remedy is `mg done` exactly as it was before.
func TestMergedRescueCommitIsNotAnUnbuiltRow(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commit("fleetprogress.go", liveRescueSubject)
	r.push("polecat-p516e")
	r.landCleanly("polecat-p516e")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-516e", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-516e")
	if !ok {
		t.Fatalf("no row\n%s", Render(rep, true))
	}
	if row.Kind != KindLandedNotClosed {
		t.Errorf("Kind = %q, want %q — the rescue commit is ON the target, so it was built",
			row.Kind, KindLandedNotClosed)
	}
	if row.Rescue != nil {
		t.Errorf("Rescue = %+v on a branch with no unmerged commits", row.Rescue)
	}
	if !strings.Contains(row.Remedy(), "mg done") {
		t.Errorf("Remedy() = %q lost the close command", row.Remedy())
	}
}

// TestOrdinarySubjectsAreNotReadAsRescues is the false-positive control on the
// whole report: nothing in a normal fleet run may acquire the new kind.
func TestOrdinarySubjectsAreNotReadAsRescues(t *testing.T) {
	subjects := []string{
		"fix(refinery): rescue the queue from a wedged worker (mg-1111)",
		"feat(gitgc): preserved-worktree rescue path (mg-2222)",
		"docs: rescued 1026 lines by hand (mg-3333)",
	}
	for i, subject := range subjects {
		r := newRepo(t)
		branch := fmt.Sprintf("polecat-q%04d", i)
		item := fmt.Sprintf("mg-%04d", i)
		r.branch(branch, "main")
		r.commit("work.md", subject)
		r.push(branch)
		r.checkout("main")

		rep, err := Scan(Options{
			Items:      board(Item{ID: item, Status: "available", Repo: r.dir}),
			LiveAgents: fleet(),
			Target:     "main",
		})
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		row, ok := rowFor(rep, item)
		if !ok {
			t.Fatalf("no row for %s (%q)\n%s", item, subject, Render(rep, true))
		}
		if row.Kind != KindStranded {
			t.Errorf("subject %q classified as %q; only a subject that BEGINS with the marker "+
				"is a rescue, or every branch that ever mentioned one loses its remedy",
				subject, row.Kind)
		}
	}
}

// TestConflictSuspectKeepsItsKindAndStillNamesTheRescue is the precedence cell,
// and it is the one place the new Kind deliberately does NOT win.
//
// KindConflictSuspect already recommends neither remedy and says why, and it
// carries a fact the rescue row cannot: the target may ALREADY hold this work.
// Overwriting it would trade a correct "the two instruments disagree, go and
// look" for a narrower statement. So the Kind stays, and Row.Rescue is set
// anyway — a reader deciding what to do about an unreviewed rescue branch needs
// to know it is one whatever cell it landed in.
func TestConflictSuspectKeepsItsKindAndStillNamesTheRescue(t *testing.T) {
	r := newRepo(t)
	var base []string
	for i := 0; i < 40; i++ {
		base = append(base, fmt.Sprintf("the original line number %02d of the shared document", i))
	}
	writeFile(t, r, "doc.md", strings.Join(base, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "chore: the shared document")
	r.push("main")
	mainBase := strings.TrimSpace(r.git("rev-parse", "HEAD"))

	var work []string
	for i := 0; i < 25; i++ {
		work = append(work, fmt.Sprintf("the rescued branch contributes substantial line %02d", i))
	}
	branchLines := append(append([]string{}, base[:20]...), append(append([]string{}, work...), base[20:]...)...)
	r.branch("polecat-p516e", mainBase)
	writeFile(t, r, "doc.md", strings.Join(branchLines, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", liveRescueSubject)
	r.push("polecat-p516e")

	r.checkout("main")
	var other []string
	for i := 0; i < 6; i++ {
		other = append(other, fmt.Sprintf("meanwhile main inserted unrelated line %02d right here", i))
	}
	mainLines := append(append([]string{}, base[:20]...), append(append([]string{}, other...), base[20:]...)...)
	writeFile(t, r, "doc.md", strings.Join(mainLines, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "feat(doc): main's unrelated change")
	r.push("main")

	resolved := append(append([]string{}, mainLines[:26]...), append(append([]string{}, work...), mainLines[26:]...)...)
	writeFile(t, r, "doc.md", strings.Join(resolved, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", liveRescueSubject)
	r.push("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-516e", Status: "available", Repo: r.dir, Title: "fleet-progress detector"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-516e")
	if !ok {
		t.Fatalf("no row\n%s", Render(rep, true))
	}
	if row.Kind != KindConflictSuspect {
		t.Fatalf("Kind = %q, want %q: %s\n%s", row.Kind, KindConflictSuspect,
			row.Presence.Describe(), Render(rep, true))
	}
	if row.Rescue == nil {
		t.Error("Rescue is nil; the marker is on the commit whatever cell the row landed in, and " +
			"a reader still has to know this branch is unreviewed rescue work")
	}
	if strings.Contains(row.Remedy(), "refinery submit") || strings.Contains(row.Remedy(), "mg done") {
		t.Errorf("Remedy() = %q must name neither action", row.Remedy())
	}
	out := Render(rep, false)
	for _, want := range []string{"VERIFY BEFORE ACTING", "RESCUE COMMIT"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lost %q:\n%s", want, out)
		}
	}
}
