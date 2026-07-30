package agent

import (
	"bytes"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// The coordinator's step-3 health check used to say: cross-reference
// `mg list --status=done` against refinery history, and "if a done item's
// branch shows as failed in refinery history" reopen it.
//
// That predicate tests for a ROW when the property is a RELATIONSHIP. The
// refinery appends every attempt to history, so a transient gate failure
// leaves a permanent status=failed row even after the branch merges seconds
// later — and retry-then-merge is the ordinary case. Measured against this
// machine's history on 2026-07-30 (mg-2ca3), all ten failed MRs had also
// merged: the rule's hit rate on real data was 0/10 and its false-positive
// rate 10/10, and following it would have returned nine `done` items and one
// `archived` item — all with code on main — to `available`, where the same
// prompt's dispatch loop re-dispatches them.
//
// Two things were wrong and both are pinned here:
//
//  1. The CONDITION: flag only when a failed MR exists for the branch AND no
//     merged MR exists for that branch. `merged >= 1` is the property; a
//     literal attempt count is not, since a branch may legitimately retry
//     several times.
//  2. The ACTION: a heuristic running unattended must REPORT, not mutate.
//     `mg reopen` feeds the dispatch loop, so a false positive costs
//     duplicated work against main rather than a noisy line in a summary.
//     Reopening is reserved for a case someone has confirmed.
//
// The section already contained a "do not reopen blindly" caution — it was
// just keyed to repeat failure instead of to whether the work had landed.
func TestMayorStep3FailedOnDoneItemsRequiresNoLaterMerge(t *testing.T) {
	body := mayorPromptBody(t)

	for _, want := range []string{
		// The condition, stated as a relationship.
		"A failed MR is not evidence the work is missing.",
		"a `failed` MR exists for its branch **and** no `merged` MR exists for that same branch afterwards",
		// No count threshold — `merged >= 1` is the property.
		"Do not encode a count threshold",
		"`merged >= 1` is the property",
		// The action: report, don't mutate.
		"**A hit is something to REPORT, not to act on.**",
		"Reopening is for a case someone has confirmed, never for a bare detection.",
		// A positive-showing instrument, not a bounded window — and since
		// mg-e9ee the window it shows must itself be scoped, because the
		// default one is capped at 100 rows.
		"pogo refinery history --since=30d | grep <branch>",
		"pogo refinery history | tail",
		// The measurement that makes the rule credible to a reader.
		"all ten failed MRs in history had also merged",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompts/mayor.md: expected %q in step-3 refinery cross-reference", want)
		}
	}

	// The pre-mg-2ca3 predicate must not come back. It is quoted here rather
	// than paraphrased so a reintroduction fails loudly.
	for _, banned := range []string{
		"If a done item's branch shows as failed in refinery history",
		"instead of reopening blindly",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("prompts/mayor.md: row-presence predicate %q is back; the condition is failed AND no later merge", banned)
		}
	}
}

// TestMayorStep3ClassifierFlagsBothArms executes the classifier the prompt
// actually ships. A rule verified only against the happy case is what shipped
// here, so both arms are exercised:
//
//   - failed>=1 AND merged>=1 must NOT be flagged. These rows are copied from
//     this machine's real refinery history (polecat-ebee, polecat-1eb6,
//     polecat-3122) — the shape the old rule got wrong ten times out of ten.
//   - failed>=1 AND merged==0 MUST be flagged. No such case exists in real
//     history, so it is constructed.
//
// The jq program is extracted from prompts/mayor.md rather than restated, so
// this tests the shipped instrument and not a copy of it. Prompts are
// executable text: a wrong instruction in one is a live defect.
func TestMayorStep3ClassifierFlagsBothArms(t *testing.T) {
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		// The prompt tells the coordinator to run jq, so this is a real
		// dependency; it is simply not one this package can install.
		t.Skip("jq not on PATH; cannot execute the classifier the prompt ships")
	}

	prog := mayorRefineryClassifier(t)

	// One object per attempt, in the shape `pogo refinery history --json`
	// emits (internal/refinery.MergeRequest). Deliberately not pre-sorted by
	// branch: the classifier must group, not assume adjacency.
	const history = `[
      {"id":"mr-a1","branch":"polecat-ebee","author":"mg-ebee","status":"failed",
       "submit_time":"2026-07-29T11:49:27+01:00","done_time":"2026-07-29T11:49:31+01:00"},
      {"id":"mr-a2","branch":"polecat-f00d","author":"mg-f00d","status":"failed",
       "submit_time":"2026-07-29T11:50:00+01:00","done_time":"2026-07-29T11:50:04+01:00"},
      {"id":"mr-a3","branch":"polecat-ebee","author":"mg-ebee","status":"merged",
       "submit_time":"2026-07-29T11:56:00+01:00","done_time":"2026-07-29T12:01:37+01:00"},
      {"id":"mr-a4","branch":"polecat-beef","author":"mg-beef","status":"failed",
       "submit_time":"2026-07-29T12:10:00+01:00","done_time":"2026-07-29T12:10:05+01:00"},
      {"id":"mr-a5","branch":"polecat-3122","author":"mg-3122","status":"failed",
       "submit_time":"2026-07-29T20:01:00+01:00","done_time":"2026-07-29T20:01:06+01:00"},
      {"id":"mr-a6","branch":"polecat-beef","author":"mg-beef","status":"failed",
       "submit_time":"2026-07-29T20:30:00+01:00","done_time":"2026-07-29T20:30:04+01:00"},
      {"id":"mr-a7","branch":"polecat-3122","author":"mg-3122","status":"merged",
       "submit_time":"2026-07-29T20:11:00+01:00","done_time":"2026-07-29T20:19:00+01:00"},
      {"id":"mr-a8","branch":"polecat-1eb6","author":"mg-1eb6","status":"failed",
       "submit_time":"2026-07-29T23:28:11+01:00","done_time":"2026-07-29T23:28:15+01:00"},
      {"id":"mr-a9","branch":"polecat-cafe","author":"mg-cafe","status":"merged",
       "submit_time":"2026-07-29T23:30:00+01:00","done_time":"2026-07-29T23:31:00+01:00"},
      {"id":"mr-a10","branch":"polecat-1eb6","author":"mg-1eb6","status":"merged",
       "submit_time":"2026-07-29T23:37:11+01:00","done_time":"2026-07-29T23:52:21+01:00"},
      {"id":"mr-a11","branch":"polecat-dead","author":"mg-dead","status":"failed",
       "submit_time":"2026-07-30T01:00:00+01:00","done_time":"2026-07-30T01:00:03+01:00"},
      {"id":"mr-a12","branch":"polecat-dead","author":"mg-dead","status":"cancelled",
       "submit_time":"2026-07-30T01:05:00+01:00","done_time":"2026-07-30T01:05:02+01:00"}
    ]`

	cmd := exec.Command(jqPath, "-r", prog)
	cmd.Stdin = strings.NewReader(history)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("the classifier in prompts/mayor.md is not valid jq: %v\nstderr: %s\nprogram:\n%s", err, stderr.String(), prog)
	}

	got := nonEmptyLines(stdout.String())
	sort.Strings(got)

	// Arm 2: flagged. polecat-beef retried and still never merged, so the
	// count must be reported as-is rather than compared to a threshold; a
	// cancelled attempt (polecat-dead) is not a merge and does not clear it.
	want := []string{
		"mg-beef\tpolecat-beef\tfailed=2\tmerged=0",
		"mg-dead\tpolecat-dead\tfailed=1\tmerged=0",
		"mg-f00d\tpolecat-f00d\tfailed=1\tmerged=0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("classifier output mismatch\n got: %q\nwant: %q", got, want)
	}

	// Arm 1: not flagged. Stated separately from the equality above so a
	// failure names the regression instead of just showing a diff — these are
	// the real-history rows the old rule reopened.
	for _, merged := range []string{"polecat-ebee", "polecat-1eb6", "polecat-3122", "polecat-cafe"} {
		if strings.Contains(stdout.String(), merged) {
			t.Errorf("%s failed and then merged; flagging it is the mg-2ca3 defect", merged)
		}
	}
}

// mayorPromptBody returns the embedded coordinator prompt — the artifact that
// ships, not the installed copy under ~/.pogo/agents.
func mayorPromptBody(t *testing.T) string {
	t.Helper()
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read embedded prompts/mayor.md: %v", err)
	}
	return string(data)
}

// mayorRefineryClassifier extracts the jq program from the step-3 refinery
// cross-reference snippet. The program is single-quoted in the shell, and
// contains no single quotes of its own, so the closing quote delimits it.
//
// The pipe is located rather than the whole command line matched literally, so
// the window the prompt asks for (`--since=30d` today) can be retuned without
// this extraction silently failing to find the classifier it is meant to test.
// What the command IS is asserted separately, right below.
func mayorRefineryClassifier(t *testing.T) string {
	t.Helper()
	body := mayorPromptBody(t)
	// Anchored at the step-3 section: the prompt pipes several other commands
	// into jq, and matching the pipe alone would extract whichever came first.
	const anchor = "A failed MR is not evidence the work is missing."
	a := strings.Index(body, anchor)
	if a < 0 {
		t.Fatalf("prompts/mayor.md: step-3 refinery cross-reference not found (missing %q)", anchor)
	}
	const marker = "--json | jq -r '"
	rel := strings.Index(body[a:], marker)
	if rel < 0 {
		t.Fatalf("prompts/mayor.md: no `pogo refinery history … %s` snippet after %q; step 3 must ship a runnable classifier, not prose alone", marker, anchor)
	}
	i := a + rel
	// The invocation must be `pogo refinery history`, and it must be scoped:
	// the unscoped form reads a window the refinery prunes at 100 entries, over
	// which the classifier's empty output means nothing (mg-e9ee).
	lineStart := strings.LastIndex(body[:i], "\n") + 1
	invocation := strings.TrimSpace(body[lineStart:i])
	if !strings.HasPrefix(invocation, "pogo refinery history") {
		t.Fatalf("prompts/mayor.md: the classifier is piped from %q, not from `pogo refinery history`", invocation)
	}
	if !strings.Contains(invocation, "--since=") {
		t.Fatalf("prompts/mayor.md: the classifier reads %q — an unscoped window the refinery prunes at 100 entries, over which empty output cannot distinguish \"no orphaned failures\" from \"the orphaned failure aged out\" (mg-e9ee). It must pass --since.", invocation)
	}

	rest := body[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		t.Fatalf("prompts/mayor.md: jq program is not closed by a single quote")
	}
	return rest[:j]
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
