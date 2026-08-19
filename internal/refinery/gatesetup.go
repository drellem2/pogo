package refinery

import (
	"fmt"
	"strings"
	"time"
)

// A quality gate whose OWN SETUP did not stand up is not a verdict on the
// branch, and until mg-15bb the refinery had no class that could say so.
//
// # What happened
//
// On 2026-08-19 mr-da2ls4qtjv1vk5gh57n0 (branch polecat-pe52c, author mg-e52c)
// was recorded:
//
//	Status:  failed
//	         DEFECT — establishes a fact about the branch. A fix is warranted.
//	Error:   quality gate: ./build.sh failed
//	         [test setup failed, not the branch: PASS: a sandbox HOME that is a
//	          symlink to the developer's home: prints the SETUP FAILURE banner]
//	retried: NO — the build gate ran on this tree and returned a verdict —
//	         re-running establishes the same fact
//
// The record contradicts itself in two adjacent fields. The error text says
// "test setup failed, NOT THE BRANCH" and the caption says the failure
// "establishes a fact about the branch"; both cannot be true, and the classifier
// had the refuting text in hand.
//
// # TWO DEFECTS, AND ONLY ONE OF THEM IS THIS FILE'S
//
// The quoted cause is a check that PASSED. `PASS:` is the first token — that
// line is an assertion in scripts/pogo-sandbox_test.sh that went GREEN, and the
// words SETUP FAILURE are the NAME of the banner the suite asserts gets printed.
// A substring scan promoted the description of a passing control to the cause of
// the build failure. The run's real failure was a network positive control,
// further down the same output and marked unambiguously in the same record's
// gate profile.
//
// THAT HALF WAS ALREADY FIXED WHEN THIS WAS MEASURED, AND IT WAS NOT RUNNING.
// mg-67c9 landed the guard (isHarnessVerdictLine, gatefailsummary.go) in d1a57e5
// against the identical specimen five days earlier. Measured here on 2026-08-19:
// the pogod serving that merge reported revision 1ebf2dc, which is 25 commits
// behind main and does NOT contain d1a57e5. So the misattribution above is a
// DEPLOYMENT gap, not an unfixed classifier — and nothing in this file would
// have prevented it either, for the same reason. Anyone reading this record
// should check `curl -s http://127.0.0.1:10000/version | jq -r .revision`
// against the fix before concluding the fix does not work.
//
// What was NOT fixed, and is what this file adds, is the CLASS. Even on a
// GENUINE setup failure — one of the three real banners, on a run where the
// sandbox truly did not stand up — the classifier reached verdictStages["build"]
// and returned ClassDefect with "the gate ran on this tree and returned a
// verdict". The summariser had already established the opposite, in the same
// process, one function earlier. The class is the field a coordinator reads
// first, so the fix has to be there and not only in the prose.
//
// # WHAT THIS CLASS DOES NOT CLAIM — mg-67c9's standing ruling, honoured
//
// mg-67c9 looked at this and refused it, in as many words: "`SETUP FAILURE` is
// NOT wired into the classifier, and should not be. A branch can break its own
// test setup, so 'the envelope did not stand up' does not establish that its
// collapse was environmental."
//
// That ruling is correct and nothing here overturns it. What it rules out is a
// class that says the ENVIRONMENT is at fault, and this class does not say that
// anywhere — not in the error, not in the triage note, not in the terminal
// reason. What it establishes is narrower and is not in dispute: the gate did
// not return a verdict on the tree. That is the same thing ClassIndeterminate
// establishes about a killed gate, and it is enough to refuse DEFECT's caption
// without asserting the opposite one.
//
// The practical consequence is that every sentence this file emits has to be
// written against a reader who may be looking at a branch that broke its own
// setup. "Fix the environment" is therefore NOT what any of them say; "establish
// which, and do not dispatch a fix on this alone" is. If a future edit reaches
// for the shorter sentence, this paragraph is why it must not.
//
// # Why it is retried, when the host-resource class is not
//
// A full disk is not restored by waiting, so ClassHost spends nothing on a
// retry. A broken test envelope is a different distribution: a leftover
// directory, a stale lock, a HOME that a concurrent run moved. Some of those
// clear on their own and some do not, and THE REFINERY CANNOT TELL WHICH FROM
// THE BANNER. What one re-run buys is exactly that discrimination — was the
// envelope broken ONCE, or is it broken STANDING — and that is worth a small,
// bounded number of gate runs to a fleet whose alternative is dispatching a
// worker to change working code.
//
// The retry is also what bounds the cost of the ruling above. A branch that
// breaks its own test setup breaks it again on every attempt, so the retry is a
// DELAY and not an erasure: the failure is still terminal, the record still
// carries every attempt, and what the reader gains is the knowledge that it
// reproduced. That is mg-67c9's own "can this mask a real defect" argument,
// applied to the class it declined to create.
//
// The budget is deliberately smaller than the gate-network one (4 attempts over
// 16m). There is no measured recovery distribution for a broken sandbox on this
// fleet — unlike the DHCP lease behind the network budgets, which has been
// sampled repeatedly — so sizing against one would be inventing evidence. Three
// attempts with 30s and 2m of backoff costs at most two extra gate runs on the
// single serial slot every queued merge waits behind, and the class, not the
// retry, is the larger half of the fix: when the budget is spent the failure is
// still reported SETUP, nobody is sent to read the branch, and the author's
// consecutive-failure streak is untouched (countsAgainstAuthor).
//
// # Why this is allowed to read the gate's output
//
// Same answer as gatehostresource.go and gatenetwork.go, with one extra guard
// that matters more here than in either of them. The reading is
// reportedSetupFailureLine, which is the SAME function summarizeGateFailure uses
// — so the class and the sentence beside it cannot disagree about what a setup
// failure is — and that function already refuses a line opening with a harness
// verdict prefix. Without that refusal this class would be strictly worse than
// no class at all: it would take the 2026-08-19 specimen, which was a red gate,
// and make it retryable and unattributed.
//
// A REMEDY IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT. The defect being
// remedied is "a classification made by substring-matching prose", and this file
// classifies by substring-matching prose. Three things bound that: the reading
// is shared rather than re-implemented, the guard against verdict lines is
// inherited rather than re-derived, and the class is judged AFTER the two kills
// and the two conditions that name something more specific (see the ordering
// note in classifyFailure). What is not bounded is a suite that prints a real
// banner from a passing path without a PASS: prefix; that would be misread here,
// and the honest statement is that nothing in this file detects it.

// gateSetupError reports a gate whose own setup failed — the envelope the tests
// run inside did not stand up, so whatever the output says about packages and
// assertions underneath it is not a finding.
type gateSetupError struct {
	Gate string
	// Banner is the line the gate printed, verbatim and untruncated at the
	// point of capture, so the classification can be audited from the record
	// rather than taken on trust.
	Banner string
	// Occurrences is how many banner lines the output carried. More than one is
	// normal — a suite that aborts usually says so per package.
	Occurrences int
	// Err is the gate's own error, kept so errors.As still reaches whatever it
	// wrapped.
	Err error
}

func (e *gateSetupError) Unwrap() error { return e.Err }

func (e *gateSetupError) Error() string {
	b := &strings.Builder{}
	// The denial leads, for the same reason hostResourceError's does: the part
	// of a failure that travels is its first line, and a caveat further down
	// does not reach the reader who forwards the headline.
	//
	// Which is exactly why this headline says "returned no verdict" and NOT
	// "not on this branch". The first draft of this file said the latter, three
	// sentences above a paragraph explaining that the banner does not say whose
	// setup failed — a record contradicting itself in two adjacent fields, which
	// is the sentence mg-15bb was filed about, reproduced inside its own remedy.
	// The headline may assert only what the class establishes.
	fmt.Fprintf(b, "gate %q FAILED IN ITS OWN SETUP AND RETURNED NO VERDICT ON THIS BRANCH: it printed a "+
		"setup-failure banner (%s), so the environment its checks needed was never established. THIS IS NOT A "+
		"FINDING AGAINST THE BRANCH AND NOT A CLEARANCE OF IT — whatever packages and assertions the output names "+
		"below the banner failed because the envelope did not stand up, so they are not findings either.\n",
		e.Gate, plural(e.Occurrences, "banner line"))
	if e.Banner != "" {
		fmt.Fprintf(b, "The banner, verbatim: %s\n", e.Banner)
	}
	b.WriteString("THE BANNER DOES NOT SAY WHOSE SETUP FAILED — a branch can break its own test setup, and so can " +
		"the box; this class deliberately does not guess, and neither should the first reader of it. Establish " +
		"which before acting: do NOT dispatch a fix on this alone, and do NOT clear the branch on it either. It IS " +
		"retried automatically on a small budget, because one re-run distinguishes an envelope that was broken " +
		"ONCE from one that is broken STANDING.")
	if e.Err != nil {
		fmt.Fprintf(b, "\nThe gate's own error, kept verbatim: %v", e.Err)
	}
	return b.String()
}

// newGateSetupError builds the error for a gate that bannered a setup failure,
// or returns nil when the output says nothing of the sort.
//
// It must be given the FULL gate output, for the reason its two siblings state:
// the copy persisted on the merge request is capped to 8 KiB with its middle
// elided (gateoutputcap.go), and an incident whose banner fell in that middle
// would read back as a clean build failure. Classification therefore happens in
// runQualityGates, before anything is capped.
func newGateSetupError(gate, output string, err error) *gateSetupError {
	banner, ok := reportedSetupFailureLine(output)
	if !ok {
		return nil
	}
	return &gateSetupError{
		Gate:        gate,
		Banner:      truncate(banner, maxSummaryLen),
		Occurrences: countSetupFailureLines(output),
		Err:         err,
	}
}

// countSetupFailureLines counts the banner lines, applying the SAME verdict-line
// guard the single reading applies. A count taken with a bare strings.Count
// would be the discarded reading creeping back in through a field nobody audits
// — a report saying "3 banner lines" over one real banner and two passing
// controls is the 2026-08-19 defect in miniature.
func countSetupFailureLines(output string) int {
	n := 0
	for _, raw := range strings.Split(output, "\n") {
		if !strings.Contains(raw, "SETUP FAILURE") {
			continue
		}
		if isHarnessVerdictLine(strings.TrimSpace(raw)) {
			continue
		}
		n++
	}
	return n
}

// Retry bounds for a gate whose own setup failed. Separate from every other
// budget for the reason they are all separate from each other: a broken
// envelope must not consume the budget that exists to wait out a network
// outage, and vice versa.
//
// The sizing argument is in the file comment above, and its honest summary is
// that there is nothing measured to size against. Three attempts is chosen to
// answer ONE question — once or standing — at a cost of at most two extra gate
// runs, not to outlast anything.
//
// Package vars rather than consts so tests can compress the schedule.
var (
	gateSetupRetryBackoff = []time.Duration{30 * time.Second, 2 * time.Minute}
	// gateSetupMaxAttempts bounds setup-class attempts, retries included.
	gateSetupMaxAttempts = 3
)

// gateSetupBackoffFor returns the delay before the nth setup retry (n starting
// at 1), clamped to the last entry in the schedule.
func gateSetupBackoffFor(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(gateSetupRetryBackoff) {
		n = len(gateSetupRetryBackoff)
	}
	return gateSetupRetryBackoff[n-1]
}
