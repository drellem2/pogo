package strandedwork

import (
	"regexp"
	"strings"
)

// This file reads what a commit SAYS ABOUT ITSELF, and it exists because the
// report built on top of this package recommended an action while hiding the
// sentence that would have changed it (mg-0c37).
//
// THE MEASURED FAILURE. `pogo check-stranded` printed, for polecat-p516e:
//
//	  1 unmerged commit(s); 77 of 2242 added line(s) already in the target (3%).
//	    RESCUE(mg-516e): fleet-progress detector recovered from preserved worktree p516e — UNREVI…
//	  -> pogo refinery submit polecat-p516e --repo=… --author=mg-516e
//
// The full subject ends `— UNREVIEWED, not this committer's work (mg-51bf)`. The
// 92-column clip landed mid-word on the one token that should stop a reader, and
// `UNREVI…` is legible to somebody looking for it and invisible to somebody
// scanning a list. The remedy under it was byte-for-byte the shape printed for
// ordinary stranded work. Six branches were submitted in one batch on that
// recommendation without a commit message being read; 4dd1b9d merged an
// UNREVIEWED prompt change across all six polecat templates, and its item was
// closed with no successor.
//
// THE ACTIONABLE HALF WAS NEVER PRINTED AT ALL. It was in the commit BODY, which
// nothing in this package read. 4dd1b9d's body says:
//
//	Looks PARTIAL: the change is prose only, with no accompanying test, and the
//	repo's internal/agent/prompt_test.go does assert over the shipped template
//	corpus. Nothing was added there.
//
// That names a specific artifact that a successor ticket has to cover. Two of
// the four rescue branches of that night were refused by the refinery gate — one
// on a failing test, one on a rebase conflict — so the gate catches some of
// this. It cannot catch this shape: the build ran and passed correctly, because
// a missing test is exactly what a passing gate cannot see.
//
// A HEDGE IS NOT A DECLARATION, AND THE DETECTOR SPLITS THEM. This is the
// scoping the ticket was refined with, and it is the reason there are two
// predicates below rather than one. Measured over the four rescue commits of
// 2026-08-19:
//
//	4dd1b9d  "no accompanying test … Nothing was added there."        REMAINDER
//	1c91003  "No changelog entry is present, which the repo's
//	          convention would normally expect."                      REMAINDER
//	112baef  "has NOT been verified to build; treat completeness
//	          as unestablished"                                       hedge
//	55b3de7  "Looks complete as a change, but has NOT been
//	          verified to build."                                     hedge
//
// A rescuer writing "completeness unestablished" is DECLINING TO JUDGE; a
// rescuer writing "no accompanying test" is naming something specific that is
// missing. Those read alike in prose and are different facts. A detector that
// fired on the hedge too would raise four flags of which two are real, which is
// the same skim-past failure this file is about, relocated one level down. So
// Remainder requires a NEGATOR ADJACENT TO A NAMED ARTIFACT and never matches
// confidence-hedging language on its own.
//
// (The ticket's own scoping note put 1c91003/pfbaf in the hedge column. Its body
// names a changelog entry, and mg-f34e is the successor ticket somebody filed by
// hand for exactly that remainder — so firing on it is the detector agreeing
// with the record, not over-firing. That correction is this file's, from reading
// the four bodies; the 2-of-4 split above is measured, the ticket's 1-of-4 was
// not.)
//
// WHAT THIS IS NOT. It is not the `declares-remainder` tag (mg-9d4e). That tag
// acts at CLOSE, after the merge; this acts at SUBMIT, before it. The mg-1d05
// failure contained both steps and fixing only the close still merges the
// partial. Nor is prose parsing a substitute for a commit trailer somebody could
// set deliberately — it is what can be done about the commits that already
// exist, all of which declare in prose that no tool parses.

// declaredUnreviewedRe matches an explicit declaration that nobody has reviewed
// this commit's content.
//
// THE TOKENS ARE EXPLICIT AND FEW, on the opposite rule from Remainder's. A
// false positive here costs a reader one `git log -1`; the tokens below are ones
// an author only writes on purpose, so the match is kept literal rather than
// inferred from tone. "not this committer's work" is included because it is the
// other half of the live spelling and carries the same fact — it says the person
// who made the commit is not the person who wrote the code.
var declaredUnreviewedRe = regexp.MustCompile(`(?i)\b(?:un-?reviewed|not[ \t]+reviewed|no[ \t]+review|not[ \t]+this[ \t]+committer'?s[ \t]+work)\b`)

// remainderRe matches a claim that a SPECIFIC NAMED ARTIFACT is absent.
//
// Every alternative below pairs a negator with a noun somebody could go and
// look for. That pairing is the whole discrimination: "no accompanying test"
// names a test, "treat completeness as unestablished" names nothing, and only
// the first is a fact a successor ticket can be written from.
//
// The verb list in the `nothing … was <verb>` and `<be> not <verb>` branches is
// deliberately restricted to verbs of ADDITION. "nothing was deleted" and
// "nothing was removed" are reassurances an author writes to bound a change, and
// 112baef contains the first of them verbatim — matching them would convert this
// detector's one clean negative into a false positive.
var remainderRe = regexp.MustCompile(`(?i)\b(?:` +
	// "no accompanying test", "No changelog entry is present", "without a benchmark"
	`(?:no|without|missing|lacks|lacking)[ \t]+(?:[\w./-]+[ \t]+){0,3}` +
	`(?:tests?|test[ \t]+files?|changelog(?:[ \t]+entry)?|changelog\.d|docs?|documentation|` +
	`fixtures?|benchmarks?|migrations?|assertions?|test[ \t]+coverage)\b` +
	// "Nothing was added there."
	`|nothing[ \t]+(?:has[ \t]+been|have[ \t]+been|was|were|is|are)[ \t]+(?:added|written|updated|covered)\b` +
	// "the test was not added", "docs are not included"
	`|(?:was|were|is|are)[ \t]+not[ \t]+(?:added|written|updated|included|present|covered)\b` +
	`)`)

// DeclarationIndex returns the byte offset in s at which a self-declaration of
// non-review begins, or -1 when s carries none.
//
// IT EXISTS FOR THE RENDERER'S CLIP, not for classification. `— UNREVI…` is the
// worst available outcome for a marker: it carries the fact and defeats it. A
// caller that must shorten a subject uses this to keep the marker rather than
// the bytes that happen to come first.
func DeclarationIndex(s string) int {
	loc := declaredUnreviewedRe.FindStringIndex(s)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// DeclaresUnreviewed reports whether this commit's SUBJECT says its content has
// not been reviewed.
//
// THE SUBJECT AND NOT THE BODY, MEASURED. Matching the body as well was the
// first draft, and over the last 400 commits of main it fires 8 times where the
// subject fires 4. Of the four extra, three are ordinary reviewed commits
// DISCUSSING a rescue — 2c6c47d rebases one, 53e0b57 and 78e7ab2 quote one — and
// the fourth (f1d3155) is a rescue whose subject uses the other spelling and
// which IsRescue catches anyway. So the body match reports the subject of the
// prose rather than a property of the commit.
// A subject is where an author declares something ABOUT THE COMMIT THEY ARE
// MAKING; a body is also where they write about everything else.
//
// It is also the right half for the defect this exists to close: the string that
// got clipped to `— UNREVI…` was a subject.
func (c Commit) DeclaresUnreviewed() bool {
	return declaredUnreviewedRe.MatchString(c.Subject)
}

// RemainderNote returns the sentence in which this commit names something
// specific that was NOT done, or "" when it names none.
//
// THE BODY ONLY, AND THE SENTENCE RATHER THAN THE MATCH. The body is where a
// remainder is written — a subject has no room for one — and returning the whole
// sentence is what makes the line actionable without a git round-trip: "no
// accompanying test" alone does not say which test file already asserts over the
// corpus, and that clause is what turns the flag into a successor ticket.
func (c Commit) RemainderNote() string {
	// THE POPULATION IS SELF-DECLARED COMMITS ONLY, AND THIS GATE IS THE LARGER
	// HALF OF THE PRECISION. Measured over the last 400 commits of main:
	// remainderRe alone fires 31 times, and the hits are dominated by SCOPE
	// STATEMENTS: "No production code changed, no assertion changed", "Comments
	// only — no behaviour, no test changes", "No assertion was relaxed", "No
	// prose change, no docs change". Those negate a CHANGE, which is an author
	// bounding what they did; a remainder negates an ARTIFACT THAT SHOULD EXIST.
	// Prose does not separate the two reliably and a wider regex here would trade
	// one skim-past failure for another, which is precisely what this ticket was
	// refined to forbid. (The 31 is a count; which of them are scope statements
	// is this file's reading of them, not an independent judgement.)
	//
	// On the self-declared population the same regex fires TWICE in 400 commits —
	// 4dd1b9d's "no accompanying test … Nothing was added there" and 1c91003's
	// "No changelog entry is present" — and both are real: mg-f34e is the
	// successor ticket somebody filed BY HAND for the second one.
	//
	// The narrowing is not free and the cost is stated rather than hidden: a
	// remainder declared by an ordinary reviewed commit is not reported. That
	// commit had a reviewer, which is the thing a rescue commit is missing and
	// the reason this report exists at all.
	if !c.IsRescue() && !c.DeclaresUnreviewed() {
		return ""
	}
	loc := remainderRe.FindStringIndex(c.Body)
	if loc == nil {
		return ""
	}
	return sentenceAround(c.Body, loc[0], loc[1])
}

// DeclaresRemainder reports whether this commit's body names a specific missing
// artifact. See remainderRe for why a hedge does not count.
func (c Commit) DeclaresRemainder() bool { return c.RemainderNote() != "" }

// sentenceAround extracts the sentence of s containing the byte range [from,to),
// with newlines and runs of whitespace flattened.
//
// Commit bodies are hard-wrapped at ~76 columns, so the sentence a match lands
// in is spread over two or three physical lines and a line-based extract would
// cut it. Boundaries are a full stop followed by whitespace, or a blank line;
// "no." inside "changelog.d" or "mg-1d05" is not one, which is why the rule is
// stop-plus-space rather than stop alone.
func sentenceAround(s string, from, to int) string {
	start := 0
	for i := from; i > 0; i-- {
		if s[i-1] == '\n' && i >= 2 && s[i-2] == '\n' {
			start = i
			break
		}
		if i >= 2 && s[i-2] == '.' && isSpaceByte(s[i-1]) {
			start = i
			break
		}
	}
	end := len(s)
	for i := to; i < len(s); i++ {
		if s[i] == '.' && (i+1 == len(s) || isSpaceByte(s[i+1])) {
			end = i + 1
			break
		}
		if s[i] == '\n' && i+1 < len(s) && s[i+1] == '\n' {
			end = i
			break
		}
	}
	return strings.Join(strings.Fields(s[start:end]), " ")
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// FirstUnreviewed returns the oldest unmerged commit declaring itself
// unreviewed, or nil.
func (f Finding) FirstUnreviewed() *Commit { return firstMatch(f.Unmerged, Commit.DeclaresUnreviewed) }

// FirstRemainder returns the oldest unmerged commit whose body names a specific
// missing artifact, or nil.
func (f Finding) FirstRemainder() *Commit { return firstMatch(f.Unmerged, Commit.DeclaresRemainder) }

func firstMatch(commits []Commit, pred func(Commit) bool) *Commit {
	for i := range commits {
		if pred(commits[i]) {
			c := commits[i]
			return &c
		}
	}
	return nil
}
