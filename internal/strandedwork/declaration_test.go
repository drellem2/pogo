package strandedwork

import (
	"strings"
	"testing"
)

// The mg-0c37 corpus. These are the four RESCUE commits of 2026-08-19 —
// transcribed from `git log` on this repository, subjects verbatim and bodies
// cut to the sentences the detector has to rule on. They are the whole reason
// there are two predicates in declaration.go rather than one, so they are pinned
// here rather than paraphrased into synthetic prose that would agree with
// whatever the regex happens to do.
const (
	subject516e = "RESCUE(mg-516e): fleet-progress detector recovered from preserved worktree " +
		"p516e — UNREVIEWED, not this committer's work (mg-51bf)"
	body516e = `Committed by p51bf under mg-51bf purely to convert an invisible and
unrecoverable state into a visible and recoverable one.

What is here: a new internal/progresswatch package (watcher + detector, ~2000
lines with tests), ` + "`pogo check-progress`" + `, a pogod heartbeat watch, config
plumbing, changelog and docs. It looks substantially complete but has NOT been
verified to build; treat completeness as unestablished.

DELIBERATELY EXCLUDED: the stray ./pogod Mach-O binary left in the tree root by
a ` + "`go build -o pogod`" + `. That is regenerable output, not authored content, and it
remains untracked in the worktree — nothing was deleted.`

	subject1d05 = "RESCUE(mg-1d05): core-budget prompt clause covering self-parallelising " +
		"libraries, recovered from preserved worktree p1d05 — UNREVIEWED, not this " +
		"committer's work (mg-51bf)"
	body1d05 = `NOTE FOR A LATER READER: the six diffs are byte-identical, which looks
mechanical. It is not generated.

Looks PARTIAL: the change is prose only, with no accompanying test, and the
repo's internal/agent/prompt_test.go does assert over the shipped template
corpus. Nothing was added there.`

	subjectFbaf = "RESCUE(mg-fbaf): `pogo agent env` + injected-env catalogue recovered from " +
		"preserved worktree pfbaf — UNREVIEWED, not this committer's work (mg-51bf)"
	bodyFbaf = `It reads as substantially complete but has NOT been verified to build; treat
completeness as unestablished. No changelog entry is present, which the repo's
convention would normally expect.`

	subjectE7ff = "RESCUE(mg-e7ff): pogo-side depends dispatch gate recovered from preserved " +
		"worktree pe7ff — UNREVIEWED, not this committer's work (mg-51bf)"
	bodyE7ff = `What is here (~209 added lines plus a new test file):
internal/agent/dispatchdepends_test.go, plus docs/CONFIGURATION.md and a
mayor.md paragraph, and a changelog entry.

Looks complete as a change, but has NOT been verified to build.`
)

// TestTheFourRescueBodiesSplitTwoAndTwo is the scoping ruling, executable.
//
// The ticket as first written asked for a flag whenever a body carries a
// self-flag. Applied to these four that raises four flags, and the mayor's
// sharper formulation is why that is wrong: "'completeness unestablished' is a
// rescuer DECLINING TO JUDGE, not a declaration that something specific is
// missing. Those read the same in prose and are different facts."
//
// A detector at 4-of-4 relocates this ticket's own skim-past failure one level
// down. So the assertion is on the SPLIT and not on the hits: two must fire and
// two must not, and the two that must not are the ones whose prose is the most
// alarming to read.
func TestTheFourRescueBodiesSplitTwoAndTwo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		subject string
		body    string
		want    bool
		why     string
	}{
		{"p1d05 names a missing test", subject1d05, body1d05, true,
			"`no accompanying test` names an artifact somebody can go and look for; " +
				"this is the commit that merged as 4dd1b9d with no successor filed"},
		{"pfbaf names a missing changelog entry", subjectFbaf, bodyFbaf, true,
			"`No changelog entry is present` names an artifact; mg-f34e is the successor " +
				"ticket a human filed by hand for exactly this remainder"},
		{"p516e only hedges", subject516e, body516e, false,
			"`treat completeness as unestablished` names nothing. Nor does `nothing was " +
				"deleted`, which is an author BOUNDING a change and would be the easiest " +
				"false positive in the corpus"},
		{"pe7ff only hedges", subjectE7ff, bodyE7ff, false,
			"`has NOT been verified to build` is the rescuer declining to judge; the body " +
				"even lists `a changelog entry` as present, which a negator-free noun match " +
				"would have tripped on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Commit{SHA: "0123456789abcdef", Subject: tc.subject, Body: tc.body}
			got := c.DeclaresRemainder()
			if got != tc.want {
				t.Fatalf("DeclaresRemainder() = %v, want %v — %s\nnote: %q",
					got, tc.want, tc.why, c.RemainderNote())
			}
		})
	}
}

// TestRemainderNoteCarriesTheNAMED ARTIFACT, not just the fact that one exists.
//
// A bare "this commit declares a remainder" is a flag; the file name is what
// turns it into a successor ticket. Commit bodies are hard-wrapped, so the
// sentence spans three physical lines and a line-based extract would cut it
// before the name.
func TestRemainderNoteCarriesTheNamedArtifact(t *testing.T) {
	c := Commit{Subject: subject1d05, Body: body1d05}
	note := c.RemainderNote()
	for _, want := range []string{"no accompanying test", "internal/agent/prompt_test.go"} {
		if !strings.Contains(note, want) {
			t.Errorf("RemainderNote() = %q\ndoes not contain %q — without it a reader still has to "+
				"open the commit to find out what is missing, which is the round-trip this exists to save",
				note, want)
		}
	}
	if strings.Contains(note, "\n") {
		t.Errorf("RemainderNote() = %q contains a newline; the caller wraps it and a raw hard "+
			"wrap inside a wrapped line renders as ragged nonsense", note)
	}
}

// TestScopeStatementsFromOrdinaryCommitsAreNotRemainders.
//
// These sentences are verbatim from ordinary reviewed commits on this
// repository's main, and every one of them matches remainderRe. They are why the
// predicate is gated on the commit declaring ITSELF unreviewed: measured over the
// last 400 commits the regex alone fires 31 times and on the self-declared
// population it fires twice.
//
// The distinction the gate stands in for: these negate a CHANGE — an author
// bounding what they did — and a remainder negates an ARTIFACT THAT SHOULD
// EXIST. Prose does not separate them reliably, so the population does.
func TestScopeStatementsFromOrdinaryCommitsAreNotRemainders(t *testing.T) {
	for _, body := range []string{
		"No production code changed, no assertion was weakened, deleted, or made to retry.",
		"Comments only — no behaviour, no test changes.",
		"No assertion was relaxed.",
		"No prose change, no docs change.",
		"Nothing was added to any agent prompt — the explicitly rejected option.",
	} {
		c := Commit{Subject: "fix(x): an ordinary reviewed change (mg-1234)", Body: body}
		if c.DeclaresRemainder() {
			t.Errorf("DeclaresRemainder() = true for an ordinary commit whose body says %q; "+
				"a report that flags 31-in-400 is the skim-past failure of mg-0c37 relocated", body)
		}
		if !remainderRe.MatchString(body) {
			t.Errorf("remainderRe no longer matches %q — this case is only interesting because "+
				"the regex DOES match it and the population gate is what suppresses it", body)
		}
	}
}

// TestUnreviewedIsReadFromTheSubjectAndNotTheBody.
//
// Three ordinary reviewed commits on main mention UNREVIEWED in their bodies
// because they are ABOUT a rescue — 2c6c47d rebases one, 53e0b57 and 78e7ab2
// quote one. A subject is where an author declares something about the commit
// they are making; a body is also where they write about everything else.
func TestUnreviewedIsReadFromTheSubjectAndNotTheBody(t *testing.T) {
	discussing := Commit{
		Subject: "feat,fix,test,docs(pogod,agent,dispatch): rebase the mg-9d4e rescue onto main (mg-9d4e)",
		Body: "The rescue commit it replays was committed UNREVIEWED, not this committer's " +
			"work, and the gate caught two defects it was never built to find.",
	}
	if discussing.DeclaresUnreviewed() {
		t.Error("DeclaresUnreviewed() = true for a reviewed commit that merely DISCUSSES a rescue; " +
			"the row would carry a marker about somebody else's commit")
	}
	declaring := Commit{Subject: subject516e}
	if !declaring.DeclaresUnreviewed() {
		t.Error("DeclaresUnreviewed() = false for the live rescue subject — this is the exact " +
			"string that was clipped to `— UNREVI…` and the whole ticket is about it")
	}
}

// TestDeclarationIndexPointsAtTheMarkerAndNotTheDash. The renderer cuts a subject
// from this offset, so an index off by a word puts the elision inside the marker
// and re-creates `UNREVI…` under a different name.
func TestDeclarationIndexPointsAtTheMarker(t *testing.T) {
	at := DeclarationIndex(subject516e)
	if at < 0 {
		t.Fatalf("DeclarationIndex(%q) = -1", subject516e)
	}
	if got := subject516e[at:]; !strings.HasPrefix(got, "UNREVIEWED") {
		t.Errorf("subject[%d:] = %q, want it to start at UNREVIEWED", at, got)
	}
	if DeclarationIndex("fix(x): an ordinary subject (mg-1234)") != -1 {
		t.Error("DeclarationIndex found a marker in an ordinary subject")
	}
}

// TestARescueWithNoUnreviewedTokenStillReachesTheRemainderCheck.
//
// 27 of the 32 rescue commits in the fleet's repositories use the mg-11fa
// spelling, which carries no UNREVIEWED token at all. Gating the body check on
// the token alone would have covered 5 of 32 and missed the larger event.
func TestARescueWithNoUnreviewedTokenStillReachesTheRemainderCheck(t *testing.T) {
	c := Commit{
		Subject: "RESCUE(p6b2d): 2 uncommitted path(s) from a retained worktree (mg-11fa)",
		Body:    "Recovered verbatim. No changelog entry is present.",
	}
	if c.DeclaresUnreviewed() {
		t.Fatal("this spelling carries no UNREVIEWED token; if it does now, the fixture is wrong")
	}
	if !c.DeclaresRemainder() {
		t.Error("DeclaresRemainder() = false — IsRescue is the other half of the population gate " +
			"and without it 27 of the 32 known rescue commits are never body-checked")
	}
}
