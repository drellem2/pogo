package agent

// The REASON attached to boot-time schedule re-registration, pinned as a
// property of the shipped corpus (mg-49b1).
//
// # The defect
//
// Every long-lived crew prompt told its agent to re-register its `pogo
// schedule` ids on every startup — which is the right practice — and justified
// it with "idempotent via `--id`, so it's safe to re-run" / "costs nothing".
// The first half is true (`--id` IS the dedup key, so you cannot stack
// duplicates) and the second does not follow from it: re-registering REPLACES
// the entry, and the replacement zeroes the entry's lifetime fire counters.
//
// Over two nights four agents reasoned from that sentence to a wrong
// conclusion about the scheduler's behaviour, and three of them proposed
// changing the PRACTICE. The practice was never the problem. The reason was:
// it invited the reader to stop looking, so nobody priced what re-registration
// actually costs until an incident made them.
//
// # Why this is a corpus test and not an edit
//
// A justification is the part of a prompt that regenerates. Delete the
// instruction and leave the reason and someone re-derives the instruction next
// time they tidy the file; that is how the sentence outlived two attempts to
// remove it. So the assertions here are about the SHAPE of the justification —
// no free-ness claim anywhere near schedule re-registration, and, in any
// prompt that instructs boot re-registration, an explicit statement of the
// cost and of the precondition that makes paying it correct.
//
// # What is deliberately NOT asserted
//
// Not the practice. Unconditional re-registration stays, and the assertions
// below pin that it stays (`unconditional`), because the alternative — check
// the listing and repair only what looks missing — puts an agent's only wake
// channel behind a per-id predicate evaluated while booting. mg-de08 is what
// that costs when it goes wrong: an agent read `pogo schedule list`, saw rows,
// concluded "registered", and ran deaf with its mail-check reaped.
//
// And not the absence of justifications generally. This test exists to stop a
// FALSE reason, and stripping a true one leaves behaviour documented and
// unmotivated — the same defect with the sign flipped. The sleep-survival
// clause that motivates preferring `pogo schedule` over an in-process
// scheduler sits in the same paragraphs and is load-bearing; it stays.

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

// freeness matches the claim being removed: that re-running a registration is
// without cost. It is matched on the CLAIM, not on the word "idempotent",
// because "idempotent" is accurate about the dedup key and appears elsewhere in
// the corpus about other commands (`mg unclaim`) where it is both true and
// harmless. What was wrong was the inference drawn from it.
var freeness = regexp.MustCompile(`(?i)costs? nothing|(?:is|are|it'?s|it is) harmless|safe to re-?run|nothing to lose|no cost|free to re-?run|idempotent`)

// reregisterTalk and scheduleTalk together select the neighbourhood the claim
// must not appear in. Both are required: the corpus discusses idempotence of
// several unrelated commands, and it discusses schedules in many places that
// have nothing to do with re-registration.
//
// reregisterTalk deliberately matches "re-run" and "re-confirm" as well as
// "re-register". The first draft of this test matched only "re-register" and
// was GREEN against the exact sentence it was written to catch — doctor.md said
// "the registration is idempotent via --id, so re-running it costs nothing",
// which never uses the word. A guard scoped narrower than the defect cannot see
// it however loudly it fires, so this is widened rather than supplemented; the
// `scheduleTalk` conjunct is what keeps the corpus's many unrelated "re-run
// that command" lines out.
var (
	reregisterTalk = regexp.MustCompile(`(?i)re-?register|re-?run|re-?confirm`)
	scheduleTalk   = regexp.MustCompile(`(?i)pogo schedule|--id\b|schedules?\b|registration`)
)

// windowRadius is how far either side of a free-ness claim counts as "attached
// to" it, before clamping. The clamp is what does the real work: a justification
// attaches to the paragraph it sits in, so the window never crosses a blank
// line. Without the clamp the six {{.Worker}} templates trip on "Re-running it
// is harmless" — a true statement about `mg reclaim` that happens to sit two
// sentences above an unrelated schedule registration.
const windowRadius = 400

// claimWindow returns the text a free-ness claim at body[lo:hi] is judged
// against: at most windowRadius either side, and never across a paragraph
// break.
func claimWindow(body string, lo, hi int) string {
	start := max(0, lo-windowRadius)
	if i := strings.LastIndex(body[start:lo], "\n\n"); i >= 0 {
		start += i + 2
	}
	end := min(len(body), hi+windowRadius)
	if i := strings.Index(body[hi:end], "\n\n"); i >= 0 {
		end = hi + i
	}
	return body[start:end]
}

func TestNoPromptJustifiesScheduleReregistrationAsFree(t *testing.T) {
	var scanned int
	err := fs.WalkDir(DefaultPromptsFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		b, err := fs.ReadFile(DefaultPromptsFS(), p)
		if err != nil {
			return err
		}
		scanned++
		body := string(b)

		for _, m := range freeness.FindAllStringIndex(body, -1) {
			window := claimWindow(body, m[0], m[1])
			if !reregisterTalk.MatchString(window) || !scheduleTalk.MatchString(window) {
				continue
			}
			t.Errorf("%s: %q is used to justify re-registering a schedule.\n"+
				"Re-registering REPLACES the entry and zeroes its lifetime fire counters "+
				"(scheduler.Add), so it is not free. That claim is the one this corpus "+
				"already reasoned from four times and got the scheduler's behaviour wrong "+
				"each time (mg-49b1). Keep the practice — state the real cost instead.\n"+
				"context:\n%s", p, body[m[0]:m[1]], indent(window))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if scanned == 0 {
		t.Fatal("scanned no prompts — the corpus walk found nothing, so this test proved nothing")
	}
	t.Logf("scanned %d prompts", scanned)
}

// bootRegistration selects the population: prompts that tell their agent to run
// a schedule registration on every startup. It is DERIVED, not enumerated, so a
// crew prompt added later is held to the same requirement on the day it is
// added rather than on the day someone remembers this file.
var bootRegistration = regexp.MustCompile(`(?i)on every (?:startup|boot)`)

// required is what such a prompt must say. Each entry is one claim the removed
// sentence used to stand in for.
var required = []struct {
	what string
	re   *regexp.Regexp
	why  string
}{
	{
		what: "the practice, still unconditional",
		re:   regexp.MustCompile(`(?i)unconditional`),
		why: "Check-then-repair was proposed and rejected: it puts the agent's only wake " +
			"channel behind a per-id predicate evaluated while booting, whose failure " +
			"mode is hours of deafness (mg-de08).",
	},
	{
		what: "the cost — zeroed lifetime counters",
		re:   regexp.MustCompile(`(?i)zeroe?s[^.\n]{0,80}counters`),
		why: "This is what re-registration actually costs. A prompt that instructs the " +
			"practice without pricing it is back to asking the reader to take it on faith.",
	},
	{
		what: "the precondition — the outstanding fire is carried",
		re:   regexp.MustCompile(`carryOutstandingFireLocked`),
		why: "Re-registering used to silently retract the catch-up fire the agent booted " +
			"to handle. mg-3cbb is the only reason it no longer does, so it is the " +
			"precondition the instruction rests on and it has to be named.",
	},
	{
		what: "what invalidates the instruction",
		re:   regexp.MustCompile(`(?i)must change with it`),
		why: "A correct practice with an unstated precondition fails exactly like one with " +
			"a false stated reason, and is harder to find — there is no wrong sentence to " +
			"grep for. Naming the condition is what makes this auditable later.",
	},
}

func TestBootReregistrationStatesItsCostAndItsPrecondition(t *testing.T) {
	var population []string
	err := fs.WalkDir(DefaultPromptsFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		b, err := fs.ReadFile(DefaultPromptsFS(), p)
		if err != nil {
			return err
		}
		body := string(b)
		if !bootRegistration.MatchString(body) || !regexp.MustCompile(`pogo schedule +\S+[^\n]*--cron`).MatchString(body) {
			return nil
		}
		population = append(population, p)

		for _, req := range required {
			if req.re.MatchString(body) {
				continue
			}
			t.Errorf("%s: instructs boot-time re-registration but does not state %s (want /%s/).\n%s",
				p, req.what, req.re, indent(req.why))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if len(population) == 0 {
		t.Fatal("no prompt instructs boot-time re-registration.\n" +
			"Either the instruction was removed from the whole corpus — which is a " +
			"practice change this test exists to catch — or the phrasing this test " +
			"selects on drifted and it is now asserting nothing.")
	}
	t.Logf("held: %s", strings.Join(population, ", "))
}

// The prompts above name `carryOutstandingFireLocked` as the precondition for
// re-registering unconditionally, and say in as many words that the instruction
// must change if the carry is removed. That sentence is a claim about code in
// another package, and nothing connects the two: delete the function and the
// prompts keep shipping a precondition that no longer holds, silently, to every
// install.
//
// This is the same defect the rest of this file is about — a justification that
// decays out from under the behaviour it justifies — so the remedy is subject to
// it too. Reading the source is how the claim gets an alarm on it.
//
// It reads the file rather than referencing the symbol because the symbol is
// unexported and in another package. If scheduler.go is split or renamed, this
// fails; re-point it deliberately, and while you are there re-read whether the
// carry still does what the prompts say it does.
func TestThePreconditionThePromptsCiteStillExists(t *testing.T) {
	const src = "../scheduler/scheduler.go"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading %s: %v\nThe prompts cite carryOutstandingFireLocked as the "+
			"precondition for unconditional boot re-registration; this test is the "+
			"only thing that notices when that citation stops being true.", src, err)
	}
	body := string(b)

	for _, want := range []struct {
		decl string
		why  string
	}{
		{
			decl: "func carryOutstandingFireLocked(",
			why: "Without it, re-registering at boot retracts the outstanding catch-up " +
				"fire — the agent works it, runs the exact ack the fire handed it, and " +
				"is refused. That is the state mg-3cbb closed and the prompts now " +
				"promise. If it is gone on purpose, the crew prompts have to stop " +
				"telling agents to re-register unconditionally.",
		},
		{
			decl: "func carryAckHistoryLocked(",
			why: "The prompts also promise that the ever-acked bit survives a " +
				"re-registration (mg-00d6). Without it a re-registered schedule reads " +
				"as never having acked, and the prompts' claim about what the reset does " +
				"NOT take with it is wrong.",
		},
	} {
		if !strings.Contains(body, want.decl) {
			t.Errorf("%s no longer declares %s.\n%s", src, want.decl, indent(want.why))
		}
	}

	// The carry is only useful if Add actually calls it — a live function nobody
	// invokes would satisfy the check above while the prompts' promise is false.
	for _, call := range []string{"carryOutstandingFireLocked(&stored,", "carryAckHistoryLocked(&stored,"} {
		if !strings.Contains(body, call) {
			t.Errorf("%s declares the carry but Add does not call it (%q not found).\n%s",
				src, call, indent("A carry that is never invoked is indistinguishable "+
					"from no carry at the only place it matters: the re-registration path."))
		}
	}
}

func indent(s string) string {
	return "\t" + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n\t")
}
