package agent

import (
	"strings"
	"testing"
)

// The step-3 orphaned-failure check ran its relationship query over
// `pogo refinery history`, which the refinery prunes destructively at 100
// entries — measured 2026-07-30, 100 rows spanning 19 hours. Over that window
// empty output cannot distinguish "no orphaned failures" from "the orphaned
// failure is row 101", and the check exists to catch work that was LOST, which
// is precisely the case where nobody is watching and the row ages out.
//
// The prompt nonetheless documented empty output as "the expected, healthy
// answer" without qualification. mg-2ca3 had warned in the same section against
// judging a branch by `pogo refinery history | tail` — "a bounded window can put
// the merge one line below the cut" — and then ran its own check over a bounded
// window, one layer up.
//
// So widening the window is only half. The other half is that the answer states
// its bound, which is what this test pins.
func TestMayorStep3StatesItsWindow(t *testing.T) {
	body := mayorPromptBody(t)

	for _, want := range []string{
		// The window is scoped deliberately, and the recipe says so.
		"scope the window deliberately with `--since`",
		"--since=30d --json | jq -r '",
		// pipefail, so a truncated answer fails the pipeline instead of
		// printing short and looking clean.
		"set -o pipefail",
		"exits non-zero",
		// The corrected claim: healthy WITHIN the window, not healthy.
		"within the window you asked for",
		// The reason the default is not good enough, with the number in it.
		"prunes destructively at 100 entries",
		// The lesson named, so the next reader does not repeat it a layer up.
		"one layer up",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompts/mayor.md: expected %q — the step-3 check must state the window it read, not just read a wider one", want)
		}
	}

	// The unqualified claim must not come back. It is the sentence that made a
	// truncated answer read as a clean bill of health.
	const banned = "Empty output is the expected, healthy answer: every failure was retried and landed."
	if strings.Contains(body, banned) {
		t.Errorf("prompts/mayor.md: the unqualified %q is back; empty output is healthy only within a named window", banned)
	}
}

// TestMayorRefineryCLIReferenceNamesTheCap covers the other place the prompt
// hands the coordinator a `pogo refinery history` invocation. A reference block
// that lists the command without its bound is how the unqualified reading gets
// picked up again from a different paragraph.
func TestMayorRefineryCLIReferenceNamesTheCap(t *testing.T) {
	body := mayorPromptBody(t)

	i := strings.Index(body, "You can also query refinery state via the CLI")
	if i < 0 {
		t.Fatal("prompts/mayor.md: refinery CLI reference block not found")
	}
	block := body[i:]
	if j := strings.Index(block, "\n## "); j > 0 {
		block = block[:j]
	}

	for _, want := range []string{
		"RETAINED window only",
		"pogo refinery history --since=30d",
		// The queue is genuinely uncapped; saying so stops the caution being
		// over-generalized into distrust of every listing.
		"not capped",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("refinery CLI reference block: expected %q", want)
		}
	}
}

// doctorPromptBody returns the embedded doctor prompt — the artifact that
// ships, not the installed copy under ~/.pogo/agents/crew.
func doctorPromptBody(t *testing.T) string {
	t.Helper()
	data, err := defaultPrompts.ReadFile("prompts/crew/doctor.md")
	if err != nil {
		t.Fatalf("read embedded prompts/crew/doctor.md: %v", err)
	}
	return string(data)
}

// mg-e9ee widened the coordinator's step-3 check and made it state its bound,
// and the three tests above pin that. But `refinery history` has a second
// consumer, and it kept the defect: the doctor's Common Issues entry read
// "Check `pogo refinery history` for error details", and its CLI reference
// labelled the command "Completed merges" with no bound at all.
//
// That consumer is the worse place for it. The doctor is asked "why did my
// polecat fail?" — a question about something that ALREADY HAPPENED, which is
// exactly the case the retained window cannot answer. Measured 2026-08-13 the
// window was 100 rows spanning 18h28m against 926 MRs over 30 days in the
// event log, so a failure from yesterday afternoon is already deleted and
// "nothing in history" reads as "no failures".
//
// The distinction that has to survive the copy is the one mg-e9ee exists for:
// an empty answer is healthy only within a named window, and a TRUNCATED one
// is not an empty answer at all — it is an unknown.
func TestDoctorRefineryHistoryStatesItsWindow(t *testing.T) {
	body := doctorPromptBody(t)

	for _, want := range []string{
		// The Common Issues entry sends the doctor to the durable log.
		"Check `pogo refinery history --since=30d` for error details",
		"name the window when you answer",
		// Why the default cannot answer the question the doctor is asked.
		"prunes destructively at 100 entries",
		"about something that already happened",
		// Both readings, kept apart. This is the pair a consumer loses.
		"empty output means healthy within the window you asked for",
		"is not a healthy empty, it is an unknown",
		// The CLI reference block carries the bound too, so the unqualified
		// reading cannot be picked up again from the other paragraph.
		"RETAINED window only",
		"pogo refinery history --since=30d # Completed merges from the durable event log",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompts/crew/doctor.md: expected %q — the doctor reads `refinery history` to answer questions about the past, so it must state the window it read", want)
		}
	}

	// The unqualified forms must not come back, in either place.
	for _, banned := range []string{
		"- **Refinery failures**: Check `pogo refinery history` for error details",
		"pogo refinery history            # Completed merges\n",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("prompts/crew/doctor.md: the unqualified %q is back; `refinery history` unbounded cannot distinguish no-failures from failure-pruned", banned)
		}
	}
}

// TestMayorStep3WarnsTheWiderWindowFindsFalsePositives pins the measurement
// that came with widening the window. The first real 30-day run produced two
// hits and both were retries under a different branch name — the classifier
// groups by `.branch`, so a resubmit as `polecat-<id>` after a failure on
// `polecat-mg-<id>` splits into two groups and the failed half looks orphaned.
//
// Shipping a widened detector without saying this would hand the coordinator an
// instrument that cries wolf on its first use, and a detector nobody trusts is
// worth no more than the blind one it replaced.
func TestMayorStep3WarnsTheWiderWindowFindsFalsePositives(t *testing.T) {
	body := mayorPromptBody(t)

	for _, want := range []string{
		"Expect false positives from the grouping key",
		"a retry submitted under a *different* branch name splits into two groups",
		// The measurement, with the cases named so a reader can re-run it.
		"`mg-a9bb` and `mg-abea`",
		"2/2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompts/mayor.md: expected %q — the widened window's measured false-positive mode must ship with it", want)
		}
	}
}
