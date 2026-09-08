package prtracking

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The two fixtures below are the measured cases behind mg-1f04, re-measured
// against the live store on 2026-09-08 before they were written down:
//
//	mg show mg-2880 -> non-zero ("no such work item")
//	mg show mg-cc4b -> non-zero
//	mg show mg-36e3 -> non-zero
//	mg show mg-c76a -> exit 0
//	mg show mg-1f04 -> exit 0   (positive control for the instrument itself)
//
// so `resolvesLocally` below is that measurement frozen, not an invention.
var localStore = map[string]bool{"mg-c76a": true, "mg-1f04": true}

func resolvesLocally(id string) bool { return localStore[id] }

// macguffin28 is drellem2/macguffin#28 as `gh pr view` returned it: a
// human-authored branch name, a title naming no work item, a body citing a
// design DOC whose filename begins `mg-`, and one review comment carrying two
// ids from another fleet's store.
var macguffin28 = PR{
	Repo:        "drellem2/macguffin",
	Number:      28,
	HeadRefName: "feat/publish-mg-image",
	Title:       "feat(image): publish mg as a digest-pinnable container artifact",
	Body: "The decision to deliver it as an artifact rather than a build-time clone is " +
		"recorded there in `docs/design/mg-artifact-delivery.md`; this PR is the producer half.",
	Comments: []string{
		"## Review round 1: PASS\nReviewer: mg-cc4b · build ticket: mg-2880 · blocking: 0 · advisory: 7\n\n" +
			"Reviewed against the merged design decision in `payitgov/agents` `docs/design/mg-artifact-delivery.md`.",
	},
}

// pogo93 is drellem2/pogo#93, the PR the open-PR pass was built after. It is
// the control for the whole change: it must STAY a finding.
var pogo93 = PR{
	Repo:        "drellem2/pogo",
	Number:      93,
	HeadRefName: "polecat-d36e3",
	Title:       "[mg-36e3] fix(deploy): resolve git by execution, bind the real GH_TOKEN file",
	Body:        "Cutting over from the broken `com.pogo.daily-rebuild` nightly to the upstream `com.pogo.deploy` job (mg-36e3)...",
	Comments:    []string{"Thanks — looking into this. Triaging as mg-c76a; no merge or close until a verdict is gated."},
}

func TestExternalFleetPRIsTrackedElsewhereAndStillActionable(t *testing.T) {
	got := Classify(macguffin28, resolvesLocally)

	if got.State != StateTrackedElsewhere {
		t.Fatalf("state = %v, want %v (prose names ids, none resolve, no mechanical id)", got.State, StateTrackedElsewhere)
	}
	if !got.Actionable() {
		t.Error("StateTrackedElsewhere is not actionable — this is the exact downgrade mg-1f04 forbids: " +
			"Daniel's ruling on this PR was to close it, so silencing the row would have hidden the case he ruled on")
	}
	if want := []string{"mg-2880", "mg-cc4b"}; !reflect.DeepEqual(got.Prose, want) {
		t.Errorf("prose ids = %v, want %v", got.Prose, want)
	}
	if len(got.Mechanical) != 0 {
		t.Errorf("mechanical ids = %v, want none — the branch is not polecat-shaped and the title names no item", got.Mechanical)
	}

	line := got.Report(macguffin28)
	for _, want := range []string{
		"tracked in a store we cannot read",
		"confirm this PR should exist",
		"mg-2880", "mg-cc4b",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("report line %q is missing %q", line, want)
		}
	}
	// The evidence has to travel with the row. A reader who cannot see which
	// ids said "external" cannot check the claim, and an unverifiable row is
	// the informational downgrade wearing a louder word.
	if strings.Contains(line, "no tracker found") {
		t.Errorf("report line %q still uses the old, wrong word", line)
	}
}

// A design-doc filename that begins `mg-` is not a work-item id, and this is
// not hypothetical: it is in the body of the PR the whole ticket is about.
func TestMgPrefixedFilenameIsNotAnID(t *testing.T) {
	if got := IDs("see docs/design/mg-artifact-delivery.md and mg-mail-notes.md"); got != nil {
		t.Errorf("IDs = %v, want none — `arti` and `mail` are not 4-hex codes", got)
	}
	// Boundaries in both directions, so a longer hex run is not silently
	// truncated into a plausible id.
	for _, text := range []string{"mg-28801", "mg-2880x", "xmg-2880"} {
		if got := IDs(text); got != nil {
			t.Errorf("IDs(%q) = %v, want none", text, got)
		}
	}
	if got := IDs("MG-2880 and mg-cc4b."); !reflect.DeepEqual(got, []string{"mg-2880", "mg-cc4b"}) {
		t.Errorf("IDs = %v, want [mg-2880 mg-cc4b] normalised to lower case", got)
	}
}

// THE CONTROL ARM. Splitting `untracked` must not take the known strand off
// the report — and pogo#93 is the sharpest possible test of that, because a
// prose id on it (mg-c76a, a triage item) DOES resolve locally. Classify on
// prose first and #93 reads "tracked" and goes quiet.
func TestKnownStrandStaysAFinding(t *testing.T) {
	got := Classify(pogo93, resolvesLocally)

	if got.State != StateNoTrackerFound {
		t.Fatalf("state = %v, want %v — pogo#93 is the strand the pass was built for", got.State, StateNoTrackerFound)
	}
	if !got.Actionable() {
		t.Fatal("pogo#93 stopped being a finding")
	}
	if !containsAll(got.Mechanical, "mg-36e3") {
		t.Errorf("mechanical ids = %v, want the title's mg-36e3 among them", got.Mechanical)
	}
	// The branch spelling has to be read too: a polecat name and a work-item id
	// are different strings, and the pass's own doc says to check both.
	if !containsAll(got.Mechanical, "mg-d36e") {
		t.Errorf("mechanical ids = %v, want the branch candidate mg-d36e among them", got.Mechanical)
	}
	if !containsAll(got.Resolved, "mg-c76a") {
		t.Errorf("resolved = %v, want the resolving prose id carried as evidence", got.Resolved)
	}
	if line := got.Report(pogo93); !strings.Contains(line, "no tracker found") {
		t.Errorf("report line %q, want the strand wording", line)
	}
}

// A live polecat's PR: mechanical id resolves. The pre-existing `tracked` arm,
// unchanged — without this the change could pass by calling everything a
// finding.
func TestFleetPRWithLiveItemIsTrackedHere(t *testing.T) {
	pr := PR{
		Repo: "drellem2/pogo", Number: 500,
		HeadRefName: "polecat-t1f04",
		Title:       "[mg-1f04] distinguish tracked-externally from untracked",
	}
	got := Classify(pr, resolvesLocally)
	if got.State != StateTrackedHere {
		t.Fatalf("state = %v, want %v", got.State, StateTrackedHere)
	}
	if got.Actionable() {
		t.Error("a tracked, in-flight PR became a finding")
	}
}

// A PR nothing names at all is still `no tracker found` — the third answer is
// evidence-bearing, not a catch-all that swallows the empty case.
func TestNothingNamedIsNoTrackerFound(t *testing.T) {
	pr := PR{Repo: "drellem2/pogo", Number: 501, HeadRefName: "fix/typo", Title: "fix a typo"}
	got := Classify(pr, resolvesLocally)
	if got.State != StateNoTrackerFound {
		t.Fatalf("state = %v, want %v", got.State, StateNoTrackerFound)
	}
	if line := got.Report(pr); !strings.Contains(line, "nothing on the PR names a work item") {
		t.Errorf("report line %q does not say what was read", line)
	}
}

// A prose-only id that DOES resolve is not promoted to `tracked here`. That
// promotion is how the third state would silence the class through the back
// door: any passing mention of a live item in a comment would take a stranded
// PR off the report.
func TestResolvingProseIDDoesNotSilenceTheRow(t *testing.T) {
	pr := PR{
		Repo: "drellem2/macguffin", Number: 502,
		HeadRefName: "feat/whatever",
		Title:       "feat: something",
		Comments:    []string{"Related to mg-c76a, which is about something else entirely."},
	}
	got := Classify(pr, resolvesLocally)
	if got.State != StateNoTrackerFound {
		t.Fatalf("state = %v, want %v", got.State, StateNoTrackerFound)
	}
	if !got.Actionable() {
		t.Error("a passing prose mention silenced the row")
	}
	if !containsAll(got.Resolved, "mg-c76a") {
		t.Errorf("resolved = %v, want mg-c76a carried so the reader can check it", got.Resolved)
	}
}

// No resolver is not a negative. Folding it into either answer would report
// every PR in the fleet at once, or none of them.
func TestNilResolverIsUnmeasured(t *testing.T) {
	got := Classify(macguffin28, nil)
	if got.State != StateUnmeasured {
		t.Fatalf("state = %v, want %v", got.State, StateUnmeasured)
	}
	if got.Actionable() {
		t.Error("an unmeasured PR became a finding")
	}
	if line := got.Report(macguffin28); !strings.Contains(line, "UNMEASURED") ||
		!strings.Contains(line, "take no disposition") {
		t.Errorf("report line %q does not tell the reader to hold", line)
	}
	// The evidence is still gathered — only the verdict is withheld.
	if len(got.Prose) != 2 {
		t.Errorf("prose ids = %v, want the two ids read even without a resolver", got.Prose)
	}
}

// The resolver refuses to exist if its own positive control fails. Without
// this, an `mg` that cannot answer turns the whole sweep into a wall of
// findings that all look measured.
func TestLocalResolverRefusesWithoutAWorkingControl(t *testing.T) {
	if _, err := LocalResolver("mg-0000-definitely-not-an-item"); err == nil {
		t.Fatal("LocalResolver returned a resolver whose control did not resolve")
	} else if !strings.Contains(err.Error(), "positive control") {
		t.Errorf("error %q does not name the control", err)
	}
	if _, err := LocalResolver(""); err != nil {
		t.Errorf("an empty control is the caller's choice, not an error: %v", err)
	}
}

func TestStateStrings(t *testing.T) {
	for state, want := range map[State]string{
		StateUnmeasured:       "unmeasured",
		StateTrackedHere:      "tracked here",
		StateTrackedElsewhere: "tracked elsewhere",
		StateNoTrackerFound:   "no tracker found",
	} {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", state, got, want)
		}
	}
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// TestTrackingRuleIsDocumented pins the doc against this package, the same way
// internal/gitgc/cherrylanding_test.go pins the landed-ness section. Two
// artifacts state this rule — a shipped predicate and the doc a PM actually
// reads — and the failure mg-1f04 repairs is a rule whose only statement was
// prose. A guard that goes green on a deletion has the failure mode it was
// written to detect, so an unreadable doc is fatal rather than skipped.
func TestTrackingRuleIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "pm-open-pr-pass.md"))
	if err != nil {
		t.Fatalf("pm-open-pr-pass.md is unreadable — this guard's subject is gone: %v", err)
	}
	text := string(doc)

	for _, want := range []string{
		// The three answers, in the words Result.Report uses.
		"tracked here",
		"tracked elsewhere",
		"no tracker found",
		// The sentence the third answer exists to produce. Daniel's ruling
		// turns on this being a question to an owner, not a filed-away note.
		"tracked in a store we cannot read — confirm this PR should exist",
		// That the third answer is NOT a downgrade. This is the half the
		// finding originally got backwards, so it is pinned explicitly.
		"is not a quieter row 4",
		// The ordering that keeps the known strand a finding.
		"Mechanical evidence beats prose",
		"Triaging as mg-c76a",
		// The propagation gap this change cannot close, keyed on a string
		// unique to the tracking half of that subsection.
		"still report an external-fleet PR as STRANDED",
		// The shipped predicate and its control, so a reader can re-run it.
		"pogo check-pr-tracking",
		"internal/prtracking",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("docs/pm-open-pr-pass.md no longer states %q", want)
		}
	}

	for _, gone := range []string{
		// The two-answer rule itself, in the doc's own former wording.
		"| no | no | **Stranded.**",
		"## The four dispositions",
		// The downgrade that was drafted and withdrawn. If this ever reappears
		// it will reappear as a softening, so the guard names the softening.
		"downgrade to informational",
	} {
		if strings.Contains(text, gone) {
			t.Errorf("docs/pm-open-pr-pass.md still carries the retracted claim %q", gone)
		}
	}
}

// The auto-derived control is what makes the control unforgettable, so its
// parse has to survive what `mg list --json` actually emits — NDJSON, one
// object per line, and (measured on this store) a trailing blank line.
func TestFirstItemIDReadsNDJSON(t *testing.T) {
	nd := "{\"id\":\"mg-1f04\",\"status\":\"claimed\"}\n{\"id\":\"mg-c76a\",\"status\":\"done\"}\n\n"
	if got := firstItemID([]byte(nd)); got != "mg-1f04" {
		t.Errorf("firstItemID = %q, want mg-1f04", got)
	}
	// An empty or unreadable listing must yield NO control, so the caller is
	// told rather than handed a resolver whose negatives mean nothing.
	for _, empty := range []string{"", "\n\n", "not json\n", "{\"status\":\"done\"}\n"} {
		if got := firstItemID([]byte(empty)); got != "" {
			t.Errorf("firstItemID(%q) = %q, want empty", empty, got)
		}
	}
}
