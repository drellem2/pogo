package memcheck

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// THE PARITY/SIZE COMPETITION — two individually-correct findings that, near the
// cap, are jointly unsatisfiable AS PRESENTED (mg-1b2f).
//
// Parity is a CORRECTNESS property (every note reachable). Size is a BUDGET
// property. They are in direct competition, because every unit of parity is
// bought with index lines and index lines are exactly what the budget is short
// of. Reporting them side by side implies both can be satisfied; near the cap
// they cannot, so a reader resolves the tension in whichever direction the diff
// is cheaper — and the cheap direction is ALWAYS to skip the hook. The size
// check then goes green while the note stays unreachable, and the composite
// reads as compliance. A green size check plus six unreachable notes looks like
// a tidy directory.
//
// MEASURED, on the corpus this ships against. Adding one hook cost ~200-224
// chars (two independent agents, their own indexes). Compaction cannot fund it:
// one careful pass over an already-compacted index recovered 71 chars, and five
// compressions of the entries judged most verbose recovered ~190 — against ~200
// to add a single line. The cheap gains were harvested in an earlier pass and do
// not come back. So "compact to make room for the hook" is not an available move
// at this size, and a reader who tries it does real, careful, measured work that
// buys nothing.
//
// THE FACT THAT MAKES THE DILEMMA FALSE, and which was written down nowhere
// until this file: the cap applies to the INDEX ALONE. Verified directly — the
// budget constant this package pins is documented as the characters of
// MEMORY.md the harness injects at session start, Locate globs MEMORY.md and
// nothing else, and an agent's session context contains its MEMORY.md verbatim
// with no note bodies. Note content is read on demand. So a note's BODY is free
// against the cap and only its INDEX LINE is scarce, which turns one forced
// dilemma into a choice of three remedies where the checker used to imply only
// the expensive one:
//
//	ADD A HOOK   costs one index line. Viable only with headroom.
//	FOLD         append the content to an already-hooked note and delete the
//	             standalone file. Satisfies parity by removing the orphan
//	             rather than by pointing at it. ZERO index cost.
//	RE-ROUTE     move the note to a less-pressured index if it belongs to one
//	             (an agent-scoped dir rather than the shared one). Zero cost to
//	             the pressured file.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO — suppress the parity finding near the
// cap. That would reproduce the exact defect parity was built to catch: an
// unindexed note would once again make the index look HEALTHIER. The finding
// stays; only what it SAYS changes.
//
// It also does not tell anyone to reorder the index. Tail-first truncation is
// measured, but a remedy built on it is a bet on the truncation MECHANISM
// staying as measured, and it inverts into active harm if the mechanism is ever
// re-measured or changed by the harness. Folding is direction-agnostic: it
// recovers characters whichever end truncates and no later correction can
// invalidate it.

// DefaultIndexLineCostChars is the fallback cost of one index line, in
// characters, used only when an index has no hook lines to measure.
//
// A pinned constant is the WORSE instrument here and is why IndexLineCostChars
// measures the index itself instead: hook length is a property of a particular
// corpus's writing style, so a global number is wrong for every index that is
// not the one it was fitted on. Measured medians across the two pressured
// indexes on the development box were 146 and 187 characters, while agents
// reported ~200-224 for hooks they had just written — fresh hooks run longer
// than the compacted corpus mean.
//
// The value sits at the high end of that spread on purpose. This number feeds a
// "do the hooks fit" estimate, and the two errors are not symmetric: saying they
// fit when they do not sends a reader to grow past the cap, while saying they do
// not fit when they would sends them to fold, which is a correct action anyway.
const DefaultIndexLineCostChars = 200

// IndexLineCostChars estimates what ONE more index line would cost this index,
// in characters, by measuring the median length of the hook lines it already
// has (the newline included, since a new line brings its own).
//
// Measured per-index rather than pinned, so it cannot rot: the file being
// checked is itself the authority on what its lines cost. The median rather than
// the mean, so one runaway paragraph-length hook does not set the price of a
// normal one.
//
// Hook lines are identified by carrying a markdown link to a .md target, which
// is the same notion of "the index names this note" that parity uses. An index
// with none — a fresh or unconventional one — falls back to
// DefaultIndexLineCostChars.
func IndexLineCostChars(data []byte) int {
	var lens []int
	for _, raw := range strings.Split(string(data), "\n") {
		if !hasMarkdownNoteLink(raw) {
			continue
		}
		lens = append(lens, utf8.RuneCountInString(raw)+1)
	}
	if len(lens) == 0 {
		return DefaultIndexLineCostChars
	}
	sort.Ints(lens)
	return lens[len(lens)/2]
}

// hasMarkdownNoteLink reports whether a line carries a `](something.md)` link
// target — the canonical shape of an index hook.
func hasMarkdownNoteLink(line string) bool {
	for off := 0; ; {
		i := strings.Index(line[off:], "](")
		if i < 0 {
			return false
		}
		at := off + i + 2
		j := strings.Index(line[at:], ")")
		if j < 0 {
			return false
		}
		if strings.HasSuffix(line[at:at+j], ".md") {
			return true
		}
		off = at
	}
}

// HeadroomChars is how many characters this index can still grow before the
// session-start auto-inject cap truncates it. Negative once already over.
func (r Result) HeadroomChars() int { return r.CapChars - r.Chars }

// AsymmetryNote states the single fact that makes the parity/size dilemma false.
// It is emitted on every parity warn, not only pressured ones, because a reader
// who does not know it will resolve the tension the wrong way the first time
// they meet it — which is exactly how it went unstated until mg-1b2f.
const AsymmetryNote = "INDEX LINES ARE CAPPED; NOTE BODIES ARE NOT. The cap measured by 'memory index size' applies to MEMORY.md alone — note content is read on demand and never auto-injected, so a note's body is free against it and only its index line is scarce."

// foldStandard is the acceptance test a fold must meet. It is stated wherever
// folding is recommended because the obvious success criterion is the WRONG one,
// and a fold judged by the wrong one is worse than leaving the orphan alone.
const foldStandard = "A fold is judged by REACHABILITY, not by the diff. The test is NOT 'the file is gone and the index did not grow' — it is 'a reader looking for this content arrives at it'. Choose the host by where a future reader would go looking, not by which note is topically nearest, and merge the content into the host's subject rather than appending it as an unrelated tail. A fold into a plausible-but-wrong host converts a LOUD, LOCAL problem (an orphan you can enumerate) into a SILENT one (content buried under a hook that does not advertise it), so a careless fold is worse than no fold. This is cheap in characters and expensive in judgement: schedule it, do not do it in passing."

// neverDropTheHook is the instruction that closes off the cheap-diff escape. The
// whole failure this guidance exists to prevent is that skipping the hook is the
// smallest diff that turns a check green.
const neverDropTheHook = "Do NOT resolve this by leaving a note unindexed to keep 'memory index size' green. That is the failure THIS axis exists to catch, and it reports as compliance: a green size check beside unreachable notes looks like a tidy directory."

// ParityRemedy renders the remedy guidance for a parity defect: unreferenced
// notes on an index with headroomChars characters left before the auto-inject
// cap, where one more index line costs about lineCostChars.
//
// It names all three remedies in every case, because fold and re-route are
// zero-cost and frequently the BETTER answer even with headroom to spare — a
// note that belongs in an agent-scoped index was misfiled, not merely unhooked.
// What the arithmetic changes is the ORDER and the framing: once the hooks
// demonstrably do not fit, "add a hook for each" stops being offered as the
// remedy and is stated plainly as unavailable, so the reader is not handed two
// instructions that cannot both be followed.
func ParityRemedy(unreferenced, headroomChars, lineCostChars int) string {
	if unreferenced <= 0 {
		return ""
	}
	if lineCostChars <= 0 {
		lineCostChars = DefaultIndexLineCostChars
	}
	cost := unreferenced * lineCostChars
	fits := headroomChars / lineCostChars
	if fits < 0 {
		fits = 0
	}

	var b strings.Builder
	b.WriteString(AsymmetryNote)
	b.WriteString(" ")

	if cost <= headroomChars {
		fmt.Fprintf(&b, "Hooks for all %d would cost ~%d chars against %d chars of headroom (~%d chars/line, measured from this index's own hooks), so they fit. Three remedies, and the cheapest is not always the hook: (1) ADD A HOOK for each note that earns a permanent slot in every session's context; (2) FOLD a note whose content belongs with an already-hooked note — append it there and delete the standalone file, satisfying parity by removing the orphan rather than pointing at it, at ZERO index cost; (3) RE-ROUTE a note that belongs to a less-pressured index (an agent-scoped dir rather than the shared one), also at zero cost here. ",
			unreferenced, cost, headroomChars, lineCostChars)
	} else {
		fmt.Fprintf(&b, "ADD A HOOK IS NOT AVAILABLE FOR ALL OF THESE: %d hooks would cost ~%d chars against %d chars of headroom (~%d chars/line, measured from this index's own hooks) — room for about %d. Compaction will not fund the rest; on an already-compacted index one careful pass recovered 71 chars and five compressions recovered ~190, against ~%d to add one line. Use the ZERO-COST remedies first: (1) FOLD — append the content to an already-hooked note and delete the standalone file, satisfying parity by removing the orphan rather than pointing at it; (2) RE-ROUTE — move a note that belongs to a less-pressured index (an agent-scoped dir rather than the shared one). (3) Spend the remaining %d hook(s) on the notes that most need a permanent slot in every session's context. ",
			unreferenced, cost, headroomChars, lineCostChars, fits, lineCostChars, fits)
	}

	b.WriteString(foldStandard)
	b.WriteString(" ")
	b.WriteString(neverDropTheHook)
	return b.String()
}

// SizeParityTension renders the clause appended to a SIZE warn when the same
// index also has unreferenced notes. Empty when it does not.
//
// The size axis needs this because its own advice — compact — reads as "shrink,
// by any means" to a reader who is simultaneously being told to add hooks. The
// cheapest way to shrink is to drop hooks, and that is the one move that must
// not be made. Naming the competition on BOTH sides is the point: a reader who
// sees only one of the two warns must still be able to tell that the other
// exists and that the obvious resolution is the wrong one.
func SizeParityTension(unreferenced, lineCostChars int) string {
	if unreferenced <= 0 {
		return ""
	}
	if lineCostChars <= 0 {
		lineCostChars = DefaultIndexLineCostChars
	}
	return fmt.Sprintf("PARITY ALSO FIRES ON THIS INDEX (%d unreferenced note(s)), and the two findings pull in opposite directions: reachability is bought with index lines, and index lines are what this budget is short of. Compacting to make room does not work at this size — a careful pass over an already-compacted index recovered 71 chars, and five compressions recovered ~190, against ~%d to add one line. So do NOT shrink this file by dropping hooks or by leaving the notes unindexed: that is the smallest diff that turns THIS check green, and it turns it green by abandoning reachability. See the 'memory index parity' warn for the zero-char remedies (fold, re-route) — note BODIES are free against this cap, only index LINES are scarce. If those do not apply, what is left is a corpus-policy question — which notes earn a permanent slot in every session's context and which should be found on demand — and that belongs to whoever owns the corpus, not to this checker.",
		unreferenced, lineCostChars)
}
