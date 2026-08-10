package strandedwork

import (
	"fmt"
	"strings"
	"testing"
)

// These tests build the control the mg-be37 ticket demanded and could not
// supply: "find a branch known to have merged with conflict resolution and check
// what the detector says. If `git cherry` cannot be made accurate there, a
// content-level comparison of the touched paths is the fallback."
//
// It is CONSTRUCTED rather than found, deliberately. A branch dug out of the
// live repo settles the question once and answers no question again — the
// archaeology is not re-runnable, and the branch is one gc away from being
// unavailable to the next reader. The construction below reproduces the
// situation on demand, in the gate, on every merge:
//
//	main       adds a paragraph in the middle of the file the branch also edits
//	branch     adds its own paragraph in the same region
//	rebase     conflicts, is resolved by KEEPING BOTH, and merges
//
// That is what a rebase-through-conflict is, and it is the case whose answer was
// unvalidated: both of the ticket's positive controls were clean merges.
//
// The archaeology was done as well, and is recorded where it is useful — in the
// package comment on content.go, which names five live branches and the shas
// they landed under. This file is the part that keeps working.

// conflictLandedRepo builds a repository where a branch's work is on main, put
// there by a rebase that had to resolve a conflict. It returns the branch name.
func conflictLandedRepo(t *testing.T) *repo {
	t.Helper()
	r := newRepo(t)

	// A file with enough distinct content that a hunk's context is meaningful.
	var base []string
	for i := 0; i < 12; i++ {
		base = append(base, fmt.Sprintf("the original line number %02d of the shared document", i))
	}
	r.write("doc.md", strings.Join(base, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "chore: the shared document")
	r.push("main")
	mainBase := strings.TrimSpace(r.git("rev-parse", "HEAD"))

	// The BRANCH inserts its work in the middle.
	branchLines := make([]string, len(base))
	copy(branchLines, base)
	work := []string{
		"the branch contributes this substantial first line of real work",
		"the branch contributes this substantial second line of real work",
		"the branch contributes this substantial third line of real work",
		"the branch contributes this substantial fourth line of real work",
		"the branch contributes this substantial fifth line of real work",
	}
	branchLines = append(branchLines[:6], append(append([]string{}, work...), branchLines[6:]...)...)
	r.branch("polecat-cf01", mainBase)
	r.write("doc.md", strings.Join(branchLines, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "feat(doc): the branch's contribution (mg-cf01)")
	r.push("polecat-cf01")

	// MAIN moves under it, in the same region, so a replay cannot apply cleanly.
	r.checkout("main")
	mainLines := make([]string, len(base))
	copy(mainLines, base)
	other := []string{
		"meanwhile main inserted this unrelated first line right here",
		"meanwhile main inserted this unrelated second line right here",
		"meanwhile main inserted this unrelated third line right here",
	}
	mainLines = append(mainLines[:6], append(append([]string{}, other...), mainLines[6:]...)...)
	r.write("doc.md", strings.Join(mainLines, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "feat(doc): main's unrelated change")
	r.push("main")

	// The MERGE: the branch's work is replayed onto main and the conflict is
	// resolved by keeping both sides, which is what resolving this conflict
	// means. The resulting commit is a NEW commit with a NEW patch id.
	resolved := make([]string, 0, len(mainLines)+len(work))
	resolved = append(resolved, mainLines[:9]...)
	resolved = append(resolved, work...)
	resolved = append(resolved, mainLines[9:]...)
	r.write("doc.md", strings.Join(resolved, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "feat(doc): the branch's contribution (mg-cf01)")
	r.push("main")
	return r
}

func (r *repo) write(file, content string) {
	r.t.Helper()
	run(r.t, r.dir, "sh", "-c", "cat > "+file+" <<'STRANDEDWORK_EOF'\n"+content+"STRANDEDWORK_EOF\n")
}

// TestCherryIsWrongOnAConflictRebase is the NEGATIVE control on the primary
// instrument, and it is the one the ticket said was missing. It asserts the
// blind spot rather than the absence of one: `git cherry` reports a branch that
// LANDED as unmerged, because resolving the conflict changed the patch.
//
// If this test ever fails because git started reporting the commit as
// equivalent, the fallback below is no longer load-bearing and the
// conflict_suspect row can go. That is a fine reason for a test to fail and the
// failure message says so.
func TestCherryIsWrongOnAConflictRebase(t *testing.T) {
	r := conflictLandedRepo(t)

	f, err := Inspect(r.dir, "polecat-cf01", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(f.Unmerged) == 0 {
		t.Fatalf("git cherry called the conflict-rebased branch merged. If that is now true in\n"+
			"general, the content-level fallback in content.go is dead weight and the\n"+
			"conflict_suspect row should be removed. Finding: %+v", f)
	}
	if !f.Stranded() {
		t.Errorf("Stranded() = false with %d unmerged commit(s); the two must agree", len(f.Unmerged))
	}
}

// TestPresenceCatchesTheConflictRebase is the POSITIVE control on the fallback:
// on the same branch, with the same repository, the content-level instrument
// gets the right answer.
func TestPresenceCatchesTheConflictRebase(t *testing.T) {
	r := conflictLandedRepo(t)
	f, err := Inspect(r.dir, "polecat-cf01", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	p, err := MeasurePresence(r.dir, f)
	if err != nil {
		t.Fatalf("MeasurePresence: %v", err)
	}
	if p.Present != p.Added || p.Added == 0 {
		t.Fatalf("Present/Added = %d/%d, want every added line found: the branch's five lines\n"+
			"are verbatim on main and the resolution kept them. %s", p.Present, p.Added, p.Describe())
	}
	// The fixture is smaller than ContentMinAdded on purpose — it is a fixture,
	// not a repository — so the confidence gate is exercised separately below.
	// Here the RATIO is the claim.
	if p.Ratio() < ContentLandedRatio {
		t.Errorf("Ratio = %.2f, want >= %.2f", p.Ratio(), ContentLandedRatio)
	}
}

// TestPresenceIsLowOnGenuinelyStrandedWork is the other polarity, and without it
// every test above passes for a measure that returns "already present" for
// everything. This is the bug the mayor named: "a predicate that can never
// return LANDED passes every test that only feeds it unmerged branches" — the
// same trap pointing the other way.
func TestPresenceIsLowOnGenuinelyStrandedWork(t *testing.T) {
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
	p, err := MeasurePresence(r.dir, f)
	if err != nil {
		t.Fatalf("MeasurePresence: %v", err)
	}
	if !p.Measured {
		t.Fatalf("Measured = false with %d added lines; the fixture must clear ContentMinAdded (%d) "+
			"or this control proves nothing", p.Added, ContentMinAdded)
	}
	if p.SuggestsLanded() {
		t.Errorf("SuggestsLanded() = true for work that exists nowhere but the branch: %s", p.Describe())
	}
	if p.Present != 0 {
		t.Errorf("Present = %d, want 0: main has no copy of audit.md at all", p.Present)
	}
}

// TestPresenceRefusesToJudgeATinyBranch pins the confidence gate. Three of the
// live branches in the 2026-08-10 population add three, four and five countable
// lines; a 3/3 hit on those is one coincidence away from a verdict that would
// tell a reader to close an unmerged item.
func TestPresenceRefusesToJudgeATinyBranch(t *testing.T) {
	p := Presence{Added: 3, Present: 3}
	if p.SuggestsLanded() {
		t.Errorf("a 3/3 branch was called landed; ContentMinAdded (%d) is not being applied", ContentMinAdded)
	}
	if !strings.Contains(p.Describe(), "not conclusive") {
		t.Errorf("Describe() = %q, want it to say the measurement was inconclusive rather than "+
			"printing a confident ratio", p.Describe())
	}
	measured := Presence{Added: ContentMinAdded, Present: ContentMinAdded, Measured: true}
	if !measured.SuggestsLanded() {
		t.Errorf("a fully-present branch at the minimum size was NOT called landed; the gate is " +
			"now suppressing the finding it was scoped to qualify")
	}
}

// TestPresenceIgnoresShortLines proves the line filter does something. Without
// it the haystack of any Go repository contains every brace and short return in
// any branch, and the measure is a constant near 1 — which would silence the
// detector by converting every stranded row into a conflict suspect.
func TestPresenceIgnoresShortLines(t *testing.T) {
	r := newRepo(t)
	r.write("main.go", "package main\n\nfunc main() {\n}\n")
	r.git("add", "main.go")
	r.git("commit", "-q", "-m", "chore: skeleton")
	r.push("main")

	r.branch("polecat-aa11", "main")
	r.write("main.go", "package main\n\nfunc main() {\n}\n\nfunc helper() {\n}\n")
	r.git("add", "main.go")
	r.git("commit", "-q", "-m", "feat: helper (mg-aa11)")
	r.push("polecat-aa11")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-aa11", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	p, err := MeasurePresence(r.dir, f)
	if err != nil {
		t.Fatalf("MeasurePresence: %v", err)
	}
	// "}" and "" are below ContentMinLine; only "func helper() {" clears it, and
	// main does not have it.
	if p.Added != 1 {
		t.Errorf("Added = %d, want 1 (only `func helper() {` is long enough to count); "+
			"short lines are being counted and will make every branch look landed", p.Added)
	}
	if p.Present != 0 {
		t.Errorf("Present = %d, want 0", p.Present)
	}
}
