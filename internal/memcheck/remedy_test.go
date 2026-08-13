package memcheck

import (
	"strings"
	"testing"
)

// The tests below are about what the checker SAYS, not what it detects. That is
// the whole subject of mg-1b2f: both findings were already correct, and the
// defect was that presenting them together handed a reader near the cap two
// instructions that cannot both be followed. Wording is the behaviour here, so
// it is what gets asserted.

// TestParityRemedy_NearCapDoesNotDemandAHook is the central control. With no
// room for the hooks, the remedy must NOT read as "add a hook for each" — that
// is the instruction that competes with the size warn, and the reader's cheapest
// way to satisfy the pair is to leave the notes unreachable.
func TestParityRemedy_NearCapDoesNotDemandAHook(t *testing.T) {
	// 6 orphans, 200 chars each = 1200 needed, 300 available.
	got := ParityRemedy(6, 300, 200)

	if !strings.Contains(got, "ADD A HOOK IS NOT AVAILABLE FOR ALL OF THESE") {
		t.Errorf("remedy does not state that hooks do not fit:\n%s", got)
	}
	for _, want := range []string{"FOLD", "SUB-INDEX"} {
		if !strings.Contains(got, want) {
			t.Errorf("remedy omits the zero-line remedy %q — the only ones available at this size:\n%s", want, got)
		}
	}
	// The fold must be named BEFORE any surviving mention of spending hooks:
	// order is the instruction when one option is scarce.
	if strings.Index(got, "FOLD") > strings.Index(got, "Spend the remaining") {
		t.Errorf("remedy lists spending hooks before folding; the zero-cost remedy must lead:\n%s", got)
	}
}

// TestParityRemedy_BranchIsDecidedByArithmetic is the discrimination control: a
// remedy that produced the same text either way, or that keyed off "the size
// check fired" rather than off whether the hooks actually fit, would pass every
// other test in this file. The boundary must sit exactly where the arithmetic
// puts it — at 80% of the cap there are still ~25 lines of room, so "the size
// axis is warning" and "the hooks do not fit" are genuinely different questions
// and only the second one may change the instruction.
func TestParityRemedy_BranchIsDecidedByArithmetic(t *testing.T) {
	const cost = 200
	// Exactly enough for 3 hooks: the affordable branch, even though an index
	// with only 600 chars of headroom is well past the size warn threshold.
	fits := ParityRemedy(3, 3*cost, cost)
	// One char short of enough: the constrained branch.
	short := ParityRemedy(3, 3*cost-1, cost)

	if fits == short {
		t.Fatal("remedy text is identical either side of the affordability boundary — the arithmetic is not reaching the wording")
	}
	if !strings.Contains(fits, "so they fit") {
		t.Errorf("exactly-affordable case did not take the affordable branch:\n%s", fits)
	}
	if strings.Contains(fits, "NOT AVAILABLE") {
		t.Errorf("exactly-affordable case reported hooks as unavailable:\n%s", fits)
	}
	if !strings.Contains(short, "NOT AVAILABLE") {
		t.Errorf("one char short of affordable did not take the constrained branch:\n%s", short)
	}
}

// TestParityRemedy_StatesTheAsymmetry pins the one fact that makes the dilemma
// false. Neither agent who hit this had it stated anywhere; one established it
// only by reading which file the checker measures.
func TestParityRemedy_StatesTheAsymmetry(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		unref, headroom, cost int
	}{
		{"with headroom", 2, 9000, 200},
		{"at the cap", 6, 300, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParityRemedy(tc.unref, tc.headroom, tc.cost)
			if !strings.Contains(got, AsymmetryNote) {
				t.Errorf("remedy omits the index-lines-are-capped/bodies-are-not asymmetry:\n%s", got)
			}
		})
	}
}

// TestParityRemedy_WithHeadroomStillOffersTheZeroLineRemedies. Folding and
// sub-indexing are not merely emergency measures: folding is often the better
// record even with room to spare. Offering them only under pressure would teach
// that they are a compromise.
func TestParityRemedy_WithHeadroomStillOffersTheZeroLineRemedies(t *testing.T) {
	got := ParityRemedy(2, 9000, 200)
	if !strings.Contains(got, "so they fit") {
		t.Errorf("remedy does not report that the hooks fit when they do:\n%s", got)
	}
	for _, want := range []string{"ADD A HOOK", "FOLD", "SUB-INDEX"} {
		if !strings.Contains(got, want) {
			t.Errorf("remedy omits %q even with headroom:\n%s", want, got)
		}
	}
}

// TestParityRemedy_AlwaysCarriesTheFoldStandard. Folding is lossy if done
// carelessly and the obvious success criterion is the wrong one — "the file is
// gone and the index did not grow" is satisfied by burying content under a hook
// that does not advertise it, which is the same existence-vs-reachability defect
// parity exists to catch, one level in.
func TestParityRemedy_AlwaysCarriesTheFoldStandard(t *testing.T) {
	for _, got := range []string{ParityRemedy(1, 9000, 200), ParityRemedy(9, 100, 200)} {
		if !strings.Contains(got, "judged by REACHABILITY") {
			t.Errorf("remedy recommends folding without its acceptance test:\n%s", got)
		}
		if !strings.Contains(got, "worse than no fold") {
			t.Errorf("remedy omits that a careless fold is worse than leaving the orphan:\n%s", got)
		}
	}
}

// TestParityRemedy_NeverPermitsDroppingTheHook closes the cheap-diff escape on
// both branches. Skipping the hook is the smallest diff that turns the size
// check green, and it does so by abandoning the property that matters.
func TestParityRemedy_NeverPermitsDroppingTheHook(t *testing.T) {
	for _, got := range []string{ParityRemedy(1, 9000, 200), ParityRemedy(9, 100, 200)} {
		if !strings.Contains(got, neverDropTheHook) {
			t.Errorf("remedy does not forbid leaving notes unindexed to keep the size check green:\n%s", got)
		}
	}
}

// TestParityRemedy_IsNeverSuppressed. The one resolution explicitly ruled out is
// staying quiet about parity near the cap: that reproduces the original defect,
// where an unindexed note made the index look HEALTHIER. Pressure changes the
// wording, never the presence.
func TestParityRemedy_IsNeverSuppressed(t *testing.T) {
	for _, headroom := range []int{20000, 1000, 0, -5000} {
		if got := ParityRemedy(3, headroom, 200); got == "" {
			t.Errorf("parity remedy went silent at headroom %d — suppression near the cap is the defect, not the fix", headroom)
		}
	}
	if got := ParityRemedy(0, 100, 200); got != "" {
		t.Errorf("remedy emitted with nothing unreferenced: %q", got)
	}
}

// TestSizeParityTension_WarnsTheSizeAxisNotToShrinkByDroppingHooks. A reader who
// sees only the size warn must still learn that the obvious way to shrink is the
// forbidden one.
func TestSizeParityTension_WarnsTheSizeAxisNotToShrinkByDroppingHooks(t *testing.T) {
	got := SizeParityTension(6, 200)
	if got == "" {
		t.Fatal("size warn says nothing about a parity defect on the same index")
	}
	for _, want := range []string{
		"PARITY ALSO FIRES ON THIS INDEX",
		"do NOT shrink this file by dropping hooks",
		"only index LINES are scarce",
		"belongs to whoever owns the corpus",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("size/parity tension clause omits %q:\n%s", want, got)
		}
	}
	if got := SizeParityTension(0, 200); got != "" {
		t.Errorf("tension clause emitted with no parity defect: %q", got)
	}
}

// TestSizeParityTension_DoesNotRecommendCompactingToFundHooks. Compaction was
// the natural response and it is measurably not a source of hook budget on an
// already-compacted index; a reader who tries it does careful work that buys
// nothing.
func TestSizeParityTension_DoesNotRecommendCompactingToFundHooks(t *testing.T) {
	got := SizeParityTension(6, 200)
	if !strings.Contains(got, "Compacting to make room does not work at this size") {
		t.Errorf("tension clause does not retire compaction as a way to fund hooks:\n%s", got)
	}
}

// TestRemedyDoesNotRecommendReordering. Tail-first truncation is measured, but a
// reordering remedy is a bet on the truncation MECHANISM: it inverts into active
// harm if the mechanism is ever re-measured or changed by the harness. Folding
// is direction-agnostic and no correction can invalidate it, so the guidance
// must not drift back toward reordering.
func TestRemedyDoesNotRecommendReordering(t *testing.T) {
	for _, got := range []string{ParityRemedy(6, 300, 200), SizeParityTension(6, 200)} {
		low := strings.ToLower(got)
		for _, bad := range []string{"reorder", "re-order", "order the index"} {
			if strings.Contains(low, bad) {
				t.Errorf("guidance recommends %q — a remedy that depends on the truncation mechanism holding:\n%s", bad, got)
			}
		}
	}
}

// TestIndexLineCostChars_MeasuresTheIndexItself. The cost of a hook is a
// property of a particular corpus's writing style, so a pinned global constant
// is wrong for every index it was not fitted on. The file being checked is the
// authority on what its own lines cost.
func TestIndexLineCostChars_MeasuresTheIndexItself(t *testing.T) {
	idx := "# Memory index\n\n" +
		"- [A](a.md) — x\n" + // 16 chars incl newline
		"- [B](b.md) — xxxxxxxxxx\n" + // 25
		"- [C](c.md) — xxxxxxxxxxxxxxxxxxxx\n" // 35
	if got, want := IndexLineCostChars([]byte(idx)), 25; got != want {
		t.Errorf("IndexLineCostChars = %d, want the median hook length %d", got, want)
	}
}

// TestIndexLineCostChars_CountsCharsNotBytes. The cap is denominated in
// characters and the em dash in the canonical hook form is three bytes. Byte
// counting is the exact instrument defect that made three independent agents
// agree on the wrong number, because `wc -m` returns bytes with the locale unset
// — so it must not be reproduced in-process.
func TestIndexLineCostChars_CountsCharsNotBytes(t *testing.T) {
	// One hook, "- [A](a.md) — x" = 15 chars / 17 bytes, +1 for the newline.
	got := IndexLineCostChars([]byte("- [A](a.md) — x\n"))
	if got != 16 {
		t.Errorf("IndexLineCostChars = %d, want 16 chars (18 would be the byte count)", got)
	}
}

// TestIndexLineCostChars_FallsBackWhenThereIsNothingToMeasure.
func TestIndexLineCostChars_FallsBackWhenThereIsNothingToMeasure(t *testing.T) {
	for _, in := range []string{"", "# Memory index\n\njust prose, no hooks\n", "- [A](a.txt) — not a note\n"} {
		if got := IndexLineCostChars([]byte(in)); got != DefaultIndexLineCostChars {
			t.Errorf("IndexLineCostChars(%q) = %d, want the %d fallback", in, got, DefaultIndexLineCostChars)
		}
	}
}

// TestDefaultIndexLineCostChars_ErrsTowardHooksNotFitting. The two errors are
// not symmetric: understating the cost tells a reader the hooks fit when they do
// not, sending them past the cap; overstating it sends them to fold, which is a
// correct action anyway. Measured medians on the pressured corpus were 146 and
// 187 chars, with ~200-224 reported for freshly written hooks.
func TestDefaultIndexLineCostChars_ErrsTowardHooksNotFitting(t *testing.T) {
	const measuredCorpusMedianHigh = 187
	if DefaultIndexLineCostChars <= measuredCorpusMedianHigh {
		t.Errorf("DefaultIndexLineCostChars = %d, must exceed the measured corpus median %d so the fit estimate errs toward 'they do not fit'",
			DefaultIndexLineCostChars, measuredCorpusMedianHigh)
	}
}

// TestHeadroomChars_TracksTheAutoInjectCap. Headroom is what decides which
// remedy is available, so it must be measured against the character cap that
// actually binds — not the token cap, whose numeral is identical and whose unit
// is not.
func TestHeadroomChars_TracksTheAutoInjectCap(t *testing.T) {
	r := Check("MEMORY.md", []byte(strings.Repeat("a", 21942)))
	if got, want := r.HeadroomChars(), HarnessAutoInjectCapChars-21942; got != want {
		t.Errorf("HeadroomChars = %d, want %d", got, want)
	}
	over := Check("MEMORY.md", []byte(strings.Repeat("a", HarnessAutoInjectCapChars+500)))
	if over.HeadroomChars() >= 0 {
		t.Errorf("HeadroomChars = %d on an over-cap index, want negative", over.HeadroomChars())
	}
}

// TestParityRemedy_DoesNotRecommendAStoreNothingLoads is a REGRESSION GUARD on a
// withdrawn recommendation, not a wording preference (mg-d97f).
//
// This remedy used to offer "RE-ROUTE — move a note that belongs to a
// less-pressured index (an agent-scoped dir rather than the shared one)". On the
// box it shipped against, those per-agent dirs had stopped being loaded on
// 2026-07-07 and 153 notes sat in them unread for five weeks — the condition
// peragent.go exists to detect. Following the advice would have moved notes into
// a directory nothing reads: the parity count would have gone down, the size
// check would have gone green, every number would have improved, and the content
// would have been unreachable. That is a loud enumerable problem converted into a
// silent one, arriving through the remedy meant to avoid it.
//
// "Less pressured" is not evidence that a destination is loaded. An unpressured
// index is exactly what a store with no readers looks like, so the phrase selects
// FOR the failure. The guard is on the phrasing because the phrasing is the
// instruction.
//
// It keys on the UPPERCASE label because that is how this file marks a remedy it
// is offering — ADD A HOOK, FOLD, SUB-INDEX — while the prose withdrawal below
// has to be free to name what it is withdrawing. A guard that banned the words
// outright would forbid saying why, which is the part a reader arriving at the
// constrained branch most needs.
func TestParityRemedy_DoesNotRecommendAStoreNothingLoads(t *testing.T) {
	for _, got := range []string{ParityRemedy(2, 9000, 200), ParityRemedy(6, 300, 200)} {
		if strings.Contains(got, "RE-ROUTE") {
			t.Errorf("remedy still OFFERS a re-route to another index; the destination it used to name had no reader:\n%s", got)
		}
	}
	// The constrained branch is where the withdrawal has to be stated rather
	// than merely omitted: that is the branch a reader reaches when the hooks do
	// not fit, which is exactly when re-routing somewhere quieter looks like the
	// answer.
	short := ParityRemedy(6, 300, 200)
	if !strings.Contains(short, "must be one something LOADS") {
		t.Errorf("constrained remedy does not state the constraint a destination must meet:\n%s", short)
	}
}

// TestParityRemedy_NamesTheOnlyRemedyThatScales. Hooks and folds are per-note; a
// sub-index is per-set. When 42 notes need reachability and there is room for 13
// hooks, the arithmetic only closes one way, and a remedy list that does not say
// so hands the reader a shortfall with no exit.
func TestParityRemedy_NamesTheOnlyRemedyThatScales(t *testing.T) {
	got := ParityRemedy(42, 1400, 107)
	if !strings.Contains(got, "SUB-INDEX") {
		t.Fatalf("remedy omits the sub-index at a count no number of hooks can cover:\n%s", got)
	}
	if strings.Index(got, "SUB-INDEX") > strings.Index(got, "FOLD") {
		t.Errorf("fold is offered before the sub-index at a count where folding 42 notes by hand is the expensive answer:\n%s", got)
	}
}

// TestParityRemedy_FoldIsNotSoldAsFree. "Zero index lines" is the sentence that
// makes folding the rational move under a margin policy, and it is true — but a
// reader who stops there folds every time and never sees the aggregate. On the
// corpus this ships against, a night of individually-correct folds produced two
// notes each LARGER than the whole 23,952-byte index that reaches them, and no
// check counted it because the parity number goes DOWN when you fold.
func TestParityRemedy_FoldIsNotSoldAsFree(t *testing.T) {
	for _, got := range []string{ParityRemedy(1, 9000, 200), ParityRemedy(9, 100, 200)} {
		if !strings.Contains(got, "FREE AGAINST THE INDEX, NOT FREE") {
			t.Errorf("remedy recommends folding without stating where the cost lands:\n%s", got)
		}
	}
}

// TestParityRemedy_TellsTheReaderToCheckTheNoteIsStillTrue is a guard on the
// step this remedy used to omit entirely (mg-5e29).
//
// Every sentence here used to be about COST, HEADROOM, or where a future reader
// would look. None of them said to read the note. So the fastest correct-looking
// way to discharge a parity finding was to add routes to notes nobody had opened
// — which is what happened: 11 orphans indexed in one pass, one of them
// resurfacing hours later as a 'memory index staleness' finding on the same file.
//
// The instruction has to appear in BOTH arithmetic branches. The constrained
// branch is if anything the more exposed one, since the remedy it leads with is
// the sub-index, and a sub-index publishes a whole set of unread notes in a
// single line.
func TestParityRemedy_TellsTheReaderToCheckTheNoteIsStillTrue(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"affordable", ParityRemedy(2, 9000, 200)},
		{"constrained", ParityRemedy(6, 300, 200)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.got, verifyBeforeHooking) {
				t.Errorf("remedy never tells the reader to check the note is still true:\n%s", tc.got)
			}
		})
	}
}

// TestVerifyBeforeHooking_CoversEveryRemedyThatPublishesTheNote is the
// discrimination control. Scoping the instruction to hooking alone would leave
// the two remedies this file leads with when the hooks do not fit — sub-index
// and fold — with no verify step, and those are exactly the bulk cases: one
// sub-index line buys reachability for as many notes as it names.
//
// It also pins the naming of the CONSEQUENCE. The rule alone ("read the note")
// reads as hygiene advice; the reason it earns space in already-long text is
// that the same report carries a staleness axis which will report the damage as
// an unrelated finding, with nothing linking it back to here.
func TestVerifyBeforeHooking_CoversEveryRemedyThatPublishesTheNote(t *testing.T) {
	for _, want := range []string{"hooking", "sub-index", "folding"} {
		if !strings.Contains(verifyBeforeHooking, want) {
			t.Errorf("verify instruction does not reach the %q remedy, which publishes the note just as much:\n%s", want, verifyBeforeHooking)
		}
	}
	if !strings.Contains(verifyBeforeHooking, "staleness") {
		t.Errorf("verify instruction does not name the finding this one manufactures when discharged at speed:\n%s", verifyBeforeHooking)
	}
	// REACHABLE vs TRUE is the distinction the whole sentence exists to draw,
	// and the header's "CORRECTNESS property" is what blurs it.
	for _, want := range []string{"REACHABLE", "TRUE"} {
		if !strings.Contains(verifyBeforeHooking, want) {
			t.Errorf("verify instruction does not draw the %q distinction it exists for:\n%s", want, verifyBeforeHooking)
		}
	}
}

// TestFoldHostNote_FiresOnlyOnDisproportion. The host-size clause rides with the
// fold advice rather than being its own check: it is not a defect, it is the
// price of the remedy being recommended in the same sentence. So it must be
// silent on an ordinary store, or it becomes noise attached to every parity warn.
func TestFoldHostNote_FiresOnlyOnDisproportion(t *testing.T) {
	if got := FoldHostNote("host.md", 900, 20000, 0); got != "" {
		t.Errorf("host clause fired with no note larger than the index: %q", got)
	}
	if got := FoldHostNote("", 0, 20000, 3); got != "" {
		t.Errorf("host clause fired with no note named: %q", got)
	}
	got := FoldHostNote("world-state-claims-decay.md", 30414, 23952, 2)
	if !strings.Contains(got, "world-state-claims-decay.md") {
		t.Errorf("host clause does not name the largest host:\n%s", got)
	}
	if !strings.Contains(got, "30414") || !strings.Contains(got, "23952") {
		t.Errorf("host clause states the disproportion without the two numbers that show it:\n%s", got)
	}
}
