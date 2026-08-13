package reviewdecl

import "strings"

// This file holds the answer to the question `stage:` cannot answer: OF THE TWO
// ITEMS THAT CARRY `stage: review` AT THE SAME TIME, WHICH ONE IS THE REVIEW
// TICKET?
//
// # Both carry it, and that is by design (mg-2829)
//
// The gh-issue track files two items per issue. The REVIEW ticket is filed at
// `stage: review` from the start (mayor.md transition 3). The BUILD ticket is
// filed at `stage: build` and ADVANCED to `stage: review` by transition 4, the
// moment its PR opens — which is exactly the window in which the review polecat
// runs. So for the whole duration of every correctly-run review loop there are
// TWO items at `stage: review` for one issue, and exactly one of them should
// carry `reviews:`.
//
// This detector's first shipped version keyed the population on `stage:` alone.
// That does not classify — it collects both — so the builder was reported as an
// undeclared review ticket every single time the track ran correctly. Measured on
// the live store on 2026-08-13: three findings, of which mg-49a1 and mg-7182 were
// real and mg-c18d was the build half of the pair whose review half (mg-bd39)
// declared it correctly. The rate is one false finding PER ISSUE, not occasional,
// and a detector at that rate is one nobody reads.
//
// The coordinator then confirmed it BY INTERVENTION rather than by inference: it
// advanced mg-c18d from `stage: review` to `stage: merge` when the PR passed, and
// mg-c18d dropped off the next report with no other property of the ticket
// changed. That is a measurement of what the classifier keys on, not a reading of
// two carriers.
//
// # THE FALSE POSITIVE'S WINDOW IS THE RISK WINDOW, WHICH IS WHY THIS IS URGENT
//
// The noise is not merely frequent, it is timed against the signal. It appears
// when the PR opens and vanishes when the branch is submitted — exactly the
// interval in which a REAL missing declaration matters, because the exemption
// exists to stop the builder being reaped WHILE ITS REVIEWER IS STILL RUNNING.
// So the detector emitted noise precisely when its signal would have been
// actionable, and went quiet once the risk had passed.
//
// A false positive at a random time is noise. A false positive whose window is
// identical to the risk window teaches the reader to discount the report exactly
// when it is right — which is a worse outcome than the detector not existing, and
// it is why this was not left to a heuristic a reader could learn to filter.
//
// # The fix is here and NOT in the state machine
//
// The obvious alternative — leave the build ticket at `stage: build` through
// review — was refused when this was filed, and the reason is that `stage:` is
// load-bearing elsewhere. It is how the playbook knows the PR exists, and
// mg-69b1 made it a DISPATCH GATE (config.IsStageGated, agent.MGDispatchGate).
// Moving a classifier is cheap; moving a state machine that two gates read is
// not.
//
// # `reviews:` itself cannot be the classifier
//
// The unambiguous marker of a review ticket is the `reviews:` line — which is
// the very thing this detector reports the absence of. Classifying on it would
// make the predicate "every review ticket that declares a build item declares a
// build item", and the finding count would be structurally zero. So the markers
// below are chosen for being NON-CIRCULAR: neither reads `reviews:`.
//
// # The two markers, in the order they are trusted
//
//	MarkerSuccessor    the item declares a PREDECESSOR — a `depends:` edge, or a
//	                   `predecessor:<id>` tag. This is the triage -> build chain:
//	                   mayor.md transition 3 files the build ticket with
//	                   `--depends=<triage ticket id>` and states in the same
//	                   breath that the review ticket takes NO depends edge, since
//	                   `--depends` carries dispatch semantics and the build ticket
//	                   stays claimed through review — an edge onto it could never
//	                   clear, and the review polecat could never claim its ticket.
//	                   So on this track a depends edge is a build-side fact.
//	MarkerTitlePrefix  the title's first word is `build`. Weaker, and it is here
//	                   as a SECOND witness rather than a primary one: a title is
//	                   prose, and prose is not a marker. It catches the build
//	                   tickets that carry no edge — mg-55f9 and mg-dd92 on the
//	                   live store, both filed before the depends chaining was
//	                   written into the playbook.
//
// Either marker alone excludes. That direction is chosen deliberately, against
// the cost asymmetry this detector was priced with: a false POSITIVE fires once
// per issue forever and costs the detector its reader, while a false NEGATIVE
// costs one recoverable round (the reviewer notices its counterparty is gone and
// mails the coordinator — gh#131 parts 1+2). Requiring both markers would keep
// flagging any build ticket filed without a triage edge.
//
// # What either marker costs, stated rather than buried
//
// A REVIEW ticket that carries a depends edge is excluded and its missing
// declaration is not reported. One exists on the live store — mg-3c19, "review:
// reap-after-merge fix", `depends: [mg-a58e]` — filed in the older style before
// the playbook forbade the edge, and archived and pre-convention besides. That is
// the whole known population of the false-negative case, and the report LISTS
// every excluded item with the marker that excluded it, so a reader can check
// this rather than trust it. An exclusion nobody can see is the defect this
// detector is a member of the family of.

// BuildMarker names the evidence on which a `stage: review` item was read as the
// BUILD ticket of a gh-issue pair rather than the review ticket.
type BuildMarker string

const (
	// MarkerNone means nothing identified the item as a build ticket, so it is
	// audited as a review ticket.
	MarkerNone BuildMarker = ""
	// MarkerSuccessor is a declared predecessor: a `depends:` edge or a
	// `predecessor:<id>` tag.
	MarkerSuccessor BuildMarker = "successor"
	// MarkerTitlePrefix is a title whose first word is `build`.
	MarkerTitlePrefix BuildMarker = "title"
)

// predecessorTagPrefix is the tag shape a coordinator writes to name the item a
// ticket succeeds. It states the same edge `depends:` does, and is read here
// because it survives independently: `mg` does not derive one from the other, so
// an item can carry either.
const predecessorTagPrefix = "predecessor:"

// classifyBuild reports whether an item carrying `stage: review` is the BUILD
// ticket of a gh-issue pair, and on what evidence.
//
// The detail string names the value that was read, not merely the marker, so the
// report can show a reader the exact fact the exclusion rests on.
func classifyBuild(it Item) (BuildMarker, string) {
	for _, d := range it.Depends {
		if d = strings.TrimSpace(d); d != "" {
			return MarkerSuccessor, "depends: " + d
		}
	}
	for _, t := range it.Tags {
		t = strings.TrimSpace(t)
		if len(t) > len(predecessorTagPrefix) &&
			strings.EqualFold(t[:len(predecessorTagPrefix)], predecessorTagPrefix) {
			return MarkerSuccessor, "tag " + t
		}
	}
	if firstWord(it.Title) == "build" {
		return MarkerTitlePrefix, "title begins `build`"
	}
	return MarkerNone, ""
}

// firstWord returns the leading run of letters of a title, lowercased.
//
// It is a WORD test and not a prefix test on purpose. `strings.HasPrefix(t,
// "build")` would also match a review ticket titled "buildable artifacts…", and
// the whole reason the title marker is admitted at all is that it is cheap — a
// cheap marker that widens silently is not cheap. The letters-only stop means
// `build:`, `build (gh#131 part 3)` and `BUILD gh#94:` all resolve to `build`,
// which are the three shapes the playbook and its history actually wrote.
func firstWord(title string) string {
	t := strings.TrimSpace(title)
	i := 0
	for i < len(t) {
		c := t[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			i++
			continue
		}
		break
	}
	return strings.ToLower(t[:i])
}
