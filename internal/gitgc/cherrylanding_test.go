package gitgc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The measurements behind docs/pm-open-pr-pass.md §"What `git cherry` does not
// cover", pinned against the shipped predicate rather than against prose.
//
// `cherryAhead` counts `git cherry`'s `+` lines, and BranchDurable reads a
// non-zero count as DurabilityLocalOnly — "these commits exist only here". The
// PM open-PR pass reads the same `+` and used to call it "not landed". Both
// readings are stronger than what git actually says: a `+` means no commit
// upstream carries this exact PATCH, and a patch-id hashes the diff's context
// lines as well as its changed ones. The four tests below are the cases where
// that gap opens, measured 2026-08-19 (mg-724c, drellem2/pogo#149).
//
// They assert the CURRENT behaviour of git, not a repair: nothing here is a bug
// in `cherryAhead`, which reports what `git cherry` reports. What they defend is
// the doc — a claim about a measurement is worth nothing once nobody can re-run
// the measurement, and this section's predecessor said "not measured" for two
// weeks while stating the predicate as if it were symmetric.

// writeLines writes file as one line per element, newline-terminated.
func (r *testRepo) writeLines(file string, lines ...string) {
	r.t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(r.dir, file), []byte(body), 0644); err != nil {
		r.t.Fatal(err)
	}
}

// commitAll stages everything and commits it with subject.
func (r *testRepo) commitAll(subject string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", subject)
}

// ahead is cherryAhead against main, fatal on error. The error path is its own
// concern (an empty cherry output means both "nothing ahead" and "git refused"),
// and these tests are about the answers git DOES give.
func (r *testRepo) ahead(head string) int {
	r.t.Helper()
	n, err := cherryAhead(r.dir, r.rev("main"), r.rev(head))
	if err != nil {
		r.t.Fatalf("cherryAhead(%s): %v", head, err)
	}
	return n
}

// tryGit runs git and returns its output and error rather than failing the
// test, for the checks whose FAILURE is the thing being asserted.
func (r *testRepo) tryGit(args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	out, err := cmd.Output()
	return string(out), err
}

// fileOnMain returns main's copy of path, or "" when main does not have it.
func (r *testRepo) fileOnMain(path string) string {
	r.t.Helper()
	out, err := r.tryGit("show", "main:"+path)
	if err != nil {
		return ""
	}
	return out
}

// TestCherry_SquashMergedMultiCommitBranchReadsAhead. The case the repo owner
// reported. A squash rewrites N commits into ONE new patch, so not one of the
// originals has a patch-equivalent upstream — every commit reads `+` while the
// content is byte-identical on main.
func TestCherry_SquashMergedMultiCommitBranchReadsAhead(t *testing.T) {
	r := newTestRepo(t)
	r.writeLines("f.txt", "l1", "l2", "l3", "l4", "l5")
	r.commitAll("base")
	r.git("checkout", "-q", "-b", "feat")
	r.writeLines("f.txt", "l1", "L2", "l3", "l4", "l5")
	r.commitAll("first of two")
	r.writeLines("f.txt", "l1", "L2", "l3", "L4", "l5")
	r.commitAll("second of two")
	r.git("checkout", "-q", "main")
	r.git("merge", "-q", "--squash", "feat")
	r.commitAll("squash: feat (#1)")

	// The predicate's answer.
	if n := r.ahead("feat"); n != 2 {
		t.Fatalf("cherryAhead = %d, want 2 (both commits read `+` after a squash)", n)
	}
	// The ground truth it disagrees with, established WITHOUT the predicate:
	// main's copy of the file and the branch's are the same bytes.
	onMain := r.fileOnMain("f.txt")
	onFeat := r.git("show", "feat:f.txt")
	if onMain != onFeat || onMain == "" {
		t.Fatalf("fixture is not the case under test: main has %q, feat has %q", onMain, onFeat)
	}
	// And the verdict that count produces, which is the operative half.
	if v, d := BranchDurable(r.dir, "feat", "main"); v != DurabilityLocalOnly {
		t.Fatalf("verdict = %s (%s); the point of this test is that it is %s "+
			"for content that IS on main", v, d, DurabilityLocalOnly)
	}
}

// TestCherry_SingleCommitFoldedIntoLargerSquashReadsAhead. It is patch-id
// identity, not commit count: one commit, folded into a squash that also carries
// somebody else's edit, reads `+` too. This is why a `+` on a single-commit PR
// is not by itself evidence that the predicate is misbehaving.
func TestCherry_SingleCommitFoldedIntoLargerSquashReadsAhead(t *testing.T) {
	r := newTestRepo(t)
	r.writeLines("f.txt", "l1", "l2", "l3", "l4", "l5")
	r.writeLines("g.txt", "other")
	r.commitAll("base")
	r.git("checkout", "-q", "-b", "feat")
	r.writeLines("f.txt", "l1", "L2", "l3", "l4", "l5")
	r.commitAll("the one commit")
	r.git("checkout", "-q", "main")
	// One commit on main carrying feat's change AND an unrelated one.
	r.writeLines("f.txt", "l1", "L2", "l3", "l4", "l5")
	r.writeLines("g.txt", "other-changed")
	r.commitAll("squash: a bigger change that includes feat (#2)")

	if n := r.ahead("feat"); n != 1 {
		t.Fatalf("cherryAhead = %d, want 1 (a single commit folded into a larger squash reads `+`)", n)
	}
	if onMain, onFeat := r.fileOnMain("f.txt"), r.git("show", "feat:f.txt"); onMain != onFeat {
		t.Fatalf("fixture is not the case under test: f.txt differs (%q vs %q)", onMain, onFeat)
	}
}

// TestCherry_ContextWindowDecidesTheAnswer. The sharpest of the four, and the
// one that says this is not confined to squash-merge repos: the SAME commit,
// squash-merged the SAME way, reads `+` or `-` depending only on where main's
// other edit fell. Inside the hunk's three-line context window it is `+`,
// because a patch-id normalises line numbers away and then hashes the context
// lines. Ordinary concurrent development in one file is enough.
//
// The two controls are what make this about the context window rather than
// about "someone else touched the file".
func TestCherry_ContextWindowDecidesTheAnswer(t *testing.T) {
	// The PR always changes l8 of a ten-line file, so its hunk's context is
	// l5..l7 and l9..l10.
	base := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10"}
	pr := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8-CHANGED-BY-PR", "l9", "l10"}

	// mainEdit rewrites one element of a copy of base.
	mainEdit := func(idx int, to string) []string {
		out := append([]string(nil), base...)
		out[idx] = to
		return out
	}

	cases := []struct {
		name      string
		where     string
		mainFile  []string // main's f.txt before the squash; nil = untouched
		mainOther string   // a different file main edits instead; "" = none
		wantAhead int
	}{
		{
			name:      "inside the PR hunk's 3-line context window",
			where:     "l6",
			mainFile:  mainEdit(5, "l6-EDITED-ON-MAIN"),
			wantAhead: 1,
		},
		{
			name:      "same file, outside the window",
			where:     "l1",
			mainFile:  mainEdit(0, "l1-EDITED-ON-MAIN"),
			wantAhead: 0,
		},
		{
			name:      "a different file",
			where:     "g.txt",
			mainOther: "other-EDITED-ON-MAIN",
			wantAhead: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepo(t)
			r.writeLines("f.txt", base...)
			r.writeLines("g.txt", "other")
			r.commitAll("base")
			r.git("checkout", "-q", "-b", "feat")
			r.writeLines("f.txt", pr...)
			r.commitAll("PR: change l8")
			r.git("checkout", "-q", "main")

			// main's own edit lands FIRST, so the squash's recorded diff is
			// computed against it. That is the whole mechanism: the squash
			// carries different context lines than the PR commit did.
			if tc.mainFile != nil {
				r.writeLines("f.txt", tc.mainFile...)
			} else {
				r.writeLines("g.txt", tc.mainOther)
			}
			r.commitAll("main: unrelated edit at " + tc.where)
			r.git("merge", "-q", "--squash", "feat")
			r.commitAll("squash: the PR (#3)")

			// Ground truth, established without the predicate: the PR's line
			// is on main in every one of the three arms.
			if !strings.Contains(r.fileOnMain("f.txt"), "l8-CHANGED-BY-PR") {
				t.Fatalf("fixture is not the case under test: the PR's line is not on main")
			}
			if n := r.ahead("feat"); n != tc.wantAhead {
				t.Fatalf("cherryAhead = %d, want %d — the same landed commit, "+
					"differing only in where main's other edit fell (%s)", n, tc.wantAhead, tc.where)
			}
		})
	}
}

// TestCherry_ContextWindowBitesTheRebaseMergePathToo. The squash cases above
// invite the reading "so this is a squash-merge problem" — the reading the
// section this test defends used to give, when it said every measurement was on
// a clean rebase-and-merge and that this is the refinery's only merge path. It
// is not a squash-merge problem. Rebase the branch onto a main that has moved
// inside the hunk's context window and the landed patch is rewritten with the
// new context, so the ORIGINAL PR head — the ref a reviewer or the open-PR pass
// actually holds — reads `+`.
//
// The control is the same rebase-and-merge with main's edit in another file.
func TestCherry_ContextWindowBitesTheRebaseMergePathToo(t *testing.T) {
	base := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10"}
	pr := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8-CHANGED-BY-PR", "l9", "l10"}
	inWindow := []string{"l1", "l2", "l3", "l4", "l5", "l6-EDITED-ON-MAIN", "l7", "l8", "l9", "l10"}

	for _, tc := range []struct {
		name      string
		mainFile  []string // main's f.txt before the rebase; nil = touch g.txt instead
		wantAhead int
	}{
		{"neighbour inside the hunk's context window", inWindow, 1},
		{"neighbour in a different file", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepo(t)
			r.writeLines("f.txt", base...)
			r.writeLines("g.txt", "other")
			r.commitAll("base")
			r.git("checkout", "-q", "-b", "feat")
			r.writeLines("f.txt", pr...)
			r.commitAll("PR: change l8")
			prHead := r.rev("feat") // what the PR points at, and never moves
			r.git("checkout", "-q", "main")
			if tc.mainFile != nil {
				r.writeLines("f.txt", tc.mainFile...)
			} else {
				r.writeLines("g.txt", "other-EDITED-ON-MAIN")
			}
			r.commitAll("main: unrelated edit")

			// The refinery's path exactly: rebase the branch onto the target,
			// then fast-forward. No conflict — the edits are four lines apart.
			r.git("checkout", "-q", "feat")
			r.git("rebase", "main")
			r.git("checkout", "-q", "main")
			r.git("merge", "--ff-only", "feat")

			if !strings.Contains(r.fileOnMain("f.txt"), "l8-CHANGED-BY-PR") {
				t.Fatalf("fixture is not the case under test: the PR's line is not on main")
			}
			n, err := cherryAhead(r.dir, r.rev("main"), prHead)
			if err != nil {
				t.Fatalf("cherryAhead: %v", err)
			}
			if n != tc.wantAhead {
				t.Fatalf("cherryAhead against the original PR head = %d, want %d", n, tc.wantAhead)
			}
		})
	}
}

// TestCherry_RevertedPatchStillReadsLanded. The `-` direction's one measured
// failure, and the only one of the four that runs the UNSAFE way: `git cherry`
// does not net a revert against what it undid, so the patch is still upstream
// and the commit reads `-` LANDED with the content gone from main.
//
// The doc's prescribed mitigation is asserted here too, because it is the check
// a reader would rely on and it does not work: the revert commit's own subject
// contains the original subject, so grepping `git log main` for it matches.
func TestCherry_RevertedPatchStillReadsLanded(t *testing.T) {
	r := newTestRepo(t)
	r.git("checkout", "-q", "-b", "feat")
	r.writeLines("feature.txt", "the feature")
	r.commitAll("add the feature")
	r.git("checkout", "-q", "main")
	// Something else lands first so the cherry-pick gets a different SHA —
	// otherwise git rebuilds a byte-identical commit and feat is an ancestor,
	// which is a different (and answerable) question.
	r.writeLines("u.txt", "unrelated")
	r.commitAll("unrelated work on main")
	r.git("cherry-pick", "feat")
	r.git("revert", "--no-edit", "HEAD")

	if n := r.ahead("feat"); n != 0 {
		t.Fatalf("cherryAhead = %d, want 0 — a reverted patch still reads `-` LANDED", n)
	}
	if got := r.fileOnMain("feature.txt"); got != "" {
		t.Fatalf("fixture is not the case under test: main still has feature.txt (%q)", got)
	}
	// Not an ancestor either, so ancestry cannot rescue the reading.
	if _, err := r.tryGit("merge-base", "--is-ancestor", r.rev("feat"), r.rev("main")); err == nil {
		t.Fatalf("fixture is not the case under test: feat is an ancestor of main")
	}
	// The mitigation the doc used to prescribe, shown failing.
	if log := r.git("log", "--oneline", "main"); !strings.Contains(log, "add the feature") {
		t.Fatalf("expected the subject-line check to PASS here (that is the defect), log:\n%s", log)
	}
	// And the verdict, which is the operative half: safe to delete the branch.
	if v, d := BranchDurable(r.dir, "feat", "main"); v != DurabilityDurable {
		t.Fatalf("verdict = %s (%s), want %s — this is the reading that authorises "+
			"a deletion for content that is NOT on main", v, d, DurabilityDurable)
	}
}

// TestCherryLandingCasesAreDocumented. The tests above are the measurement; the
// doc is where anyone acting on a `+` actually reads. Neither is worth much
// without the other, and the failure this whole change repairs was exactly a doc
// that had drifted from what the command does.
func TestCherryLandingCasesAreDocumented(t *testing.T) {
	// Fatal rather than Skip, which is where the event-log.md guards elsewhere
	// in the tree land. Those catalog an enum against a doc that is not going
	// anywhere; this one exists to catch drift, and a rename or a deletion is
	// the maximal drift. A guard that goes green on it has the failure mode it
	// was written to detect.
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "pm-open-pr-pass.md"))
	if err != nil {
		t.Fatalf("pm-open-pr-pass.md is unreadable — this guard's subject is gone: %v", err)
	}
	text := string(doc)
	for _, want := range []string{
		// The reframing: `+` is inconclusive, not negative.
		"could not establish that this commit landed",
		// The three `+` cases.
		"A squash merge rewrites N commits into one new patch",
		"patch-id identity, not commit count",
		"three-line context window",
		// The `-` case, and that the subject-line check misses it.
		"**reverted** still reads `-` LANDED",
		"does not catch it",
		// The propagation gap this change cannot close. Keyed on a string
		// unique to that subsection: the doc has named ~/.pogo/agents/pm/
		// since it shipped, so the path alone would pass vacuously.
		"no change to this file can correct them",
		// The qualification on the refinery's own path, in the paragraph a
		// reader acts on rather than only in the exposure section below it.
		// TestCherry_ContextWindowBitesTheRebaseMergePathToo is the
		// measurement; this is the sentence it licenses.
		"only when `main` has\nnot moved inside the hunk's three-line context window",
		// Row 4 of the disposition table closes a PR and is reachable from a
		// `+`, so the table's safety claim has to name it.
		"**Row 4 is not**",
		// The test file itself, so a reader can re-run the measurement.
		"internal/gitgc/cherrylanding_test.go",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("docs/pm-open-pr-pass.md no longer states %q", want)
		}
	}
	// The retracted claim must be gone, not merely contradicted somewhere else
	// in the file — a doc that says both is worse than one that says the wrong
	// thing once.
	for _, gone := range []string{
		"**Any `+` means not landed.**",
		// The flat reassurance about the refinery path. Measured false by
		// TestCherry_ContextWindowBitesTheRebaseMergePathToo, and it lived in
		// the operative paragraph while the retraction lived two sections
		// down — exactly the "says both" shape this loop rejects.
		"reports all\n`-`, because rebasing preserves patch-ids",
		// The disposition table's safety claim before it accounted for row 4.
		"none of them closes or merges anything",
		"`git cherry` cannot report\n  `landed` for content that is not on `main`",
		"**Not measured** — no squash-merged PR was available to test against.",
	} {
		if strings.Contains(text, gone) {
			t.Errorf("docs/pm-open-pr-pass.md still carries the retracted claim %q", gone)
		}
	}
}
