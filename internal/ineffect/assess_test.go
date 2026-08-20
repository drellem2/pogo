package ineffect

import (
	"errors"
	"strings"
	"testing"
)

// fake is a Deps builder for a small synthetic history. Ancestry is expressed
// as an explicit map so a test states the graph it means rather than depending
// on git.
type fake struct {
	paths    []string
	ancestor map[string]bool // "commit->carrierRev" = commit is an ancestor
	blobs    map[string]string
	history  map[string][]string
	rev      []RevCarrier
	files    map[string][]FileCarrier
	gomap    map[string][]string
	goErr    error
	ancErr   error
}

func (f fake) deps() Deps {
	return Deps{
		SurveyRef: "main",
		Reporter:  "aaaaaaaaaaaabbbb",
		Resolve: func(rev string) (string, string, string, error) {
			if rev == "nope" {
				return "", "", "", errors.New("unknown revision")
			}
			return rev, "subject of " + rev, "2026-08-19T00:00:00Z", nil
		},
		ChangedPaths: func(string) ([]string, error) { return f.paths, nil },
		IsAncestor: func(anc, desc string) (bool, error) {
			if f.ancErr != nil {
				return false, f.ancErr
			}
			return f.ancestor[anc+"->"+desc], nil
		},
		BlobAt: func(rev, p string) ([]byte, error) {
			b, ok := f.blobs[rev+":"+p]
			if !ok {
				return nil, ErrNoBlob()
			}
			return []byte(b), nil
		},
		PathHistory: func(p string, _ int) ([]string, error) { return f.history[p], nil },
		RevCarriers: f.rev,
		GoCarriers: func(dir string) ([]string, error) {
			if f.goErr != nil {
				return nil, f.goErr
			}
			return f.gomap[dir], nil
		},
		FileCarriers: func(p string) []FileCarrier { return f.files[p] },
	}
}

func verdictFor(t *testing.T, r Report, carrier string) CarrierVerdict {
	t.Helper()
	for _, f := range r.Findings {
		for _, c := range f.Carriers {
			if c.Carrier == carrier {
				return c
			}
		}
	}
	t.Fatalf("no carrier row named %q in report; rows were %v", carrier, allCarriers(r))
	return CarrierVerdict{}
}

func allCarriers(r Report) []string {
	var out []string
	for _, f := range r.Findings {
		for _, c := range f.Carriers {
			out = append(out, string(c.Verdict)+":"+c.Carrier)
		}
	}
	return out
}

func findingFor(t *testing.T, r Report, class Class) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Class == class {
			return f
		}
	}
	t.Fatalf("no %s finding in report", class)
	return Finding{}
}

// THE TICKET'S CASE, at the shape it actually occurred in on 2026-08-19: one
// commit whose script half was live in an installed copy while its compiled
// half sat in a daemon five days behind. A vocabulary of {live, inert} has to
// pick one of those to be wrong about; `half-live` is what makes both sayable.
func TestHalfLiveIsAVerdict(t *testing.T) {
	f := fake{
		paths: []string{"internal/agent/api.go", "scripts/launchd/pogo-deploy.sh"},
		rev: []RevCarrier{
			{Name: "running pogod", At: "http://x/version", Revision: "old111111111", Binary: "pogod", Remedy: "restart"},
			{Name: "this checkout", At: "/repo", Revision: "new222222222", Checkout: true},
		},
		gomap:    map[string][]string{"internal/agent": {"pogod"}},
		ancestor: map[string]bool{"C->new222222222": true, "C->S1": true},
		files: map[string][]FileCarrier{
			"scripts/launchd/pogo-deploy.sh": {{Name: "installed copy", Path: "/home/bin/pogo-deploy.sh", Data: []byte("current")}},
		},
		history: map[string][]string{"scripts/launchd/pogo-deploy.sh": {"S1", "S0"}},
		blobs: map[string]string{
			"S1:scripts/launchd/pogo-deploy.sh": "current",
			"S0:scripts/launchd/pogo-deploy.sh": "older",
		},
	}
	r := Assess("C", f.deps())

	if r.Overall != OverallHalfLive {
		t.Errorf("Overall = %q, want %q; rows were %v", r.Overall, OverallHalfLive, allCarriers(r))
	}
	if got := verdictFor(t, r, "running pogod").Verdict; got != Inert {
		t.Errorf("running pogod = %q, want %q", got, Inert)
	}
	if got := verdictFor(t, r, "installed copy").Verdict; got != Live {
		t.Errorf("installed copy = %q, want %q — the copy holds the bytes of a commit that contains C", got, Live)
	}
	if !strings.Contains(r.Summary, "running pogod") {
		t.Errorf("summary does not name the carrier that is behind: %q. The subject line is the part that travels", r.Summary)
	}
}

// UNKNOWN IS NOT A SOFT INERT. The two owe different actions and only one of
// them supports a decision; a report that folded them would go green (or red)
// on a carrier it never read.
func TestUnknownNeverRendersAsInertOrLive(t *testing.T) {
	f := fake{
		paths: []string{"internal/agent/api.go"},
		rev: []RevCarrier{
			{Name: "running pogod", At: "http://x/version", Revision: "<unreachable>", Binary: "pogod"},
			{Name: "installed pogo", At: "/bin/pogo", Revision: "<unstamped>", Binary: "pogo"},
		},
		gomap: map[string][]string{"internal/agent": {"pogod", "pogo"}},
	}
	r := Assess("C", f.deps())

	for _, name := range []string{"running pogod", "installed pogo"} {
		if got := verdictFor(t, r, name).Verdict; got != Unknown {
			t.Errorf("%s = %q, want %q; a sentinel is comparable to nothing", name, got, Unknown)
		}
	}
	if r.Overall != OverallUnknown {
		t.Errorf("Overall = %q, want %q — a report that measured nothing must not read as an answer", r.Overall, OverallUnknown)
	}
	if r.ExitCode() != 3 {
		t.Errorf("ExitCode = %d, want 3; a gate must be able to tell `not in effect` from `not established`", r.ExitCode())
	}
}

// An ancestry probe that ERRORS is a failure to measure, not a negative. git
// exits 1 for "not an ancestor" and >1 for "I could not tell"; only the first
// is an answer.
func TestAncestryErrorIsUnknownNotInert(t *testing.T) {
	f := fake{
		paths:  []string{"internal/agent/api.go"},
		rev:    []RevCarrier{{Name: "running pogod", Revision: "abc123abc123", Binary: "pogod"}},
		gomap:  map[string][]string{"internal/agent": {"pogod"}},
		ancErr: errors.New("bad object"),
	}
	r := Assess("C", f.deps())
	if got := verdictFor(t, r, "running pogod").Verdict; got != Unknown {
		t.Errorf("verdict on an unreadable ancestry = %q, want %q", got, Unknown)
	}
}

func TestAllCarriersLiveIsLive(t *testing.T) {
	f := fake{
		paths:    []string{"cmd/pogo/main.go"},
		rev:      []RevCarrier{{Name: "installed pogo", Revision: "new222222222", Binary: "pogo"}},
		gomap:    map[string][]string{"cmd/pogo": {"pogo"}},
		ancestor: map[string]bool{"C->new222222222": true},
	}
	r := Assess("C", f.deps())
	if r.Overall != OverallLive {
		t.Errorf("Overall = %q, want %q; rows were %v", r.Overall, OverallLive, allCarriers(r))
	}
	if r.ExitCode() != 0 {
		t.Errorf("ExitCode = %d, want 0", r.ExitCode())
	}
}

// A docs-only commit has no runtime carrier, and that is a distinct answer from
// "in effect". Saying `live` here would train a reader to read the word as
// "merged", which is the conflation the command exists to break.
func TestDocsOnlyCommitReportsNoRuntimeCarrier(t *testing.T) {
	f := fake{paths: []string{"ARCHITECTURE.md", "changelog.d/mg-3d0e.added.md"}}
	r := Assess("C", f.deps())
	if r.Overall != OverallNoCarriers {
		t.Errorf("Overall = %q, want %q", r.Overall, OverallNoCarriers)
	}
	if got := findingFor(t, r, ClassNoCarrier).Carriers[0].Verdict; got != NotApplicable {
		t.Errorf("verdict = %q, want %q", got, NotApplicable)
	}
	if strings.Contains(strings.ToLower(r.Summary), "in effect:") {
		t.Errorf("summary %q reads as an in-effect pass for a commit nothing executes", r.Summary)
	}
}

// An unrecognised path must produce an UNKNOWN row of its own, not fall into
// the documentation bucket where it would sit under an N/A that reads as clean.
func TestUnclassifiedPathIsItsOwnUnknownRow(t *testing.T) {
	f := fake{paths: []string{"Dockerfile"}}
	r := Assess("C", f.deps())
	fnd := findingFor(t, r, ClassUnclassified)
	if len(fnd.Carriers) != 1 || fnd.Carriers[0].Verdict != Unknown {
		t.Fatalf("unclassified finding = %+v, want one UNKNOWN row", fnd)
	}
	if r.Overall != OverallUnknown {
		t.Errorf("Overall = %q, want %q", r.Overall, OverallUnknown)
	}
}

// A prompt finding carries the running-agent caveat on EVERY run. No
// observation available here can establish what text a live agent holds, so a
// prompt row that read as a plain pass would be this ticket's own defect
// committed by its own fix.
func TestPromptFindingAlwaysCarriesTheRunningAgentCaveat(t *testing.T) {
	f := fake{
		paths:    []string{"internal/agent/prompts/mayor.md"},
		rev:      []RevCarrier{{Name: "running pogod", Revision: "new222222222", Binary: "pogod"}},
		gomap:    map[string][]string{PromptEmbedPkg: {"pogod"}},
		ancestor: map[string]bool{"C->new222222222": true, "C->P1": true},
		files: map[string][]FileCarrier{
			"internal/agent/prompts/mayor.md": {{Name: "installed prompt", Path: "/home/agents/mayor.md", Data: []byte("body")}},
		},
		history: map[string][]string{"internal/agent/prompts/mayor.md": {"P1"}},
		blobs:   map[string]string{"P1:internal/agent/prompts/mayor.md": "body"},
	}
	r := Assess("C", f.deps())
	fnd := findingFor(t, r, ClassPrompt)
	if !strings.Contains(fnd.Note, "mg-385f") {
		t.Errorf("prompt note does not cross-reference the running-agent residual: %q", fnd.Note)
	}
	if got := verdictFor(t, r, "installed prompt").Verdict; got != Live {
		t.Errorf("installed prompt = %q, want %q", got, Live)
	}
	// Both prompt carriers must appear: the embed compiled into a binary AND
	// the installed file. Reporting one of them is how a half-live prompt
	// change reads as settled.
	if len(fnd.Carriers) < 2 {
		t.Errorf("prompt finding has %d carrier(s) (%v); the corpus is both embedded and installed", len(fnd.Carriers), fnd.Carriers)
	}
}

// The CONTENT predicate's negative: a copy holding the bytes of a revision that
// does NOT contain the commit is inert, and the row says which revision it is
// so the reader can check by hand.
func TestInstalledCopyOfAnOlderRevisionIsInert(t *testing.T) {
	f := fake{
		paths:    []string{"scripts/launchd/pogo-deploy.sh"},
		rev:      []RevCarrier{{Name: "this checkout", Revision: "new222222222", Checkout: true}},
		ancestor: map[string]bool{"C->new222222222": true},
		files: map[string][]FileCarrier{
			"scripts/launchd/pogo-deploy.sh": {{Name: "installed copy", Path: "/home/bin/pogo-deploy.sh", Data: []byte("older")}},
		},
		history: map[string][]string{"scripts/launchd/pogo-deploy.sh": {"S1", "S0"}},
		blobs: map[string]string{
			"S1:scripts/launchd/pogo-deploy.sh": "current",
			"S0:scripts/launchd/pogo-deploy.sh": "older",
		},
	}
	r := Assess("C", f.deps())
	cv := verdictFor(t, r, "installed copy")
	if cv.Verdict != Inert {
		t.Errorf("installed copy = %q, want %q", cv.Verdict, Inert)
	}
	if !strings.Contains(cv.Why, "S0") {
		t.Errorf("why = %q; the row must name the revision the copy holds so it can be re-derived", cv.Why)
	}
}

// A locally edited copy matches no revision and therefore cannot be dated. That
// is UNKNOWN — reporting it as inert would assert a comparison that was never
// made, and reporting it as live would be worse.
func TestUndatableCopyIsUnknown(t *testing.T) {
	f := fake{
		paths: []string{"scripts/launchd/pogo-deploy.sh"},
		files: map[string][]FileCarrier{
			"scripts/launchd/pogo-deploy.sh": {{Name: "installed copy", Path: "/home/bin/x.sh", Data: []byte("hand-edited")}},
		},
		history: map[string][]string{"scripts/launchd/pogo-deploy.sh": {"S1"}},
		blobs:   map[string]string{"S1:scripts/launchd/pogo-deploy.sh": "current"},
	}
	r := Assess("C", f.deps())
	cv := verdictFor(t, r, "installed copy")
	if cv.Verdict != Unknown {
		t.Errorf("verdict = %q, want %q", cv.Verdict, Unknown)
	}
	if !strings.Contains(cv.Why, "locally edited") {
		t.Errorf("why = %q, want it to name the two ways this happens", cv.Why)
	}
}

// A compiled change whose build graph could not be read reports UNKNOWN with
// the reason. The tempting alternative — assume every binary carries it — would
// manufacture inert rows nobody measured.
func TestUnreadableBuildGraphIsUnknown(t *testing.T) {
	f := fake{
		paths: []string{"internal/agent/api.go"},
		rev:   []RevCarrier{{Name: "running pogod", Revision: "new222222222", Binary: "pogod"}},
		goErr: errors.New("go: not found"),
	}
	r := Assess("C", f.deps())
	cv := verdictFor(t, r, "compiled binaries")
	if cv.Verdict != Unknown {
		t.Errorf("verdict = %q, want %q", cv.Verdict, Unknown)
	}
	if !strings.Contains(cv.Why, "go: not found") {
		t.Errorf("why = %q, want it to carry the underlying failure", cv.Why)
	}
}

func TestUnresolvableCommitIsAnError(t *testing.T) {
	f := fake{}
	r := Assess("nope", f.deps())
	if r.Err == "" || r.Overall != OverallUnknown {
		t.Errorf("Assess of an unknown rev = %+v, want an error and %q", r, OverallUnknown)
	}
	if r.ExitCode() != 3 {
		t.Errorf("ExitCode = %d, want 3", r.ExitCode())
	}
}

// The reporter's own revision travels on the report. This command ships inside
// the pogo CLI and is an artifact of the class it reports on; a reader holding
// an old binary is reading that build's rules.
func TestReportNamesTheBuildThatProducedIt(t *testing.T) {
	f := fake{paths: []string{"ARCHITECTURE.md"}}
	r := Assess("C", f.deps())
	if r.Reporter != "aaaaaaaaaaaabbbb" {
		t.Errorf("Reporter = %q, want the deps value", r.Reporter)
	}
	if !strings.Contains(r.Text(), "aaaaaaaaaaaa") {
		t.Errorf("Text() does not name the producing build:\n%s", r.Text())
	}
}

// A plist in the commit adds the note that says this command does not judge it.
// The plist is RENDERED by the installer, so it matches no git blob and the
// content predicate would call it undatable forever; silently offering only the
// checkout rows would read as a complete answer about an artifact whose real
// carrier was never examined.
func TestPlistFindingSaysItIsNotJudgedHere(t *testing.T) {
	f := fake{
		paths: []string{"scripts/launchd/com.pogo.deploy.plist"},
		rev:   []RevCarrier{{Name: "this checkout", Revision: "new222222222", Checkout: true}},
	}
	r := Assess("C", f.deps())
	note := findingFor(t, r, ClassAsset).Note
	if !strings.Contains(note, "NOT judged here") || !strings.Contains(note, "doctor --check") {
		t.Errorf("plist note = %q; it must say the plist is not judged here and name the check that does", note)
	}
}

// The compiled note states that the import graph is read from the CURRENT tree.
// For a commit that moved an import, the carrier list is today's and not that
// commit's — a limit that is invisible in the output and would otherwise be
// discovered only by someone acting on a wrong row.
func TestCompiledNoteStatesTheImportGraphIsCurrent(t *testing.T) {
	f := fake{
		paths: []string{"internal/agent/api.go"},
		rev:   []RevCarrier{{Name: "running pogod", Revision: "new222222222", Binary: "pogod"}},
		gomap: map[string][]string{"internal/agent": {"pogod"}},
	}
	r := Assess("C", f.deps())
	if note := findingFor(t, r, ClassCompiled).Note; !strings.Contains(note, "CURRENT working tree") {
		t.Errorf("compiled note = %q; it must name the limit that the carrier list comes from today's import graph", note)
	}
}
