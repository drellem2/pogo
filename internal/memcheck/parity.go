package memcheck

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// INDEX/FILE PARITY — the second of three ways an auto-memory store fails, and
// the one a size check reads BACKWARDS.
//
// A memory store is two artifacts that must agree: the note files on disk, and
// the MEMORY.md index that is the only thing loaded into context at session
// start. Recall starts at the index. A note NO ROUTE arrives at is written,
// costs disk, and is unreachable — the agent that wrote it cannot recall it, and
// neither can anyone else.
//
// "NO ROUTE" IS NOT "NOT IN MEMORY.md", and conflating the two is what this file
// used to do (mg-d97f). The index is where recall STARTS, not where it ends: a
// note the index does not name is still reachable if an indexed note names it,
// and a sub-index hooked from MEMORY.md makes everything IT names reachable for
// the price of one line. Measured on the shared corpus this ships against, at
// the moment the conflation was found:
//
//	237 notes
//	195 named by MEMORY.md directly
//	 42 reachable only through another note — 28 of them via ONE hooked sub-index
//	  0 unreachable by any route
//
// All 42 were reported as "on disk and unreachable by recall: nothing points at
// them". Something pointed at every one of them. A check that enumerates 42
// non-defects is a check that gets tuned out, and it took the number straight
// into a corpus-policy argument about whether reachability and the size cap were
// jointly satisfiable — an argument that did not need to happen, because nothing
// was unreachable.
//
// So the populations are separated and both are reported:
//
//	UNREACHABLE  no route from the index at all.  THE DEFECT.
//	INDIRECT     reachable, but only via another note. NOT a defect — a
//	             question about PROMINENCE, which is a real property worth
//	             tracking and a different one from reachability.
//
// The three tiers differ in discovery cost, not in reachability: MEMORY.md is
// auto-injected and surfaces unprompted; a sub-index is one hop and needs a
// reason to look; a link-only note needs the reader to already be in the
// neighbourhood. Deciding which notes earn the auto-injected tier is the corpus
// owner's judgement and is not a thing this checker can compute — but it must
// not be reported in the vocabulary of unreachability, which is a defect.
//
// Why the size check cannot find this. All three known failures of a memory
// store are invisible from inside the session that has them:
//
//	OVER-CAP TRUNCATION  invisible because the dropped entries never arrive.  <- Check/CheckFile
//	AN UNINDEXED NOTE    invisible because nothing points at it.             <- THIS FILE
//	A STALE HOOK         invisible because it reads as current.              <- staleness.go
//
// The middle one is not merely unmeasured by a size check: a size check reads it
// as an IMPROVEMENT. Dropping a hook makes MEMORY.md smaller, so the instrument
// moves the wrong way — it reports progress at the moment a note becomes
// unrecallable. That is the reason parity is a separate predicate rather than a
// refinement of the byte/token math.
//
// Measured across all 16 memory dirs on the development box before this check
// existed (mg-cb71): 16 unreferenced notes in 3 dirs, the other 13 at exact
// parity. Two of them mattered concretely — an agent had written two notes the
// same night and would have failed to recall its own freshest guidance, and a
// 1352-byte note describing mayor.md git drift sat unrecallable on disk while
// that exact drift was being re-diagnosed from scratch.
//
// DO NOT REPORT THIS AS A BARE COUNT. A noisy check gets ignored, and this one
// has legitimate false positives by construction — see UnindexedMarker.

// UnindexedMarker is the frontmatter key/value pair a note uses to declare that
// it is DELIBERATELY absent from the index, and that parity should stay silent
// about it.
//
// The opt-out is not a convenience. Deliberate non-indexing is a CORRECT action
// that produces a parity "defect", and both shapes were found in the wild:
//
//   - A hook was removed on purpose because it asserted an open review for an
//     item that had since been archived. Keeping the file and dropping the hook
//     was the right call — a stale hook is worse than no hook (which is what
//     staleness.go is about). Parity would flag the repair.
//   - A 127KB working scratch queue lives in the memory dir because that is
//     where the agent's sweep looks for it. It is not a recall note and should
//     never have a hook.
//
// Without an opt-out those two arrive as permanent warns, the check gets tuned
// out, and the 14 real ones go with it.
//
// GREPPABLE BY DESIGN. The marker must be findable without reading every note
// in full — `grep -rl 'index: none' <dir>` answers it in one pass, and this
// package reads only a bounded frontmatter prefix (see frontmatterScanLimit)
// rather than whole files. That bound is load-bearing, not tidiness: the scratch
// queue that motivated the opt-out is 127KB, and a checker that read it whole to
// discover four bytes of intent would cost more than the check is worth.
//
// It lives in the note's own frontmatter rather than in a per-directory ignore
// list so it CANNOT DRIFT: a note carries its own opt-out when it is moved or
// copied, and deleting the note deletes the declaration with it. A sidecar list
// would need its own parity check.
const UnindexedMarker = "index: none"

// frontmatterScanLimit bounds how much of a note is read to find its
// frontmatter. A frontmatter block is a few hundred bytes; 4KB is generous
// headroom while keeping the cost of checking a directory independent of how
// large its largest note is.
const frontmatterScanLimit = 4096

// ParityResult is the outcome of comparing one MEMORY.md against the note files
// beside it.
type ParityResult struct {
	// IndexPath is the MEMORY.md that was read; Dir is the directory it and the
	// notes share.
	IndexPath string
	Dir       string
	// Notes is how many *.md files were considered — every sibling except
	// MEMORY.md itself.
	Notes int
	// Direct is how many notes the index names itself. These sit on the
	// auto-injected tier: they surface without the reader asking.
	Direct int
	// Unreachable holds the basenames no route arrives at, sorted. These are the
	// actionable defects: written, on disk, unrecallable — the agent that wrote
	// them cannot get back to them either.
	Unreachable []string
	// Indirect holds the basenames the index does not name but that a reader
	// still arrives at, by following links from a note it does name (a hooked
	// sub-index is the common and cheapest case). NOT defects. Reported because
	// prominence is worth tracking, never counted as unreachability.
	Indirect []string
	// OptedOut holds the basenames that declared UnindexedMarker. They are
	// reported separately from Unreachable — they are NOT defects, and counting
	// them as such is what makes a parity check too noisy to keep.
	OptedOut []string
	// InParity is true when nothing is unreachable without having opted out.
	InParity bool

	// IndexBytes, FattestNote and FattestNoteBytes measure where FOLDING sends
	// the cost. A fold is free against the index cap and is therefore what a
	// margin policy incentivises — but the content still has to live somewhere,
	// and it lands in the host. Nothing else in this package counts host size,
	// so repeated folding moves a findability problem from the index down one
	// level to where no instrument is pointed, and the parity number goes DOWN
	// as it happens, which reads as improvement. See foldCostsTheHost.
	IndexBytes       int
	FattestNote      string
	FattestNoteBytes int
	// NotesOverIndex is how many notes are individually larger than the whole
	// index that reaches them — the tell that folding has been the cheap move
	// for a while. On the corpus this ships against it was 2 at the moment the
	// pattern was noticed, both hosts grown by folds taken to avoid paying an
	// index line.
	NotesOverIndex int
}

// CheckParity reads the index at indexPath and the *.md notes beside it, and
// reports which of them no route from the index arrives at.
//
// WHAT "REACHABLE" MEANS HERE, stated because this function's whole subject is
// an instrument that was named for a property wider than the one it measured.
// This measures LINK-GRAPH reachability from the index: a chain of mentions
// starting at MEMORY.md and ending at the note. That is strictly narrower than
// "a reader arrives" — the reader still has to notice the hook, open the note,
// and notice the link, and each hop costs attention. It is also, deliberately,
// permissive about the form of a mention, so a note named only in passing inside
// a reachable note counts as reached. So a clean result here means "no note is
// cut off", NOT "every note is easy to find"; the second is the prominence
// question, which is reported separately and is a judgement, not a measurement.
//
// It is read-only: it never writes the index and never writes a note. Repairing
// parity means either adding a hook or declaring the opt-out, and both are
// judgment calls about what belongs in a shared durable record — the same reason
// Check never rewrites MEMORY.md (see mg-15c0). A checker that auto-appended
// hooks would happily index a 127KB scratch queue.
func CheckParity(indexPath string) (ParityResult, error) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return ParityResult{}, err
	}
	dir := filepath.Dir(indexPath)
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return ParityResult{}, err
	}
	res := ParityResult{IndexPath: indexPath, Dir: dir}
	indexBase := filepath.Base(indexPath)

	// The population: every sibling note. Held as a set so link extraction can
	// resolve a mention to a real file in one lookup rather than searching each
	// note's text once per candidate filename.
	res.IndexBytes = len(data)
	exists := make(map[string]bool, len(paths))
	for _, p := range paths {
		base := filepath.Base(p)
		if base == indexBase {
			continue
		}
		exists[base] = true
		res.Notes++
		// Host size, measured by stat rather than by reading: the point is
		// how big folding has made these, and that is a property of the file
		// rather than of its content.
		fi, serr := os.Stat(p)
		if serr != nil {
			continue
		}
		n := int(fi.Size())
		if n > res.FattestNoteBytes {
			res.FattestNote, res.FattestNoteBytes = base, n
		}
		if n > res.IndexBytes {
			res.NotesOverIndex++
		}
	}

	direct := noteLinks(string(data), exists)
	res.Direct = len(direct)

	// Transitive reachability. The frontier starts at what the index names and
	// expands through the links of notes ALREADY REACHED — which is why an
	// unreachable note's body is never opened: a 127KB scratch queue nothing
	// points at costs one Glob entry and no read. The bound on this walk falls
	// out of the predicate rather than being imposed on it, so unlike the
	// frontmatter scan there is no size limit to trip over and no link near the
	// end of a long note that goes silently unfollowed.
	reached := make(map[string]bool, len(direct))
	queue := make([]string, 0, len(direct))
	for base := range direct {
		reached[base] = true
		queue = append(queue, base)
	}
	for len(queue) > 0 {
		cur := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		body, rerr := os.ReadFile(filepath.Join(dir, cur))
		if rerr != nil {
			// An unreadable note cannot be traversed. Its own reachability is
			// unaffected; what is lost is anything reachable ONLY through it,
			// which then reports as unreachable. That is the safe direction:
			// a visible defect rather than a silent pass.
			continue
		}
		for base := range noteLinks(string(body), exists) {
			if reached[base] {
				continue
			}
			reached[base] = true
			queue = append(queue, base)
		}
	}

	for base := range exists {
		if direct[base] {
			continue
		}
		if reached[base] {
			res.Indirect = append(res.Indirect, base)
			continue
		}
		if declaresUnindexed(filepath.Join(dir, base)) {
			res.OptedOut = append(res.OptedOut, base)
			continue
		}
		res.Unreachable = append(res.Unreachable, base)
	}
	sort.Strings(res.Unreachable)
	sort.Strings(res.Indirect)
	sort.Strings(res.OptedOut)
	res.InParity = len(res.Unreachable) == 0
	return res, nil
}

// noteLinks returns the subset of exists that text mentions, in any of the three
// forms a corpus actually uses.
//
// It scans the text ONCE and looks each candidate up, rather than searching the
// text once per candidate filename. That is not micro-optimisation: reachability
// reads note bodies, and the pairwise form is quadratic in the size of the
// corpus times the size of its notes — on the 237-note store this ships against
// it would scan hundreds of megabytes to answer a question about a few thousand
// links.
//
// The three forms, all treated as equivalent because the question is "can recall
// arrive here" and every one of them gets a reader there:
//
//	](a-note.md)     the canonical index hook and inline markdown link
//	[[a-note]]       the wikilink the corpus uses BETWEEN notes; the `.md` is
//	                 implied, and `|alias` / `#section` suffixes are stripped
//	a-note.md        a bare mention in prose, including "superseded by X"
//
// The wikilink form is why this replaced a filename-containment test. Notes link
// to each other almost exclusively as `[[slug]]`, with no `.md` anywhere in the
// string, so a containment test could not see the corpus's own link graph at
// all — it read every one of those edges as absent.
func noteLinks(text string, exists map[string]bool) map[string]bool {
	found := make(map[string]bool)
	if len(exists) == 0 {
		return found
	}
	// Bare and markdown-target mentions: find each ".md" and walk back over the
	// bytes that could belong to a filename. The trailing boundary matters as
	// much as the leading one — `a-note.mdx` names no note.
	for off := 0; ; {
		i := strings.Index(text[off:], ".md")
		if i < 0 {
			break
		}
		at := off + i
		end := at + len(".md")
		off = end
		if end < len(text) && isNameByte(text[end]) {
			continue
		}
		start := at
		for start > 0 && isNameByte(text[start-1]) {
			start--
		}
		if cand := text[start:end]; exists[cand] {
			found[cand] = true
		}
	}
	// Wikilinks.
	for off := 0; ; {
		i := strings.Index(text[off:], "[[")
		if i < 0 {
			break
		}
		at := off + i + 2
		j := strings.Index(text[at:], "]]")
		if j < 0 {
			break
		}
		off = at + j + 2
		target := text[at : at+j]
		if k := strings.IndexAny(target, "|#"); k >= 0 {
			target = target[:k]
		}
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if !strings.HasSuffix(target, ".md") {
			target += ".md"
		}
		if exists[target] {
			found[target] = true
		}
	}
	return found
}

// references reports whether text names base anywhere, in any of the forms
// noteLinks accepts.
//
// Deliberately PERMISSIVE about form. The canonical hook is a markdown link,
// `- [Title](base.md) — hook`, but an index line may name a note in prose, in a
// "superseded by" note, or inside a parenthetical, and notes name each other as
// `[[base]]`. A parity check exists to answer "can recall reach this note", and
// a mention in any form means yes.
//
// The boundary checks are the one place strictness is required: a bare
// strings.Contains would let a reference to `xa.md` satisfy a note named `a.md`,
// and one to `a-note.mdx` satisfy `a-note.md`. A match must be flanked on both
// sides by something that cannot belong to a filename — which is also why
// `feedback_drive.md` is not satisfied by a reference to
// `feedback_drive_dont_ask.md`, nor the reverse.
func references(text, base string) bool {
	return noteLinks(text, map[string]bool{base: true})[base]
}

// isNameByte reports whether c could be part of a memory-note filename.
func isNameByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_', c == '-', c == '.':
		return true
	}
	return false
}

// declaresUnindexed reports whether the note at path opts out of parity.
//
// The marker is honoured ONLY inside the leading frontmatter block. A note that
// merely discusses indexing in its prose — this package's own documentation
// would qualify — must not silence a real defect, and confining the marker to
// frontmatter makes that structural rather than a matter of phrasing. A note
// with no frontmatter has no way to opt out, which is correct: the opt-out is a
// deliberate declaration, and its absence is the default.
//
// Any read error is treated as "no opt-out", so an unreadable note surfaces as a
// parity defect rather than being silently excused.
func declaresUnindexed(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, frontmatterScanLimit)
	n, err := f.Read(buf)
	if n == 0 && err != nil && err != io.EOF {
		return false
	}
	return frontmatterHas(string(buf[:n]), UnindexedMarker)
}

// frontmatterHas reports whether head's leading `---` frontmatter block contains
// marker on a line of its own (leading and trailing space ignored).
//
// If the block is not terminated within head the search still covers what was
// read: a frontmatter block longer than frontmatterScanLimit is malformed, and
// scanning the prefix is strictly better than declaring no marker exists.
func frontmatterHas(head, marker string) bool {
	lines := strings.Split(head, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	for _, ln := range lines[1:] {
		t := strings.TrimSpace(ln)
		if t == "---" {
			return false
		}
		if t == marker {
			return true
		}
	}
	return false
}
