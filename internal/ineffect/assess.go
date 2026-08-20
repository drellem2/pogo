package ineffect

// The CARRIER -> VERDICT half (mg-3d0e). classify.go says what kind of carrier
// a changed file has; this file asks the carriers on this box whether they
// carry the commit, and refuses to guess when it cannot tell.
//
// THREE VERDICTS PER CARRIER, NEVER TWO. `unknown` is not a soft `inert`: the
// two owe different actions (redeploy vs investigate) and the failure this
// whole ticket is about is a reader taking an unmeasured thing for a measured
// one. So a carrier that could not be read never renders in the same column as
// a carrier that was read and disagreed.
//
// AND AN OVERALL VERDICT THAT CAN SAY `half-live`, because on this box that is
// the common case and it has no name in any existing surface. 2026-08-19:
// pogo-deploy.sh was current and live while the pogod that would have executed
// the same commit's Go changes was five days behind. One commit, two carriers,
// two different true answers. A verdict vocabulary of {live, inert} cannot
// express that and would have to pick one of them to be wrong about.
//
// TWO PREDICATES, AND THEY ARE NOT INTERCHANGEABLE:
//
//	ANCESTRY   for a carrier that reports a revision (a binary's vcs stamp, a
//	           daemon's /version, a checkout's HEAD). `merge-base --is-ancestor`
//	           and nothing else. Cheap, exact, and unavailable for anything that
//	           does not carry a stamp.
//	CONTENT    for a carrier that is a COPY of one file (~/.pogo/bin/foo.sh,
//	           ~/.pogo/agents/mayor.md). A copy carries no revision, so the only
//	           available question is which revision's bytes it holds. We find the
//	           newest commit touching that path whose blob matches the copy, and
//	           then ask ancestry about THAT commit.
//
// The content predicate has one honest limit, stated here rather than buried: a
// commit that is later REVERTED leaves the file's bytes equal to a pre-commit
// revision, so a copy of the reverted content is reported against the revert's
// commit — which is correct about the bytes and correct about "the change is
// not in this file", but arrives at the reader as `inert` rather than as
// `reverted`. Distinguishing them would need the semantic history, which no
// file copy carries.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Verdict is one carrier's answer about one commit.
type Verdict string

const (
	// Live — this carrier was read and it carries the commit.
	Live Verdict = "live"
	// Inert — this carrier was read and it does not carry the commit.
	Inert Verdict = "inert"
	// Unknown — the carrier could not be read, or what was read is not
	// comparable (no vcs stamp, a foreign revision, a locally edited copy).
	// NEVER collapse into Inert: "I could not look" is not "I looked and it is
	// not there", and only the second one supports a decision.
	Unknown Verdict = "unknown"
	// NotApplicable — this class has no runtime carrier to ask.
	NotApplicable Verdict = "n/a"
)

// Overall is the report's single-word answer.
type Overall string

const (
	OverallLive       Overall = "live"
	OverallInert      Overall = "inert"
	OverallHalfLive   Overall = "half-live"
	OverallUnknown    Overall = "unknown"
	OverallNoCarriers Overall = "no-runtime-carrier"
)

// RevCarrier is a carrier that reports a revision of its own.
type RevCarrier struct {
	// Name is the label the report uses, e.g. "running pogod".
	Name string `json:"name"`
	// At is where the observation was made — a URL, a binary path, a checkout
	// path. A verdict whose source cannot be re-derived by hand is a verdict a
	// reader has to take on faith.
	At string `json:"at"`
	// Revision is the observed revision, or a sentinel (see selfdrift's
	// RevMissing / RevUnstamped / RevUnreachable, which HostDeps passes
	// through unchanged).
	Revision string `json:"revision"`
	// Binary names the cmd/ program this carrier is built from, "" for a
	// checkout. Compiled changes are matched to carriers by this field.
	Binary string `json:"binary,omitempty"`
	// Checkout marks a carrier that executes files in place from a working
	// tree rather than from a build.
	Checkout bool `json:"checkout,omitempty"`
	// Remedy is the command that would make this carrier current.
	Remedy string `json:"remedy,omitempty"`
}

// FileCarrier is an installed COPY of one repo file.
type FileCarrier struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Data   []byte `json:"-"`
	Err    error  `json:"-"`
	Remedy string `json:"remedy,omitempty"`
}

// Deps is every outside observation, split out so the judgement is testable
// with no git, no daemon and no home directory.
type Deps struct {
	// Resolve turns a user-supplied rev into a full sha plus one line of
	// context for the header.
	Resolve func(rev string) (full, subject, when string, err error)
	// ChangedPaths lists the repo-relative paths a commit touched.
	ChangedPaths func(rev string) ([]string, error)
	// IsAncestor reports whether anc is an ancestor of (or equal to) desc.
	// Re-derivable by hand: `git merge-base --is-ancestor anc desc`.
	IsAncestor func(anc, desc string) (bool, error)
	// BlobAt returns the bytes of path at rev.
	BlobAt func(rev, p string) ([]byte, error)
	// PathHistory lists commits touching p on the survey ref, newest first,
	// capped at max.
	PathHistory func(p string, max int) ([]string, error)
	// RevCarriers is every revision-reporting carrier found on this box.
	RevCarriers []RevCarrier
	// GoCarriers names the cmd/ binaries whose build graph includes pkgDir.
	// An empty pkgDir means "everything" (go.mod, go.sum).
	GoCarriers func(pkgDir string) ([]string, error)
	// FileCarriers returns the installed copies of a repo path — empty when
	// the path has none, which is itself an observation and not an error.
	FileCarriers func(repoPath string) []FileCarrier
	// NormalizePrompt strips whatever the prompt installer stamps into an
	// installed file, so the content comparison is of bodies. Identity is a
	// valid value; nil means identity.
	NormalizePrompt func([]byte) []byte
	// SurveyRef names the ref PathHistory walks, for the report's prose.
	SurveyRef string
	// Reporter identifies the binary producing the report, with the provenance
	// of its revision (HostDeps fills it from internal/version, the enumerated
	// reader of the toolchain stamp — see SelfDescription).
	//
	// THIS FIELD IS THE COMMAND APPLIED TO ITSELF. `pogo in-effect` ships
	// inside the pogo CLI, so it is an artifact of exactly the class it
	// reports on: an installed `pogo` older than this commit does not have the
	// command at all, and one older than a later change to Classify answers
	// with that build's rules, not main's. A report that did not say which
	// build produced it would be a merged-not-live hazard wearing the uniform
	// of the instrument for merged-not-live hazards. Empty renders as
	// "unstamped", never as silence.
	Reporter string
}

// HistoryDepth caps PathHistory. Deep enough that a copy from any plausibly
// current deploy is found, shallow enough that the command stays interactive.
const HistoryDepth = 200

// CarrierVerdict is one carrier's row.
type CarrierVerdict struct {
	Carrier  string  `json:"carrier"`
	At       string  `json:"at"`
	Observed string  `json:"observed,omitempty"`
	Verdict  Verdict `json:"verdict"`
	Why      string  `json:"why"`
	Remedy   string  `json:"remedy,omitempty"`
}

// Finding is one artifact class, its paths, and every carrier for them.
type Finding struct {
	Class    Class            `json:"class"`
	Paths    []string         `json:"paths"`
	Carriers []CarrierVerdict `json:"carriers"`
	Note     string           `json:"note,omitempty"`
}

// Report is the whole answer.
type Report struct {
	Commit   string    `json:"commit"`
	Subject  string    `json:"subject,omitempty"`
	When     string    `json:"when,omitempty"`
	Ref      string    `json:"survey_ref,omitempty"`
	Findings []Finding `json:"findings"`
	Overall  Overall   `json:"overall"`
	Summary  string    `json:"summary"`
	// Reporter identifies the binary that produced this report — see
	// Deps.Reporter. Read it before trusting an answer that surprises you.
	Reporter string `json:"reporter,omitempty"`
	Err      string `json:"error,omitempty"`
}

// runningAgentsNote is the standing caveat on every prompt finding. It is
// unconditional on purpose: no observation available here can establish what
// text a live agent is holding, because the agent read it at spawn and keeps no
// copy anything can compare. Rendering a prompt row as plain `live` would be
// this ticket's own defect committed by its own fix.
const runningAgentsNote = "an agent reads its prompt ONCE, at spawn: even a current installed corpus is not in effect for any agent already running, and nothing on this box reports what text a live agent is holding (mg-385f). `pogo agent list` gives start times; an agent started before the corpus was installed is holding the old text."

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Assess answers "is rev in effect?" by asking every carrier of every artifact
// class the commit touched.
func Assess(rev string, deps Deps) Report {
	rep := Report{Ref: deps.SurveyRef, Reporter: deps.Reporter}

	full, subject, when, err := deps.Resolve(rev)
	if err != nil {
		rep.Err = err.Error()
		rep.Overall = OverallUnknown
		rep.Summary = fmt.Sprintf("cannot answer: %v", err)
		return rep
	}
	rep.Commit, rep.Subject, rep.When = full, subject, when

	paths, err := deps.ChangedPaths(full)
	if err != nil {
		rep.Err = err.Error()
		rep.Overall = OverallUnknown
		rep.Summary = fmt.Sprintf("cannot answer: %v", err)
		return rep
	}
	if len(paths) == 0 {
		rep.Overall = OverallNoCarriers
		rep.Summary = "the commit changed no files, so there is nothing for any carrier to carry"
		return rep
	}

	byClass := map[Class][]string{}
	for _, p := range paths {
		c := Classify(p)
		byClass[c] = append(byClass[c], p)
	}

	for _, class := range classOrder {
		ps := byClass[class]
		if len(ps) == 0 {
			continue
		}
		sort.Strings(ps)
		rep.Findings = append(rep.Findings, buildFinding(class, ps, full, deps))
	}

	rep.Overall, rep.Summary = conclude(rep.Findings)
	return rep
}

func buildFinding(class Class, ps []string, commit string, deps Deps) Finding {
	f := Finding{Class: class, Paths: ps}
	switch class {
	case ClassCompiled:
		f.Carriers = compiledCarriers(ps, commit, deps)
		f.Note = "a compiled change needs BOTH a rebuild and a restart of every binary below; a rebuilt binary that has not been re-executed is still running the old code. Which binaries carry a package is read from the CURRENT working tree's import graph, so for a commit that moved an import the carrier list is today's, not that commit's"
	case ClassPrompt:
		f.Carriers = append(compiledCarriersFor([]string{PromptEmbedPkg}, commit, deps),
			fileCarriers(ps, commit, deps, deps.NormalizePrompt)...)
		f.Note = runningAgentsNote
	case ClassAsset:
		f.Carriers = append(fileCarriers(ps, commit, deps, nil), checkoutCarriers(commit, deps)...)
		f.Note = "an asset with no installed copy is carried by whichever checkout executes it; a fresh checkout of a branch containing the commit carries it by construction, which is why gate scripts take effect on merge"
		if note := plistNote(ps); note != "" {
			f.Note += ". " + note
		}
	case ClassUnclassified:
		for _, p := range ps {
			f.Carriers = append(f.Carriers, CarrierVerdict{
				Carrier: "unknown", At: p, Verdict: Unknown,
				Why:    "no classification rule matches this path, so which carrier reaches a runtime with it has not been established",
				Remedy: "add a rule to internal/ineffect.Classify, or say in the report that this file was not judged",
			})
		}
	case ClassNoCarrier:
		f.Carriers = []CarrierVerdict{{
			Carrier: "none", Verdict: NotApplicable,
			Why: "documentation, changelog fragments, tests and fixtures are executed by no runtime; `in effect` has no meaning for them and this row is not a pass for anything else in the commit",
		}}
	}
	return f
}

// compiledCarriers resolves the package dirs the paths belong to and asks the
// dependency graph which binaries build them.
func compiledCarriers(ps []string, commit string, deps Deps) []CarrierVerdict {
	seen := map[string]bool{}
	var dirs []string
	for _, p := range ps {
		d := PkgDir(p)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return compiledCarriersFor(dirs, commit, deps)
}

func compiledCarriersFor(dirs []string, commit string, deps Deps) []CarrierVerdict {
	bins := map[string]bool{}
	var lookupErr error
	for _, d := range dirs {
		names, err := deps.GoCarriers(d)
		if err != nil {
			lookupErr = err
			continue
		}
		for _, n := range names {
			bins[n] = true
		}
	}
	var out []CarrierVerdict
	for _, rc := range deps.RevCarriers {
		if rc.Binary == "" || !bins[rc.Binary] {
			continue
		}
		out = append(out, revVerdict(rc, commit, deps))
	}
	if len(out) == 0 {
		why := "no binary on this box is built from these packages, so nothing here executes this change"
		if lookupErr != nil {
			why = fmt.Sprintf("the build graph could not be read (%v), so which binaries carry this change was NOT established", lookupErr)
		}
		out = append(out, CarrierVerdict{
			Carrier: "compiled binaries", Verdict: Unknown, Why: why,
			Remedy: "run `go list -deps ./cmd/...` from the repo to see which programs import these packages",
		})
	}
	return out
}

func checkoutCarriers(commit string, deps Deps) []CarrierVerdict {
	var out []CarrierVerdict
	for _, rc := range deps.RevCarriers {
		if !rc.Checkout {
			continue
		}
		out = append(out, revVerdict(rc, commit, deps))
	}
	return out
}

// revVerdict is the ANCESTRY predicate.
func revVerdict(rc RevCarrier, commit string, deps Deps) CarrierVerdict {
	cv := CarrierVerdict{Carrier: rc.Name, At: rc.At, Observed: rc.Revision, Remedy: rc.Remedy}
	if isSentinel(rc.Revision) {
		cv.Verdict = Unknown
		cv.Why = fmt.Sprintf("the carrier reports %s, which is not a revision and is comparable to nothing", rc.Revision)
		return cv
	}
	ok, err := deps.IsAncestor(commit, rc.Revision)
	if err != nil {
		cv.Verdict = Unknown
		cv.Why = fmt.Sprintf("ancestry could not be established (%v); `git merge-base --is-ancestor %s %s` is the check", err, short(commit), short(rc.Revision))
		return cv
	}
	if ok {
		cv.Verdict = Live
		cv.Why = fmt.Sprintf("%s contains %s", short(rc.Revision), short(commit))
		cv.Remedy = ""
		return cv
	}
	cv.Verdict = Inert
	cv.Why = fmt.Sprintf("%s does not contain %s", short(rc.Revision), short(commit))
	return cv
}

// fileCarriers is the CONTENT predicate, one row per installed copy.
func fileCarriers(ps []string, commit string, deps Deps, norm func([]byte) []byte) []CarrierVerdict {
	var out []CarrierVerdict
	for _, p := range ps {
		for _, fc := range deps.FileCarriers(p) {
			out = append(out, contentVerdict(fc, p, commit, deps, norm))
		}
	}
	return out
}

func contentVerdict(fc FileCarrier, repoPath, commit string, deps Deps, norm func([]byte) []byte) CarrierVerdict {
	cv := CarrierVerdict{Carrier: fc.Name, At: fc.Path, Remedy: fc.Remedy}
	if fc.Err != nil {
		cv.Verdict = Unknown
		cv.Why = fmt.Sprintf("the installed copy could not be read (%v), so what it holds was NOT established", fc.Err)
		return cv
	}
	if norm == nil {
		norm = func(b []byte) []byte { return b }
	}
	want := sum(norm(fc.Data))

	hist, err := deps.PathHistory(repoPath, HistoryDepth)
	if err != nil {
		cv.Verdict = Unknown
		cv.Why = fmt.Sprintf("the history of %s could not be read (%v), so the copy could not be dated", repoPath, err)
		return cv
	}
	for _, c := range hist {
		blob, err := deps.BlobAt(c, repoPath)
		if err != nil {
			if errors.Is(err, errNoBlob) {
				continue
			}
			cv.Verdict = Unknown
			cv.Why = fmt.Sprintf("%s at %s could not be read (%v)", repoPath, short(c), err)
			return cv
		}
		if sum(norm(blob)) != want {
			continue
		}
		cv.Observed = short(c)
		ok, err := deps.IsAncestor(commit, c)
		if err != nil {
			cv.Verdict = Unknown
			cv.Why = fmt.Sprintf("the copy is %s as of %s, and ancestry against %s could not be established (%v)", repoPath, short(c), short(commit), err)
			return cv
		}
		if ok {
			cv.Verdict = Live
			cv.Why = fmt.Sprintf("the copy is byte-identical to %s as of %s, which contains %s", repoPath, short(c), short(commit))
			cv.Remedy = ""
		} else {
			cv.Verdict = Inert
			cv.Why = fmt.Sprintf("the copy is byte-identical to %s as of %s, which does not contain %s", repoPath, short(c), short(commit))
		}
		return cv
	}
	cv.Verdict = Unknown
	cv.Observed = "unmatched"
	cv.Why = fmt.Sprintf("the copy matches no revision of %s in the last %d commits touching it on %s — it is locally edited, or from another branch, and cannot be dated (`pogo check-staleness` compares the whole installed corpus against a ref and says which)", repoPath, HistoryDepth, refOr(deps.SurveyRef))
	return cv
}

func refOr(ref string) string {
	if ref == "" {
		return "the survey ref"
	}
	return ref
}

// errNoBlob is what a Deps.BlobAt implementation returns when the path does not
// exist at that revision — an expected condition while walking history past a
// rename or an add, not a failure to measure.
var errNoBlob = errors.New("path does not exist at that revision")

// ErrNoBlob is the sentinel Deps.BlobAt implementations wrap for "absent at
// that revision".
func ErrNoBlob() error { return errNoBlob }

func isSentinel(rev string) bool {
	return rev == "" || strings.HasPrefix(rev, "<")
}

func short(rev string) string {
	if isSentinel(rev) {
		return rev
	}
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// conclude folds every carrier row into one word, and writes the sentence that
// travels. The counts are in the sentence deliberately: a bare `half-live` in a
// subject line is the part that gets forwarded, and it has to carry which half.
func conclude(fs []Finding) (Overall, string) {
	var live, inert, unknown, na int
	inertNames := map[string]bool{}
	for _, f := range fs {
		for _, c := range f.Carriers {
			switch c.Verdict {
			case Live:
				live++
			case Inert:
				inert++
				inertNames[c.Carrier] = true
			case Unknown:
				unknown++
			case NotApplicable:
				na++
			}
		}
	}
	names := make([]string, 0, len(inertNames))
	for n := range inertNames {
		names = append(names, n)
	}
	sort.Strings(names)

	switch {
	case live+inert+unknown == 0:
		return OverallNoCarriers, fmt.Sprintf("nothing in this commit reaches a runtime: all %d artifact classes touched are documentation, changelog or test material", len(fs))
	case inert > 0 && live > 0:
		return OverallHalfLive, fmt.Sprintf("HALF-LIVE: %d carrier(s) carry this commit and %d do not (%s). Which half a reader is acting through decides whether the change is protecting them",
			live, inert, strings.Join(names, ", "))
	case inert > 0:
		return OverallInert, fmt.Sprintf("NOT IN EFFECT: %d carrier(s) do not carry this commit (%s)%s", inert, strings.Join(names, ", "), unknownSuffix(unknown))
	case unknown > 0 && live > 0:
		return OverallUnknown, fmt.Sprintf("PARTLY UNESTABLISHED: %d carrier(s) carry this commit, %d could not be read. An unread carrier is not a passing one", live, unknown)
	case unknown > 0:
		return OverallUnknown, fmt.Sprintf("NOT ESTABLISHED: %d carrier(s) could not be read, so nothing here says this commit is or is not in effect", unknown)
	default:
		return OverallLive, fmt.Sprintf("IN EFFECT: all %d carrier(s) of this commit's artifacts carry it", live)
	}
}

func unknownSuffix(unknown int) string {
	if unknown == 0 {
		return ""
	}
	return fmt.Sprintf(", and %d more could not be read at all", unknown)
}

// plistNote names the one asset this command deliberately does not judge.
//
// A LaunchAgent plist is not COPIED from the repo — internal/service RENDERS it,
// binding POGO_HOME and resolved binary paths, so the file in
// ~/Library/LaunchAgents is byte-identical to no git blob and the content
// predicate above would report every plist as undatable forever. That question
// already has an instrument with the right predicate (would re-running the
// installer change this file?), and pointing at it is better than a second,
// weaker answer next to it — a plist whose fires are missing is exactly the
// failure mg-fc99 was filed for, and it wants that check, not this one.
func plistNote(ps []string) string {
	for _, p := range ps {
		if strings.HasSuffix(p, ".plist") {
			return "a LaunchAgent plist is RENDERED by the installer rather than copied, so it matches no git blob and is NOT judged here: `pogo doctor --check` carries the activation audit, which asks the installer's own question (would re-running it change this file?)"
		}
	}
	return ""
}

// pathsPreview renders at most n paths for prose.
func pathsPreview(ps []string, n int) string {
	if len(ps) <= n {
		return strings.Join(ps, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ps[:n], ", "), len(ps)-n)
}

// basename is used by the host wiring to find installed copies by name.
func basename(p string) string { return path.Base(p) }
