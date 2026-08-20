package agent

import (
	"regexp"
	"strings"
	"testing"
)

// The mg-5b5e guard: the durability check that decides whether stopping a
// {{.Worker}} is as good as landing its work.
//
// THE DEFECT WAS THE ORDERING, and that is why this is a placement test and not
// a presence one. The pre-deploy procedure is *warn them to push → wait → check
// → stop*, so the warning is an instruction to create the very state the check
// tests for, and the gap between check and stop is exactly when a push lands. On
// 2026-08-20 that gap was 19 seconds: the sweep read zero at 00:51:23Z, p8188
// submitted at 00:51:40Z, the stop went out at 00:51:42Z, and mg-8188 was
// recorded as having "left NO state". The MR merged at 01:03:47Z.
//
// Nothing was lost — the cost fell on the RECORD, which is the surface the next
// reader acts from. A procedure that reliably produces false claims in durable
// records is worth pinning even when it never loses a byte.
//
// The second half is instrument coverage, and it is pinned here because the
// obvious remedy repeats the defect: "read the last refinery_merge_attempted
// from the event log" cannot see an MR that is submitted but still QUEUED, since
// no attempt event exists until a gate picks it up — behind a running gate, tens
// of minutes of blindness in place of the ~minute it was meant to fix.

// durabilitySection returns the durability-check section of the coordinator
// prompt, bounded by its own heading and the next one.
func durabilitySection(t *testing.T, s string) string {
	t.Helper()
	const heading = "### Before you stop a live {{.Worker}}: the durability check"
	start := strings.Index(s, heading)
	if start < 0 {
		t.Fatalf("mayor.md: the durability-check section is gone — without it a coordinator "+
			"stopping a {{.Worker}} has no stated way to tell durable work from none, which is "+
			"how mg-8188 was recorded as having left nothing minutes after it submitted; wanted %q", heading)
	}
	rest := s[start+len(heading):]
	end := strings.Index(rest, "\n### ")
	if end < 0 {
		end = strings.Index(rest, "\n## ")
	}
	if end < 0 {
		t.Fatalf("mayor.md: could not find the end of the durability-check section")
	}
	return rest[:end]
}

// flowed collapses runs of whitespace so an assertion pins the CLAIM rather than
// the line breaks around it. The prompt is hard-wrapped prose: a phrase that
// straddles a wrap is the same instruction, and a test that fails on a reflow
// teaches an editor to delete the assertion instead of keeping the claim.
var whitespace = regexp.MustCompile(`\s+`)

func flowed(s string) string { return whitespace.ReplaceAllString(s, " ") }

// The check has to be the last act before each individual stop, and the prompt
// has to say that this NARROWS the window rather than closing it. A remedy that
// claims to close it is the same defect one layer up: the next operator writes
// an unqualified "left no state" from a reading they were told was atomic.
func TestMayorPromptDurabilityCheckIsPerAgentAndAdmitsItsResidualRace(t *testing.T) {
	sec := flowed(durabilitySection(t, mustMayorPrompt(t)))

	for _, want := range []string{
		"per agent, as the last act before that agent's stop",
		"narrows the window; it does not close it",
		"stale by construction",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("mayor.md durability check: missing %q — the ordering, and the honesty about "+
				"what re-ordering can and cannot buy, are the whole of mg-5b5e's first defect", want)
		}
	}

	// The record is what the defect actually damaged, so the ban on writing a
	// universal from a point reading is load-bearing prose, not a caveat.
	if !strings.Contains(sec, "Never write a universal into a ticket from a pre-stop reading") {
		t.Error("mayor.md durability check: the ban on writing a universal (\"left no state\") from a " +
			"single pre-stop reading is missing — that sentence in a ticket body is the entire cost " +
			"of mg-5b5e, and the next reader acts on it")
	}
	if !strings.Contains(sec, "[stranded-push]") {
		t.Error("mayor.md durability check: the section does not name pogod's `[stranded-push]` mail — " +
			"it fires inside pogod after the process is gone, so it is the one reading of this that " +
			"cannot be raced, and it is what corrects a wrong pre-stop note")
	}
	if !strings.Contains(sec, "mg-5b5e") {
		t.Error("mayor.md durability check: the section does not cite mg-5b5e — without the incident " +
			"the ordering rule reads as a preference and gets edited out as verbosity")
	}
}

// Both views, and what each is blind to. Prescribing either alone is a defect
// with a measured shape in each direction.
func TestMayorPromptDurabilityCheckReadsBothViewsAndNamesTheirBlindSpots(t *testing.T) {
	sec := flowed(durabilitySection(t, mustMayorPrompt(t)))

	if !strings.Contains(sec, "pogo refinery queue --json") {
		t.Error("mayor.md durability check: the pipeline view is not prescribed — `queue` is the only " +
			"view that sees an MR between submit and the gate picking it up")
	}
	if !strings.Contains(sec, "pogo refinery history --since=") {
		t.Error("mayor.md durability check: the durable-log view is not prescribed — the retained " +
			"history is a 100-entry window and `queue` is drained, so nothing else covers a finished merge")
	}

	// The event-log route's blind spot is the one that would otherwise be
	// invisible: it is the remedy mg-5b5e itself proposed.
	if !strings.Contains(sec, "refinery_merge_attempted") || !strings.Contains(sec, "QUEUED") {
		t.Error("mayor.md durability check: the section does not say that the event-log view is blind " +
			"to a submitted-but-QUEUED MR (no `refinery_merge_attempted` until a gate starts it) — " +
			"that omission is what makes the event log look like a complete instrument on its own")
	}

	// Durability is the pushed branch, not the merge status. A reader who
	// requires status=merged stops agents whose work is queued and calls it clean.
	if !strings.Contains(sec, "A hit in either view means the work is durable, whatever its status") {
		t.Error("mayor.md durability check: the section does not state that ANY row for the author is " +
			"durable work — a merge request exists only because a branch was pushed, and a pushed " +
			"branch outlives the stop")
	}

	// Zero in both views answers "no merge request", not "left no state". The
	// gap between those two sentences is where mg-5b5e's false claim was
	// written, and a pushed-but-never-submitted branch lands squarely in it.
	if !strings.Contains(sec, `Zero in both views is not "left no state"`) {
		t.Error("mayor.md durability check: the section does not distinguish a zero refinery reading " +
			"from \"left no state\" — a {{.Worker}} that pushed and never submitted is durable and " +
			"invisible to both views, and reading zero as nothing-left is the exact substitution " +
			"that put a false claim on mg-8188")
	}
	if !strings.Contains(sec, "pogo check-stranded") {
		t.Error("mayor.md durability check: the section does not name `pogo check-stranded` — it is " +
			"the pre-stop instrument for a pushed branch that was never submitted, which neither " +
			"refinery view can see")
	}

	// The claim that `queue` hides the in-flight row was true until mg-0c51 and
	// is false against any daemon carrying it. A directive that hardcodes either
	// answer rots; one that says how to ask the running daemon does not.
	for _, want := range []string{"ef18372", "merge-base --is-ancestor"} {
		if !strings.Contains(sec, want) {
			t.Errorf("mayor.md durability check: missing %q — whether `queue` shows the in-flight row "+
				"is a property of the RUNNING pogod, and a hardcoded answer in either direction sends "+
				"the reader to the less complete view", want)
		}
	}
}

// Placement, and single-copy discipline. The section lives with the instruments
// it prescribes; the stop sites POINT at it. Two copies of an instrument drift,
// which is the failure mg-5b5e's own retirement condition was written to avoid.
func TestMayorPromptDurabilityCheckIsPointedAtFromTheStopSite(t *testing.T) {
	s := mustMayorPrompt(t)

	sec := strings.Index(s, "### Before you stop a live {{.Worker}}: the durability check")
	refinery := strings.Index(s, "## The Refinery")
	troubleshooting := strings.Index(s, "## Troubleshooting Stalled Agents")
	if refinery < 0 || troubleshooting < 0 {
		t.Fatalf("mayor.md: could not bound the sections (refinery=%d troubleshooting=%d)", refinery, troubleshooting)
	}
	if sec < refinery || sec > troubleshooting {
		t.Errorf("mayor.md: the durability check sits outside The Refinery (at %d, section runs %d..%d) — "+
			"it prescribes refinery views and belongs beside them", sec, refinery, troubleshooting)
	}

	// The stop escalation must send the reader to the section by name.
	stopSite := flowed(s[troubleshooting:])
	if !strings.Contains(stopSite, `see "Before you stop a live`) {
		t.Error("mayor.md: the stall-escalation stop does not point at the durability check — a " +
			"coordinator stopping a stalled {{.Worker}} is exactly the reader who needs it, and a " +
			"rule they only meet in another section is a rule they meet too late")
	}

	// ...and must not carry its own copy of the instrument.
	if strings.Contains(stopSite, "pogo refinery queue --json") || strings.Contains(stopSite, "refinery_merge_attempted") {
		t.Error("mayor.md: the stop site carries a SECOND copy of the durability instrument — two " +
			"copies of one instruction drift apart, which is the failure mg-5b5e's retirement " +
			"condition exists to prevent. Point at the section; do not restate it")
	}
}
