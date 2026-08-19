package strandwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The mg-0c37 battery. Three separate failures, each with its own test, because
// the ticket names three and any one of them alone leaves the loss reachable:
//
//  1. the subject clip destroyed the declaration (`— UNREVI…`);
//  2. the remedy line — the part that gets COPIED — carried no marker at all;
//  3. the commit BODY, where the actionable remainder lives, was never read.
//
// What it cost: mayor submitted six branches in one batch on this report's
// recommendation without reading a commit message. 4dd1b9d merged — an
// UNREVIEWED prompt change across all six polecat templates — and its item was
// closed and archived with no successor. The declaration was on disk, in the
// commit being submitted, before the submit.

// commitBody makes a commit with a real message body. The shared `commit` helper
// writes a subject only, and a body is exactly what this ticket is about.
func (r *repo) commitBody(file, subject, body string) string {
	r.t.Helper()
	path := filepath.Join(r.dir, file)
	prev, _ := os.ReadFile(path)
	content := string(prev)
	for i := 0; i < 8; i++ {
		content += fmt.Sprintf("%s — substantive content line %02d\n", subject, i)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		r.t.Fatalf("write %s: %v", path, err)
	}
	r.git("add", file)
	r.git("commit", "-q", "-m", subject, "-m", body)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

// The p1d05 rescue, verbatim in the subject and cut to the operative sentences
// in the body. This is the commit that merged.
const (
	rescueSubject1d05 = "RESCUE(mg-1d05): core-budget prompt clause covering self-parallelising " +
		"libraries, recovered from preserved worktree p1d05 — UNREVIEWED, not this " +
		"committer's work (mg-51bf)"
	rescueBody1d05 = "Looks PARTIAL: the change is prose only, with no accompanying test, and the\n" +
		"repo's internal/agent/prompt_test.go does assert over the shipped template\n" +
		"corpus. Nothing was added there."
	rescueBody516e = "It looks substantially complete but has NOT been verified to build; treat\n" +
		"completeness as unestablished. Nothing was deleted."
)

// TestTheDeclarationSurvivesTheClip is the first ask, and it is measured against
// the exact string the tool printed:
//
//	RESCUE(mg-516e): fleet-progress detector recovered from preserved worktree p516e — UNREVI…
//
// The clip landed mid-word on the one token that should stop a reader. That is
// worse than printing nothing: it carries the fact and defeats it, and a reader
// who skims past it has been shown the evidence.
func TestTheDeclarationSurvivesTheClip(t *testing.T) {
	if len(liveRescueSubject) <= 92 {
		t.Fatalf("fixture subject is %d bytes and no longer overruns the 92-column clip; "+
			"this test asserts nothing", len(liveRescueSubject))
	}
	got := truncateSubject(liveRescueSubject, 92)
	if strings.Contains(got, "UNREVI…") {
		t.Fatalf("truncateSubject = %q — this is the measured defect, byte for byte", got)
	}
	if !strings.Contains(got, "UNREVIEWED") {
		t.Errorf("truncateSubject = %q drops the declaration entirely; the marker is the "+
			"reason the line is printed at all", got)
	}
	if !strings.Contains(got, "not this committer's work") {
		t.Errorf("truncateSubject = %q keeps the marker but drops the qualification after it; "+
			"`not this committer's work` is the half that says who to ask", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("truncateSubject = %q shows no elision, so a reader cannot tell the subject "+
			"was shortened", got)
	}
	if strings.Contains(got, "…UNREVIEWED") {
		t.Errorf("truncateSubject = %q butts the elision against the marker; `…UNREVIEWED` is "+
			"one glyph away from `UNREVI…` and reads the same for the length of a glance", got)
	}
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("truncateSubject = %q cut through a multi-byte rune; subjects in this repo "+
			"carry em dashes and a replacement character reads as a rendering bug", got)
	}
}

// TestAnOrdinarySubjectIsStillClippedFromTheEnd. The marker-preserving path must
// not become the only path: a subject with no declaration has nothing to
// preserve, and moving its elision would churn every other row of the report.
func TestAnOrdinarySubjectIsStillClippedFromTheEnd(t *testing.T) {
	long := "fix,test,docs(strandwatch): " + strings.Repeat("an ordinary subject with no marker ", 5) + "(mg-1234)"
	got := truncateSubject(long, 92)
	if len(got) > 94 { // 92 budget, "…" is three bytes for one column
		t.Errorf("truncateSubject = %q is %d bytes, over the budget", got, len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateSubject = %q no longer elides at the end for a subject with no "+
			"declaration to keep", got)
	}
}

// TestTheRemedyLineCarriesTheDeclaration is the second and most important ask.
//
// THE REMEDY IS WHAT GETS COPIED; the context line is what gets skimmed. This
// fixture is deliberately NOT a rescue commit — a rescue row already withholds
// its submit (mg-aed4) — so it is the case that still prints a paste-ready
// `pogo refinery submit` today, and the marker has to ride on that command.
func TestTheRemedyLineCarriesTheDeclaration(t *testing.T) {
	subject := "feat(progresswatch): fleet-progress detector, recovered by hand — " +
		"UNREVIEWED, not this committer's work (mg-0c37)"
	r := newRepo(t)
	r.branch("polecat-p0c37", "main")
	r.commit("progresswatch.go", subject)
	r.push("polecat-p0c37")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-0c37", Status: "available", Repo: r.dir, Title: "fleet-progress detector"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-0c37")
	if !ok {
		t.Fatalf("no row for mg-0c37\n%s", Render(rep, true))
	}
	if row.Kind != KindStranded {
		t.Fatalf("Kind = %q, want %q — this fixture exists to exercise the ONE cell that still "+
			"prints a paste-ready submit\n%s", row.Kind, KindStranded, Render(rep, true))
	}
	if row.Declared == nil {
		t.Fatal("Declared is nil for a commit whose subject says UNREVIEWED")
	}
	remedy := row.Remedy()
	if !strings.Contains(remedy, "refinery submit") {
		t.Fatalf("Remedy() = %q no longer prints the submit; withholding it here would be "+
			"mg-aed4's fix applied to a population it was not argued for", remedy)
	}
	if !strings.Contains(remedy, "DECLARES ITSELF UNREVIEWED") {
		t.Errorf("Remedy() = %q carries no marker. Six branches were submitted in one batch off "+
			"a line of exactly this shape", remedy)
	}
	if !strings.Contains(remedy, "git -C "+r.dir+" log -1 ") {
		t.Errorf("Remedy() = %q names no runnable way to read the commit; `read it first` is "+
			"the instruction that already lost", remedy)
	}
	if strings.Contains(remedy, "\n") {
		t.Errorf("Remedy() = %q is no longer one line; being one copyable line is the property "+
			"that made the remedy the thing readers act on", remedy)
	}
	if !strings.Contains(remedy, "do NOT dispatch at mg-0c37") {
		t.Errorf("Remedy() = %q lost the caution it already carried", remedy)
	}
	// The marker leads the comment. A reader scanning left to right after a long
	// command reaches the strongest thing first.
	_, comment, found := strings.Cut(remedy, "   # ")
	if !found || !strings.HasPrefix(comment, "COMMIT DECLARES ITSELF UNREVIEWED") {
		t.Errorf("Remedy() = %q does not lead its comment with the marker", remedy)
	}
}

// TestTheBodyRemainderIsSurfaced is the third ask, on the one shape the refinery
// gate cannot catch. p516e and p9d4e were both refused by the gate that night —
// one on a failing test, one on a rebase conflict. p1d05's build ran and passed
// correctly, because a missing test is exactly what a passing gate cannot see.
func TestTheBodyRemainderIsSurfaced(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p1d05", "main")
	r.commitBody("prompts.go", rescueSubject1d05, rescueBody1d05)
	r.push("polecat-p1d05")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-1d05", Status: "available", Repo: r.dir, Title: "core-budget prompt clause"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-1d05")
	if !ok {
		t.Fatalf("no row for mg-1d05\n%s", Render(rep, true))
	}
	if row.BodiesUnread != "" {
		t.Fatalf("BodiesUnread = %q; the bodies must actually be read from git, not assumed", row.BodiesUnread)
	}
	if row.Remainder == nil {
		t.Fatalf("Remainder is nil. This is the sentence that should have become a successor "+
			"ticket and did not:\n%s", rescueBody1d05)
	}
	out := Render(rep, false)
	for _, want := range []string{
		"NAMES A REMAINDER",
		"no accompanying test",
		"internal/agent/prompt_test.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report does not contain %q — the file name is what makes this a "+
				"successor ticket rather than a worry\n%s", want, out)
		}
	}
	if !strings.Contains(row.Remedy(), "NAMES A REMAINDER") {
		t.Errorf("Remedy() = %q carries no remainder marker", row.Remedy())
	}
}

// TestAHedgeIsNotReportedAsARemainder. The scoping ruling, at the report level.
//
// Applied to the four rescue branches of 2026-08-19 an ungated detector raises
// four flags of which two are real. A list whose one actionable entry is
// 1-of-4 is this ticket's own skim-past failure, relocated one level down.
func TestAHedgeIsNotReportedAsARemainder(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p516e", "main")
	r.commitBody("fleetprogress.go", liveRescueSubject, rescueBody516e)
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
		t.Fatalf("no row for mg-516e\n%s", Render(rep, true))
	}
	if row.Remainder != nil {
		t.Errorf("Remainder = %q for a body that only hedges. `treat completeness as "+
			"unestablished` is a rescuer DECLINING TO JUDGE, not a claim that something "+
			"specific is missing; `nothing was deleted` is an author bounding a change",
			row.Remainder.RemainderNote())
	}
	if out := Render(rep, false); strings.Contains(out, "NAMES A REMAINDER") {
		t.Errorf("the report flags a remainder on a hedging body:\n%s", out)
	}
	// It is still a rescue row and still carries its own marker on the remedy.
	if !strings.Contains(row.Remedy(), "UNREVIEWED") {
		t.Errorf("Remedy() = %q; suppressing the REMAINDER flag must not suppress the "+
			"unreviewed one, which is true of this branch", row.Remedy())
	}
}

// TestUnreadBodiesAreNotACleanRow. This package's standing rule: a failed
// measurement is not a low one. Every self-declaration predicate answers "no" on
// an empty body, so without a stated gap an unreadable commit message renders
// exactly like a commit that declared nothing.
func TestUnreadBodiesAreNotACleanRow(t *testing.T) {
	rep := Report{
		ItemsScanned: 1, ItemsChecked: 1,
		Rows: []Row{{
			Item:   Item{ID: "mg-0c37", Status: "available", Repo: "/repo", Title: "a thing"},
			Branch: "polecat-p0c37", Ref: "refs/remotes/origin/polecat-p0c37", Pushed: true,
			Target: "refs/remotes/origin/main", Kind: KindStranded, Unmerged: 1,
			Subjects:     []string{"feat(x): a thing (mg-0c37)"},
			BodiesUnread: "git log --format=%b in /repo: exit status 128",
		}},
	}
	out := Render(rep, false)
	if !strings.Contains(out, "COMMIT BODIES NOT READ") {
		t.Errorf("the report is silent about an unread body:\n%s", out)
	}
	if !strings.Contains(out, "not evidence there is none") {
		t.Errorf("the report names the gap but not what it means; the reading to refuse is "+
			"`no remainder was reported, so there is none`:\n%s", out)
	}
}

// TestTheRescueRowStillWithholdsItsSubmit. mg-aed4's fix is load-bearing and
// this ticket must not spend it: the marker is added to remedies, and the one
// remedy that prints NO submit has to keep printing none.
func TestTheRescueRowStillWithholdsItsSubmit(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p1d05", "main")
	r.commitBody("prompts.go", rescueSubject1d05, rescueBody1d05)
	r.push("polecat-p1d05")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-1d05", Status: "available", Repo: r.dir, Title: "core-budget prompt clause"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if out := Render(rep, false); strings.Contains(out, "refinery submit") {
		t.Errorf("a rescue row is printing a submit line again:\n%s", out)
	}
}
