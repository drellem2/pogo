package memcheck

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The session-start auto-injection measurement (mg-9a89).
//
// WHY IT NEEDED A SEPARATE MEASUREMENT. mg-b938 established the harness READ
// TOOL budget rigorously — an over-cap file, the refusal quoting both numbers,
// an under-cap file served whole — and scoped its finding honestly, noting that
// the auto-inject path was NOT verified. That note was the whole gap: MEMORY.md
// reaches an agent through auto-injection, once, before its first turn. The
// read tool is a path an agent takes only if it opens the file by hand. So the
// detector was calibrated against a path nobody relies on.
//
// HOW IT WAS MEASURED. Auto-injection fires once per session and cannot be
// re-triggered from inside a running one, so a fresh session IS the instrument.
// Fixtures were staged in a SCRATCH memory directory — never a live agent's,
// since a truncation test on real memories risks the exact loss it measures —
// and a throwaway session was spawned per fixture. Each probe was asked for the
// entry COUNT and the LAST entry VERBATIM, then compared against disk. It was
// never asked whether its context "looked complete": a truncated view cannot
// show what was cut, so self-assessment of completeness is worthless here and
// count-vs-disk is the only reliable signal.
//
// WHAT WAS OBSERVED.
//
//	prose fixture   800 entries, 144816 chars / 146416 bytes
//	  -> injected entries 1..138 only; probe reported COUNT=138,
//	     LAST="- [Memory item 00138]...ENTRY-00138-END"
//	  -> NOTICE: "> WARNING: MEMORY.md is 802 lines and 141.4KB. Only part of
//	     it was loaded. Keep index entries to one line under ~200 chars; move
//	     detail into topic files."
//
//	dense fixture   800 entries, 144816 chars, IDENTICAL character geometry but
//	                ~2x the token density (punctuation/hex, ~1.3 chars/token
//	                against the prose fixture's ~2.6)
//	  -> cut at the SAME line: COUNT=138, LAST="- [D00138]..."
//
//	  This pair is the discriminator. A TOKEN budget cannot cut two different
//	  token densities at the same character offset. A BYTE budget is excluded
//	  too: 138 prose entries is 25270 bytes (multi-byte em dashes), overshooting
//	  any 25000-byte reading, while the same prefix is 24994 CHARACTERS.
//
//	boundary        25001 chars -> loaded whole, NOTICE=NONE
//	                25100 chars -> truncated, and the harness NAMED ITS LIMIT:
//	  "> WARNING: MEMORY.md is 24.5KB (limit: 24.4KB) — index entries are too
//	   long. Only part of it was loaded."
//	  24.4 KiB = 24.4*1024 = 24986 ≈ 25000, and the sizes the harness reports
//	  are the file's CHARACTER count over 1024, not its byte count (144816
//	  chars / 1024 = 141.4, exactly the "141.4KB" it printed for a 146416-byte
//	  file). The limit is stated by the harness, not inferred from a file that
//	  happened to fail.
//
// THE TWO CONSEQUENCES.
//
//  1. Auto-injection truncates VISIBLY. It prepends a warning naming the size
//     and the limit. This is the less alarming of the two possible outcomes —
//     had it been silent, the finding would have been more serious than the
//     read path's, which at least errors. It is still a real loss: the entries
//     past the cut are simply absent, and an agent cannot notice an absence.
//     The warning is addressed to whoever reads the transcript, not to a
//     process that can act on it.
//
//  2. The budgets are 25000 of DIFFERENT THINGS, and the tighter one binds.
//     25000 tokens of index prose is ~65000 characters, so auto-injection cuts
//     ~2.6x sooner than the read cap for the content MEMORY.md is made of.
//
// WarnFraction was re-checked against this budget and kept at 0.8 — see
// AutoInjectWarnThresholdChars for why the same fraction carries a different
// justification on an exactly-counted budget.

// measuredAutoInject records the prose-fixture observation above.
var measuredAutoInject = struct {
	totalEntries    int
	totalChars      int
	totalBytes      int
	injectedEntries int
	injectedChars   int
}{
	totalEntries:    800,
	totalChars:      144816,
	totalBytes:      146416,
	injectedEntries: 138,
	injectedChars:   24994,
}

// probeEntry reproduces one line of the prose fixture used in the measurement,
// character-for-character, so the arithmetic below is checked against the real
// geometry rather than a remembered summary.
func probeEntry(i int) string {
	return "- [Memory item " + pad5(i) + "](item-" + pad5(i) + ".md) — synthetic probe entry number " + pad5(i) +
		" carrying filler prose so that each index line costs a realistic number of tokens; sentinel ENTRY-" + pad5(i) + "-END\n"
}

func pad5(i int) string {
	s := ""
	for _, d := range []int{10000, 1000, 100, 10, 1} {
		s += string(rune('0' + (i/d)%10))
	}
	return s
}

func proseFixture(n int) string {
	var b strings.Builder
	b.WriteString("# Memory index\n\n")
	for i := 1; i <= n; i++ {
		b.WriteString(probeEntry(i))
	}
	return b.String()
}

// TestMeasuredFixtureGeometryHolds guards the arithmetic the constant rests on.
// If the reproduced fixture no longer has the measured character and byte
// counts, the recorded observation no longer describes it and the constant it
// supports is unanchored.
func TestMeasuredFixtureGeometryHolds(t *testing.T) {
	full := proseFixture(measuredAutoInject.totalEntries)
	if got := utf8.RuneCountInString(full); got != measuredAutoInject.totalChars {
		t.Fatalf("fixture drifted: %d chars, want the measured %d", got, measuredAutoInject.totalChars)
	}
	if got := len(full); got != measuredAutoInject.totalBytes {
		t.Fatalf("fixture drifted: %d bytes, want the measured %d", got, measuredAutoInject.totalBytes)
	}

	prefix := proseFixture(measuredAutoInject.injectedEntries)
	if got := utf8.RuneCountInString(prefix); got != measuredAutoInject.injectedChars {
		t.Fatalf("the injected prefix is %d chars, want the measured %d", got, measuredAutoInject.injectedChars)
	}
}

// TestConstantMatchesTheObservedCut is the calibration check: the injected
// prefix must be the largest whole-line prefix that fits under the constant,
// and one more entry must not fit. That is a two-sided pin — a constant set too
// low or too high fails a different half of it.
func TestConstantMatchesTheObservedCut(t *testing.T) {
	kept := utf8.RuneCountInString(proseFixture(measuredAutoInject.injectedEntries))
	oneMore := utf8.RuneCountInString(proseFixture(measuredAutoInject.injectedEntries + 1))

	if kept > HarnessAutoInjectCapChars {
		t.Fatalf("the harness INJECTED %d chars but the constant claims a %d-char cap — the constant is too LOW and the detector will cry cliff on indexes that load fine",
			kept, HarnessAutoInjectCapChars)
	}
	if oneMore <= HarnessAutoInjectCapChars {
		t.Fatalf("the harness DROPPED entry %d at %d chars, which the constant's %d-char cap says should have fit — the constant is too HIGH and the detector will stay silent through real truncation",
			measuredAutoInject.injectedEntries+1, oneMore, HarnessAutoInjectCapChars)
	}
	t.Logf("observed cut at %d chars (entry %d kept), next entry would reach %d — both sides of the %d-char constant",
		kept, measuredAutoInject.injectedEntries, oneMore, HarnessAutoInjectCapChars)
}

// TestPositiveControl_FiresOnTheMeasuredTruncatedIndex: the detector must go red
// on the exact index the harness was OBSERVED to truncate. A detector that
// cannot be made to fire on a demonstrated real-world failure has been made
// plausible, not calibrated — and this is the specific failure it exists for.
func TestPositiveControl_FiresOnTheMeasuredTruncatedIndex(t *testing.T) {
	body := []byte(proseFixture(measuredAutoInject.totalEntries))
	r := Check("MEMORY.md", body)
	if !r.ApproachingAutoInject {
		t.Fatalf("POSITIVE CONTROL FAILED: the harness injected only %d of this index's %d entries, but the detector stayed silent (%d chars vs threshold %d)",
			measuredAutoInject.injectedEntries, measuredAutoInject.totalEntries, r.Chars, r.ThresholdChars)
	}
	if len(r.FattestLines) == 0 {
		t.Fatal("fired but named no heavy lines; the warn must give the fix a target")
	}
	lostEntries := measuredAutoInject.totalEntries - measuredAutoInject.injectedEntries
	t.Logf("measured truncation: %d of %d entries (%.0f%%) never injected; detector RED as required",
		lostEntries, measuredAutoInject.totalEntries, 100*float64(lostEntries)/float64(measuredAutoInject.totalEntries))
}

// TestNegativeControl_SilentOnTheMeasuredWholeLoadedIndex is the other side,
// and it uses the other measured observation: the 25001-character index that
// auto-injected whole with no notice. The detector warns before the cliff by
// design, so it does NOT have to be silent at 25001 — but it must be silent on
// an index comfortably below the warn point, or the warn is noise.
func TestNegativeControl_SilentOnTheMeasuredWholeLoadedIndex(t *testing.T) {
	// Half the warn threshold: unambiguously healthy on the character budget.
	n := 1
	for utf8.RuneCountInString(proseFixture(n+1)) < AutoInjectWarnThresholdChars()/2 {
		n++
	}
	body := []byte(proseFixture(n))
	r := Check("MEMORY.md", body)
	if r.ApproachingAutoInject {
		t.Fatalf("FALSE ALARM: %d chars is well under the %d-char warn threshold, but the detector fired", r.Chars, r.ThresholdChars)
	}
	t.Logf("healthy index: %d entries, %d chars vs %d-char threshold — detector GREEN as required", n, r.Chars, r.ThresholdChars)
}

// TestWarnPointLeavesRoomToCompact re-checks WarnFraction against the auto-inject
// budget, which is what mg-9a89's acceptance asks for. The character count is
// exact, so unlike the token path there is no estimator error to absorb; what
// the headroom has to buy is time to compact deliberately before entries start
// disappearing. Assert it is a usable amount of room, not a token gesture.
func TestWarnPointLeavesRoomToCompact(t *testing.T) {
	headroom := HarnessAutoInjectCapChars - AutoInjectWarnThresholdChars()
	if headroom != 5000 {
		t.Fatalf("headroom between the warn point and the cliff is %d chars, want 5000 (WarnFraction %.2f of %d)",
			headroom, WarnFraction, HarnessAutoInjectCapChars)
	}
	// A typical index entry is one line under ~200 chars — the harness's own
	// advice in its truncation warning. The headroom should be worth a
	// meaningful number of them, so a warn is actionable rather than a
	// notification that it is already too late.
	const typicalEntryChars = 200
	if entries := headroom / typicalEntryChars; entries < 20 {
		t.Fatalf("warn fires only %d typical entries before the cliff — too late to compact deliberately", entries)
	}
	t.Logf("warn at %d chars, cliff at %d: %d chars of headroom, ~%d typical index entries",
		AutoInjectWarnThresholdChars(), HarnessAutoInjectCapChars, headroom, headroom/typicalEntryChars)
}
