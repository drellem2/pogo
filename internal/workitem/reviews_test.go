package workitem

import (
	"strings"
	"testing"
)

// The `reviews:` carrier line (mg-aaf6, drellem2/pogo#131).
//
// It names the BUILD item that a review ticket's review covers, and pogod's
// done-reaper reads it to keep a builder alive while its reviewer is running.
// The line is written ONCE, when the coordinator files the review ticket, and is
// never cleared — so every test here is about a declaration that is permanent by
// design, and about the two ways such a declaration could go wrong: being read
// where no declaration was made, and not being read where one was.

func TestCarrierBlockParsesReviews(t *testing.T) {
	// The shipped shape, exactly as mayor.md transition 3 writes it.
	item := parseBody(t, `# review: gh#131 part 3 PR against the adopted shape on mg-aaf6
workflow: gh-issue
stage: review
gh: drellem2/pogo#131
reviews: mg-aaf6

Review the PR from mg-aaf6 against the approved triage recommendation (mg-89fe).
`)
	if item.Reviews != "mg-aaf6" {
		t.Errorf("Reviews = %q, want %q", item.Reviews, "mg-aaf6")
	}
	if item.Workflow != "gh-issue" || item.Stage != "review" {
		t.Errorf("the rest of the block stopped parsing: workflow=%q stage=%q", item.Workflow, item.Stage)
	}
	if item.CarrierUnreadable {
		t.Error("a well-formed leading block must not read as unreadable")
	}
}

// TestCarrierBlockReviewsIsEmptyByDefault. Every item that is not a gh-issue
// review ticket declares no review, and that must read as "declares none" rather
// than as anything the reaper could act on. This is the overwhelming majority of
// the store.
func TestCarrierBlockReviewsIsEmptyByDefault(t *testing.T) {
	for name, body := range map[string]string{
		"a build carrier": `# build: something (drellem2/pogo#131)
workflow: gh-issue
stage: build
gh: drellem2/pogo#131
`,
		"an ordinary item with no carrier at all": `# fix the thing

It is broken. Fix it.
`,
	} {
		if got := parseBody(t, body).Reviews; got != "" {
			t.Errorf("%s: Reviews = %q, want empty", name, got)
		}
	}
}

// TestCarrierBlockIgnoresProseThatMentionsReviews is the negative case that
// carries the weight, and it is not hypothetical: this ticket's own tree —
// mg-89fe, mg-aaf6, and the source comments implementing the feature — all write
// «reviews: mg-xxxx» in running prose while explaining the mechanism. A
// body-wide search would find those and exempt an unrelated polecat from the
// reap on the strength of a sentence.
func TestCarrierBlockIgnoresProseThatMentionsReviews(t *testing.T) {
	item := parseBody(t, `# triage: a builder that self-closes strands its reviewer (drellem2/pogo#131)
workflow: gh-issue
stage: triage
gh: drellem2/pogo#131

The adopted shape is a carrier line on the REVIEW ticket:

reviews: mg-aaf6

written once at creation and never cleared. The mayor already writes that same
fact in prose one line below, so it is a formatting change.
`)
	if item.Reviews != "" {
		t.Errorf("Reviews = %q — a `reviews:` line in a PARAGRAPH is prose, not a declaration; "+
			"reading it would exempt a polecat on the strength of a sentence about the mechanism", item.Reviews)
	}
	if item.CarrierUnreadable {
		t.Error("a stray `reviews:` line below the block must not gate the item: it bears no gate, " +
			"exactly as `gh:` does not, and refusing work over it would refuse work over a discussion of the feature")
	}
}

// TestCarrierBlockReviewsIsNotGateBearing. `reviews:` changes no dispatch
// decision — it is read after the fact, by the reaper, about a polecat already
// running. A body whose ONLY misplaced carrier line is a `reviews:` one must
// therefore stay dispatchable; only `workflow:`/`stage:` out of reach means the
// item's GATE could not be read.
func TestCarrierBlockReviewsIsNotGateBearing(t *testing.T) {
	item := parseBody(t, `# review: something

A lead-in sentence, which ends the leading block before it starts.
reviews: mg-aaf6
`)
	if item.CarrierUnreadable {
		t.Error("an unreachable `reviews:` line marked the item CarrierUnreadable — it carries no gate, " +
			"so gating on it refuses work for a line that could not have gated it either way")
	}
	if item.Reviews != "" {
		t.Errorf("Reviews = %q — an out-of-reach line must not be read as a declaration", item.Reviews)
	}
}

// TestCarrierUnreadableStillGatesAReviewTicket is the precondition the mg-aaf6
// design rests on, pinned so it cannot quietly stop holding.
//
// A `reviews:` line the parser cannot reach reads as "declares nothing", which
// would fail-open: the builder gets reaped mid-review and the failure looks
// EXACTLY like the bug being fixed, with the declaration visible in `mg show` to
// any human who checks. What makes that unreachable is that a review ticket
// always declares `workflow:` and `stage:` in the same block — so a block out of
// the parser's reach makes the item CarrierUnreadable, which every dispatch gate
// treats as gated. No review polecat is ever spawned, so no exemption is ever
// needed. Sequencing this after mg-27d4 is what bought that; this test is the
// receipt.
func TestCarrierUnreadableStillGatesAReviewTicket(t *testing.T) {
	item := parseBody(t, `# review: gh#131 part 3

PR: https://github.com/drellem2/pogo/pull/999

workflow: gh-issue
stage: review
reviews: mg-aaf6
`)
	if !item.CarrierUnreadable {
		t.Fatal("a review ticket whose whole carrier block sits below a lead-in line must be CarrierUnreadable — " +
			"otherwise it dispatches with its `reviews:` declaration silently unread, and the builder is reaped " +
			"mid-review exactly as in gh#131 (mg-27d4 is a blocking predecessor of mg-aaf6)")
	}
	if item.Reviews != "" {
		t.Errorf("Reviews = %q — an unreachable block must yield no value at all", item.Reviews)
	}
}

// TestParseCarrierMatchesTheFileParse. pogod reads `reviews:` out of a body
// string (`mg show --json` .body), while stallwatch and the dispatch gate read
// it off the file on disk. Those must be the SAME parse: a second, looser reader
// would find declarations in prose that this one deliberately refuses, and the
// disagreement would surface as a guard that fires on one path and not the
// other.
func TestParseCarrierMatchesTheFileParse(t *testing.T) {
	bodies := []string{
		"# review: x\nworkflow: gh-issue\nstage: review\nreviews: mg-aaf6\n",
		"# review: x\n\nreviews: mg-aaf6\n",
		"# build: x\nworkflow: gh-issue\nstage: build\n",
		"# x\n\nprose first\nworkflow: gh-issue\nstage: gated\nreviews: mg-aaf6\n",
		"# x\n\nnothing carrier-shaped here at all\n",
	}
	for _, body := range bodies {
		item := parseBody(t, body)
		got := ParseCarrier(body)
		want := Carrier{
			Workflow:   item.Workflow,
			Stage:      item.Stage,
			Reviews:    item.Reviews,
			Unreadable: item.CarrierUnreadable,
		}
		if got != want {
			t.Errorf("ParseCarrier and parseWorkItem disagree on\n%s\ngot  %+v\nwant %+v",
				strings.TrimRight(body, "\n"), got, want)
		}
	}
}

// TestParseCarrierOnAnMgShowBody. `mg show --json` returns `.body` with a
// leading blank line before the title heading — the shape pogod actually hands
// to ParseCarrier. Pinned because it is the only production caller's input, and
// a parser that needed the heading on line 1 would read every live review ticket
// as declaring nothing.
func TestParseCarrierOnAnMgShowBody(t *testing.T) {
	c := ParseCarrier("\n# review: gh#131 part 3\nworkflow: gh-issue\nstage: review\ngh: drellem2/pogo#131\nreviews: mg-aaf6\n\nReview the PR.\n")
	if c.Reviews != "mg-aaf6" {
		t.Errorf("Reviews = %q, want mg-aaf6 — this is the exact body shape pogod parses", c.Reviews)
	}
	if c.Unreadable {
		t.Error("a leading blank line before the heading is not an unreadable carrier")
	}
}

// TestCarrierBlockReviewsValueMustBeBare pins the trap a coordinator is most
// likely to walk into when writing this line by hand, because it is the one that
// does NOT look like a mistake: `reviews: mg-aaf6 (the build ticket)`.
//
// Every carrier value is a single whitespace-free token, so a value with a space
// is not a carrier line at all — it ENDS the block where it sits. The two
// placements then fail in opposite directions, and the second is the dangerous
// one: above `stage:` the stage line falls below the end of the block, which is
// exactly the `CarrierUnreadable` shape, and the freshly-filed review ticket
// refuses to dispatch.
//
// This was measured against the shipped parser while answering whether the line
// was safe to add to a live ticket. The answer was yes for the bare form and no
// for this one, so both are pinned rather than remembered.
func TestCarrierBlockReviewsValueMustBeBare(t *testing.T) {
	t.Run("spaced value LAST in the block: declaration dropped, item still dispatches", func(t *testing.T) {
		item := parseBody(t, `# review: x
workflow: gh-issue
stage: review
gh: drellem2/pogo#131
reviews: mg-aaf6 (the build ticket)

Review the PR.
`)
		if item.Reviews != "" {
			t.Errorf("Reviews = %q — a spaced value is not a carrier line, so it declares nothing", item.Reviews)
		}
		if item.Stage != "review" || item.CarrierUnreadable {
			t.Errorf("the gate must survive a malformed trailing line: stage=%q unreadable=%v",
				item.Stage, item.CarrierUnreadable)
		}
	})

	t.Run("spaced value ABOVE stage: takes the gate down with it", func(t *testing.T) {
		item := parseBody(t, `# review: x
workflow: gh-issue
reviews: mg-aaf6 (the build ticket)
stage: review
gh: drellem2/pogo#131

Review the PR.
`)
		if item.Stage != "" {
			t.Errorf("stage = %q — the spaced line ends the block, so everything below it is out of reach", item.Stage)
		}
		if !item.CarrierUnreadable {
			t.Fatal("a `stage:` line pushed below the end of the block MUST read as CarrierUnreadable — " +
				"otherwise a review ticket with a hand-typed parenthetical dispatches with no gate read at all (mg-27d4)")
		}
	})

	t.Run("the bare form is unaffected by its position", func(t *testing.T) {
		for _, body := range []string{
			"# review: x\nworkflow: gh-issue\nstage: review\nreviews: mg-aaf6\n",
			"# review: x\nreviews: mg-aaf6\nworkflow: gh-issue\nstage: review\n",
		} {
			item := parseBody(t, body)
			if item.Reviews != "mg-aaf6" || item.Stage != "review" || item.CarrierUnreadable {
				t.Errorf("bare form should parse anywhere in the block: reviews=%q stage=%q unreadable=%v",
					item.Reviews, item.Stage, item.CarrierUnreadable)
			}
		}
	})
}
