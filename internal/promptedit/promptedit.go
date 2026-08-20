// Package promptedit is the HAND-EDIT detector for the installed prompt corpus
// (mg-0c96): it reports deployed prompts whose body no longer matches the hash
// their own stamp records, which is the signature of an edit made in place
// after the installer wrote the file.
//
// # The instrument already existed; nothing ran it
//
// Every deployed prompt with an upstream carries a self-describing stamp on its
// first line:
//
//	<!-- pogo-prompt: embed=sha256:… body=sha256:… -->
//
// A disagreement between that body hash and sha256(body) IS the hand-edit
// signature, and it was verified by hand on 2026-08-20 (mg-0635) with two
// commands per file — no .dist sidecar and no comparison against shipped
// source needed:
//
//	head -1 <prompt>
//	tail -n +2 <prompt> | shasum -a 256
//
// It was armed and unscheduled. It fired that night only because a shipped
// update happened to collide with the edited region of mayor.md and pogod
// declined the sync; had the edit been in a region no shipped change touched,
// nothing would have reported it — indefinitely. This package is the reader on
// a cadence. See watcher.go for why the cadence is pogod's heartbeat and not
// `pogo doctor --check` (mg-10e3: a detector armed at a surface nobody reads on
// a cadence is the same defect one level down).
//
// # THE DOMAIN CONSTRAINT, which is most of this file
//
// An unstamped file is AMBIGUOUS between "by design, no upstream" and "should
// have a stamp and lost it", and nothing in the file distinguishes them. On the
// live tree on 2026-08-20 the corpus-shaped installed set was 18 files of which
// 9 are stamped-and-shipped; the rest have no upstream in dev/pogo, so the
// deployed file IS the source and "hand-edited since it shipped" is not a
// meaningful question to ask of them.
//
// So the report must not say "9 clean, 9 unknown". Nine unknowns reported as
// findings would be nine false positives and the report would get ignored,
// which is mg-10e3's failure again; nine unknowns waved through would let a
// genuinely lost stamp hide among them. Every installed file is therefore put
// in exactly one of four buckets, and each of the three non-reading buckets
// carries a DISTINCT reason:
//
//	shipped + stamped      JUDGED. Mismatch is a finding; match is clean.
//	shipped + unstamped    OutOfDomain{StampMissing} — the corpus ships this
//	                       path, so the installer would have stamped it. The
//	                       stamp is gone. Unjudgeable, and notable on its own.
//	unshipped + stamped    OutOfDomain{UpstreamWithdrawn} — an older pogo
//	                       shipped it and the corpus no longer does. The stamp
//	                       still reads (Edited says what it says), but there is
//	                       no upstream to reconcile against, so mailing its
//	                       agent asks for work that cannot be done.
//	unshipped + unstamped  OutOfDomain{NoUpstream} — the deployed file is the
//	                       source. Not a defect, not a reading.
//
// The withdrawn-upstream bucket is not hypothetical: `crew/pm-onethird.md` and
// `crew/pm-pogo.md` were shipped by mg-6805, deleted from the corpus by mg-5d9e
// ("scrub personal-project pollution"), and both still carry a v0 stamp that
// disagrees with their current body. They are the exact shape that a naive
// stamped-means-judgeable sweep would report as two findings against two PMs
// who did nothing wrong.
//
// # Report-only, and here that is a hard constraint rather than a convention
//
// There is no repair seam in this package and there must not be one. A repair
// that carries a local line forward changes the body, which stales the stamp,
// and the stamp cannot be recomputed without knowing the installer's exact
// canonicalisation. p0635 hit this and correctly stopped rather than guess. A
// tool that recomputes the stamp without that knowledge silently certifies a
// body it never validated — it would convert an honest "unknown" into a false
// "verified", which is strictly worse than the defect. The agent named on each
// finding is the only party who can judge whether the edit is still
// load-bearing.
package promptedit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
)

// Verdict is the reading for an in-domain file.
type Verdict string

const (
	// VerdictClean means the stamp's body hash equals sha256(body): the file is
	// byte-identical to what the installer wrote.
	VerdictClean Verdict = "clean"
	// VerdictEdited means they disagree. The file has been changed in place
	// since install.
	VerdictEdited Verdict = "edited"
)

// OutOfDomainReason names WHY a file could not be read as a hand-edit
// judgement. Each value is a different fact about the file, and they are kept
// separate so a lost stamp cannot hide among the files that never had one.
type OutOfDomainReason string

const (
	// ReasonNoUpstream: the corpus ships no such path and the file carries no
	// stamp. The deployed file is the source. Expected, and the largest bucket.
	ReasonNoUpstream OutOfDomainReason = "no-upstream"
	// ReasonStampMissing: the corpus DOES ship this path, so the installer
	// would have stamped it, and the stamp is not there. The file cannot be
	// judged and the absence is itself worth a reader's attention.
	ReasonStampMissing OutOfDomainReason = "stamp-missing"
	// ReasonUpstreamWithdrawn: stamped by an install, but the corpus no longer
	// ships the path. The stamp still reads — Entry.Edited carries it — but
	// there is nothing to reconcile against, so it is reported rather than
	// mailed.
	ReasonUpstreamWithdrawn OutOfDomainReason = "upstream-withdrawn"
)

// Entry is one installed corpus file and what could be established about it.
type Entry struct {
	// Path is corpus-relative and slash-separated ("crew/doctor.md").
	Path string `json:"path"`
	// Agent is the agent that can act on this file, and Owned reports whether
	// it OWNS the prompt or is the coordinator standing in for a file no
	// running agent owns. Resolved through agent.PromptAddressee.
	Agent string `json:"agent"`
	Owned bool   `json:"owned"`
	// Stamped reports whether a stamp was present at all. Never infer this
	// from an empty RecordedHash.
	Stamped bool `json:"stamped"`
	// StampVersion is agent.PromptStampV1 or agent.PromptStampV0, meaningful
	// only when Stamped. A v0 stamp's recorded body hash is INFERRED from the
	// single embed hash it carries, and the report says so.
	StampVersion int `json:"stamp_version"`
	// RecordedHash is the body hash the stamp claims; ActualHash is
	// sha256(body) now.
	RecordedHash string `json:"recorded_hash,omitempty"`
	ActualHash   string `json:"actual_hash,omitempty"`
	// Shipped reports whether the running binary's embedded corpus carries this
	// path.
	Shipped bool `json:"shipped"`
}

// Edited reports whether this entry's stamp disagrees with its body. It is
// meaningful for ANY stamped file, in or out of domain — the withdrawn-upstream
// bucket uses it to state what the stamp says while still declining to raise a
// finding.
func (e Entry) Edited() bool {
	return e.Stamped && e.RecordedHash != "" && e.RecordedHash != e.ActualHash
}

// Finding is an in-domain file whose stamp and body disagree.
type Finding struct {
	Entry
}

// OutOfDomain is one file that was enumerated and deliberately not judged.
type OutOfDomain struct {
	Entry
	Reason OutOfDomainReason `json:"reason"`
}

// Report is the detector's whole answer: what it judged, what it declined to
// judge, and why.
//
// Clean, Findings, OutOfDomain and Unreadable partition the enumerated corpus.
// Total() is asserted against that partition in the tests, because a
// classification that silently drops a file reports "clean" over an unexamined
// remainder — the failure this package's domain rule exists to prevent.
type Report struct {
	// Root is the installed tree that was read, named so a reader can check it.
	Root string `json:"root"`
	// ShippedPaths is how many paths the running binary's embed carries. It is
	// the denominator of the domain and travels with every report: a binary
	// whose embed is empty would judge nothing and must not read as clean.
	ShippedPaths int `json:"shipped_paths"`
	// Findings are the readings that say "edited". Sorted by path.
	Findings []Finding `json:"findings"`
	// Clean are the in-domain files whose stamp matches. Sorted by path.
	Clean []string `json:"clean"`
	// OutOfDomain is the classified census. Sorted by path.
	OutOfDomain []OutOfDomain `json:"out_of_domain"`
	// Unreadable are corpus-shaped files that could not be read. A finding of a
	// different kind rather than a census line: an unreadable prompt is a
	// prompt whose content is unknown, and unknown is not the same as matching.
	Unreadable []string `json:"unreadable,omitempty"`
}

// Total is the number of installed files the scan enumerated.
func (r Report) Total() int {
	return len(r.Findings) + len(r.Clean) + len(r.OutOfDomain) + len(r.Unreadable)
}

// OutOfDomainBy counts the census by reason, so a renderer can state each
// bucket's size without re-deriving the classification.
func (r Report) OutOfDomainBy(reason OutOfDomainReason) []OutOfDomain {
	var out []OutOfDomain
	for _, o := range r.OutOfDomain {
		if o.Reason == reason {
			out = append(out, o)
		}
	}
	return out
}

// Layout is the shape of the shipped corpus: which directories it occupies and
// which file extensions it uses.
//
// The installed side is enumerated THROUGH it, which is what keeps the walk
// inside the corpus and out of ~/.pogo/agents' runtime directories. That matters
// more here than it looks: a recursive sweep of ~/.pogo/agents on 2026-08-20
// returned 100+ .md files — agent scratch notes, thread dossiers, attic
// checkouts, synthesized-prompt.md copies — none of which is a prompt the
// installer wrote, and every one of which would have been an unstamped
// "unknown" in the report.
type Layout struct {
	Dirs       []string
	Extensions map[string]bool
}

// Shipped is the set of corpus-relative paths the reference corpus carries,
// used both as the domain and to derive the Layout.
type Shipped map[string]bool

// LoadShipped reads the shipped path set out of an fs.FS — in production
// agent.DefaultPromptsFS(), the running binary's own embed.
//
// THE EMBED IS THE RIGHT REFERENCE HERE, and deliberately not a git tree. The
// embed is the same artifact that WRITES the stamps this detector reads, so the
// domain definition and the stamp writer move together by construction; a
// git-ref reference could disagree with the installer that produced the files
// being judged and the disagreement would surface as findings. The cost is that
// a prompt added to the repo after this binary was built reads as unshipped
// until the daemon is redeployed — which lands it in the no-upstream census,
// never in the findings.
func LoadShipped(fsys fs.FS) (Shipped, error) {
	shipped := Shipped{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		shipped[path.Clean(p)] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the shipped prompt corpus: %w", err)
	}
	if len(shipped) == 0 {
		// A binary whose embed is empty can judge nothing. Say so rather than
		// returning an empty domain that would classify every installed file as
		// no-upstream and read as a clean sweep.
		return nil, errors.New("the shipped prompt corpus is empty — nothing to define the domain")
	}
	return shipped, nil
}

// LayoutOf derives the enumeration layout from the shipped set, so the
// installed-side walk is defined by what the binary ships rather than by a
// hand-kept list that would silently stop covering a new directory.
func LayoutOf(s Shipped) Layout {
	l := Layout{Extensions: map[string]bool{}}
	seen := map[string]bool{}
	for rel := range s {
		dir := path.Dir(rel)
		if !seen[dir] {
			seen[dir] = true
			l.Dirs = append(l.Dirs, dir)
		}
		if ext := path.Ext(rel); ext != "" {
			l.Extensions[ext] = true
		}
	}
	sort.Strings(l.Dirs)
	return l
}

// Scan classifies every corpus-shaped file under root against the shipped set.
//
// It never writes, never repairs, and never consults anything but the files
// themselves — the stamp is self-describing, which is the whole reason this
// check needs no .dist sidecar and no reference checkout.
//
// The walk is NON-RECURSIVE within each shipped directory, extension-filtered,
// and skips dotfiles. Each of those rules is load-bearing:
//
//   - non-recursive keeps `mayor/`, `pa/thread-index/` and the other per-agent
//     state directories out. They live under the same root and hold hundreds of
//     .md files that were never installed;
//   - the extension filter keeps the installer's own sidecars out. `mayor.md.dist`
//     and `mayor.md.bak-1784309533` have extensions `.dist` and `.bak-…`, so
//     neither is mistaken for a prompt. They are the RESULT of a conflict being
//     handled, and counting them as corpus would report the handling as a defect;
//   - dotfiles are skipped because ~/.pogo/agents/templates has held `.#polecat.md`,
//     an Emacs lock file that is a DANGLING SYMLINK with an .md extension. An
//     editor left open over a prompt is not a fact about the corpus, and it took
//     internal/staleness's whole witness down once before that rule existed.
//
// An unreadable non-dotfile is recorded in Unreadable rather than raised,
// because one bad file must not decide the others. A directory that cannot be
// read at all IS raised: that is the instrument failing, not the corpus.
func Scan(root string, shipped Shipped, coordinator string) (Report, error) {
	rep := Report{Root: root, ShippedPaths: len(shipped)}
	layout := LayoutOf(shipped)

	for _, dir := range layout.Dirs {
		abs := root
		if dir != "." {
			abs = filepath.Join(root, filepath.FromSlash(dir))
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			if os.IsNotExist(err) {
				// A shipped directory with nothing installed under it. Not an
				// error and not a finding: this detector answers "was an
				// installed file edited", and there is no installed file here.
				continue
			}
			return rep, fmt.Errorf("reading %s: %w", abs, err)
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !layout.Extensions[path.Ext(e.Name())] {
				continue
			}
			rel := e.Name()
			if dir != "." {
				rel = dir + "/" + e.Name()
			}
			data, err := os.ReadFile(filepath.Join(abs, e.Name()))
			if err != nil {
				rep.Unreadable = append(rep.Unreadable, rel)
				continue
			}
			rep.classify(rel, data, shipped[rel], coordinator)
		}
	}

	sort.Slice(rep.Findings, func(i, j int) bool { return rep.Findings[i].Path < rep.Findings[j].Path })
	sort.Slice(rep.OutOfDomain, func(i, j int) bool { return rep.OutOfDomain[i].Path < rep.OutOfDomain[j].Path })
	sort.Strings(rep.Clean)
	sort.Strings(rep.Unreadable)
	return rep, nil
}

// classify puts one file in exactly one bucket. The four-way split is the
// package doc's table, and it is written as one switch so that adding a case
// without deciding its bucket is a compile-visible omission rather than a file
// that silently falls out of the report.
func (r *Report) classify(rel string, data []byte, shipped bool, coordinator string) {
	to, owned := agent.PromptAddressee(rel, coordinator)
	stamp, stamped := agent.ReadPromptStamp(data)
	e := Entry{
		Path:         rel,
		Agent:        to,
		Owned:        owned,
		Stamped:      stamped,
		StampVersion: stamp.Version,
		RecordedHash: stamp.BodyHash,
		ActualHash:   agent.PromptBodyHash(data),
		Shipped:      shipped,
	}

	switch {
	case shipped && stamped:
		if e.Edited() {
			r.Findings = append(r.Findings, Finding{Entry: e})
		} else {
			r.Clean = append(r.Clean, rel)
		}
	case shipped && !stamped:
		r.OutOfDomain = append(r.OutOfDomain, OutOfDomain{Entry: e, Reason: ReasonStampMissing})
	case !shipped && stamped:
		r.OutOfDomain = append(r.OutOfDomain, OutOfDomain{Entry: e, Reason: ReasonUpstreamWithdrawn})
	default:
		r.OutOfDomain = append(r.OutOfDomain, OutOfDomain{Entry: e, Reason: ReasonNoUpstream})
	}
}
