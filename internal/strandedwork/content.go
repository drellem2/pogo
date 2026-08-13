package strandedwork

import (
	"fmt"
	"strings"
)

// This file is the SECOND OPINION on `git cherry`, and it exists because the
// first opinion has a measured blind spot.
//
// Inspect compares by PATCH ID, which is the only instrument that survives the
// refinery's rebase-and-fast-forward merge: a branch that landed cleanly has
// every commit rewritten under a new sha, and any sha-based or ancestry-based
// test reports all of them as absent forever. That much was already validated in
// both directions.
//
// What patch-id does NOT survive is CONTEXT DRIFT. A patch id is computed over
// the diff TEXT, which includes the unchanged lines surrounding each hunk. The
// refinery rebases into its own working copy and fast-forwards the target; it
// never force-pushes the branch. So origin keeps the commit as it was written,
// the target gets the commit as it was replayed, and if the target had moved
// anywhere near the same lines the two diffs carry different context and hash
// differently. The branch then reports as unmerged for the rest of its life.
//
// THE MECHANISM IS NOT CONFLICT RESOLUTION, and the correction matters because
// it makes the case COMMONER rather than rarer. The mg-be37 ticket predicted this
// blind spot for "a branch the refinery rebased through a CONFLICT", and the
// coordinator later corrected that half: the refinery ABORTS the rebase and fails
// the MR on conflict (internal/refinery/merge.go, mg-eac0, and the abort predates
// it), so it never merges through one. The measurement below stands anyway, and
// the reason it stands is stronger than the prediction was — no conflict is
// required, only a neighbouring change.
//
// A SECOND ROUTE TO THE SAME LOSS, MEASURED 2026-08-13 (mg-5ec6): THE REBASE
// DROPS A HUNK THE TARGET ALREADY HAS. `polecat-a3d4` in onethird_program landed
// as 2919d28 on main — same author date, same subject, committer date four
// minutes later — and `git cherry` still calls its tip c2f1854 unmerged. The
// branch commit touches 20 files and adds 3264 lines; the landed one touches 14
// and adds 3262. The six missing files are a `.gitignore` and five `.pyc`
// deletions the target ALREADY HAD by the time the rebase replayed them, so git
// dropped those hunks as no-ops. The remaining 14 files are byte-identical at
// `-U0`. Nothing conflicted and nothing drifted: the rebase had less left to
// apply than the branch had written, and that alone rewrote the patch id.
//
// That case was filed as evidence of a conflicted refinery merge. It is not one,
// and there are none to be had: mergeBranch runs a plain `git rebase
// origin/<target>` and `rebase --abort`s on any failure (merge.go:605-626), and
// failureclass.go classifies every conflict signal as ClassDefect/non-retryable
// (failureclass.go:360), so a conflicted branch FAILS its merge request rather
// than landing through it. "Should an ordinary conflicted refinery merge be
// reported as unlanded" has no instances to be about. What does have instances is
// the weaker and commoner claim: an ordinary CLEAN refinery merge is enough to
// break patch identity, by drift or by drop.
//
// MeasurePresence catches a3d4 — 2008 of 2063 countable added lines are already
// on main, ratio 0.973, over ContentLandedRatio. That is luck of this instance
// and not a property: the dropped hunk here was two lines, both under
// ContentMinLine. A rebase that dropped a SUBSTANTIVE hunk would score low and
// the branch would stay STRANDED, which is the safe direction and the one this
// file is built to fail in.
//
// THIS IS NOT HYPOTHETICAL AND IT IS NOT RARE. Measured on this repo's origin on
// 2026-08-10: 57 of 634 polecat branches had at least one commit `git cherry`
// called unmerged. Five of the top seven by the measure below are on main right
// now under a different sha, each nameable in both places:
//
//	polecat-79dc   77e012c  ->  1e1292f on main   (369/377 added lines present)
//	polecat-mg-a009            4f2f21c on main   ( 32/33)
//	polecat-3dc3               c205b74 on main   (296/310)
//	polecat-p2097              eb5bb8f on main   (245/261)
//	polecat-p6d2f              974edc1 on main   (555/607)
//
// polecat-79dc is the control, and it is exact rather than suggestive. The two
// commits have identical `--stat` output; every ADDED AND REMOVED LINE of the two
// patches is byte-identical (`diff` of both `-U0` bodies is empty); and their
// patch ids are 959d2fa2… and 5a479b4d…. The whole difference is context. A clean
// replay was enough.
//
// WHAT IS STILL UNMEASURED, stated so nobody reads coverage into the above: which
// route each of those five took to main — a polecat rebasing its own branch
// before submitting, a hand-merge, or the refinery's ordinary replay — has not
// been established. The five are evidence that the blind spot is REAL and that
// the measure below catches it, not evidence about who caused it.
//
// SO WHAT DOES THIS MEASURE. Not patches — LINES. It asks what fraction of the
// substantive lines a branch's unmerged commits ADD are already present in the
// target's copy of the files they touch. Context drift moves the lines AROUND a
// change without touching the change, and even a conflict resolved by keeping
// both sides leaves the branch's own added lines intact. That is what makes a
// line-level measure survive where a patch-level one cannot.
//
// WHY IT MUST NOT BE ALLOWED TO SAY "MERGED". A wrong "already present" verdict
// converts `pogo refinery submit` into `mg done` and throws the branch away —
// the exact loss this whole detector exists to prevent, re-created by its own
// remedy. So this never clears a finding. A high ratio downgrades a STRANDED row
// to a CHECK-BY-HAND row, which recommends neither action. The instrument is a
// heuristic with the failure direction pointed at noise.
//
// The thresholds are set from that measurement, deliberately conservative:
// branches at 0.88, 0.91 and 0.94 are ALSO on main and are ALSO not demoted.
// Under-demoting costs a line of report; over-demoting costs a branch.
//
// AND A FAILED MEASUREMENT IS NOT A LOW ONE. Every error path here returns an
// error rather than a zero Presence, and the caller must leave such a row
// stranded. The natural shape of this kind of check — run git, grep for a
// pattern, treat "no output" as the clean answer — answers CLEAN whenever git
// FAILS, so on a sweep a network blip silently converts a stranded branch into a
// clean row. That is this ticket's own defect rebuilt inside its own remedy, and
// it is why nothing below swallows an exit status.

const (
	// ContentMinLine is how long a line must be, trimmed, to count. Short lines
	// ("}", "return nil", "import (") appear in every Go file in the repo, so
	// counting them would find any branch's additions "already present" in any
	// target and the measure would be a constant.
	ContentMinLine = 12

	// ContentMinAdded is the fewest substantive added lines a branch must have
	// before a ratio over it is allowed to mean anything. Three of the branches
	// in the live population add 3, 4 and 5 countable lines; a 3/3 hit is one
	// coincidence away from a wrong verdict and carries no evidence.
	ContentMinAdded = 20

	// ContentLandedRatio is the fraction at or above which a branch is called a
	// conflict suspect. 0.95 sits above the highest-scoring branch this measure
	// has been shown wrong about and below every confirmed landed one it is meant
	// to catch.
	ContentLandedRatio = 0.95
)

// Presence is how much of what a branch ADDS the target already has.
//
// Measured is false when the branch's unmerged commits contributed fewer than
// ContentMinAdded countable lines. That is a real answer — "this branch is too
// small to measure this way" — and it must never be read as either verdict.
type Presence struct {
	// Added is the number of distinct substantive lines the unmerged commits add.
	Added int `json:"added"`
	// Present is how many of those the target's copies of the touched files
	// already contain.
	Present int `json:"present"`
	// Measured reports whether Added cleared ContentMinAdded.
	Measured bool `json:"measured"`
	// Paths is how many files the unmerged commits touched.
	Paths int `json:"paths"`
}

// Ratio is Present/Added, or 0 when nothing was added.
func (p Presence) Ratio() float64 {
	if p.Added == 0 {
		return 0
	}
	return float64(p.Present) / float64(p.Added)
}

// SuggestsLanded reports whether the target already holds essentially everything
// this branch adds — the signature of a branch that merged through a conflict
// and lost its patch identity doing it.
//
// It is phrased as "suggests" in the name for the reason spelled out at the top
// of this file: no caller may treat it as a merge verdict.
func (p Presence) SuggestsLanded() bool {
	return p.Measured && p.Ratio() >= ContentLandedRatio
}

// Describe renders the measurement for a report line, including the unmeasured
// case, which a bare ratio would print as a confident 0.00.
func (p Presence) Describe() string {
	if !p.Measured {
		return fmt.Sprintf("content check not conclusive (%d added line(s), fewer than the %d needed to measure)",
			p.Added, ContentMinAdded)
	}
	return fmt.Sprintf("%d of %d added line(s) already in the target (%.0f%%)",
		p.Present, p.Added, 100*p.Ratio())
}

// Corroborate runs the content second opinion on a finding and renders the one
// sentence a REPORT about it should carry, alongside the measurement itself.
//
// WHY IT EXISTS (mg-5ec6). MeasurePresence shipped with exactly one consumer,
// `pogo check-stranded`. The three routes inside pogod — the dispatch refusal,
// the release-time reporter and the restart sweep — acted on `git cherry`'s bare
// verdict, so the one instrument built for this blind spot was absent from the
// three places that speak to an operator. The refusal is the sharpest of them: it
// REFUSES a dispatch, and a refusal a reader can demonstrate is wrong is how a
// gate gets overridden by habit rather than by judgement.
//
// IT NEVER CHANGES A DISPOSITION, and that restriction is not negotiable. A
// wrong "already present" verdict converts `pogo refinery submit` into `mg done`
// and throws a branch away — the loss this whole detector exists to prevent,
// re-created by its own remedy. So this returns TEXT for a human to weigh, and
// every caller keeps reporting the branch as stranded whatever it says.
//
// A FAILED MEASUREMENT RETURNS A LOUD SENTENCE, NOT AN EMPTY ONE. "The second
// opinion did not run" and "the second opinion found nothing" are different
// facts, and the whole family of defects around this detector is the first being
// rendered as the second.
func Corroborate(repo string, f Finding) (Presence, string) {
	if len(f.Unmerged) == 0 {
		return Presence{}, ""
	}
	p, err := MeasurePresence(repo, f)
	if err != nil {
		return Presence{}, fmt.Sprintf("SECOND OPINION UNAVAILABLE: the content check could not run (%v). "+
			"That is not a low score and not a clean verdict — this row rests on `git cherry` alone.", err)
	}
	if !p.Measured {
		return p, fmt.Sprintf("Second opinion: %s.", p.Describe())
	}
	if p.SuggestsLanded() {
		return p, fmt.Sprintf("SECOND OPINION SAYS THIS MAY ALREADY HAVE LANDED UNDER A DIFFERENT SHA: %s. "+
			"`git cherry` compares PATCH IDS, and an ordinary clean refinery rebase can rewrite one — by "+
			"replaying a hunk into moved context, or by dropping a hunk the target already had — so a branch "+
			"that DID merge reads as unmerged forever (mg-5ec6). This is NOT a merge verdict and it does not "+
			"clear the row: check by hand before submitting or deleting the branch, and expect to find the "+
			"same subject on the target under another sha.", p.Describe())
	}
	return p, fmt.Sprintf("Second opinion agrees the work is absent from the target: %s.", p.Describe())
}

// MeasurePresence answers Presence for a finding's unmerged commits.
//
// It reads the branch's commits and the TARGET'S copies of the files they touch
// — never the branch's copies — because the question is what the target already
// has. A path the target does not have contributes no haystack, so lines added
// in a file that only exists on the branch count as absent, which is right.
func MeasurePresence(repo string, f Finding) (Presence, error) {
	var p Presence
	if len(f.Unmerged) == 0 {
		return p, nil
	}
	added := map[string]bool{}
	paths := map[string]bool{}
	for _, c := range f.Unmerged {
		touched, err := commitPaths(repo, c.SHA)
		if err != nil {
			return p, err
		}
		for _, path := range touched {
			paths[path] = true
		}
		lines, err := commitAddedLines(repo, c.SHA)
		if err != nil {
			return p, err
		}
		for _, l := range lines {
			added[l] = true
		}
	}
	p.Added = len(added)
	p.Paths = len(paths)
	p.Measured = p.Added >= ContentMinAdded
	if p.Added == 0 {
		return p, nil
	}

	haystack := map[string]bool{}
	for path := range paths {
		// A path absent from the target is not an error: the branch created it,
		// and every line in it is correctly counted as missing.
		blob, err := git(repo, "show", f.Target+":"+path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(blob, "\n") {
			if t := strings.TrimSpace(line); len(t) >= ContentMinLine {
				haystack[t] = true
			}
		}
	}
	for line := range added {
		if haystack[line] {
			p.Present++
		}
	}
	return p, nil
}

// commitPaths lists the files one commit touched.
//
// -z rather than a newline split: git QUOTES a path containing a space or a
// non-ASCII byte when it has to use a line-oriented format, and a quoted path
// handed back to `git show <ref>:<path>` resolves to nothing. The NUL-delimited
// form is never quoted.
func commitPaths(repo, sha string) ([]string, error) {
	out, err := git(repo, "show", "--pretty=format:", "--name-only", "-z", sha)
	if err != nil {
		return nil, fmt.Errorf("paths of %s in %s: %w", shortSHA(sha), repo, err)
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// commitAddedLines returns the substantive lines one commit adds.
//
// --unified=0 drops context lines from the output entirely, so nothing has to
// distinguish an added line from a context line that happens to start with '+'.
// The "+++ " file header is the one remaining collision and is excluded by
// prefix.
func commitAddedLines(repo, sha string) ([]string, error) {
	out, err := git(repo, "show", "--pretty=format:", "--unified=0", "--no-color", sha)
	if err != nil {
		return nil, fmt.Errorf("diff of %s in %s: %w", shortSHA(sha), repo, err)
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		if t := strings.TrimSpace(line[1:]); len(t) >= ContentMinLine {
			lines = append(lines, t)
		}
	}
	return lines, nil
}
