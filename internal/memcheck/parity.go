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
// start. Recall goes through the index. A note the index does not name is
// written, costs disk, and is unreachable — the agent that wrote it cannot
// recall it, and neither can anyone else.
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
	// Unreferenced holds the basenames of notes the index never names, sorted.
	// These are the actionable defects: written, on disk, unreachable by recall.
	Unreferenced []string
	// OptedOut holds the basenames that declared UnindexedMarker. They are
	// reported separately from Unreferenced — they are NOT defects, and counting
	// them as such is what makes a parity check too noisy to keep.
	OptedOut []string
	// InParity is true when nothing is unreferenced without having opted out.
	InParity bool
}

// CheckParity reads the index at indexPath and every *.md note beside it, and
// reports which notes the index does not reference.
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
	notes, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return ParityResult{}, err
	}
	res := ParityResult{IndexPath: indexPath, Dir: dir}
	index := string(data)
	indexBase := filepath.Base(indexPath)
	for _, note := range notes {
		base := filepath.Base(note)
		if base == indexBase {
			continue
		}
		res.Notes++
		if references(index, base) {
			continue
		}
		if declaresUnindexed(note) {
			res.OptedOut = append(res.OptedOut, base)
			continue
		}
		res.Unreferenced = append(res.Unreferenced, base)
	}
	sort.Strings(res.Unreferenced)
	sort.Strings(res.OptedOut)
	res.InParity = len(res.Unreferenced) == 0
	return res, nil
}

// references reports whether the index names base anywhere.
//
// Deliberately PERMISSIVE about form. The canonical hook is a markdown link,
// `- [Title](base.md) — hook`, but an index line may name a note in prose, in a
// "superseded by" note, or inside a parenthetical. A parity check exists to
// answer "can recall reach this note", and a mention in any form means yes. Two
// instruments — a `\(([A-Za-z0-9._-]+\.md)\)` link-target regex and this plain
// containment test — were run across all 16 memory dirs on the development box
// and agreed on every file, so the stricter form buys no accuracy here while
// costing false positives on any index that drifts from the link convention.
//
// The boundary check is the one place strictness is required: a bare
// strings.Contains would let an index reference to `xa.md` satisfy a note named
// `a.md`. A match must not be preceded by a character that could be part of a
// longer filename. The trailing `.md` supplies the other boundary, which is why
// `feedback_drive.md` is not satisfied by a reference to
// `feedback_drive_dont_ask.md`.
func references(index, base string) bool {
	for off := 0; ; {
		i := strings.Index(index[off:], base)
		if i < 0 {
			return false
		}
		at := off + i
		if at == 0 || !isNameByte(index[at-1]) {
			return true
		}
		off = at + 1
	}
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
