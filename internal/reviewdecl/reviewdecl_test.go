package reviewdecl

import (
	"strings"
	"testing"
	"time"
)

// after and before are stamps either side of the convention boundary, so a test
// case says which side it is on rather than repeating a date.
var (
	after  = ConventionLandedAt.Add(time.Hour)
	before = ConventionLandedAt.Add(-time.Hour)
)

func review(id string, created time.Time, reviews string) Item {
	return Item{ID: id, Title: "review " + id, Status: "claimed", Stage: "review", Reviews: reviews, Created: created}
}

// TestDetectMissingDeclaration is the finding this package exists to produce: a
// review ticket filed since the convention landed with no `reviews:` line.
func TestDetectMissingDeclaration(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", after, "")}, ConventionLandedAt)
	if len(rep.Missing) != 1 || rep.Missing[0].Item.ID != "mg-0001" {
		t.Fatalf("missing = %+v, want the one undeclared review ticket", rep.Missing)
	}
	if !rep.Actionable() {
		t.Error("a review ticket with no declaration is not actionable — the whole point of mg-253e is that it becomes visible")
	}
	if rep.Unprotected() != 1 {
		t.Errorf("Unprotected() = %d, want 1", rep.Unprotected())
	}
	if rep.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1 — the denominator must count the ticket it judged", rep.Scanned)
	}
}

// TestDetectDeclaredIsClean pins the healthy case, and pins that it is COUNTED.
// A declared ticket that vanished from the report would leave "0 missing" with
// no denominator, which mg-253e asked specifically not to ship.
func TestDetectDeclaredIsClean(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", after, "mg-aaf6")}, ConventionLandedAt)
	if rep.Actionable() {
		t.Fatalf("a declared review ticket is actionable: %+v", rep)
	}
	if len(rep.Declared) != 1 {
		t.Fatalf("Declared = %+v, want the one declared ticket counted", rep.Declared)
	}
	if rep.Declared[0].Detail != "mg-aaf6" {
		t.Errorf("Detail = %q, want the declared build item id", rep.Declared[0].Detail)
	}
}

// TestDetectPreConventionIsNotAFinding is the boundary mayor added to this
// ticket at 05:12Z. Without it the detector's FIRST run reports every review
// ticket in the store's history as missing a line that did not exist when it was
// written, and a detector that opens with 31 false findings is never read again.
func TestDetectPreConventionIsNotAFinding(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", before, "")}, ConventionLandedAt)
	if rep.Actionable() {
		t.Fatalf("a review ticket filed BEFORE the convention landed is a finding: %+v", rep.Missing)
	}
	if len(rep.PreConvention) != 1 {
		t.Fatalf("PreConvention = %+v, want the excluded ticket LISTED — an exclusion nobody can see "+
			"is indistinguishable from a scan that missed it", rep.PreConvention)
	}
	if len(rep.Missing) != 0 {
		t.Errorf("Missing = %+v, want empty", rep.Missing)
	}
}

// TestDetectPreConventionTicketThatCarriesTheLineIsNotSpecialCased pins mg-1c60,
// which mayor wrote the `reviews:` line onto BY HAND at 04:04Z, before the
// convention was on main. mayor's dispatch note required that it "must not be
// special-cased away".
//
// Nothing special-cases it: the ordering in Detect tests the declaration before
// the boundary, so a pre-convention ticket that HAS a usable line is simply
// declared. This test exists so that ordering cannot be quietly inverted.
func TestDetectPreConventionTicketThatCarriesTheLineIsNotSpecialCased(t *testing.T) {
	rep := Detect([]Item{review("mg-1c60", before, "mg-aaf6")}, ConventionLandedAt)
	if len(rep.Declared) != 1 || rep.Declared[0].Item.ID != "mg-1c60" {
		t.Fatalf("Declared = %+v, want mg-1c60 — a pre-convention ticket that DOES carry the line "+
			"is declared, not excluded", rep.Declared)
	}
	if len(rep.PreConvention) != 0 {
		t.Errorf("PreConvention = %+v, want empty: the boundary only excuses an ABSENT line", rep.PreConvention)
	}
}

// TestDetectSelfReferenceProtectsNothing. donereap refuses a self-reference by
// name (cmd/pogod/donereap.go:384) — it would exempt a polecat from its own
// completion forever — so a ticket declaring itself is exactly as unprotected as
// one declaring nothing, and must not read as declared.
func TestDetectSelfReferenceProtectsNothing(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", after, "mg-0001")}, ConventionLandedAt)
	if len(rep.Declared) != 0 {
		t.Fatalf("a self-reference was counted as declared: %+v", rep.Declared)
	}
	if len(rep.SelfReference) != 1 {
		t.Fatalf("SelfReference = %+v, want the self-declaring ticket", rep.SelfReference)
	}
	if !rep.Actionable() || rep.Unprotected() != 1 {
		t.Error("a self-reference must count as an unprotected builder: the exemption cannot fire for it")
	}
}

// TestDetectMalformedValueProtectsNothing. donereap matches by EXACT STRING
// EQUALITY against a live polecat's work-item id, so any decoration on the value
// means the match never happens. mg-aaf6 shipped a prompt fix for this exact
// slip (commit 20a5d74); this is the mechanical half of it.
func TestDetectMalformedValueProtectsNothing(t *testing.T) {
	for _, val := range []string{
		"mg-aaf6 (the build ticket)",
		"https://github.com/drellem2/pogo/pull/133",
		"the build item filed as mg-aaf6",
		"see below",
	} {
		t.Run(val, func(t *testing.T) {
			rep := Detect([]Item{review("mg-0001", after, val)}, ConventionLandedAt)
			if len(rep.Declared) != 0 {
				t.Fatalf("%q was counted as declared, but donereap can never match it", val)
			}
			if len(rep.Malformed) != 1 {
				t.Fatalf("Malformed = %+v, want %q reported", rep.Malformed, val)
			}
			if rep.Malformed[0].Detail != val {
				t.Errorf("Detail = %q, want the offending value verbatim so a reader can see what to strip", rep.Malformed[0].Detail)
			}
		})
	}
}

// TestDetectUndatableIsNeitherSide. An item with no `reviews:` line and no
// readable `created:` stamp cannot be placed against the boundary. Resolving it
// to "pre-convention" would hide a real finding; resolving it to "missing" would
// manufacture one. It gets its own bucket, which is the answer this lineage
// keeps having to relearn (ghteardown's StateUnknown, verdictwatch's
// UNDECIDABLE).
func TestDetectUndatableIsNeitherSide(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", time.Time{}, "")}, ConventionLandedAt)
	if len(rep.Missing) != 0 || len(rep.PreConvention) != 0 {
		t.Fatalf("an undatable ticket was resolved to a side: missing=%+v pre=%+v", rep.Missing, rep.PreConvention)
	}
	if len(rep.Undatable) != 1 {
		t.Fatalf("Undatable = %+v, want the unstamped ticket", rep.Undatable)
	}
	if !rep.Actionable() {
		t.Error("an unreachable verdict must be actionable — a failure to reach one is not a clean result")
	}
	// It is NOT an unprotected builder: we do not know that. Unprotected() is the
	// number that goes in a subject line and it must not be inflated by items the
	// scan could not judge.
	if rep.Unprotected() != 0 {
		t.Errorf("Unprotected() = %d, want 0 — an undatable ticket is unjudged, not known-unprotected", rep.Unprotected())
	}
}

// TestDetectOpaqueCarrierIsNotReadAsAbsent is mg-27d4's lesson applied here. A
// carrier block the parser cannot reach yields an EMPTY Stage, and an empty
// Stage is not "this is not a review ticket" — it is "I could not tell". Falling
// through to the stage test would silently drop such an item from the population
// while reporting a denominator that implies it was seen.
func TestDetectOpaqueCarrierIsNotReadAsAbsent(t *testing.T) {
	it := Item{ID: "mg-779b", Title: "parked", Status: "available", CarrierUnreadable: true}
	rep := Detect([]Item{it}, ConventionLandedAt)
	if len(rep.Opaque) != 1 {
		t.Fatalf("Opaque = %+v, want the unreadable carrier listed", rep.Opaque)
	}
	if rep.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 — an item whose stage cannot be read is not IN the review population, "+
			"and counting it would overstate the denominator", rep.Scanned)
	}
	if rep.Actionable() {
		t.Error("an opaque carrier must not alarm: the dispatch gate already refuses such an item " +
			"(internal/agent/dispatchgate.go:161), so no review polecat is spawned and the exemption is never needed")
	}
	if rep.Population != 1 {
		t.Errorf("Population = %d, want 1 — it was still READ", rep.Population)
	}
}

// TestDetectOpaqueWins pins the ORDER of the two guards: an unreadable carrier
// that happens to have left a `stage:` value behind must still be opaque. No
// field of an unreachable block may be trusted, including one that parsed.
func TestDetectOpaqueWins(t *testing.T) {
	it := Item{ID: "mg-0001", Stage: "review", Reviews: "mg-aaf6", CarrierUnreadable: true, Created: after}
	rep := Detect([]Item{it}, ConventionLandedAt)
	if len(rep.Declared) != 0 {
		t.Fatalf("an unreadable carrier's `reviews:` value was trusted: %+v", rep.Declared)
	}
	if len(rep.Opaque) != 1 {
		t.Fatalf("Opaque = %+v, want the item", rep.Opaque)
	}
}

// TestDetectIgnoresNonReviewItems. The population is review tickets. A build
// ticket with no `reviews:` line is not a finding — it is every other item in
// the store.
func TestDetectIgnoresNonReviewItems(t *testing.T) {
	items := []Item{
		{ID: "mg-0001", Stage: "build", Created: after},
		{ID: "mg-0002", Stage: "", Created: after},
		{ID: "mg-0003", Stage: "merge", Created: after},
		{ID: "mg-0004", Stage: "triage", Created: after},
	}
	rep := Detect(items, ConventionLandedAt)
	if rep.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0", rep.Scanned)
	}
	if rep.Actionable() {
		t.Fatalf("a non-review item was reported: %+v", rep)
	}
	if rep.Population != 4 {
		t.Errorf("Population = %d, want 4 — they were read even though they were not judged", rep.Population)
	}
}

// TestDetectStageMatchIsCaseAndSpaceInsensitive. `stage: Review` and
// `stage: review ` are the same declaration to a human writing the ticket, and a
// detector that silently excluded them would under-report in exactly the
// direction that makes it useless.
func TestDetectStageMatchIsTolerantOfCaseAndSpace(t *testing.T) {
	for _, stage := range []string{"review", "Review", "REVIEW", " review "} {
		rep := Detect([]Item{{ID: "mg-0001", Stage: stage, Created: after}}, ConventionLandedAt)
		if rep.Scanned != 1 {
			t.Errorf("stage %q: Scanned = %d, want 1", stage, rep.Scanned)
		}
	}
}

// TestDetectWhitespaceOnlyDeclarationIsMissing. `reviews:   ` parses as a
// present key with an empty value; nothing it could ever match. It must be a
// missing declaration and not a malformed one — the repair is to write an id,
// which is what the missing-declaration text says.
func TestDetectWhitespaceOnlyDeclarationIsMissing(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", after, "   ")}, ConventionLandedAt)
	if len(rep.Missing) != 1 {
		t.Fatalf("Missing = %+v (malformed=%+v), want a whitespace-only value treated as absent", rep.Missing, rep.Malformed)
	}
}

// TestDetectOrderIsStable. A report that reshuffles between runs looks like it
// changed, and the watcher's fingerprint would mail on every sample. Both halves
// matter, so both are pinned.
func TestDetectOrderIsStable(t *testing.T) {
	items := []Item{review("mg-ccc1", after, ""), review("mg-aaa1", after, ""), review("mg-bbb1", after, "")}
	rep := Detect(items, ConventionLandedAt)
	var got []string
	for _, f := range rep.Missing {
		got = append(got, f.Item.ID)
	}
	if strings.Join(got, ",") != "mg-aaa1,mg-bbb1,mg-ccc1" {
		t.Errorf("Missing order = %v, want sorted by id", got)
	}
	if a, b := Detect(items, ConventionLandedAt).fingerprint(), rep.fingerprint(); a != b {
		t.Errorf("fingerprint is unstable across identical scans: %s != %s", a, b)
	}
}

// TestFingerprintIgnoresNonActionableChanges. Pre-convention, declared and
// opaque items never mail, so a change among them must not trigger a
// re-notification of an unchanged finding set.
func TestFingerprintIgnoresNonActionableChanges(t *testing.T) {
	base := []Item{review("mg-0001", after, "")}
	a := Detect(base, ConventionLandedAt)
	b := Detect(append(append([]Item{}, base...),
		review("mg-0002", before, ""),
		review("mg-0003", after, "mg-aaf6"),
		Item{ID: "mg-0004", CarrierUnreadable: true},
	), ConventionLandedAt)
	if a.fingerprint() != b.fingerprint() {
		t.Error("adding non-actionable items changed the fingerprint — an unchanged finding set would re-mail")
	}
}

// TestZeroBoundaryJudgesEverything backs `--since`: an operator auditing history
// passes a boundary of their own, and a zero one means "apply none".
func TestZeroBoundaryJudgesEverything(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", before, "")}, time.Time{})
	if len(rep.Missing) != 1 {
		t.Fatalf("Missing = %+v, want the pre-convention ticket judged when no boundary is applied", rep.Missing)
	}
}

// TestBoundaryIsExactCommitterDate pins WHICH stamp the boundary is, because the
// two candidates are 87 minutes apart and the choice is documented rather than
// arbitrary. A future edit that "fixes" this to the author date would start
// judging tickets filed while the convention was still only on a branch.
func TestBoundaryIsExactCommitterDate(t *testing.T) {
	want := "2026-08-12T04:51:59Z"
	if got := ConventionLandedAt.UTC().Format(time.RFC3339); got != want {
		t.Errorf("ConventionLandedAt = %s, want %s (committer date of c045a9a — when `reviews:` was on main, "+
			"not when it was written)", got, want)
	}
}

// TestBoundaryIsInclusiveOfItsOwnInstant. An item filed at exactly the boundary
// is judged, not excused. `Before` is the comparison, so the instant itself is
// on the convention's side — stated in a test because an off-by-one here is
// invisible in the rendered report.
func TestBoundaryIsInclusiveOfItsOwnInstant(t *testing.T) {
	rep := Detect([]Item{review("mg-0001", ConventionLandedAt, "")}, ConventionLandedAt)
	if len(rep.Missing) != 1 {
		t.Errorf("an item filed AT the boundary was excused: missing=%+v pre=%+v", rep.Missing, rep.PreConvention)
	}
}
