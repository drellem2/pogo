package strandedwork

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The DROP mechanism, and the second opinion that has to survive it (mg-5ec6).
//
// content.go's other fixture builds a branch that landed through a CONFLICT
// resolved by hand. That is a real way to lose a patch id, but it is not a way
// the refinery can produce: mergeBranch runs a plain `git rebase` and aborts on
// failure, and failureclass.go calls every conflict signal a non-retryable
// defect, so a conflicted branch fails its MR instead of landing.
//
// This fixture is a route the refinery CAN produce, and it was found in the
// field: polecat-a3d4 in onethird_program landed as 2919d28 and `git cherry`
// still calls its tip unmerged. Its commit deleted files and added a file that
// the target had, independently, ALREADY deleted and added by the time the
// rebase replayed it — so git dropped those hunks as no-ops, and the landed
// commit is a strict subset of what the branch wrote. Nothing conflicted. A
// clean rebase was enough.
//
// So the fixture below uses a REAL `git rebase`, not a hand-built replay: the
// claim is about what git does, and a simulated merge cannot establish it.

// dropRebasedRepo builds a repository where a branch landed through an ordinary
// clean rebase that DROPPED one of its hunks, because the target had already
// made that same change. It returns the repo with origin/main carrying the
// landed commit and origin/polecat-d40p carrying the original.
func dropRebasedRepo(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)

	// A generated file both sides will independently delete — the .pyc shape.
	r.write("generated.txt", "this file is checked in by mistake and both sides remove it\n")
	var doc []string
	for i := 0; i < 8; i++ {
		doc = append(doc, fmt.Sprintf("the original line number %02d of the shared document", i))
	}
	r.write("doc.md", strings.Join(doc, "\n")+"\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "chore: the shared document and a stray generated file")
	r.push("main")
	base := strings.TrimSpace(r.git("rev-parse", "HEAD"))

	// THE BRANCH does its real work AND removes the stray file, in one commit.
	r.branch("polecat-d40p", base)
	var work []string
	for i := 0; i < 25; i++ {
		work = append(work, fmt.Sprintf("the branch contributes this substantial line of real work, number %02d", i))
	}
	r.write("doc.md", strings.Join(doc, "\n")+"\n"+strings.Join(work, "\n")+"\n")
	r.rm("generated.txt")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "feat(doc): the branch's contribution (mg-d40p)")
	r.push("polecat-d40p")

	// MAIN removes the same stray file first, on its own.
	r.checkout("main")
	r.rm("generated.txt")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "chore: drop the stray generated file")
	r.push("main")

	// THE MERGE, exactly as the refinery does it: rebase onto the target and
	// fast-forward. No conflict — git simply has one hunk fewer to apply.
	r.checkout("polecat-d40p")
	r.git("rebase", "main")
	r.checkout("main")
	r.git("merge", "-q", "--ff-only", "polecat-d40p")
	r.push("main")
	// The branch on ORIGIN is untouched by the rebase, which is the whole point:
	// the refinery never force-pushes it. Put the local head back so the two
	// disagree the way they do in production.
	r.git("branch", "-f", "polecat-d40p", "refs/remotes/origin/polecat-d40p")
	return r
}

func (r *repo) rm(file string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.dir, file)); err != nil {
		r.t.Fatalf("rm %s: %v", file, err)
	}
}

// TestCherryIsWrongOnACleanRebaseThatDroppedAHunk is the field case reduced to a
// fixture: NO conflict, an ordinary refinery-shaped merge, and `git cherry` still
// calls the branch unmerged.
//
// It matters because it removes the last precondition. The conflict fixture next
// door needs a rebase the refinery will not perform; this one needs only that the
// target already contained part of what the branch wrote, which is the ordinary
// condition of any repo where two branches touch the same cleanup.
func TestCherryIsWrongOnACleanRebaseThatDroppedAHunk(t *testing.T) {
	r := dropRebasedRepo(t)

	// The control first: the merge really was clean and really did land. If the
	// rebase had conflicted, the fixture would have failed inside git.
	landed := r.git("log", "--format=%s", "-1", "main", "--")
	if !strings.Contains(landed, "(mg-d40p)") {
		t.Fatalf("the fixture did not land the branch on main; main's tip is %q", strings.TrimSpace(landed))
	}

	f, err := Inspect(r.dir, "polecat-d40p", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(f.Unmerged) == 0 {
		t.Fatalf("git cherry called the drop-rebased branch merged. If that is now true in general, "+
			"this whole second-opinion layer is dead weight and can go — that is a fine reason for "+
			"this test to fail. Finding: %+v", f)
	}
	if !f.Stranded() {
		t.Errorf("Stranded() = false with %d unmerged commit(s); the two must agree", len(f.Unmerged))
	}
}

// TestCorroborateFlagsTheDropRebaseWithoutClearingIt is the whole contract in one
// test: the second opinion has to SEE the landing and must not ACT on it.
//
// A wrong "already present" verdict converts `pogo refinery submit` into
// `mg done` and throws the branch away — the loss the detector exists to prevent,
// rebuilt inside its own remedy. So the assertion is two-sided, and the second
// half is the important one.
func TestCorroborateFlagsTheDropRebaseWithoutClearingIt(t *testing.T) {
	r := dropRebasedRepo(t)
	f, err := Inspect(r.dir, "polecat-d40p", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	before := f.Disposition

	p, note := Corroborate(r.dir, f)
	if !p.SuggestsLanded() {
		t.Fatalf("the content measure did not recognise a branch that is verbatim on main: %s", p.Describe())
	}
	if !strings.Contains(note, "MAY ALREADY HAVE LANDED") {
		t.Errorf("the note does not tell the reader what to suspect; got: %s", note)
	}
	if !strings.Contains(note, "does not clear the row") {
		t.Errorf("the note reads as a merge verdict. It must say in terms that it is not one, or a "+
			"reader deletes the branch on it; got: %s", note)
	}
	if f.Disposition != before || !f.Stranded() {
		t.Errorf("Corroborate changed the disposition (%s -> %s). It renders text and nothing else: "+
			"the day it can clear a finding is the day a network blip deletes a branch", before, f.Disposition)
	}
}

// TestCorroborateAgreesWhenTheWorkIsGenuinelyAbsent is the polarity control.
// Without it every assertion above is satisfied by a function that says "may
// already have landed" about everything — which would disarm all three reports it
// is attached to on the same day it shipped.
func TestCorroborateAgreesWhenTheWorkIsGenuinelyAbsent(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-9a19", "main")
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("this line of the audit exists only on the branch, number %02d", i))
	}
	r.write("audit.md", strings.Join(lines, "\n")+"\n")
	r.git("add", "audit.md")
	r.git("commit", "-q", "-m", "feat(audit): drift battery (mg-9a19)")
	r.push("polecat-9a19")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-9a19", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	_, note := Corroborate(r.dir, f)
	if strings.Contains(note, "MAY ALREADY HAVE LANDED") {
		t.Errorf("work that exists nowhere but the branch was hedged as possibly landed: %s", note)
	}
	if !strings.Contains(note, "agrees the work is absent") {
		t.Errorf("the corroborating case produced no corroboration; got: %s", note)
	}
}

// TestCorroborateIsLoudWhenItCannotMeasure. "The second opinion did not run" and
// "the second opinion found nothing" are different facts, and this whole family
// of defects is the first being rendered as the second. An unreadable repo must
// produce a sentence that says so — never silence, and never a confident 0%.
func TestCorroborateIsLoudWhenItCannotMeasure(t *testing.T) {
	f := Finding{
		Repo: "/definitely/not/a/repo", Branch: "polecat-9a19", Found: true,
		Target: "refs/remotes/origin/main", Disposition: DispositionResubmit,
		Unmerged: []Commit{{SHA: "abc123abc123", Subject: "feat: x (mg-9a19)"}},
	}
	p, note := Corroborate("/definitely/not/a/repo", f)
	if note == "" {
		t.Fatal("a failed measurement produced no note at all; the reader cannot tell it from a " +
			"measurement that was taken and came back low")
	}
	if !strings.Contains(note, "UNAVAILABLE") {
		t.Errorf("the note does not say the check failed to run; got: %s", note)
	}
	if p.SuggestsLanded() || p.Measured {
		t.Errorf("a failed measurement produced a usable Presence %+v; it must be the zero value and "+
			"must not read as a verdict", p)
	}
}

// TestCorroborateSaysNothingWithNothingToMeasure. A clean finding has no unmerged
// commits, so there is no second opinion to render and the empty string is the
// honest answer — the callers append it only when it is non-empty.
func TestCorroborateSaysNothingWithNothingToMeasure(t *testing.T) {
	if _, note := Corroborate("/repo", Finding{Disposition: DispositionClean}); note != "" {
		t.Errorf("a clean finding produced a second opinion: %s", note)
	}
}
