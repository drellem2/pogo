// Package selfdrift answers one question a pogo installation could not
// previously ask: "am I running what I think I am running?" (mg-75ec)
//
// THE GAP THIS CLOSES. `mg` self-installs on merge; `pogod` does not. So every
// consumer's daemon drifts from its own source silently — a merge lands, the
// binary on disk is never rebuilt, the daemon never restarts, and nothing in
// any shipped surface says so. The fleet solved this for ITSELF with
// scripts/pogo-self-deploy (mg-6afa), whose `check` subcommand prints exactly
// this three-way report. That script is a repo file, not a shipped binary: a
// consumer who installed pogo with `go install` has no copy of it, and so has
// no way to see drift at all. This package is that report, in Go, reachable
// from `pogo service status`.
//
// REPORT-ONLY, DELIBERATELY. Nothing here rebuilds, restarts, or installs
// anything. A consumer's daemon does not self-update — that was ruled out of
// scope when this was filed, and the reasoning is the same one the reconcile
// package states directly: an auto-fix loop fighting a genuinely-broken
// artifact is the unbounded-reaper failure shape. Make the drift visible and
// let a human act.
//
// THE STAMP IS EVIDENCE, NOT TRUTH (inherited from mg-8f09, whose lesson this
// package would otherwise have to relearn). Every axis reports what a binary
// CLAIMS about its own provenance, and a claim has three failure modes that owe
// three different actions:
//
//	present & ours    — a commit that exists in the repo. Comparable to main.
//	ABSENT            — no vcs stamp at all. NOT comparable to anything.
//	present & FOREIGN — a real commit, from a DIFFERENT repo. NOT comparable.
//
// Collapsing those into one empty string is how a check ends up saying "behind
// main" — a claim about ancestry it never measured and, for the last two cases,
// could not measure. So each observation carries its own sentinel, and the
// provenance gate runs BEFORE any comparison to main.
//
// THE DEGRADED MODE IS THE POINT, NOT A FALLBACK. The shell script always has a
// checkout — it is IN one. A consumer usually is not, and for them the `main`
// axis is simply unavailable. But two of the three axes still are, and
// running-vs-installed is a real, self-contained finding: a daemon still
// running code that `go install` already replaced on disk is drifted, and that
// is decidable with no repo, no git, and no network. Reporting the two axes we
// can measure, and naming the third as unavailable, is strictly better than
// refusing to answer — and it is the case the ticket's value statement
// describes ("without cloning the repo").
package selfdrift

import (
	"fmt"
	"sort"
	"strings"
)

// Revision sentinels. Each names a DIFFERENT kind of absence, because each owes
// a different action; two absences that mean different things must never arrive
// at the classifier as the same empty string. They are angle-bracketed so they
// can never collide with a hex revision.
const (
	RevUnstamped   = "<unstamped>"   // the binary is there and carries NO vcs stamp
	RevMissing     = "<missing>"     // no binary at that path at all
	RevUnreachable = "<unreachable>" // pogod is not answering /version
)

// DeployedCmds is every binary a pogo deploy owns, in report order.
//
// `pogo` is here and not optional cargo: the CLI is the operator's only
// interface to the daemon, and a `pogo` older than the `pogod` it talks to is a
// protocol mismatch waiting to happen. A drift check that reads only the daemon
// reports health it has not measured — that is how the CLI once sat three days
// behind main while the check called the box clean (mg-ddf1). Add a binary here
// and every axis below covers it for free; that coupling is deliberate.
var DeployedCmds = []string{"pogod", "pogo"}

// Status is the top-level verdict. Three values, not two, because "we cannot
// tell" is a real answer and must not be reported as either of the others.
type Status string

const (
	// StatusClean — every axis measured, everything matches.
	StatusClean Status = "clean"
	// StatusDrift — measured, and something is out of date.
	StatusDrift Status = "drift"
	// StatusUnknown — the check could not establish what is deployed. NEVER
	// report this as clean: a binary that has told us nothing is not a clean
	// box, and it is not a stale one either.
	StatusUnknown Status = "unknown"
)

// Axis is one of the three things a revision can be observed on.
type Axis struct {
	// Name is the axis label, e.g. "running pogod" or "installed pogo".
	Name string `json:"name"`
	// Revision is the observed revision, or one of the sentinels above.
	Revision string `json:"revision"`
	// Path is where the observation was made — the binary path for an
	// installed axis, the daemon URL for the running one. A report that names
	// a revision but not where it came from cannot be re-derived by hand.
	Path string `json:"path,omitempty"`
	// Note annotates the revision with what we know ABOUT it (foreign, no
	// stamp, unreachable). A bare hash tells the reader nothing about whether
	// it is even this repo's hash.
	Note string `json:"note,omitempty"`
}

// Report is the whole three-way, plus the verdict.
type Report struct {
	// Repo is the pogo source checkout used for the `main` axis; empty when
	// none could be resolved.
	Repo string `json:"repo,omitempty"`
	// RepoNote says how the repo was found, or — when Repo is empty — why it
	// was not. The second half matters more: a consumer with no checkout must
	// be told that is why the third axis is missing, not left to guess.
	RepoNote string `json:"repo_note,omitempty"`
	// Ref is the deploy ref whose HEAD is the `main` axis (default "main").
	Ref string `json:"ref"`
	// Main is Ref's HEAD in Repo; empty when unresolvable.
	Main string `json:"main,omitempty"`

	Axes []Axis `json:"axes"`

	Status       Status `json:"status"`
	NeedsBuild   bool   `json:"needs_build"`
	NeedsRestart bool   `json:"needs_restart"`
	// Action is the one-line owed-action sentence, written so it can be
	// re-derived from its own output rather than re-investigated.
	Action string `json:"action"`
}

// Deps is every outside observation the check makes. Split out so the
// classifier — the part with all the judgement in it — is testable without a
// daemon, a git checkout, or an installed binary. HostDeps wires the real ones.
type Deps struct {
	// RunningRev is the revision the LIVE pogod self-reports over HTTP, plus
	// the URL probed. Must come from the process, not from `go version -m` on
	// the on-disk file: `go install` rewrites that file underneath a live
	// daemon, so the file's revision diverges from the running one the instant
	// a rebuild happens — which is precisely the drift we are looking for.
	// Returns RevUnreachable when the daemon does not answer, RevUnstamped
	// when it answers without a revision. Those are different states: a daemon
	// that will not talk owes a restart, a daemon that talks but cannot say
	// what it is owes an investigation.
	RunningRev func() (rev, url string)
	// InstalledBin resolves where a deployed binary would be found, whether or
	// not it exists.
	InstalledBin func(name string) string
	// BinaryRev reads the vcs stamp baked into an on-disk binary, returning
	// RevMissing when there is no file and RevUnstamped when there is a file
	// with no stamp. Those are different too: a build IS owed for the first
	// and a build fixes it; a build is NOT owed for the second and does not
	// fix it, because the rebuild is unstamped as well and the drift would
	// never clear — a reconcile loop against an artifact that is not broken.
	BinaryRev func(path string) string
	// ResolveRepo returns the pogo source checkout and a note describing how
	// it was found; an empty repo with a note explaining the absence is a
	// legitimate, expected answer for a consumer who has no checkout.
	ResolveRepo func() (repo, note string)
	// MainRev resolves ref's HEAD in repo, or "" when it cannot.
	MainRev func(repo, ref string) string
	// RevInRepo reports whether repo actually contains rev. This is the whole
	// foreign-stamp test and it is re-derivable by hand:
	// `git -C <repo> cat-file -e <rev>^{commit}`.
	RevInRepo func(repo, rev string) bool
}

// DefaultRef is the deploy ref whose HEAD the installed binaries are compared
// against.
const DefaultRef = "main"

// isSentinel reports whether rev is one of the not-comparable-to-anything
// values (including the empty string, which no caller should produce but which
// must not be silently compared to a hash if one does).
func isSentinel(rev string) bool {
	switch rev {
	case RevUnstamped, RevMissing, RevUnreachable, "":
		return true
	}
	return false
}

// Short truncates a revision for prose, leaving sentinels intact — a truncated
// "<unreacha" helps nobody.
func Short(rev string) string {
	if isSentinel(rev) {
		return rev
	}
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// runningAxis is the axis name for the live daemon.
const runningAxis = "running pogod"

// installedAxis names the on-disk axis for a deployed binary.
func installedAxis(name string) string { return "installed " + name }

// Check makes every observation through deps and classifies the result. It
// never mutates anything.
func Check(deps Deps, ref string) Report {
	if ref == "" {
		ref = DefaultRef
	}
	r := Report{Ref: ref}
	r.Repo, r.RepoNote = deps.ResolveRepo()
	if r.Repo != "" {
		r.Main = deps.MainRev(r.Repo, ref)
		if r.Main == "" && r.RepoNote != "" {
			r.RepoNote += fmt.Sprintf("; %s has no ref %q", r.Repo, ref)
		}
	}

	runRev, runURL := deps.RunningRev()
	r.Axes = append(r.Axes, Axis{Name: runningAxis, Revision: runRev, Path: runURL})
	for _, name := range DeployedCmds {
		path := deps.InstalledBin(name)
		r.Axes = append(r.Axes, Axis{Name: installedAxis(name), Revision: deps.BinaryRev(path), Path: path})
	}

	annotate(&r, deps)
	classify(&r)
	return r
}

// annotate fills each axis's Note. Done before classification so the human
// report and the verdict cannot disagree about what was observed.
func annotate(r *Report, deps Deps) {
	for i := range r.Axes {
		a := &r.Axes[i]
		switch a.Revision {
		case RevUnstamped:
			a.Note = "no vcs stamp — provenance UNKNOWN"
			continue
		case RevMissing:
			a.Note = "not on disk"
			continue
		case RevUnreachable:
			a.Note = "pogod is not answering /version"
			continue
		}
		if r.Repo == "" {
			continue // no repo: the foreign test is not measurable, so do not imply it ran
		}
		if !deps.RevInRepo(r.Repo, a.Revision) {
			a.Note = "FOREIGN — no such commit in " + r.Repo
		}
	}
}

// axesWhere names the axes whose revision satisfies pred, in report order.
func axesWhere(r *Report, pred func(Axis) bool) []string {
	var out []string
	for _, a := range r.Axes {
		if pred(a) {
			out = append(out, a.Name)
		}
	}
	return out
}

// rev returns the observed revision for a named axis.
func (r *Report) rev(name string) string {
	for _, a := range r.Axes {
		if a.Name == name {
			return a.Revision
		}
	}
	return ""
}

// staleBins names the deployed binaries whose installed revision is behind
// main, so the operator can read WHY a build is owed.
func (r *Report) staleBins() string {
	var out []string
	for _, name := range DeployedCmds {
		if r.rev(installedAxis(name)) != r.Main {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// classify sets Status, NeedsBuild, NeedsRestart and Action. It is the whole
// judgement of this package and makes no outside call.
func classify(r *Report) {
	// THE PROVENANCE GATE, first — before any comparison to main, because a
	// comparison to main is only meaningful once we know the thing being
	// compared is a commit from this repo. Both branches below report UNKNOWN
	// rather than owing a build: a build is the answer to "behind", and
	// neither of these is behind.
	if un := axesWhere(r, func(a Axis) bool { return a.Revision == RevUnstamped }); len(un) > 0 {
		r.Status = StatusUnknown
		r.Action = fmt.Sprintf("UNKNOWN PROVENANCE: %s — NO vcs stamp. This is not a clean install and it is not a stale one; the binary has told us nothing. A rebuild will NOT clear this: an unstamped binary rebuilds unstamped. Verify with: go version -m $(command -v pogod) | grep vcs",
			strings.Join(un, ", "))
		return
	}
	if r.Repo != "" {
		var foreign []string
		for _, a := range r.Axes {
			if strings.HasPrefix(a.Note, "FOREIGN") {
				foreign = append(foreign, fmt.Sprintf("%s claims %s", a.Name, Short(a.Revision)))
			}
		}
		if len(foreign) > 0 {
			r.Status = StatusUnknown
			r.Action = fmt.Sprintf("FOREIGN STAMP: %s — no such commit in %s. This binary was built against a DIFFERENT repository; its revision describes that repo, not this one. Verify with: git -C %s cat-file -e <rev>^{commit}",
				strings.Join(foreign, "; "), r.Repo, r.Repo)
			return
		}
	}

	running := r.rev(runningAxis)
	installedD := r.rev(installedAxis("pogod"))

	// THE DEGRADED MODE. No checkout means no `main` axis — but running vs
	// installed is still fully measurable, and a disagreement between them is
	// real drift that needs no repo to establish.
	if r.Main == "" {
		why := r.RepoNote
		if why == "" {
			why = "no pogo source checkout resolved"
		}
		switch {
		case running == RevUnreachable:
			r.Status = StatusUnknown
			r.Action = fmt.Sprintf("pogod is NOT RUNNING (or not answering %s), so there is nothing to compare the installed binary against. Installed pogod is %s. The main axis is unavailable too: %s",
				r.axisPath(runningAxis), Short(installedD), why)
		case installedD == RevMissing:
			r.Status = StatusUnknown
			r.Action = fmt.Sprintf("a pogod is running (%s) but no pogod binary is installed at %s, so a restart would not bring it back. The main axis is unavailable: %s",
				Short(running), r.axisPath(installedAxis("pogod")), why)
		case running == installedD:
			r.Status = StatusUnknown
			r.Action = fmt.Sprintf("running pogod matches the installed binary (%s), but there is no third axis to compare them to: %s. Pass --repo (or set POGO_REPO) to a pogo checkout for the full running/installed/main report",
				Short(running), why)
		default:
			r.Status = StatusDrift
			r.NeedsRestart = true
			r.Action = fmt.Sprintf("RESTART owed: the running pogod is %s but the binary installed at %s is %s — the daemon is running code that has already been replaced on disk. Restart it to pick the new binary up. (main HEAD not compared: %s)",
				Short(running), r.axisPath(installedAxis("pogod")), Short(installedD), why)
		}
		return
	}

	// A build is owed if ANY deployed binary is behind main, not just pogod.
	for _, name := range DeployedCmds {
		if r.rev(installedAxis(name)) != r.Main {
			r.NeedsBuild = true
		}
	}
	r.NeedsRestart = running != r.Main

	switch {
	case !r.NeedsBuild && !r.NeedsRestart:
		r.Status = StatusClean
		r.Action = fmt.Sprintf("clean — running == installed (%s) == %s HEAD %s; nothing owed",
			strings.Join(DeployedCmds, ", "), r.Ref, Short(r.Main))
	case running == RevUnreachable:
		// Distinct from "stale": there is no process to be stale. Saying
		// "restart owed" without saying the daemon is down sends the reader
		// looking for a version mismatch that is not the finding.
		r.Status = StatusDrift
		r.Action = fmt.Sprintf("pogod is NOT RUNNING (nothing answering %s). Installed pogod is %s, %s HEAD is %s%s",
			r.axisPath(runningAxis), Short(installedD), r.Ref, Short(r.Main), buildSuffix(r))
	case !r.NeedsBuild:
		r.Status = StatusDrift
		r.Action = fmt.Sprintf("RESTART owed (no rebuild): the installed binaries already match %s HEAD %s, but the running pogod is %s. Restart the daemon.",
			r.Ref, Short(r.Main), Short(running))
	case !r.NeedsRestart:
		r.Status = StatusDrift
		r.Action = fmt.Sprintf("BUILD owed (no restart): the running pogod is already %s HEAD %s, but installed %s %s behind. Reinstall from %s.",
			r.Ref, Short(r.Main), r.staleBins(), pluralIs(r.staleBins()), r.Repo)
	case running == installedD:
		r.Status = StatusDrift
		r.Action = fmt.Sprintf("BUILD + RESTART owed: running == installed (%s), both behind %s HEAD %s. Reinstall from %s, then restart the daemon.",
			Short(running), r.Ref, Short(r.Main), r.Repo)
	default:
		r.Status = StatusDrift
		r.Action = fmt.Sprintf("BUILD + RESTART owed: running (%s), installed (%s) and %s HEAD (%s) all differ — %s behind. Reinstall from %s, then restart the daemon.",
			Short(running), Short(installedD), r.Ref, Short(r.Main), r.staleBins(), r.Repo)
	}
}

// buildSuffix appends the build half of the verdict to the not-running case,
// which would otherwise silently drop a stale on-disk binary.
func buildSuffix(r *Report) string {
	if !r.NeedsBuild {
		return " (the installed binaries are current; the daemon simply is not up)"
	}
	return fmt.Sprintf(" — and installed %s %s behind, so a plain restart would start stale code",
		r.staleBins(), pluralIs(r.staleBins()))
}

// pluralIs agrees the verb with a comma-joined binary list.
func pluralIs(list string) string {
	if strings.Contains(list, ",") {
		return "are"
	}
	return "is"
}

// axisPath returns an axis's observation path, for prose that must name where
// it looked.
func (r *Report) axisPath(name string) string {
	for _, a := range r.Axes {
		if a.Name == name {
			return a.Path
		}
	}
	return "<unknown>"
}

// Text renders the operator-facing report: the axes, then the verdict. Same
// shape as scripts/pogo-self-deploy's `check`, so an operator who knows one
// reads the other.
//
// Every axis prints WHERE it was observed, not just what it observed. A
// revision with no provenance is exactly the kind of output that gets
// re-investigated from scratch rather than re-derived — a stale hash and a
// hash read from a binary nobody expected to be on the PATH look identical
// until the path is on the page.
func (r *Report) Text() string {
	rows := make([][2]string, 0, len(r.Axes)+3)
	for _, a := range r.Axes {
		rev := a.Revision
		if isSentinel(a.Revision) && a.Note != "" {
			rev = "<" + a.Note + ">"
		} else if a.Note != "" {
			rev += "  <" + a.Note + ">"
		}
		if a.Path != "" {
			rev += "  (" + a.Path + ")"
		}
		rows = append(rows, [2]string{a.Name, rev})
	}
	main := r.Main
	if main == "" {
		main = "<unavailable>"
		if r.RepoNote != "" {
			main = "<unavailable — " + r.RepoNote + ">"
		}
	}
	rows = append(rows,
		[2]string{r.Ref + " HEAD", main},
		[2]string{"status", string(r.Status)},
		[2]string{"action", r.Action},
	)

	width := 0
	for _, row := range rows {
		if len(row[0]) > width {
			width = len(row[0])
		}
	}
	repo := r.Repo
	if repo == "" {
		repo = "<none>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "revision drift (repo: %s, ref: %s)\n", repo, r.Ref)
	for _, row := range rows {
		fmt.Fprintf(&b, "  %-*s : %s\n", width, row[0], row[1])
	}
	return b.String()
}
