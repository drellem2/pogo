// Package closingref detects GitHub closing-keyword adjacency in the two texts
// GitHub acts on — commit messages and pull request BODIES — the shape that
// lets narrative prose, or a template's default, silently close a real issue.
//
// THE INCIDENT (mg-2627). Commit e83f394 was mg-6e57: the report-only detector
// for gh-issue carriers whose issue was left OPEN. Its body says, correctly,
// "It never closes an issue and never comments; closing an external issue stays
// human-gated." That guarantee was true of the CODE. The commit closed
// drellem2/pogo#89 anyway — an external contributor's issue, silently, with no
// explanation on a thread that had been quiet for four days.
//
// The cause was a LINE WRAP:
//
//	line 7:  ...and every promise in the thread was fulfilled — but nobody closed
//	line 8:  drellem2/pogo#89, and it sat OPEN from Jul 17 to Jul 21. No alarm fired,
//
// No directive was written. `closed` is a past-tense verb inside a narrative
// sentence about someone ELSE's omission. The adjacency that GitHub parsed as
// "closing keyword + reference" DID NOT EXIST IN THE AUTHOR'S SENTENCE — the
// wrap created it. That is why this package exists and why it looks across
// newlines: a same-line regex would MISS the only real instance we have.
//
// WHAT IS AND IS NOT FLAGGED. The predicate mirrors GitHub's own rule rather
// than approximating it with a proximity window: a closing keyword, optional
// colon, ANY run of whitespace (newlines included), then a reference. Nothing
// else. This matters as much as the catch direction — our commit bodies cite
// issues constantly and legitimately, and a check that flags every `#N` gets
// deleted within a week. `Refs drellem2/pogo#89` has no keyword before the
// ref and passes. So does "this fixes the case where the #89 thread went
// quiet" — `fixes` and `#89` are separated by prose, so GitHub does not link
// them and neither do we.
//
// THE OVERRIDE, and why it is not decorative. Sometimes you genuinely mean to
// close an issue from a commit. The escape is a per-reference trailer that
// lands in the permanent record:
//
//	Closing-ref-ack: drellem2/pogo#89 — intentional; teardown lands with this commit
//
// It is deliberately NOT a config flag, an inventory file, or a --force. Those
// silence the check for everything and leave nothing behind; the body-metachar
// ratchet's own comment makes the point — "if the inventory can be grown to
// silence the test, the ratchet is decorative." This trailer suppresses exactly
// the one reference it names, requires the author to state intent at the moment
// of the hazard, and stays greppable in `git log` forever. Acknowledging is a
// commit-message edit, not an exemption someone else inherits.
//
// The trailer token is spelled "Closing-" rather than "Closes-" on purpose:
// neither GitHub nor the pattern below treats "Closing" as a keyword, so the
// acknowledgement cannot itself trigger the close it is acknowledging.
//
// THE SECOND ARTIFACT (mg-f9e0). For a year this package's doc said "commit
// messages" and every caller passed one. GitHub acts on TWO texts: a commit
// message that lands on the default branch, and the BODY of a pull request
// merged into it. The shipped polecat-build-pr template told every builder to
// write "Resolves <owner>/<repo>#<n>" in the PR body — the channel no caller
// inspected — so the fleet's default path went through the one artifact the
// guard could not see. A check that cannot fail on the default path is not a
// guard; it is a check that passes. Both artifacts are now inspected (see
// Artifact, and internal/refinery/closingref_gate.go for the callers), and the
// remedy text differs by artifact because amending a commit does nothing to a
// PR body.
//
// WHAT THIS PACKAGE STILL DOES NOT SEE. Stated because the last omission was
// invisible for exactly as long as nobody wrote it down:
//
//   - Check is a predicate over text handed to it. COVERAGE IS A PROPERTY OF
//     CALLERS, not of this file. Today: the commit-msg hook and
//     `pogo check-commit-body` (both local, both skippable), and the refinery
//     gate, which reads the branch's own commits and the PR body.
//   - Commits already on the target ref are excluded by the refinery gate on
//     purpose — the branch is judged by what it ADDS.
//   - A PR body EDITED after the gate reads it and before the merge lands is
//     unobserved. The gate is a snapshot, not a lock.
//   - PR TITLES are not inspected, because GitHub does not link from a title.
//     A squash-merge through the GitHub UI promotes the title into the commit
//     subject and would close from there; the refinery rebases and
//     fast-forwards, so its merges never take that path.
//   - Issue and PR COMMENTS are not inspected and do not need to be: GitHub
//     acts only on the PR description and commit messages.
//   - GitHub closes only when the merge reaches the DEFAULT branch. The gate
//     flags a closing keyword on any target ref, which over-flags a merge into
//     a non-default branch. Over-strict with a per-reference escape is the
//     safe direction; under-strict is the incident.
package closingref

import (
	"fmt"
	"regexp"
	"strings"
)

// AckPrefix marks a line that deliberately acknowledges a closing reference.
// Matched case-insensitively at the start of a line, leading space allowed.
const AckPrefix = "Closing-ref-ack:"

// closingPattern is GitHub's rule, not a proximity heuristic: keyword, optional
// colon, any whitespace run, reference.
//
// `[[:space:]]*` is the fix-relevant token — it spans the newline that caused
// the incident. Writing this as `[ \t]*` would produce a check that passes the
// one commit it was built for.
//
// The keyword alternation is GitHub's closing set exactly: close/closes/closed,
// fix/fixes/fixed, resolve/resolves/resolved. Word boundaries keep "closure",
// "prefix" and "resolver" out — GitHub matches whole words and so must we, or
// the false-positive rate does the check in.
//
// The reference alternation is GitHub's linked-issue set exactly, and that set
// has THREE forms, not two: `#123`, `owner/repo#123`, and `GH-123` (mg-f9e0).
// The third was missing here until someone asked what else this pattern
// believed it covered. `GH-123` is not a spelling anyone on this fleet reaches
// for, which is precisely why a reference written that way would have sailed
// through a check everyone believed was complete.
var closingPattern = regexp.MustCompile(
	`(?i)\b(close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[[:space:]]*:?[[:space:]]*((?:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?#[0-9]+|GH-[0-9]+)`,
)

// Artifact is the kind of text a finding was read from. GitHub acts on closing
// keywords in exactly two artifacts, and they are fixed in different places:
// a commit message is amended and re-pushed, a PR body is edited with
// `gh pr edit`. Reporting the commit remedy at someone holding a PR body sends
// them to amend a commit that does not contain the string — which is how a
// clear failure message becomes an ignored one.
type Artifact int

const (
	// CommitMessage is a commit body, judged as `git log --format=%B` returns it.
	CommitMessage Artifact = iota
	// PullRequestBody is a pull request's description — the text `gh pr create
	// --body-file` writes and `gh pr view --json body` reads back.
	PullRequestBody
)

// Finding is one keyword→reference adjacency that GitHub would act on.
type Finding struct {
	// Keyword is the closing verb as written, e.g. "closed".
	Keyword string
	// Ref is the reference as written, e.g. "drellem2/pogo#89" or "#89".
	Ref string
	// KeywordLine and RefLine are 1-based line numbers within the message.
	KeywordLine int
	RefLine     int
	// Wrapped reports that the keyword and the reference are on DIFFERENT
	// lines — the incident's shape, where the adjacency exists only after
	// GitHub joins the lines and is invisible in the author's editor.
	Wrapped bool
	// Excerpt is the source text spanning keyword through reference.
	Excerpt string
}

// Check reports every closing-keyword adjacency in a commit message that is
// not covered by a Closing-ref-ack trailer naming the same reference.
//
// Acknowledged lines are removed before scanning rather than filtered
// afterwards, so an ack that quotes a keyword ("Closing-ref-ack: closes #89")
// cannot report itself.
func Check(message string) []Finding {
	scanned, acked := stripAcks(message)

	var findings []Finding
	for _, m := range closingPattern.FindAllStringSubmatchIndex(scanned, -1) {
		ref := scanned[m[4]:m[5]]
		if acked[normalizeRef(ref)] {
			continue
		}
		kwLine := lineOf(scanned, m[2])
		refLine := lineOf(scanned, m[4])
		findings = append(findings, Finding{
			Keyword:     scanned[m[2]:m[3]],
			Ref:         ref,
			KeywordLine: kwLine,
			RefLine:     refLine,
			Wrapped:     refLine != kwLine,
			Excerpt:     scanned[m[0]:m[1]],
		})
	}
	return findings
}

// stripAcks blanks out Closing-ref-ack lines (preserving newlines so line
// numbers in findings still refer to the original message) and returns the set
// of references those lines acknowledge.
func stripAcks(message string) (string, map[string]bool) {
	acked := map[string]bool{}
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), strings.ToLower(AckPrefix)) {
			continue
		}
		for _, ref := range refPattern.FindAllString(line, -1) {
			acked[normalizeRef(ref)] = true
		}
		lines[i] = ""
	}
	return strings.Join(lines, "\n"), acked
}

// refPattern finds bare references on an ack line, where no keyword precedes.
// It carries the same three forms as closingPattern's reference group, so an
// ack can name a reference in whatever spelling the hazard used.
var refPattern = regexp.MustCompile(`(?:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?#[0-9]+|(?i:GH-[0-9]+)`)

// normalizeRef lowercases so ack matching is not defeated by case, and folds
// the `GH-123` spelling into `#123` — those are the SAME issue in the same
// repo, so an ack written either way must silence the other. It does NOT
// equate "#89" with "owner/repo#89": those are different issues in general, and
// an ack for one must not silence the other.
func normalizeRef(ref string) string {
	ref = strings.ToLower(ref)
	if n, ok := strings.CutPrefix(ref, "gh-"); ok {
		return "#" + n
	}
	return ref
}

// lineOf returns the 1-based line number containing byte offset off.
func lineOf(s string, off int) int {
	return 1 + strings.Count(s[:off], "\n")
}

// Report renders findings as an operator-facing failure message for the given
// artifact. source names the specific text — "commit 1a2b3c4d", "pull request
// #57 body" — and artifact selects the remedy, because the two are edited by
// different commands.
//
// It always prints the neutral rendering and the acknowledgement trailer. A
// check that says only "rejected" teaches nothing and gets worked around; the
// author needs the exact string that makes their change land, at the moment
// they are blocked, or the next attempt is `--no-verify`.
func Report(artifact Artifact, source string, findings []Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d closing-keyword reference(s) GitHub would act on:\n\n", source, len(findings))
	for _, f := range findings {
		if f.Wrapped {
			fmt.Fprintf(&b, "  line %d→%d  %q + newline + %q  (WRAPPED — the adjacency exists only after GitHub joins the lines)\n",
				f.KeywordLine, f.RefLine, f.Keyword, f.Ref)
		} else {
			fmt.Fprintf(&b, "  line %d      %q %q\n", f.KeywordLine, f.Keyword, f.Ref)
		}
		fmt.Fprintf(&b, "             %s\n", strings.Join(strings.Split(f.Excerpt, "\n"), " ⏎ "))
	}
	b.WriteString(`
Merging this would CLOSE those issues on GitHub. If they belong to an external
reporter, that is an outward-facing action our process gates behind a human.
GitHub closes the WHOLE issue — there is no way to close part of one.

To keep the sentence and NOT close anything, use a neutral rendering:

  - move the reference so no closing keyword precedes it, or
  - spell it out in prose: "pogo issue 89", or
  - cite it on its own line as "Refs drellem2/pogo#89".
`)
	b.WriteString(remedy[artifact])
	return b.String()
}

// remedy is the artifact-specific tail of a Report: how to change THIS text,
// and how to acknowledge a closure in it on purpose.
var remedy = map[Artifact]string{
	CommitMessage: `
Reflowing the paragraph is NOT a fix — a later wrap puts the words back
together. This is exactly how e83f394 closed drellem2/pogo#89: the author wrote
"nobody closed" at the end of one line and the reference began the next.

If the closure IS intended, say so per reference in the commit body:

  ` + AckPrefix + ` drellem2/pogo#89 — intentional; <why>

Then amend the message and re-push. The trailer suppresses only the reference
it names, and stays greppable in git log forever.
`,
	PullRequestBody: `
This text is a PULL REQUEST BODY. It is not in any commit, so amending and
re-pushing changes nothing — edit the pull request itself:

  gh pr edit <number> --body-file -

"Refs owner/repo#N" is the safe default and costs one manual close on the days
the PR really does discharge the whole issue. "Resolves" is unsafe in a case
this fleet hits routinely: a PR that lands part of an issue while the rest is
deliberately deferred. A parenthetical scopes NOTHING — a body reading
"Resolves #N (item 1)" closed the whole issue here once already.

If the closure IS intended — this PR discharges the issue ENTIRELY — say so per
reference in the PR body:

  ` + AckPrefix + ` drellem2/pogo#89 — intentional; this PR discharges the issue in full

The trailer suppresses only the reference it names, and records who decided the
close and why on the PR where a reader will find it.
`,
}
