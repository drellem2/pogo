package staleness

// THE INSTALLER CEILING (mg-1e8e).
//
// The prompt witness above answers "is what the fleet reads the same as what
// the repo ships". When the answer is no it prints a remedy:
//
//	Fix: redeploy, or 'pogo agent prompt install' from a build of the reference.
//
// That line was printed blind. It names two installers and says nothing about
// what either one CARRIES, and an installer can only ever write the corpus
// embedded in the binary that runs it. So the remedy was advice of exactly the
// kind the report exists to replace: a claim about an artifact, made without
// reading the artifact.
//
// WHAT THAT COST, measured on this box on 2026-09-08 rather than argued:
//
//	~/.pogo/agents/mayor.md          mtime 2026-08-22 03:01, 129 lines behind the reference
//	pogod RUNNING                    7edd223, built 2026-08-20, booted 2026-09-01 12:07
//	pogod ON DISK (next boot execs)  499eb8a, installed 2026-09-08 03:00
//	the reference (deploy-src)       499eb8a
//
// mg-1e8e was filed on the reading that the prompt-install path is a THIRD
// nightly failure mode, distinct from the binary install (works) and the pogod
// restart (fails since 09-01) — the argument being that a 17-day-old prompt
// beside an 11-hour-old binary proves two separate paths. Both halves of that
// are wrong, and the ceiling is what shows it:
//
//   - There IS an install step, it is not the deploy script, and it is not
//     skipping. pogod calls agent.InstallPrompts in-process at every boot
//     (cmd/pogod/main.go; see cmd/pogod/promptrefreshrecord.go, which already
//     documents that `grep -c 'prompt install' scripts/pogo-self-deploy` is 0
//     and always has been, and why that grep proves nothing).
//   - It ran. The `prompt_refresh` event stream holds seven runs since 08-22,
//     the most recent 2026-09-01T11:07:58Z, every one of them ok=true. The last
//     four read `changed=0 conflicts=[] skipped=[...all nine...]`.
//
// Those runs were correct. ~/.pogo/agents already held 7edd223's corpus — the
// 08-22 boot wrote it, which is precisely why mayor.md is stamped 08-22 — and
// every boot since has been the same 7edd223 binary declining to rewrite its
// own bytes. The installer did not fail to close the gap; it could not. The
// prompt path is not a third failure mode, it is DOWNSTREAM of the restart
// failure: a prompt corpus is capped at the revision of the process that
// installs it, so an unrestarted daemon freezes the prompts at its own build
// and reports ok=true forever while doing it.
//
// A CEILING IS AN UPPER BOUND, NOT A PREDICTION, and the distinction is
// load-bearing rather than a hedge. Content an installer does not carry it
// cannot write, full stop — that half is decidable here and is the half that
// explains 17 days. Content it does carry it may still decline to write: the
// hand-edit conflict cell writes a .dist sidecar and leaves the canonical
// alone (see InstallPrompts' matrix, and `pogo check-prompt-edits`). Reporting
// a ceiling as a forecast would re-commit this ticket's own error one level up
// — asserting what an installer will do without reading the state it decides
// against — so the printed line says which of the two it is.

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// InstallerCeiling is one installer's answer to "of the deltas this report just
// found, which could you close at all?"
type InstallerCeiling struct {
	// Name is the installer as an operator would refer to it.
	Name string `json:"name"`
	// How is the invocation or event that runs it — the difference between a
	// command a reader can type and a boot they have to wait for.
	How string `json:"how"`
	// Source says where the carried corpus was read from, so a surprising row
	// can be re-derived by hand.
	Source string `json:"source,omitempty"`
	// Revision identifies the installer when it is identified by one. Empty for
	// an installer read directly (this binary reads its own embed and needs no
	// revision to do it).
	Revision string `json:"revision,omitempty"`
	// Unknown is why the carried corpus could not be read. Set means every
	// other field below is empty and this installer decided NOTHING — an
	// unreadable ceiling is never an all-clear and never a refusal.
	Unknown string `json:"unknown,omitempty"`
	// Closes, Frozen and Third partition the report's deltas three ways, and
	// the split is the finding rather than presentation (see the THREE CELLS
	// note above the judge).
	//
	//   Closes — this installer carries the REFERENCE content; an install
	//            writes exactly what the delta says is missing.
	//   Frozen — it carries what is ALREADY INSTALLED. An install rewrites
	//            identical bytes and changes nothing, which is what an
	//            unrestarted daemon has been doing, correctly, since 08-22.
	//   Third  — it carries neither: a version this report has not judged.
	//            Ahead of a lagging reference is the ordinary cause and it is
	//            NOT the same as incapable.
	Closes []string `json:"closes"`
	Frozen []string `json:"frozen"`
	Third  []string `json:"third"`
}

// Leaves is every delta this installer does not carry the reference content
// for — the two non-closing cells together, for a caller that only needs the
// complement of Closes.
func (c InstallerCeiling) Leaves() []string {
	out := make([]string, 0, len(c.Frozen)+len(c.Third))
	out = append(out, c.Frozen...)
	out = append(out, c.Third...)
	sort.Strings(out)
	return out
}

// Known reports whether this row measured anything.
func (c InstallerCeiling) Known() bool { return c.Unknown == "" }

// CarriesAll reports whether this installer carries the reference content for
// every delta — i.e. whether it is capable of closing the whole gap. False for
// an unknown row, because unknown is not the same as capable.
func (c InstallerCeiling) CarriesAll() bool {
	return c.Known() && len(c.Frozen) == 0 && len(c.Third) == 0
}

// Verdict is the one-clause summary for the human line.
func (c InstallerCeiling) Verdict() string {
	total := len(c.Closes) + len(c.Frozen) + len(c.Third)
	switch {
	case !c.Known():
		return "UNKNOWN — " + c.Unknown
	case total == 0:
		return "nothing to close"
	case len(c.Closes) == total:
		return fmt.Sprintf("carries the reference on all %d — it CAN close them", total)
	case len(c.Frozen) == total:
		return fmt.Sprintf("carries what is ALREADY INSTALLED on all %d — an install here is a no-op", total)
	case len(c.Third) == total:
		return fmt.Sprintf("carries a THIRD version of all %d — neither the reference nor what is installed", total)
	default:
		return fmt.Sprintf("carries the reference on %d of %d (%d already-installed, %d a third version)",
			len(c.Closes), total, len(c.Frozen), len(c.Third))
	}
}

// CeilingSource is an installer plus a way to obtain the corpus it carries.
//
// A function rather than a corpus so the caller decides what a reading costs:
// the embed is free, a revision costs a git read, and a running daemon costs an
// HTTP call that can hang. internal/staleness stays out of that choice — it is
// handed the sources and judges whatever they return.
type CeilingSource struct {
	Name string
	How  string
	// Corpus returns the carried corpus and a description of where it was read
	// from. An error becomes the row's Unknown reason verbatim.
	Corpus func(ctx context.Context) (Corpus, string, error)
	// Revision labels the row when the installer is identified by one.
	Revision string
}

// MeasureFS measures every file of a corpus rooted at an fs.FS, using the same
// body-hash reduction as the git and on-disk sides.
//
// The same `measure`, deliberately: a ceiling compared with a different
// reduction than the deltas it judges would be answering a different question
// with the same words. The stamp strip matters here too — an embed carries no
// stamp, but routing all three sides through one function is what keeps that a
// property of the code rather than of three matching comments.
func MeasureFS(fsys fs.FS) (Corpus, error) {
	corpus := Corpus{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", p, readErr)
		}
		corpus[filepath.ToSlash(p)] = measure(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(corpus) == 0 {
		return nil, fmt.Errorf("the corpus is empty — nothing to compare")
	}
	return corpus, nil
}

// EmbedCeilingSource reads an installer's own embedded corpus. No git, no
// network, no daemon: the binary running this check is itself one of the
// installers the remedy names, and it can answer for that one for free.
func EmbedCeilingSource(name, how string, fsys fs.FS) CeilingSource {
	return CeilingSource{
		Name: name, How: how,
		Corpus: func(context.Context) (Corpus, string, error) {
			c, err := MeasureFS(fsys)
			if err != nil {
				return nil, "", err
			}
			return c, "this binary's own embedded corpus", nil
		},
	}
}

// RevisionCeilingSource reads the corpus a revision carries out of the
// reference repo's object store.
//
// This is how a binary that cannot be asked directly is still measured by
// CONTENT rather than by a revision-ancestry argument. "7edd223 is an ancestor
// of 499eb8a" would establish only that the binary is older, which says nothing
// about whether the corpus moved between them; reading prompts/ at 7edd223 says
// exactly what it carries.
//
// A rev that is not a revision — empty, or one of the readers' angle-bracketed
// sentinels (`<unreachable>`, `<missing>`, `<unstamped>`) — is reported as
// unknown rather than guessed in either direction. It is matched HERE rather
// than left to git because git would reject it too, with a rev-parse error that
// reads like a broken reference repo instead of the daemon or binary that could
// not be read. Two world-states, one message, is the shape this whole file
// exists to stop producing.
func RevisionCeilingSource(name, how, repo, rev, why string) CeilingSource {
	return CeilingSource{
		Name: name, How: how, Revision: rev,
		Corpus: func(ctx context.Context) (Corpus, string, error) {
			if !IsRevision(rev) {
				if rev != "" {
					return nil, "", fmt.Errorf("%s (read as %s)", why, rev)
				}
				return nil, "", fmt.Errorf("%s", why)
			}
			c, info, err := LoadShippedCorpus(ctx, repo, rev)
			if err != nil {
				return nil, "", err
			}
			return c, fmt.Sprintf("%s at %s, read from %s", PromptsSubtree, shortRev(info.Commit), repo), nil
		},
	}
}

// IsRevision reports whether a string is something to hand to git at all.
//
// The revision readers in internal/selfdrift never return "": they return
// `<unreachable>` for a daemon that will not talk, `<missing>` for a binary
// that is not there and `<unstamped>` for one that cannot say what it is —
// three states deliberately kept apart, and all three are "no revision" here.
func IsRevision(rev string) bool {
	return rev != "" && !strings.HasPrefix(rev, "<")
}

func shortRev(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// JudgeCeilings partitions each source's carried corpus against the deltas the
// report already found.
//
// Deltas, not the whole corpus: the question a remedy line has to answer is
// "can this installer fix what is actually wrong", and an installer that
// differs from the reference on a file nobody reported is not part of that
// answer. Paths are reported sorted so two runs over unchanged input read
// identically, the same rule the deltas and the census follow.
//
// THREE CELLS, NOT TWO, and the third one was found by running this against
// the live box rather than reasoned out. The first version asked one question —
// does the installer carry the reference content — and printed "carries 0 of 3,
// it CANNOT close them" over BOTH remaining cases. That is a true sentence
// about the reference and a false impression about the installer, and it landed
// on two rows that could not be more different:
//
//   - the running pogod carried, byte for byte, what was already on disk. It
//     is the reason nothing has moved since 08-22, and an install from it
//     rewrites identical bytes.
//   - the dev build printing the report carried a corpus NEWER than the
//     reference, because the reference is `~/.pogo/deploy-src` and is itself
//     a lagging snapshot (the row above says so: BEHIND THE REMOTE). Installing
//     from it would have changed all three files.
//
// Collapsing "would change nothing" and "would change them to something this
// report has not judged" into one CANNOT is the same defect as the remedy line
// this whole file replaces: a verdict wider than the reading behind it. The
// split costs one extra hash comparison against InstalledHash, which the
// deltas already carry, and no additional git or network call.
func JudgeCeilings(ctx context.Context, shipped Corpus, deltas []PromptDelta, sources []CeilingSource) []InstallerCeiling {
	out := make([]InstallerCeiling, 0, len(sources))
	for _, src := range sources {
		row := InstallerCeiling{Name: src.Name, How: src.How, Revision: src.Revision}
		carried, where, err := src.Corpus(ctx)
		if err != nil {
			row.Unknown = err.Error()
			out = append(out, row)
			continue
		}
		row.Source = where
		for _, d := range deltas {
			want, shipsIt := shipped[d.Path]
			got, carries := carried[d.Path]
			switch {
			case shipsIt && carries && got.Hash == want.Hash:
				row.Closes = append(row.Closes, d.Path)
			// InstalledHash is empty on a "not-installed" delta, and an
			// installer that carries nothing for the path has an empty hash of
			// its own. Requiring both to be non-empty keeps those two absences
			// from matching each other and reporting a file nobody has as
			// frozen.
			case carries && d.InstalledHash != "" && got.Hash == d.InstalledHash:
				row.Frozen = append(row.Frozen, d.Path)
			default:
				row.Third = append(row.Third, d.Path)
			}
		}
		sort.Strings(row.Closes)
		sort.Strings(row.Frozen)
		sort.Strings(row.Third)
		out = append(out, row)
	}
	return out
}

// CeilingRemedy is the Fix line, derived from the ceilings rather than fixed
// text. It returns the empty string when no ceiling was read at all, so the
// caller keeps its unconditional advice rather than printing a remedy built
// out of nothing.
func CeilingRemedy(ceilings []InstallerCeiling) string {
	var anyKnown bool
	for _, c := range ceilings {
		if c.Known() {
			anyKnown = true
		}
		if c.CarriesAll() {
			return fmt.Sprintf("Fix: %s — it carries the reference content for every file above.", c.How)
		}
	}
	if !anyKnown {
		return ""
	}
	// No installer carries the reference for every file. WHY decides the
	// remedy, and the two reasons want opposite actions, so they are answered
	// separately rather than under one "reinstall and see".
	for _, c := range ceilings {
		if c.Known() && len(c.Third) > 0 {
			return "Fix: UNDECIDED from here — at least one installer carries a version that is neither " +
				"the reference nor what is installed. That is what an installer AHEAD of a lagging reference " +
				"looks like, so re-run with --fetch to move the reference to the remote head before choosing " +
				"a remedy; installing against a stale reference decides nothing."
		}
	}
	return "Fix: NOT an install — every installer that could be read carries exactly what is already " +
		"on disk, so an install rewrites identical bytes and changes nothing. A newer binary has to land " +
		"AND BE RUN first (the nightly's build + restart); only then does an install have anything newer to write."
}
